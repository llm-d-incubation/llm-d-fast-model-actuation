#!/usr/bin/env bash
set -euo pipefail

# Test script for ValidatingAdmissionPolicy CEL rules

# Required environment variables:
#   FMA_NAMESPACE
#   reqlb
#   launcherlb

# Verify required variables are set
if [ -z "${reqlb:-}" ] || [ -z "${launcherlb:-}" ] || [ -z "${FMA_NAMESPACE:-}" ]; then
  echo "ERROR: This script must be called with environment variables FMA_NAMESPACE, reqlb, and launcherlb defined" >&2
  exit 1
fi

if ! kubectl get -n "$FMA_NAMESPACE" pod "${reqlb}" > /dev/null ; then
    echo "ERROR: server-requesting Pod $reqlb does not exist in namespace $FMA_NAMESPACE!" >&2
    exit 1
fi
if ! kubectl get -n "$FMA_NAMESPACE" pod "${launcherlb}" > /dev/null ; then
    echo "ERROR: launcher Pod $launcherlb does not exist in namespace $FMA_NAMESPACE!" >&2
    exit 1
fi


cleanup() {
  echo "Cleaning up test resources"
  kubectl delete -n "$FMA_NAMESPACE" pod "${POD_NAME}" --ignore-not-found || true
  kubectl delete -n "$FMA_NAMESPACE" pod "${UNBOUND_REQUESTER_POD_NAME}" --ignore-not-found || true
}

# Trap EXIT to run cleanup
trap 'rc=$?; cleanup; exit $rc' EXIT

POD_NAME=${POD_NAME:-my-regular-test}
UNBOUND_REQUESTER_POD_NAME=${UNBOUND_REQUESTER_POD_NAME:-my-unbound-requester-test}

if ! kubectl get validatingadmissionpolicy fma-immutable-fields fma-bound-serverreqpod >/dev/null 2>&1; then
  echo "ERROR: Required validating admission policies not found. Ensure they are installed correctly."
  exit 1
fi

echo "=== Running ValidatingAdmissionPolicy Tests ==="

echo "Test 1: Attempting to change immutable annotation 'dual-pods.llm-d.ai/requester' on launcher pod — expect rejection"
if output=$(kubectl annotate -n "$FMA_NAMESPACE" pod "${launcherlb}" "dual-pods.llm-d.ai/requester=xyz patched-requester" --overwrite 2>&1); then
  echo "ERROR: annotation change succeeded but should have been rejected"
  echo "kubectl output: ${output}"
  exit 5
else
  echo "✓ SUCCESS: annotation change was rejected, as expected"
fi

echo "Test 2: Attempting to change immutable label 'dual-pods.llm-d.ai/dual' on launcher pod — expect rejection"
if output=$(kubectl label -n "$FMA_NAMESPACE" pod "${launcherlb}" "dual-pods.llm-d.ai/dual=patched-pod" --overwrite 2>&1); then
  echo "ERROR: label change succeeded but should have been rejected"
  echo "kubectl output: ${output}"
  exit 6
else
  echo "✓ SUCCESS: label change was rejected, as expected"
fi

echo "Test 3: Attempting to delete immutable label 'dual-pods.llm-d.ai/dual' on launcher pod — expect rejection"
if output=$(kubectl label -n "$FMA_NAMESPACE" pod "${launcherlb}" "dual-pods.llm-d.ai/dual-" 2>&1); then
  echo "ERROR: label deletion succeeded but should have been rejected"
  echo "kubectl output: ${output}"
  exit 7
else
  echo "✓ SUCCESS: label deletion was rejected, as expected"
fi

echo "Test 4: Attempting to change immutable annotation 'dual-pods.llm-d.ai/inference-server-config' on bound pod — expect rejection"
if output=$(kubectl annotate -n "$FMA_NAMESPACE" pod "${reqlb}" "dual-pods.llm-d.ai/inference-server-config=patched-config" --overwrite 2>&1); then
  echo "ERROR: bound pod annotation change succeeded but should have been rejected"
  echo "kubectl output: ${output}"
  exit 8
else
  echo "✓ SUCCESS: bound pod annotation change was rejected, as expected"
fi

echo "Test 5: Attempting to delete 'dual-pods.llm-d.ai/inference-server-config' annotation — expect rejection"
if output=$(kubectl annotate -n "$FMA_NAMESPACE" pod "${reqlb}" "dual-pods.llm-d.ai/inference-server-config-" 2>&1); then
  echo "ERROR: annotation deletion succeeded but should have been rejected"
  echo "kubectl output: ${output}"
  exit 9
else
  echo "✓ SUCCESS: annotation deletion was rejected, as expected"
fi

echo "Test 6: Attempting to change non-protected label on bound pod — expect no rejection"
if output=$(kubectl label -n "$FMA_NAMESPACE" pod "${reqlb}" "regular-label=yes" --overwrite 2>&1); then
  echo "✓ SUCCESS: non-protected label update on bound pod allowed, as expected"
else
  echo "ERROR: non-protected label update on bound pod was rejected but should have been allowed"
  echo "kubectl output: ${output}"
  exit 10
fi

if [ "${FMA_NAMESPACE}" != default ]; then
    echo "Tests 7 and 8 need work before they can run on OpenShift"
else
    
requester_img=$(make echo-var VAR=TEST_REQUESTER_IMG)

cat <<EOF | kubectl apply -n "$FMA_NAMESPACE" -f -
apiVersion: v1
kind: Pod
metadata:
  name: ${UNBOUND_REQUESTER_POD_NAME}
  annotations:
    dual-pods.llm-d.ai/inference-server-config: unbound-config
spec:
  containers:
  - name: inference-server
    image: $requester_img
    imagePullPolicy: IfNotPresent
    command:
    - /ko-app/test-requester
    - --node=\$(NODE_NAME)
    - --pod-uid=\$(POD_UID)
    - --namespace=\$(NAMESPACE)
    env:
      - name: NODE_NAME
        valueFrom:
          fieldRef: { fieldPath: spec.nodeName }
      - name: POD_UID
        valueFrom:
          fieldRef: { fieldPath: metadata.uid }
      - name: NAMESPACE
        valueFrom:
          fieldRef: { fieldPath: metadata.namespace }
EOF

echo "Created unbound server requesting pod ${UNBOUND_REQUESTER_POD_NAME}"

echo "Test 7: Attempting to modify protected annotation on unbound server pod (missing 'dual' label) — expect no rejection"
if output=$(kubectl annotate -n "$FMA_NAMESPACE" pod "${UNBOUND_REQUESTER_POD_NAME}" "dual-pods.llm-d.ai/inference-server-config=new-unbound-config" --overwrite 2>&1); then
  echo "✓ SUCCESS: annotation change on unbound pod allowed, as expected"
else
  echo "ERROR: annotation change on unbound pod was rejected but should have been allowed"
  echo "kubectl output: ${output}"
  exit 11
fi

echo "Test 8: Attempting to update an unbound server pod (missing 'dual' label) — expect no rejection"
if output=$(kubectl label -n "$FMA_NAMESPACE" pod "${UNBOUND_REQUESTER_POD_NAME}" "regular-label=yes" --overwrite 2>&1); then
  echo "✓ SUCCESS: Unbound pod update allowed, as expected"
else
  echo "ERROR: Unbound pod update was rejected but should have been allowed"
  echo "kubectl output: ${output}"
  exit 14
fi

fi # [ -n "$RUNTIME_CLASS_NAME" ]

cat <<EOF | kubectl apply -n "$FMA_NAMESPACE" -f -
apiVersion: v1
kind: Pod
metadata:
  name: ${POD_NAME}
spec:
  containers:
  - name: main
    image: alpine:latest
    imagePullPolicy: IfNotPresent
    command: ["/bin/sh","-c","sleep 3600"]
EOF

echo "Created regular pod ${POD_NAME}"

echo "Test 9: Attempting to update a regular pod (no FMA fields) — expect no rejection"
if output=$(kubectl label -n "$FMA_NAMESPACE" pod "${POD_NAME}" "regular-label=yes" --overwrite 2>&1); then
  echo "✓ SUCCESS: label update on regular pod allowed, as expected"
else
  echo "ERROR: label update on regular pod was rejected but should have been allowed"
  echo "kubectl output: ${output}"
  exit 13
fi

echo ""
echo "=== All ValidatingAdmissionPolicy tests passed successfully ==="
