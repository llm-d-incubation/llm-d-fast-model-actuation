#!/usr/bin/env bash
set -euo pipefail

# Test script for ValidatingAdmissionPolicy CEL rules
NS=fma-cel-test
POD_NAME=my-request-test
BOUND_POD_NAME=my-bound-request-test

echo "Removing existing namespace ${NS} if present"
kubectl delete namespace ${NS} --ignore-not-found --wait=true || true

echo "Creating test namespace ${NS}"
kubectl create namespace ${NS} --dry-run=client -o yaml | kubectl apply -f -

if kubectl api-resources --api-group=admissionregistration.k8s.io -o name | grep -q 'validatingadmissionpolicies'; then
  echo "Removing any existing policies and bindings"
  kubectl delete -f config/policies/validating-admission-policy-binding-fields.yaml --ignore-not-found --wait=true || true
  kubectl delete -f config/policies/validating-admission-policy-bindings-serverReqPod.yaml --ignore-not-found --wait=true || true
  kubectl delete -f config/policies/validating-admission-policy-immutable-fields.yaml --ignore-not-found --wait=true || true
  kubectl delete -f config/policies/validating-admission-policy-bound-serverReqPod.yaml --ignore-not-found --wait=true || true

  echo "Applying CEL policies and bindings"
  kubectl apply -f config/policies/validating-admission-policy-immutable-fields.yaml
  kubectl apply -f config/policies/validating-admission-policy-bound-serverReqPod.yaml
  kubectl apply -f config/policies/validating-admission-policy-binding-fields.yaml
  kubectl apply -f config/policies/validating-admission-policy-bindings-serverReqPod.yaml

  echo "Checking policies presence"
  kubectl get validatingadmissionpolicy fma-immutable-fields fma-bound-serverreqpod >/dev/null 2>&1 && echo "policies present" || (echo "policy missing"; exit 1)
else
  echo "Cluster does not support ValidatingAdmissionPolicy (CEL). Skipping policy apply and tests."
  exit 0
fi

cat <<EOF | kubectl apply -n ${NS} -f -
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
    dual-pods.llm-d.ai/sleeping: "false"
  annotations:
    dual-pods.llm-d.ai/nominal: "test-nominal"
spec:
  containers:
  - name: requester
    image: busybox
    command: ["/bin/sh","-c","sleep 3600"]
EOF

echo "Created launcher pod ${POD_NAME} in ${NS}"

echo "Attempting to change immutable annotation as a regular user — expect rejection"
set +e
PATCH_CMD=$(kubectl patch pod ${POD_NAME} -n ${NS} -p '{"metadata":{"annotations":{"dual-pods.llm-d.ai/nominal":"test-nominal-patched"}}}' --type=merge 2>&1)
PATCH_RC=$?
set -e

if [ ${PATCH_RC} -eq 0 ]; then
  echo "ERROR: annotation patch succeeded as regular user but should have been rejected by policies"
  echo "kubectl output: ${PATCH_CMD}"
  exit 3
else
  echo "SUCCESS: annotation patch was rejected, as expected. kubectl output:"
  echo "${PATCH_CMD}"
fi

echo "Attempting to change immutable label as a regular user — expect rejection"
set +e
PATCH_CMD=$(kubectl patch pod ${POD_NAME} -n ${NS} -p '{"metadata":{"labels":{"dual-pods.llm-d.ai/sleeping":"true"}}}' --type=merge 2>&1)
PATCH_RC=$?
set -e

if [ ${PATCH_RC} -eq 0 ]; then
  echo "ERROR: label patch succeeded as regular user but should have been rejected by policies"
  echo "kubectl output: ${PATCH_CMD}"
  exit 4
else
  echo "SUCCESS: label patch was rejected, as expected. kubectl output:"
  echo "${PATCH_CMD}"
fi

cat <<EOF | kubectl apply -n ${NS} -f -
apiVersion: v1
kind: Pod
metadata:
  name: ${BOUND_POD_NAME}
  labels:
    app: dp-example
    dual-pods.llm-d.ai/dual: "bound-1"
  annotations:
    dual-pods.llm-d.ai/inference-server-config: '{"model":"test"}'
    dual-pods.llm-d.ai/admin-port: "8081"
    dual-pods.llm-d.ai/accelerators: "GPU-0"
spec:
  containers:
  - name: requester
    image: busybox
    command: ["/bin/sh","-c","sleep 3600"]
EOF

echo "Created bound server requesting pod ${BOUND_POD_NAME} in ${NS}"

echo "Attempting to change admin-port on bound pod as regular user — expect rejection"
set +e
PATCH_CMD=$(kubectl patch pod ${BOUND_POD_NAME} -n ${NS} -p '{"metadata":{"annotations":{"dual-pods.llm-d.ai/admin-port":"9999"}}}' --type=merge 2>&1)
PATCH_RC=$?
set -e

if [ ${PATCH_RC} -eq 0 ]; then
  echo "ERROR: bound pod admin-port patch succeeded as regular user but should have been rejected by policies"
  echo "kubectl output: ${PATCH_CMD}"
  exit 5
else
  echo "SUCCESS: bound pod admin-port patch was rejected, as expected. kubectl output:"
  echo "${PATCH_CMD}"
fi

echo "Cleaning up test pods and namespace"
kubectl delete pod ${POD_NAME} -n ${NS} --ignore-not-found
kubectl delete pod ${BOUND_POD_NAME} -n ${NS} --ignore-not-found
kubectl delete namespace ${NS} --ignore-not-found

echo "Test completed successfully"
