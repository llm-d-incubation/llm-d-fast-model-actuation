#!/usr/bin/env bash

# Live monitoring dashboard for the FMA + HPA demo.
#
# Refreshes every N seconds showing HPA status, pod states, and metrics.
# Run in a dedicated terminal alongside demo-fma-hpa-ocp.sh and loadgen.
#
# Prerequisites:
#   - demo-fma-hpa-ocp.sh has been run
#   - oc/kubectl authenticated
#
# Optional environment variables:
#   NAMESPACE  - target namespace (default: fma-hpa)
#   INTERVAL   - refresh interval in seconds (default: 5)

set -euo pipefail

NAMESPACE="${NAMESPACE:-fma-hpa}"
INTERVAL="${INTERVAL:-5}"

while true; do
    clear
    echo "==========================================="
    echo "  FMA + HPA Demo Monitor   $(date +%H:%M:%S)"
    echo "==========================================="

    echo ""
    echo "--- HPA ---"
    kubectl get hpa fma-hpa -n "$NAMESPACE" 2>/dev/null || echo "  (not found)"

    echo ""
    echo "--- Pods (requesters + launchers) ---"
    # Show all requester and launcher pods with FMA labels in one table
    kubectl get pods -n "$NAMESPACE" \
        -L dual-pods.llm-d.ai/dual,dual-pods.llm-d.ai/sleeping \
        --field-selector=status.phase!=Succeeded \
        2>/dev/null \
        | grep -E "^NAME|requester|^launcher" || echo "  (no FMA pods)"

    echo ""
    echo "--- External Metrics ---"
    QS=$(kubectl get --raw "/apis/external.metrics.k8s.io/v1beta1/namespaces/${NAMESPACE}/epp_queue_size" 2>/dev/null \
        | grep -o '"value":"[^"]*"' | head -1 | cut -d'"' -f4 || echo "n/a")
    RR=$(kubectl get --raw "/apis/external.metrics.k8s.io/v1beta1/namespaces/${NAMESPACE}/epp_running_requests" 2>/dev/null \
        | grep -o '"value":"[^"]*"' | head -1 | cut -d'"' -f4 || echo "n/a")
    echo "  epp_queue_size:        $QS"
    echo "  epp_running_requests:  $RR"

    echo ""
    echo "--- ReplicaSet ---"
    kubectl get rs fma-requester -n "$NAMESPACE" --no-headers 2>/dev/null || echo "  (not found)"

    sleep "$INTERVAL"
done
