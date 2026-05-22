#!/usr/bin/env bash

# Tears down resources created by demo-fma-hpa-ocp.sh.
#
# By default removes FMA objects, HPA, and FMA controllers but keeps
# the namespace, CRDs, EPP, Gateway, and prometheus-adapter rules.
# Set FULL_CLEANUP=true to also remove namespace, node labels,
# and the FMA-specific rules in prometheus-adapter (preserving WVA and others).
# Set CLEAN_AGENTGATEWAY=true to also tear down the cluster-wide agentgateway
# control plane in agentgateway-system (do this only if no other tenant uses it).
#
# Prerequisites:
#   - oc/kubectl authenticated
#   - helm, yq (only required when FULL_CLEANUP=true)
#
# Optional environment variables:
#   NAMESPACE          - target namespace (default: fma-hpa)
#   FULL_CLEANUP       - if "true", also delete namespace, node labels, and FMA prom-adapter rules (default: false)
#   CLEAN_AGENTGATEWAY - if "true", also delete the agentgateway control plane in agentgateway-system (default: false)
#   PROM_ADAPTER_NS    - prometheus-adapter namespace (default: openshift-user-workload-monitoring)
#   PROM_ADAPTER_REL   - prometheus-adapter Helm release name (default: prometheus-adapter)

set -euo pipefail

NAMESPACE="${NAMESPACE:-fma-hpa}"
FULL_CLEANUP="${FULL_CLEANUP:-false}"
CLEAN_AGENTGATEWAY="${CLEAN_AGENTGATEWAY:-false}"
PROM_ADAPTER_NS="${PROM_ADAPTER_NS:-openshift-user-workload-monitoring}"
PROM_ADAPTER_REL="${PROM_ADAPTER_REL:-prometheus-adapter}"

echo "========================================="
echo "  FMA + HPA Demo Cleanup"
echo "========================================="
echo ""
echo "  Namespace:          $NAMESPACE"
echo "  Full cleanup:       $FULL_CLEANUP"
echo "  Clean agentgateway: $CLEAN_AGENTGATEWAY"
echo ""

# Skip if namespace doesn't exist
if ! kubectl get ns "$NAMESPACE" &>/dev/null; then
    echo "  Namespace $NAMESPACE not found — nothing to do in-namespace."
    SKIP_NS_OPS=true
else
    SKIP_NS_OPS=false
fi

# Helper: strip dual-pods finalizers from all pods in the namespace.
# Without this, deleting controllers before pods leaves dangling finalizers
# that block pod (and namespace) deletion forever.
strip_dual_pods_finalizers() {
    local pods
    pods=$(kubectl get pods -n "$NAMESPACE" -o name 2>/dev/null || true)
    [ -z "$pods" ] && return 0
    while read -r pod; do
        [ -z "$pod" ] && continue
        local fin
        fin=$(kubectl get "$pod" -n "$NAMESPACE" -o jsonpath='{.metadata.finalizers}' 2>/dev/null || echo "")
        if echo "$fin" | grep -q "dual-pods.llm-d.ai"; then
            echo "  Removing finalizers from $pod"
            kubectl patch "$pod" -n "$NAMESPACE" --type=merge \
                -p '{"metadata":{"finalizers":null}}' 2>/dev/null || true
        fi
    done <<< "$pods"
}

if [ "$SKIP_NS_OPS" = "false" ]; then
    # 1. Loadgen pod
    echo "--- Cleaning up loadgen ---"
    kubectl delete pod fma-loadgen -n "$NAMESPACE" --ignore-not-found 2>/dev/null

    # 2. HPA first — stops creating new requesters
    echo "--- Deleting HPA ---"
    kubectl delete hpa fma-hpa -n "$NAMESPACE" --ignore-not-found 2>/dev/null

    # 3. ReplicaSet — stops recreating requester pods
    echo "--- Deleting ReplicaSet ---"
    kubectl delete rs fma-requester -n "$NAMESPACE" --ignore-not-found 2>/dev/null

    # 4. Give the controller a moment to process pending bind/unbind events
    echo "--- Waiting for controller to drain (10s) ---"
    sleep 10

    # 5. Strip finalizers — must happen BEFORE we delete controllers, otherwise
    # finalizer removal is never processed and pods (and the namespace) hang
    echo "--- Stripping dual-pods finalizers from pods ---"
    strip_dual_pods_finalizers

    # 6. Delete launcher pods explicitly (matches old + new label conventions)
    echo "--- Deleting launcher pods ---"
    kubectl delete pods -n "$NAMESPACE" -l app.kubernetes.io/component=launcher --ignore-not-found 2>/dev/null
    kubectl delete pods -n "$NAMESPACE" -l dual-pods.llm-d.ai/sleeping --ignore-not-found 2>/dev/null
    kubectl delete pods -n "$NAMESPACE" -l dual-pods.llm-d.ai/launcher-config-name --ignore-not-found 2>/dev/null

    # 7. FMA objects (CRs)
    echo "--- Deleting FMA objects ---"
    kubectl delete launcherpopulationpolicy lpp-hpa -n "$NAMESPACE" --ignore-not-found 2>/dev/null
    kubectl delete launcherconfig lc-hpa -n "$NAMESPACE" --ignore-not-found 2>/dev/null
    kubectl delete inferenceserverconfig isc-smol -n "$NAMESPACE" --ignore-not-found 2>/dev/null

    # 8. FMA controllers (Helm release)
    echo "--- Uninstalling FMA controllers ---"
    helm uninstall fma -n "$NAMESPACE" 2>/dev/null || true

    # 9. RBAC
    echo "--- Deleting RBAC ---"
    kubectl delete rolebinding testreq testlauncher -n "$NAMESPACE" --ignore-not-found 2>/dev/null
    kubectl delete role testreq testlauncher -n "$NAMESPACE" --ignore-not-found 2>/dev/null
    kubectl delete sa testreq testlauncher -n "$NAMESPACE" --ignore-not-found 2>/dev/null
fi

# 10. Cluster-scoped FMA resources
echo "--- Deleting cluster-scoped FMA resources ---"
kubectl delete clusterrolebinding fma-node-view --ignore-not-found 2>/dev/null
kubectl delete clusterrole fma-node-view --ignore-not-found 2>/dev/null
kubectl delete clusterrolebinding "${NAMESPACE}-${NAMESPACE}-epp" --ignore-not-found 2>/dev/null
kubectl delete clusterrole "${NAMESPACE}-${NAMESPACE}-epp" --ignore-not-found 2>/dev/null

if [ "$FULL_CLEANUP" = "true" ]; then
    echo ""
    echo "--- Full cleanup ---"

    # Remove node label
    echo "  Removing fma-hpa-poc label from nodes..."
    kubectl get nodes -l fma-hpa-poc=true -o name 2>/dev/null | while read -r node; do
        kubectl label "$node" fma-hpa-poc- 2>/dev/null || true
    done

    # Remove FMA-specific rules from prometheus-adapter, preserving WVA and others
    if helm status "$PROM_ADAPTER_REL" -n "$PROM_ADAPTER_NS" &>/dev/null; then
        if ! command -v yq &>/dev/null; then
            echo "  WARNING: yq not installed — skipping prometheus-adapter rule cleanup."
            echo "  Install yq (https://github.com/mikefarah/yq) to enable this step."
        else
            CURRENT_VALUES=$(mktemp)
            NEW_VALUES=$(mktemp)
            trap 'rm -f "$CURRENT_VALUES" "$NEW_VALUES"' EXIT

            helm get values "$PROM_ADAPTER_REL" -n "$PROM_ADAPTER_NS" > "$CURRENT_VALUES"
            # Remove only the rules whose name.as is one of our two FMA metrics.
            # Anything else (WVA, other tenants, defaults) is preserved.
            yq eval 'del(.rules.external[] | select(.name.as == "epp_queue_size" or .name.as == "epp_running_requests"))' \
                "$CURRENT_VALUES" > "$NEW_VALUES"

            if ! diff -q "$CURRENT_VALUES" "$NEW_VALUES" >/dev/null 2>&1; then
                echo "  Removing FMA rules from prometheus-adapter (preserving other rules)..."
                # Resolve chart from the existing release so we don't depend on a specific repo alias
                CHART=$(helm get metadata "$PROM_ADAPTER_REL" -n "$PROM_ADAPTER_NS" -o json 2>/dev/null \
                    | yq -r '.chart' 2>/dev/null || echo "")
                if [ -n "$CHART" ] && [ "$CHART" != "null" ]; then
                    helm upgrade "$PROM_ADAPTER_REL" "prometheus-community/$CHART" \
                        -n "$PROM_ADAPTER_NS" \
                        -f "$NEW_VALUES" 2>&1 | tail -5 || \
                        echo "  WARNING: helm upgrade failed. Apply manually: helm upgrade $PROM_ADAPTER_REL <chart> -n $PROM_ADAPTER_NS -f $NEW_VALUES"
                else
                    echo "  Could not resolve chart name. Apply manually:"
                    echo "    helm upgrade $PROM_ADAPTER_REL prometheus-community/prometheus-adapter -n $PROM_ADAPTER_NS -f $NEW_VALUES"
                fi
            else
                echo "  prometheus-adapter: no FMA rules found, nothing to remove."
            fi
        fi
    else
        echo "  prometheus-adapter Helm release '$PROM_ADAPTER_REL' not found in $PROM_ADAPTER_NS — skipping."
    fi

    # Delete namespace last (removes EPP, Gateway, and everything else in it)
    if [ "$SKIP_NS_OPS" = "false" ]; then
        echo "  Deleting namespace $NAMESPACE..."
        kubectl delete ns "$NAMESPACE" --ignore-not-found --timeout=120s 2>/dev/null || true

        # If still hung, strip namespace finalizers as a last resort
        if kubectl get ns "$NAMESPACE" &>/dev/null; then
            echo "  Namespace still present — stripping finalizers as last resort..."
            kubectl get ns "$NAMESPACE" -o json 2>/dev/null \
                | jq '.spec.finalizers = []' \
                | kubectl replace --raw "/api/v1/namespaces/$NAMESPACE/finalize" -f - 2>/dev/null || true
        fi
    fi

    echo ""
    echo "  Full cleanup complete."
    echo "  NOTE: CRDs (Gateway API, GAIE, FMA) are NOT removed — they may be cluster-shared."
else
    echo ""
    echo "  Cleanup complete. Namespace $NAMESPACE preserved."
    echo "  EPP, Gateway, and prometheus-adapter rules are still in place."
    echo "  Run with FULL_CLEANUP=true to also remove the namespace and FMA prom-adapter rules."
fi

if [ "$CLEAN_AGENTGATEWAY" = "true" ]; then
    echo ""
    echo "--- Removing agentgateway control plane ---"
    if ! kubectl get ns agentgateway-system &>/dev/null; then
        echo "  agentgateway-system not present — skipping."
    elif ! command -v helmfile &>/dev/null; then
        echo "  WARNING: helmfile not installed. Run manually:"
        echo "    helmfile destroy -f \${LLM_D_GUIDES_DIR}/prereq/gateway-provider/agentgateway.helmfile.yaml"
    elif [ -z "${LLM_D_GUIDES_DIR:-}" ]; then
        echo "  WARNING: LLM_D_GUIDES_DIR not set. Run manually:"
        echo "    helmfile destroy -f \${LLM_D_GUIDES_DIR}/prereq/gateway-provider/agentgateway.helmfile.yaml"
    else
        helmfile destroy -f "${LLM_D_GUIDES_DIR}/prereq/gateway-provider/agentgateway.helmfile.yaml" || true
    fi
fi
