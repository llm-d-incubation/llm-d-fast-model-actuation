#!/usr/bin/env bash

# Generates sustained traffic against the Gateway to trigger HPA scale-up.
#
# Runs an in-cluster pod with N concurrent workers sending /v1/completions
# requests through the Gateway → EPP → vLLM path.
#
# Prerequisites:
#   - demo-fma-hpa-ocp.sh has been run (Gateway + EPP + FMA deployed)
#   - oc/kubectl authenticated
#
# Optional environment variables:
#   NAMESPACE   - target namespace (default: fma-hpa)
#   WORKERS     - number of concurrent workers (default: 300)
#   DURATION    - load duration in seconds (default: 120)
#   MODEL       - model name for completions (default: HuggingFaceTB/SmolLM2-360M-Instruct)
#   MAX_TOKENS  - max tokens per request (default: 4096)

set -euo pipefail

NAMESPACE="${NAMESPACE:-fma-hpa}"
WORKERS="${WORKERS:-300}"
DURATION="${DURATION:-120}"
MODEL="${MODEL:-HuggingFaceTB/SmolLM2-360M-Instruct}"
MAX_TOKENS="${MAX_TOKENS:-4096}"
POD_NAME="fma-loadgen"

cleanup() {
    echo ""
    echo "Cleaning up loadgen pod..."
    kubectl delete pod "$POD_NAME" -n "$NAMESPACE" --ignore-not-found --wait=false 2>/dev/null
}
trap cleanup EXIT

# Find the gateway service ClusterIP
GW_SVC=$(kubectl get svc -n "$NAMESPACE" -o name 2>/dev/null | grep -m1 gateway | sed 's|service/||' || true)
if [ -z "$GW_SVC" ]; then
    echo "ERROR: No gateway service found in namespace $NAMESPACE" >&2
    exit 1
fi
GW_IP=$(kubectl get svc "$GW_SVC" -n "$NAMESPACE" -o jsonpath='{.spec.clusterIP}' 2>/dev/null)
GW_PORT=$(kubectl get svc "$GW_SVC" -n "$NAMESPACE" -o jsonpath='{.spec.ports[0].port}' 2>/dev/null)
GW_URL="http://${GW_IP}:${GW_PORT}"

echo "========================================="
echo "  FMA + HPA Load Generator"
echo "========================================="
echo ""
echo "  Gateway:   $GW_URL ($GW_SVC)"
echo "  Model:     $MODEL"
echo "  Workers:   $WORKERS"
echo "  Duration:  ${DURATION}s"
echo "  MaxTokens: $MAX_TOKENS"
echo ""

# Delete any leftover pod
kubectl delete pod "$POD_NAME" -n "$NAMESPACE" --ignore-not-found 2>/dev/null

echo "Starting loadgen pod..."
kubectl run "$POD_NAME" -n "$NAMESPACE" --rm -i --restart=Never \
    --image=curlimages/curl:latest -- sh -c "
GW=\"${GW_URL}\"
MODEL=\"${MODEL}\"
WORKERS=${WORKERS}
DURATION=${DURATION}
MAX_TOKENS=${MAX_TOKENS}

send_request() {
  while true; do
    curl -s -o /dev/null -w '' \"\$GW/v1/completions\" \
      -H 'Content-Type: application/json' \
      -d '{\"model\":\"\$MODEL\",\"prompt\":\"Write a very long and detailed essay about the history of artificial intelligence from the 1950s to the present day, covering all major breakthroughs, researchers, institutions, and technological advances in great detail.\",\"max_tokens\":'\$MAX_TOKENS',\"temperature\":0.9,\"stream\":false}' 2>/dev/null
  done
}

echo \"[loadgen] Starting \$WORKERS workers for \${DURATION}s against \$GW\"
for i in \$(seq 1 \$WORKERS); do
  send_request &
done

sleep \$DURATION
echo \"[loadgen] Duration reached, stopping workers...\"
kill \$(jobs -p) 2>/dev/null
wait 2>/dev/null
echo \"[loadgen] Done\"
"

echo ""
echo "Load generation finished."
