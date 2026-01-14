#!/usr/bin/env bash
set -euo pipefail

cleanup() {
  echo "Cleaning up test pods"
  kubectl delete pod "${POD_NAME}" --ignore-not-found || true
  kubectl delete pod "${BOUND_POD_NAME}" --ignore-not-found || true
}

# Trap EXIT to run cleanup
trap 'rc=$?; cleanup; exit $rc' EXIT

# Test script for ValidatingAdmissionPolicy CEL rules
POD_NAME=${POD_NAME:-my-request-test}
BOUND_POD_NAME=${BOUND_POD_NAME:-my-bound-request-test}

if kubectl api-resources --api-group=admissionregistration.k8s.io -o name | grep -q 'validatingadmissionpolicies'; then
  echo "Cluster supports ValidatingAdmissionPolicy (CEL)"
  if command -v helm >/dev/null 2>&1 && [ -d "charts/dpctlr" ]; then
    helm template dpctlr charts/dpctlr --set policies.enabled=true | kubectl apply -f -
    sleep 5 # wait for policies to be registered
  else
    echo "Helm not available or 'charts/dpctlr' missing; cannot install policies automatically."
    exit 1
  fi
else
  echo "Cluster does not support ValidatingAdmissionPolicy (CEL). Skipping tests."
  exit 0
fi

cat <<EOF | kubectl apply -f -
apiVersion: v1
kind: Pod
metadata:
  name: ${POD_NAME}
  labels:
    app: dp-example
    app.kubernetes.io/component: launcher
    dual-pods.llm-d.ai/generated-by: launcher-populator
    dual-pods.llm-d.ai/launcher-config-name: test-launcher-config
    dual-pods.llm-d.ai/node-name: test-node
    dual-pods.llm-d.ai/dual: "launcher-1"
  annotations:
    dual-pods.llm-d.ai/requester: "abcd test-requester"
    dual-pods.llm-d.ai/status: "ok"
spec:
  containers:
  - name: requester
    image: busybox
    command: ["/bin/sh","-c","sleep 3600"]
EOF

echo "Created launcher pod ${POD_NAME}"

echo "Attempting to change immutable annotation 'dual-pods.llm-d.ai/requester' as a regular user — expect rejection"
if output=$(kubectl patch pod "${POD_NAME}" -p '{"metadata":{"annotations":{"dual-pods.llm-d.ai/requester":"xyz patched-requester"}}}' --type=merge 2>&1); then
  echo "ERROR: annotation patch succeeded as regular user but should have been rejected"
  echo "kubectl output: ${output}"
  exit 3
else
  echo "SUCCESS: annotation patch was rejected, as expected. kubectl output: ${output}"
fi

echo "Attempting to change immutable label 'dual-pods.llm-d.ai/dual' as a regular user — expect rejection"
if output=$(kubectl patch pod "${POD_NAME}" -p '{"metadata":{"labels":{"dual-pods.llm-d.ai/dual":"patched-pod"}}}' --type=merge 2>&1); then
  echo "ERROR: label patch succeeded as regular user but should have been rejected"
  echo "kubectl output: ${output}"
  exit 4
else
  echo "SUCCESS: label patch was rejected, as expected. kubectl output: ${output}"
fi

cat <<EOF | kubectl apply -f -
apiVersion: v1
kind: Pod
metadata:
  name: ${BOUND_POD_NAME}
  labels:
    app: dp-example
    dual-pods.llm-d.ai/dual: "bound-1"
  annotations:
    dual-pods.llm-d.ai/inference-server-config: '{"model":"test"}'
spec:
  containers:
  - name: requester
    image: busybox
    command: ["/bin/sh","-c","sleep 3600"]
EOF

echo "Created bound server requesting pod ${BOUND_POD_NAME}"

echo "Attempting to change immutable annotation 'dual-pods.llm-d.ai/inference-server-config' on bound pod as regular user — expect rejection"
if output=$(kubectl patch pod "${BOUND_POD_NAME}" -p '{"metadata":{"annotations":{"dual-pods.llm-d.ai/inference-server-config":"{\"model\":\"patched\"}"}}}' --type=merge 2>&1); then
  echo "ERROR: bound pod patch succeeded as regular user but should have been rejected"
  echo "kubectl output: ${output}"
  exit 5
else
  echo "SUCCESS: bound pod patch was rejected, as expected. kubectl output: ${output}"
fi

echo "Test completed successfully"
