#!/usr/bin/env bash
set -euo pipefail

cleanup() {
  echo "Cleaning up test resources"
  kubectl delete pod "${POD_NAME}" --ignore-not-found || true
  kubectl delete pod "${LAUNCHER_POD_NAME}" --ignore-not-found || true
  kubectl delete replicaset "${REQUESTER_POD_NAME}" --ignore-not-found || true
  kubectl delete pod "${UNBOUND_REQUESTER_POD_NAME}" --ignore-not-found || true
  kubectl delete inferenceserverconfig "${INFERENCE_SERVER_CONFIG_NAME}" --ignore-not-found || true
  kubectl delete launcherconfig "${LAUNCHER_CONFIG_NAME}" --ignore-not-found || true
}

# Trap EXIT to run cleanup
trap 'rc=$?; cleanup; exit $rc' EXIT

# Test script for ValidatingAdmissionPolicy CEL rules
POD_NAME=${POD_NAME:-my-regular-test}
LAUNCHER_POD_NAME=${LAUNCHER_POD_NAME:-my-launcher-test}
REQUESTER_POD_NAME=${REQUESTER_POD_NAME:-my-requester-test}
UNBOUND_REQUESTER_POD_NAME=${UNBOUND_REQUESTER_POD_NAME:-my-unbound-requester-test}
LAUNCHER_CONFIG_NAME=${LAUNCHER_CONFIG_NAME:-test-launcher-config}
INFERENCE_SERVER_CONFIG_NAME=${INFERENCE_SERVER_CONFIG_NAME:-test-config}

if ! kubectl get validatingadmissionpolicy fma-immutable-fields fma-bound-serverreqpod >/dev/null 2>&1; then
  echo "ERROR: Required validating admission policies not found. Ensure run.sh installed them correctly."
  exit 1
fi

requester_img=$(make echo-var VAR=TEST_REQUESTER_IMG)
launcher_img=$(make echo-var VAR=TEST_LAUNCHER_IMG)

cat <<EOF | kubectl apply -f -
apiVersion: fma.llm-d.ai/v1alpha1
kind: LauncherConfig
metadata:
  name: ${LAUNCHER_CONFIG_NAME}
spec:
  maxSleepingInstances: 3
  podTemplate:
    spec:
      containers:
        - name: inference-server
          image: $launcher_img
          imagePullPolicy: Never
          command:
          - /bin/bash
          - "-c"
          args:
          - |
            uvicorn launcher:app \
            --host 0.0.0.0 \
            --log-level info \
            --port 8001
---
apiVersion: fma.llm-d.ai/v1alpha1
kind: InferenceServerConfig
metadata:
  name: ${INFERENCE_SERVER_CONFIG_NAME}
spec:
  launcherConfigName: ${LAUNCHER_CONFIG_NAME}
  modelServerConfig:
    port: 8005
    options: "--model TinyLlama/TinyLlama-1.1B-Chat-v1.0"
    env_vars:
      VLLM_SERVER_DEV_MODE: "1"
      VLLM_USE_V1: "1"
      VLLM_LOGGING_LEVEL: "DEBUG"
    labels:
      component: inference
    annotations:
      description: "Example InferenceServerConfig"
EOF

cat <<EOF | kubectl apply -f -
apiVersion: apps/v1
kind: ReplicaSet
metadata:
  name: ${REQUESTER_POD_NAME}
  labels:
    app: validation-example
spec:
  replicas: 1
  selector:
    matchLabels:
      app: validation-example
  template:
    metadata:
      labels:
        app: validation-example
      annotations:
        dual-pods.llm-d.ai/admin-port: "8081"
        dual-pods.llm-d.ai/inference-server-config: "${INFERENCE_SERVER_CONFIG_NAME}"
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
          ports:
          - name: probes
            containerPort: 8080
          - name: spi
            containerPort: 8081
          readinessProbe:
            httpGet:
              path: /ready
              port: 8080
            initialDelaySeconds: 2
            periodSeconds: 5
          resources:
            limits:
              nvidia.com/gpu: "1"
              cpu: "200m"
              memory: 250Mi
      serviceAccount: testreq
EOF

echo "Created server-requesting ReplicaSet ${REQUESTER_POD_NAME}"

kubectl wait --for=condition=Ready pod -l app=validation-example --timeout=120s
REQUESTER_POD=$(kubectl get pods -l app=validation-example -o jsonpath='{.items[0].metadata.name}')
if [ -z "${REQUESTER_POD}" ]; then
  echo "ERROR: Failed to find pod created by ReplicaSet ${REQUESTER_POD_NAME}"
  exit 3
fi

REQUESTER_UID=$(kubectl get pod "${REQUESTER_POD}" -o jsonpath='{.metadata.uid}')
if [ -z "${REQUESTER_UID}" ]; then
  echo "ERROR: Failed to get UID for requester pod ${REQUESTER_POD}"
  exit 3
fi

cat <<EOF | kubectl apply -f -
apiVersion: v1
kind: Pod
metadata:
  name: ${LAUNCHER_POD_NAME}
  labels:
    app.kubernetes.io/component: launcher
    dual-pods.llm-d.ai/generated-by: launcher-populator
    dual-pods.llm-d.ai/launcher-config-name: "${LAUNCHER_CONFIG_NAME}"
    dual-pods.llm-d.ai/dual: "${REQUESTER_POD}"
  annotations:
    dual-pods.llm-d.ai/requester: "${REQUESTER_UID} ${REQUESTER_POD}"
spec:
  containers:
  - name: inference-server
    image: $launcher_img
    imagePullPolicy: IfNotPresent
    command:
    - /bin/bash
    - "-c"
    args:
    - |
      uvicorn launcher:app \
        --host 0.0.0.0 \
        --log-level info \
        --port 8001
EOF

echo "Created launcher pod ${LAUNCHER_POD_NAME}"

for i in {1..15}; do
  REQUESTER_DUAL=$(kubectl get pod "${REQUESTER_POD}" -o jsonpath='{.metadata.labels.dual-pods\.llm-d\.ai/dual}' 2>/dev/null || echo "")
  if [ "${REQUESTER_DUAL}" = "${LAUNCHER_POD_NAME}" ]; then
    echo "Binding established: ${REQUESTER_POD} pod has dual label pointing to ${LAUNCHER_POD_NAME}"
    break
  fi
  sleep 1
done

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
if output=$(kubectl annotate pod "${REQUESTER_POD}" "dual-pods.llm-d.ai/inference-server-config=patched-config" --overwrite 2>&1); then
  echo "ERROR: bound pod annotation change succeeded as regular user but should have been rejected"
  echo "kubectl output: ${output}"
  exit 8
else
  echo "SUCCESS: bound pod annotation change was rejected, as expected. kubectl output: ${output}"
fi

echo "Attempting to delete 'dual-pods.llm-d.ai/inference-server-config' annotation"
if output=$(kubectl annotate pod "${REQUESTER_POD}" "dual-pods.llm-d.ai/inference-server-config-" 2>&1); then
  echo "ERROR: annotation deletion succeeded but should have been rejected"
  echo "kubectl output: ${output}"
  exit 9
else
  echo "SUCCESS: annotation deletion was rejected, as expected. kubectl output: ${output}"
fi

echo "Attempting to change non-protected label on bound pod ${REQUESTER_POD} — expect no rejection"
if output=$(kubectl label pod "${REQUESTER_POD}" "regular-label=yes" --overwrite 2>&1); then
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

echo "Attempting to modify protected annotation on unbound server pod (missing 'dual' label) — expect no rejection"
if output=$(kubectl annotate pod "${UNBOUND_REQUESTER_POD_NAME}" "dual-pods.llm-d.ai/inference-server-config=new-unbound-config" --overwrite 2>&1); then
  echo "SUCCESS: annotation change on unbound pod allowed, as expected. kubectl output: ${output}"
else
  echo "ERROR: annotation change on unbound pod was rejected but should have been allowed"
  echo "kubectl output: ${output}"
  exit 11
fi

echo "Attempting to update an unbound server pod (missing 'dual' label) — expect no rejection"
if output=$(kubectl label pod "${UNBOUND_REQUESTER_POD_NAME}" "regular-label=yes" --overwrite 2>&1); then
  echo "SUCCESS: Unbound pod update allowed, as expected. kubectl output: ${output}"
else
  echo "ERROR: Unbound pod update was rejected but should have been allowed"
  echo "kubectl output: ${output}"
  exit 14
fi

cat <<EOF | kubectl apply -f -
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

echo "Attempting to update a regular pod (no FMA fields) — expect no rejection"
if output=$(kubectl label pod "${POD_NAME}" "regular-label=yes" --overwrite 2>&1); then
  echo "SUCCESS: label update on regular pod allowed, as expected. kubectl output: ${output}"
else
  echo "ERROR: label update on regular pod was rejected but should have been allowed"
  echo "kubectl output: ${output}"
  exit 13
fi

echo "Test completed successfully"
