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
#   LLM_D_GUIDES_DIR   - path to llm-d/guides repo (only required when CLEAN_AGENTGATEWAY=true; see demo-fma-hpa-ocp.sh)

set -euo pipefail

NAMESPACE="${NAMESPACE:-fma-hpa}"
FULL_CLEANUP="${FULL_CLEANUP:-false}"
CLEAN_AGENTGATEWAY="${CLEAN_AGENTGATEWAY:-false}"

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

    # 6. FMA objects (CRs) — deleting the LPP triggers launcher pod cleanup by the controller
    echo "--- Deleting FMA objects ---"
    kubectl delete launcherpopulationpolicy lpp-hpa -n "$NAMESPACE" --ignore-not-found 2>/dev/null
    kubectl delete launcherconfig lc-hpa -n "$NAMESPACE" --ignore-not-found 2>/dev/null
    kubectl delete inferenceserverconfig isc-smol -n "$NAMESPACE" --ignore-not-found 2>/dev/null

    echo "--- Waiting for controller to clean up resources (10s) ---"
    sleep 10

    # 7. FMA controllers (Helm release)
    echo "--- Uninstalling FMA controllers ---"
    helm uninstall fma -n "$NAMESPACE" 2>/dev/null || true

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
    PROM_ADAPTER_NS=$(kubectl get apiservice v1beta1.external.metrics.k8s.io \
        -o jsonpath='{.spec.service.namespace}' 2>/dev/null || echo "")
    PROM_ADAPTER_SVC=$(kubectl get apiservice v1beta1.external.metrics.k8s.io \
        -o jsonpath='{.spec.service.name}' 2>/dev/null || echo "")

    if [ -z "$PROM_ADAPTER_NS" ] || [ -z "$PROM_ADAPTER_SVC" ]; then
        echo "  Could not find external.metrics.k8s.io APIService — skipping."
    elif ! command -v yq &>/dev/null; then
        echo "  WARNING: yq not installed — skipping prometheus-adapter rule cleanup."
    else
        CURRENT_CONFIG=$(mktemp)
        NEW_CONFIG=$(mktemp)
        trap 'rm -f "$CURRENT_CONFIG" "$NEW_CONFIG"' EXIT

        kubectl get configmap "$PROM_ADAPTER_SVC" -n "$PROM_ADAPTER_NS" \
            -o jsonpath='{.data.config\.yaml}' > "$CURRENT_CONFIG" 2>/dev/null

        if [ ! -s "$CURRENT_CONFIG" ]; then
            echo "  Could not read adapter configmap — skipping."
        else
            yq eval 'del(.externalRules[] | select(.name.as == "epp_queue_size" or .name.as == "epp_running_requests"))' \
                "$CURRENT_CONFIG" > "$NEW_CONFIG"

            if ! diff -q "$CURRENT_CONFIG" "$NEW_CONFIG" >/dev/null 2>&1; then
                echo "  Removing FMA rules from prometheus-adapter (preserving other rules)..."
                kubectl create configmap "$PROM_ADAPTER_SVC" -n "$PROM_ADAPTER_NS" \
                    --from-file=config.yaml="$NEW_CONFIG" \
                    --dry-run=client -o yaml | kubectl apply -f - 2>/dev/null
                kubectl rollout restart deployment "$PROM_ADAPTER_SVC" -n "$PROM_ADAPTER_NS" 2>/dev/null || true
            else
                echo "  prometheus-adapter: no FMA rules found, nothing to remove."
            fi
        fi
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
    elif [ -z "${LLM_D_GUIDES_DIR:-}" ]; then
        echo "  WARNING: LLM_D_GUIDES_DIR not set. Run manually:"
        echo "    helmfile destroy -f \${LLM_D_GUIDES_DIR}/prereq/gateway-provider/agentgateway.helmfile.yaml"
    elif ! command -v helmfile &>/dev/null; then
        echo "  WARNING: helmfile not installed. Run manually:"
        echo "    helmfile destroy -f ${LLM_D_GUIDES_DIR}/prereq/gateway-provider/agentgateway.helmfile.yaml"
    else
        helmfile destroy -f "${LLM_D_GUIDES_DIR}/prereq/gateway-provider/agentgateway.helmfile.yaml" || true
    fi
fi
