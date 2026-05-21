#!/usr/bin/env bash

# Tears down resources created by setup-fma-wva.sh, in one of three nested
# --level tiers (default: namespace):
#   workload  - demo workload only; leaves the platform running
#   namespace - workload + namespaced platform (FMA/WVA/EPP); no cluster objects
#   all       - everything: + cluster RBAC, node labels, namespace (CRDs kept)
#
# Prerequisites: oc (logged in), helm, kubectl, jq. --level all also needs git
# (to clone WVA to --wva-repo-path for its undeploy).
#
# Run with --help for the full list of flags.

set -euo pipefail

# ----------------------------------------------------------------------------
# CLI parsing — flags are the primary interface; matching env vars are honored
# as a fallback (useful for CI/scripted runs) but flags take precedence.
# ----------------------------------------------------------------------------
usage() {
    cat <<EOF
Usage: $(basename "$0") [OPTIONS]

Tears down resources created by setup-fma-wva.sh.

Options:
  -n, --namespace NAME       Target namespace (default: fma-wva-demo)
      --level LEVEL          How much to tear down (default: namespace):
                               workload  - demo workload only; leave the platform
                               namespace - workload + namespaced platform
                                           (FMA/WVA/EPP); no cluster-scoped objects
                               all       - everything: + cluster RBAC, node
                                           labels, and the namespace (CRDs kept)
      --wva-repo-path PATH   Path to WVA repo (default: <repo-root>/.wva-checkout)
      --wva-repo-url URL     WVA git URL
                             (default: https://github.com/llm-d/llm-d-workload-variant-autoscaler)
      --wva-version TAG      WVA version — must be a published release tag
                             (e.g. v0.8.0); pass the same value used at
                             deploy time. (default: v0.8.0)
  -h, --help                 Show this help and exit

Environment variables (NAMESPACE, CLEANUP_LEVEL, WVA_REPO_PATH, WVA_REPO_URL,
WVA_VERSION) are also accepted, but flags take precedence.
EOF
}

# Seed defaults from env vars.
NAMESPACE="${NAMESPACE:-fma-wva-demo}"
CLEANUP_LEVEL="${CLEANUP_LEVEL:-}"

while [[ $# -gt 0 ]]; do
    case "$1" in
        -n|--namespace)
            [[ $# -ge 2 ]] || { echo "ERROR: $1 requires an argument" >&2; exit 2; }
            NAMESPACE="$2"; shift 2 ;;
        --level)
            [[ $# -ge 2 ]] || { echo "ERROR: $1 requires an argument" >&2; exit 2; }
            CLEANUP_LEVEL="$2"; shift 2 ;;
        --wva-repo-path)
            [[ $# -ge 2 ]] || { echo "ERROR: $1 requires an argument" >&2; exit 2; }
            WVA_REPO_PATH="$2"; shift 2 ;;
        --wva-repo-url)
            [[ $# -ge 2 ]] || { echo "ERROR: $1 requires an argument" >&2; exit 2; }
            WVA_REPO_URL="$2"; shift 2 ;;
        --wva-version)
            [[ $# -ge 2 ]] || { echo "ERROR: $1 requires an argument" >&2; exit 2; }
            WVA_VERSION="$2"; shift 2 ;;
        -h|--help)
            usage; exit 0 ;;
        *)
            echo "ERROR: Unknown option: $1" >&2
            usage >&2
            exit 2 ;;
    esac
done

# Default level, validate, and map to an ordered rank (nested tiers).
CLEANUP_LEVEL="${CLEANUP_LEVEL:-namespace}"
case "$CLEANUP_LEVEL" in
    workload)  LEVEL_RANK=1 ;;
    namespace) LEVEL_RANK=2 ;;
    all)       LEVEL_RANK=3 ;;
    *)
        echo "ERROR: invalid --level '$CLEANUP_LEVEL' (expected: workload, namespace, or all)" >&2
        usage >&2
        exit 2 ;;
esac

echo "========================================="
echo "  FMA + WVA Demo Cleanup"
echo "========================================="
echo ""
echo "  Namespace:          $NAMESPACE"
echo "  Level:              $CLEANUP_LEVEL"
echo ""

# Skip if namespace doesn't exist
if ! kubectl get ns "$NAMESPACE" &>/dev/null; then
    echo "  Namespace $NAMESPACE not found — nothing to do in-namespace."
    SKIP_NS_OPS=true
else
    SKIP_NS_OPS=false
fi

# Helper: strip ONLY dual-pods.llm-d.ai/* finalizers from pods in the namespace,
# preserving any other finalizers (sidecars, operators, etc.)
strip_dual_pods_finalizers() {
    local pods
    pods=$(kubectl get pods -n "$NAMESPACE" -o name 2>/dev/null || true)
    [ -z "$pods" ] && return 0
    while read -r pod; do
        # Build the new finalizer list: keep everything except dual-pods.llm-d.ai/*.
        # If jq returns nothing (no finalizers at all) skip the patch.
        local new_fin
        new_fin=$(kubectl get "$pod" -n "$NAMESPACE" -o json 2>/dev/null \
            | jq -c '[.metadata.finalizers[]? | select(startswith("dual-pods.llm-d.ai/") | not)]' 2>/dev/null) || continue
        # Only patch if the pod actually has a dual-pods finalizer to strip.
        local had_fma
        had_fma=$(kubectl get "$pod" -n "$NAMESPACE" -o json 2>/dev/null \
            | jq -r '[.metadata.finalizers[]? | select(startswith("dual-pods.llm-d.ai/"))] | length' 2>/dev/null) || continue
        if [ "${had_fma:-0}" -gt 0 ]; then
            # new_fin is a JSON array of the finalizers we're keeping; count its
            # elements (not the string length) for an accurate "preserving N".
            local kept
            kept=$(jq -r 'length' <<< "$new_fin" 2>/dev/null || echo 0)
            echo "  Removing dual-pods finalizers from $pod (preserving ${kept} others)"
            # An empty array [] tells kubectl "remove all" via merge patch; that's
            # the desired behavior when the only finalizers were dual-pods ones.
            kubectl patch "$pod" -n "$NAMESPACE" --type=merge \
                -p "{\"metadata\":{\"finalizers\":${new_fin}}}" 2>/dev/null || true
        fi
    done <<< "$pods"
}

if [ "$SKIP_NS_OPS" = "false" ]; then
    # 1. WVA-managed ScaledObject — its llm-d.ai/managed annotation is what WVA
    #    discovers, so deleting it detaches the workload. KEDA GCs the HPA it
    #    created; we also delete it explicitly in case KEDA is already gone.
    echo "--- Deleting WVA-managed ScaledObject ---"
    kubectl delete scaledobject fma-requester-scaler -n "$NAMESPACE" --ignore-not-found 2>/dev/null
    kubectl delete hpa wva-keda-hpa-fma-requester -n "$NAMESPACE" --ignore-not-found 2>/dev/null

    # 2. Deployment — deletes any existing requester pods
    echo "--- Deleting Deployment ---"
    kubectl delete deployment fma-requester -n "$NAMESPACE" --ignore-not-found 2>/dev/null

    # 3. Give the controllers a moment to process pending bind/unbind events
    echo "--- Waiting for controllers to drain (10s) ---"
    sleep 10

    # 4. FMA CRs. Delete the LPP first and pause: the populator skips
    # reconciliation when the LauncherConfig is missing, so removing the LC
    # too soon strands launchers it should have scaled down.
    echo "--- Deleting LauncherPopulationPolicy ---"
    kubectl delete launcherpopulationpolicy lpp-fma -n "$NAMESPACE" --ignore-not-found 2>/dev/null
    sleep 10
    echo "--- Deleting LauncherConfig + InferenceServerConfig ---"
    kubectl delete launcherconfig lc-fma -n "$NAMESPACE" --ignore-not-found 2>/dev/null
    kubectl delete inferenceserverconfig isc-smol -n "$NAMESPACE" --ignore-not-found 2>/dev/null

    # 5. PodMonitor
    echo "--- Deleting PodMonitor ---"
    kubectl delete podmonitor fma-vllm-monitor -n "$NAMESPACE" --ignore-not-found 2>/dev/null

    # ---- Tier 2 (namespace): namespaced platform + finalizer recovery ----
    if [ "$LEVEL_RANK" -ge 2 ]; then
        echo "--- Waiting for controllers to clean up resources (10s) ---"
        sleep 10

        # 6. FMA controllers (Helm release).
        echo "--- Uninstalling FMA controllers ---"
        helm uninstall fma -n "$NAMESPACE" 2>/dev/null || true

        # 7. llm-d-router namespaced objects. Names mirror undeploy_epp() in the
        #    WVA repo (deploy/lib/infra_epp.sh); its cluster RBAC is removed at all.
        echo "--- Uninstalling llm-d-router (namespaced) ---"
        helm uninstall optimized-baseline -n "$NAMESPACE" 2>/dev/null || true
        kubectl delete secret llm-d-hf-token -n "$NAMESPACE" --ignore-not-found 2>/dev/null

        # 8. WVA controller namespaced objects (by label). Cluster RBAC at all.
        echo "--- Deleting WVA controller (namespaced) ---"
        kubectl delete deployment,service,serviceaccount,rolebinding,role,configmap \
            -n "$NAMESPACE" -l app.kubernetes.io/name=workload-variant-autoscaler \
            --ignore-not-found 2>/dev/null || true

        # 9. Recovery: strip leftover dual-pods finalizers. Only meaningful after
        # controller uninstall — otherwise the controller would re-add them.
        echo "--- Stripping dual-pods finalizers from any leftover pods ---"
        strip_dual_pods_finalizers
    fi
fi

# ---- Tier 3 (all): cluster-scoped objects, node label, namespace ----
if [ "$LEVEL_RANK" -ge 3 ]; then
    echo ""
    echo "--- Removing cluster-scoped resources (--level all) ---"

    # Cluster-scoped FMA + EPP RBAC (by name). WVA's is removed via its own
    # undeploy below (prefixed names + binds a cluster builtin — unsafe by name).
    kubectl delete clusterrolebinding fma-node-view --ignore-not-found 2>/dev/null
    kubectl delete clusterrole fma-node-view --ignore-not-found 2>/dev/null
    kubectl delete clusterrolebinding "${NAMESPACE}-${NAMESPACE}-epp" --ignore-not-found 2>/dev/null
    kubectl delete clusterrole "${NAMESPACE}-${NAMESPACE}-epp" --ignore-not-found 2>/dev/null
    kubectl delete clusterrolebinding optimized-baseline-epp-tokenreview --ignore-not-found 2>/dev/null
    kubectl delete clusterrole optimized-baseline-epp-tokenreview --ignore-not-found 2>/dev/null

    # Resolve WVA repo (auto-clone if not present). Behavior at $WVA_REPO_PATH:
    #   - missing or empty                 → clone fresh
    #   - exists with a .git directory     → reuse as-is
    #   - exists, non-empty, but no .git   → fail loudly (refuse to clobber)
    # Default keeps the clone under the FMA repo, not $HOME.
    SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
    REPO_ROOT="$(cd "$SCRIPT_DIR/../../.." && pwd)"
    WVA_REPO_PATH="${WVA_REPO_PATH:-$REPO_ROOT/.wva-checkout}"
    WVA_REPO_URL="${WVA_REPO_URL:-https://github.com/llm-d/llm-d-workload-variant-autoscaler}"
    WVA_VERSION="${WVA_VERSION:-v0.8.0}"

    if [ ! -d "$WVA_REPO_PATH/.git" ]; then
        if [ -d "$WVA_REPO_PATH" ] && [ -n "$(ls -A "$WVA_REPO_PATH" 2>/dev/null)" ]; then
            echo "  ERROR: $WVA_REPO_PATH exists but is not a git checkout."
            echo "  Remove it or set WVA_REPO_PATH to a different location."
            exit 1
        fi
        echo "  WVA repo not found at $WVA_REPO_PATH — cloning $WVA_REPO_URL ($WVA_VERSION)..."
        mkdir -p "$(dirname "$WVA_REPO_PATH")"
        # Shallow clone the release tag (same value used at deploy time).
        if ! git clone --depth 1 --branch "$WVA_VERSION" "$WVA_REPO_URL" "$WVA_REPO_PATH"; then
            echo "  ERROR: failed to clone $WVA_REPO_URL at tag '$WVA_VERSION'." >&2
            echo "  --wva-version must be a published WVA release tag (e.g. v0.8.0)." >&2
            exit 1
        fi
    else
        echo "  Using existing WVA repo at $WVA_REPO_PATH"
    fi

    # Remove node label
    echo "  Removing fma-poc label from nodes..."
    kubectl get nodes -l fma-poc=true -o name 2>/dev/null | while read -r node; do
        kubectl label "$node" fma-poc- 2>/dev/null || true
    done

    # Full WVA undeploy — removes WVA's cluster-scoped RBAC (idempotent with the
    # Tier 2 namespaced delete). Prometheus flags forced false so the undeploy
    # matches what the demo deployed (no spurious "uninstalling adapter" warning).
    echo "  Undeploying WVA controller (cluster-scoped RBAC)..."
    (
        cd "$WVA_REPO_PATH"
        WVA_NS="$NAMESPACE" \
        LLMD_NS="$NAMESPACE" \
        ENVIRONMENT=openshift \
        DEPLOY_PROMETHEUS=false \
        DEPLOY_PROMETHEUS_ADAPTER=false \
        UNDEPLOY=true \
        ./deploy/install.sh || true
    )

    # Undeploy llm-d-router — same rationale as WVA above.
    echo "  Undeploying EPP and Gateway..."
    (
        cd "$WVA_REPO_PATH"
        LLMD_NS="$NAMESPACE" \
        ENVIRONMENT=openshift \
        UNDEPLOY=true \
        ./deploy/install-epp.sh || true
    )

    # Delete namespace last (removes everything else in it)
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

fi

echo ""
echo "========================================="
echo "  Cleanup Summary (level: $CLEANUP_LEVEL)"
echo "========================================="
echo ""
# Tier 1 (always, when the namespace exists)
echo "  ✓ Removed demo workload (ScaledObject/HPA, Deployment, FMA CRs, PodMonitor)"
if [ "$LEVEL_RANK" -ge 2 ]; then
    echo "  ✓ Removed namespaced platform (FMA controllers, WVA controller, llm-d-router)"
else
    echo "  ⚠ Preserved the platform (FMA/WVA/EPP controllers) for reuse"
fi
if [ "$LEVEL_RANK" -ge 3 ]; then
    echo "  ✓ Removed cluster-scoped RBAC and node labels"
    echo "  ✓ Removed namespace $NAMESPACE"
else
    echo "  ⚠ Preserved cluster-scoped objects and namespace $NAMESPACE"
fi
echo "  NOTE: CRDs (Gateway API, GAIE, FMA, WVA) are never removed — they may be cluster-shared."
if [ "$LEVEL_RANK" -lt 3 ]; then
    echo ""
    echo "  For a deeper teardown: ./cleanup-fma-wva.sh --level namespace|all"
fi
echo ""
