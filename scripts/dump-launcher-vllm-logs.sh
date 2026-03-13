#!/usr/bin/env bash
# Dump vLLM instance logs from all launcher pods.
#
# Usage: dump-launcher-vllm-logs.sh [namespace]
#   namespace: Kubernetes namespace (defaults to kubectl current context)

set -euo pipefail

NS_FLAG=()
if [ -n "${1:-}" ]; then
  NS_FLAG=(-n "$1")
fi

echo "Fetching vLLM instance logs from launcher pods..."

LAUNCHER_PODS=$(kubectl get pods "${NS_FLAG[@]}" \
  -l "dual-pods.llm-d.ai/launcher-config-name" \
  -o jsonpath='{.items[*].metadata.name}' 2>/dev/null || true)

if [ -z "$LAUNCHER_PODS" ]; then
  echo "No launcher pods found"
  exit 0
fi

for LAUNCHER_POD in $LAUNCHER_PODS; do
  echo "=========================================="
  echo "=== Launcher pod: $LAUNCHER_POD ==="
  echo "=========================================="

  kubectl port-forward "${NS_FLAG[@]}" "pod/$LAUNCHER_POD" 18001:8001 &
  PF_PID=$!
  sleep 2

  # Get list of vLLM instances
  echo ""
  echo "=== vLLM instances status ==="
  INSTANCES_JSON=$(curl -s "http://localhost:18001/v2/vllm/instances" || true)
  echo "$INSTANCES_JSON" | jq . 2>/dev/null || echo "$INSTANCES_JSON"

  # Get instance IDs
  INSTANCE_IDS=$(echo "$INSTANCES_JSON" | jq -r '.instances[].instance_id // empty' 2>/dev/null || true)

  if [ -z "$INSTANCE_IDS" ]; then
    echo "No vLLM instances found on launcher: $LAUNCHER_POD"
  else
    # Fetch logs for each instance
    for id in $INSTANCE_IDS; do
      echo ""
      echo "=== vLLM instance $id log ==="
      curl -s "http://localhost:18001/v2/vllm/instances/$id/log" || true
      echo ""
    done
  fi

  # Clean up
  kill $PF_PID 2>/dev/null || true
  wait $PF_PID 2>/dev/null || true
done
