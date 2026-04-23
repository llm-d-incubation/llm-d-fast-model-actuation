#!/usr/bin/env bash

# Tears down resources created by demo-fma-hpa-ocp.sh.
#
# By default removes FMA objects, HPA, and FMA controllers but keeps
# the namespace, CRDs, EPP, Gateway, and prometheus-adapter rules.
# Set FULL_CLEANUP=true to also remove namespace and node labels.
#
# Prerequisites:
#   - oc/kubectl authenticated
#
# Optional environment variables:
#   NAMESPACE     - target namespace (default: fma-hpa)
#   FULL_CLEANUP  - if "true", also delete namespace and node labels (default: false)

set -euo pipefail

NAMESPACE="${NAMESPACE:-fma-hpa}"
FULL_CLEANUP="${FULL_CLEANUP:-false}"

echo "========================================="
echo "  FMA + HPA Demo Cleanup"
echo "========================================="
echo ""
echo "  Namespace:    $NAMESPACE"
echo "  Full cleanup: $FULL_CLEANUP"
echo ""

# Delete loadgen pod if still running
echo "--- Cleaning up loadgen ---"
kubectl delete pod fma-loadgen -n "$NAMESPACE" --ignore-not-found 2>/dev/null

# Delete HPA
echo "--- Deleting HPA ---"
kubectl delete hpa fma-hpa -n "$NAMESPACE" --ignore-not-found 2>/dev/null

# Delete ReplicaSet (this terminates requester pods)
echo "--- Deleting ReplicaSet ---"
kubectl delete rs fma-requester -n "$NAMESPACE" --ignore-not-found 2>/dev/null

# Delete launcher pods (sleeping or active)
echo "--- Deleting launcher pods ---"
kubectl delete pods -n "$NAMESPACE" -l dual-pods.llm-d.ai/sleeping --ignore-not-found 2>/dev/null
kubectl delete pods -n "$NAMESPACE" -l fma.llm-d.ai/launcher-config --ignore-not-found 2>/dev/null

# Delete FMA objects
echo "--- Deleting FMA objects ---"
kubectl delete launcherpopulationpolicy lpp-hpa -n "$NAMESPACE" --ignore-not-found 2>/dev/null
kubectl delete launcherconfig lc-hpa -n "$NAMESPACE" --ignore-not-found 2>/dev/null
kubectl delete inferenceserverconfig isc-smol -n "$NAMESPACE" --ignore-not-found 2>/dev/null

# Uninstall FMA controllers (Helm)
echo "--- Uninstalling FMA controllers ---"
helm uninstall fma -n "$NAMESPACE" 2>/dev/null || true

# Delete RBAC
echo "--- Deleting RBAC ---"
kubectl delete rolebinding testreq testlauncher -n "$NAMESPACE" --ignore-not-found 2>/dev/null
kubectl delete role testreq testlauncher -n "$NAMESPACE" --ignore-not-found 2>/dev/null
kubectl delete sa testreq testlauncher -n "$NAMESPACE" --ignore-not-found 2>/dev/null
kubectl delete cm gpu-map -n "$NAMESPACE" --ignore-not-found 2>/dev/null

# Delete ClusterRole created by deploy_fma.sh
kubectl delete clusterrole fma-node-view --ignore-not-found 2>/dev/null
kubectl delete clusterrolebinding fma-node-view --ignore-not-found 2>/dev/null

if [ "$FULL_CLEANUP" = "true" ]; then
    echo ""
    echo "--- Full cleanup ---"

    # Remove node label
    echo "  Removing fma-hpa-poc label from nodes..."
    kubectl get nodes -l fma-hpa-poc=true -o name 2>/dev/null | while read node; do
        kubectl label "$node" fma-hpa-poc- 2>/dev/null || true
    done

    # Delete namespace (removes EPP, Gateway, and everything else)
    echo "  Deleting namespace $NAMESPACE..."
    kubectl delete ns "$NAMESPACE" --ignore-not-found 2>/dev/null

    echo ""
    echo "  Full cleanup complete. Namespace $NAMESPACE deleted."
    echo "  NOTE: CRDs and prometheus-adapter rules are NOT removed."
    echo "  To remove prometheus-adapter EPP rules, run:"
    echo "    helm upgrade prometheus-adapter prometheus-community/prometheus-adapter \\"
    echo "      -n openshift-user-workload-monitoring --reuse-values \\"
    echo "      --set rules.external=<original-rules-without-epp>"
else
    echo ""
    echo "  Cleanup complete. Namespace $NAMESPACE preserved."
    echo "  EPP, Gateway, and prometheus-adapter rules are still in place."
    echo "  Run with FULL_CLEANUP=true to also remove the namespace."
fi
