#!/usr/bin/env bash
set -euo pipefail

cleanup() {
  echo "Cleaning up test pods"
  kubectl delete pod "${POD_NAME}" --ignore-not-found || true
  kubectl delete pod "${LAUNCHER_POD_NAME}" --ignore-not-found || true
  kubectl delete pod "${REQUESTER_POD_NAME}" --ignore-not-found || true
  kubectl delete pod "${UNBOUND_REQUESTER_POD_NAME}" --ignore-not-found || true
}

# Trap EXIT to run cleanup
trap 'rc=$?; cleanup; exit $rc' EXIT

# Test script for ValidatingAdmissionPolicy CEL rules
POD_NAME=${POD_NAME:-my-regular-test}
LAUNCHER_POD_NAME=${LAUNCHER_POD_NAME:-my-launcher-test}
REQUESTER_POD_NAME=${REQUESTER_POD_NAME:-my-requester-test}
UNBOUND_REQUESTER_POD_NAME=${UNBOUND_REQUESTER_POD_NAME:-my-unbound-requester-test}

if ! kubectl get validatingadmissionpolicy fma-immutable-fields fma-bound-serverreqpod >/dev/null 2>&1; then
  echo "ERROR: Required validating admission policies not found. Ensure run.sh installed them correctly."
  exit 1
fi

cat <<EOF | kubectl apply -f -
apiVersion: v1
kind: Pod
metadata:
  name: ${REQUESTER_POD_NAME}
  labels:
    app: dp-example
    dual-pods.llm-d.ai/dual: "${LAUNCHER_POD_NAME}"
  annotations:
    dual-pods.llm-d.ai/inference-server-config: "test-config"
spec:
  containers:
  - name: requester
    image: busybox
    command: ["/bin/sh","-c","sleep 3600"]
EOF

echo "Created server-requesting pod ${REQUESTER_POD_NAME} and waiting for it to become Ready"

if ! kubectl wait --for=condition=Ready pod/"${REQUESTER_POD_NAME}" --timeout=60s >/dev/null 2>&1; then
  echo "ERROR: pod ${REQUESTER_POD_NAME} did not become Ready within 60s"
  exit 2
fi

REQUESTER_UID=$(kubectl get pod "${REQUESTER_POD_NAME}" -o jsonpath='{.metadata.uid}')
if [ -z "${REQUESTER_UID}" ]; then
  echo "ERROR: Failed to get UID for requester pod ${REQUESTER_POD_NAME}"
  exit 3
fi

cat <<EOF | kubectl apply -f -
apiVersion: v1
kind: Pod
metadata:
  name: ${LAUNCHER_POD_NAME}
  labels:
    app: dp-example
    app.kubernetes.io/component: launcher
    dual-pods.llm-d.ai/generated-by: launcher-populator
    dual-pods.llm-d.ai/launcher-config-name: test-launcher-config
    dual-pods.llm-d.ai/node-name: test-node
    dual-pods.llm-d.ai/dual: "${REQUESTER_POD_NAME}"
  annotations:
    dual-pods.llm-d.ai/requester: "${REQUESTER_UID} ${REQUESTER_POD_NAME}"
spec:
  containers:
  - name: launcher
    image: busybox
    command: ["/bin/sh","-c","sleep 3600"]
EOF

echo "Created launcher pod ${LAUNCHER_POD_NAME} and waiting for it to become Ready"

if ! kubectl wait --for=condition=Ready pod/"${LAUNCHER_POD_NAME}" --timeout=60s >/dev/null 2>&1; then
  echo "ERROR: pod ${LAUNCHER_POD_NAME} did not become Ready within 60s"
  exit 4
fi

echo "Attempting to change immutable annotation 'dual-pods.llm-d.ai/requester' on launcher pod as a regular user — expect rejection"
if output=$(kubectl annotate pod "${LAUNCHER_POD_NAME}" "dual-pods.llm-d.ai/requester=xyz patched-requester" --overwrite 2>&1); then
  echo "ERROR: annotation change succeeded as regular user but should have been rejected"
  echo "kubectl output: ${output}"
  exit 5
else
  echo "SUCCESS: annotation change was rejected, as expected. kubectl output: ${output}"
fi

echo "Attempting to change immutable label 'dual-pods.llm-d.ai/dual' on launcher pod as a regular user — expect rejection"
if output=$(kubectl label pod "${LAUNCHER_POD_NAME}" "dual-pods.llm-d.ai/dual=patched-pod" --overwrite 2>&1); then
  echo "ERROR: label change succeeded as regular user but should have been rejected"
  echo "kubectl output: ${output}"
  exit 6
else
  echo "SUCCESS: label change was rejected, as expected. kubectl output: ${output}"
fi

echo "Attempting to delete immutable label 'dual-pods.llm-d.ai/dual' on launcher pod — expect rejection"
if output=$(kubectl label pod "${LAUNCHER_POD_NAME}" "dual-pods.llm-d.ai/dual-" 2>&1); then
  echo "ERROR: label deletion succeeded but should have been rejected"
  echo "kubectl output: ${output}"
  exit 7
else
  echo "SUCCESS: label deletion was rejected, as expected. kubectl output: ${output}"
fi

echo "Attempting to change immutable annotation 'dual-pods.llm-d.ai/inference-server-config' on bound pod as regular user — expect rejection"
if output=$(kubectl annotate pod "${REQUESTER_POD_NAME}" "dual-pods.llm-d.ai/inference-server-config=patched-config" --overwrite 2>&1); then
  echo "ERROR: bound pod annotation change succeeded as regular user but should have been rejected"
  echo "kubectl output: ${output}"
  exit 8
else
  echo "SUCCESS: bound pod annotation change was rejected, as expected. kubectl output: ${output}"
fi

echo "Attempting to delete 'dual-pods.llm-d.ai/inference-server-config' annotation"
if output=$(kubectl annotate pod "${REQUESTER_POD_NAME}" "dual-pods.llm-d.ai/inference-server-config-" 2>&1); then
  echo "ERROR: annotation deletion succeeded but should have been rejected"
  echo "kubectl output: ${output}"
  exit 9
else
  echo "SUCCESS: annotation deletion was rejected, as expected. kubectl output: ${output}"
fi

echo "Attempting to change non-protected label on bound pod ${REQUESTER_POD_NAME} — expect no rejection"
if output=$(kubectl label pod "${REQUESTER_POD_NAME}" "regular-label=yes" --overwrite 2>&1); then
  echo "SUCCESS: non-protected label update on bound pod allowed, as expected. kubectl output: ${output}"
else
  echo "ERROR: non-protected label update on bound pod was rejected but should have been allowed"
  echo "kubectl output: ${output}"
  exit 10
fi

cat <<EOF | kubectl apply -f -
apiVersion: v1
kind: Pod
metadata:
  name: ${UNBOUND_REQUESTER_POD_NAME}
  annotations:
    dual-pods.llm-d.ai/inference-server-config: "unbound-config"
spec:
  containers:
  - name: requester
    image: busybox
    command: ["/bin/sh","-c","sleep 3600"]
EOF

echo "Created unbound server requesting pod ${UNBOUND_REQUESTER_POD_NAME} and waiting for it to become Ready"

if ! kubectl wait --for=condition=Ready pod/"${UNBOUND_REQUESTER_POD_NAME}" --timeout=60s >/dev/null 2>&1; then
  echo "ERROR: pod ${UNBOUND_REQUESTER_POD_NAME} did not become Ready within 60s"
  exit 11
fi

echo "Attempting to update an unbound server pod (missing 'dual' label) — expect no rejection"
if output=$(kubectl label pod "${UNBOUND_REQUESTER_POD_NAME}" "regular-label=yes" --overwrite 2>&1); then
  echo "SUCCESS: Unbound pod update allowed, as expected. kubectl output: ${output}"
else
  echo "ERROR: Unbound pod update was rejected but should have been allowed"
  echo "kubectl output: ${output}"
  exit 12
fi

cat <<EOF | kubectl apply -f -
apiVersion: v1
kind: Pod
metadata:
  name: ${POD_NAME}
spec:
  containers:
  - name: main
    image: busybox
    command: ["/bin/sh","-c","sleep 3600"]
EOF

echo "Created regular pod ${POD_NAME} and waiting for it to become Ready"

if ! kubectl wait --for=condition=Ready pod/"${POD_NAME}" --timeout=60s >/dev/null 2>&1; then
  echo "ERROR: pod ${POD_NAME} did not become Ready within 60s"
  exit 13
fi

echo "Attempting to update a regular pod (no FMA fields) — expect no rejection"
if output=$(kubectl label pod "${POD_NAME}" "regular-label=yes" --overwrite 2>&1); then
  echo "SUCCESS: label update on regular pod allowed, as expected. kubectl output: ${output}"
else
  echo "ERROR: label update on regular pod was rejected but should have been allowed"
  echo "kubectl output: ${output}"
  exit 14
fi

echo "Test completed successfully"
