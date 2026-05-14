#!/usr/bin/env bash

# Creates test Kubernetes objects for the OpenShift / real-cluster E2E path.
#
# Usage: mkobjs-openshift.sh [-n <namespace>] [--node <node-name>]
#
# Required environment variables:
#   LAUNCHER_IMAGE   - container image for the launcher pod
#   REQUESTER_IMAGE  - container image for the requester pod
#
# Optional environment variables:
#   RUNTIME_CLASS_NAME - if set, injects runtimeClassName into pod specs
#   IMAGE_PULL_POLICY  - image pull policy (default: Always)
#
# Outputs (one per line, to be parsed by caller):
#   isc_smol lc rs isc_qwen isc_tinyllama lpp

set -euo pipefail

# Parse optional flags
ns_flag=()
node_name=""
while [ $# -gt 0 ]; do
    case "$1" in
        -n|--namespace)
            if [ $# -gt 1 ] ; then
                ns_flag=(-n "$2")
                shift 2
            else
                echo "Missing --namespace argument" >&2
                exit 1
            fi
            ;;
        --node)
            if [ $# -gt 1 ] ; then
                node_name="$2"
                shift 2
            else
                echo "Missing --node argument" >&2
                exit 1
            fi
            ;;
        *)
            echo "Unknown argument: $1" >&2
            exit 1
            ;;
    esac
done

: "${LAUNCHER_IMAGE:?LAUNCHER_IMAGE is required}"
: "${REQUESTER_IMAGE:?REQUESTER_IMAGE is required}"

pull_policy="${IMAGE_PULL_POLICY:-Always}"
inst=$(date +%d-%H-%M-%S)

runtime_class=""
if [ -n "${RUNTIME_CLASS_NAME:-}" ]; then
    runtime_class="runtimeClassName: ${RUNTIME_CLASS_NAME}"
fi

# When a node is specified, pin the ReplicaSet's pods to it.
if [ -n "$node_name" ]; then
    node_selector="nodeSelector:
        kubernetes.io/hostname: \"$node_name\""
else
    node_selector=""
fi

if out=$(kubectl apply "${ns_flag[@]}" -f - 2>&1 <<EOF
apiVersion: v1
kind: ServiceAccount
metadata:
  name: launcher-$inst
  labels:
    fma-e2e-instance: "$inst"
---
apiVersion: rbac.authorization.k8s.io/v1
kind: Role
metadata:
  name: launcher-pod-state-writer-$inst
  labels:
    fma-e2e-instance: "$inst"
rules:
- apiGroups:
  - ""
  resources:
  - pods
  verbs:
  - get
  - patch
---
apiVersion: rbac.authorization.k8s.io/v1
kind: RoleBinding
metadata:
  name: launcher-pod-state-writer-$inst
  labels:
    fma-e2e-instance: "$inst"
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: Role
  name: launcher-pod-state-writer-$inst
subjects:
- kind: ServiceAccount
  name: launcher-$inst
---
apiVersion: fma.llm-d.ai/v1alpha1
kind: InferenceServerConfig
metadata:
  name: inference-server-config-smol-$inst
  labels:
    fma-e2e-instance: "$inst"
spec:
  modelServerConfig:
    port: 8005
    options: "--model HuggingFaceTB/SmolLM2-360M-Instruct --enable-sleep-mode"
    env_vars:
      VLLM_SERVER_DEV_MODE: "1"
      VLLM_USE_V1: "1"
      VLLM_LOGGING_LEVEL: "DEBUG"
    labels:
      e2e-test.fma.llm-d.ai/isc-label: test-value
    annotations:
      e2e-test.fma.llm-d.ai/isc-annotation: test-value
  launcherConfigName: "$inst"
---
apiVersion: fma.llm-d.ai/v1alpha1
kind: InferenceServerConfig
metadata:
  name: inference-server-config-qwen-$inst
  labels:
    fma-e2e-instance: "$inst"
spec:
  modelServerConfig:
    port: 8006
    options: "--model Qwen/Qwen2.5-0.5B-Instruct --enable-sleep-mode"
    env_vars:
      VLLM_SERVER_DEV_MODE: "1"
      VLLM_USE_V1: "1"
      VLLM_LOGGING_LEVEL: "DEBUG"
    labels:
      e2e-test.fma.llm-d.ai/isc-label: test-value
    annotations:
      e2e-test.fma.llm-d.ai/isc-annotation: test-value
  launcherConfigName: "$inst"
---
apiVersion: fma.llm-d.ai/v1alpha1
kind: InferenceServerConfig
metadata:
  name: inference-server-config-tinyllama-$inst
  labels:
    fma-e2e-instance: "$inst"
spec:
  modelServerConfig:
    port: 8007
    options: "--model TinyLlama/TinyLlama-1.1B-Chat-v1.0 --enable-sleep-mode"
    env_vars:
      VLLM_SERVER_DEV_MODE: "1"
      VLLM_USE_V1: "1"
      VLLM_LOGGING_LEVEL: "DEBUG"
    labels:
      e2e-test.fma.llm-d.ai/isc-label: test-value
    annotations:
      e2e-test.fma.llm-d.ai/isc-annotation: test-value
  launcherConfigName: "$inst"
---
apiVersion: fma.llm-d.ai/v1alpha1
kind: LauncherConfig
metadata:
  name: "$inst"
  labels:
    fma-e2e-instance: "$inst"
spec:
  maxSleepingInstances: 3
  podTemplate:
    metadata:
      labels:
        e2e-test.fma.llm-d.ai/template-label: from-launcher-config
      annotations:
        e2e-test.fma.llm-d.ai/template-annotation: from-launcher-config
    spec:
      ${runtime_class}
      serviceAccountName: launcher-$inst
      containers:
        - name: inference-server
          image: ${LAUNCHER_IMAGE}
          imagePullPolicy: ${pull_policy}
          command:
          - /app/launcher.py
          - --host=0.0.0.0
          - --log-level=info
          - --port=8001
          env:
          - name: HF_HOME
            value: "/tmp"
          - name: VLLM_CACHE_ROOT
            value: "/tmp"
          - name: FLASHINFER_WORKSPACE_BASE
            value: "/tmp"
          - name: TRITON_CACHE_DIR
            value: "/tmp"
          - name: XDG_CACHE_HOME
            value: "/tmp"
          - name: XDG_CONFIG_HOME
            value: "/tmp"
---
apiVersion: fma.llm-d.ai/v1alpha1
kind: LauncherPopulationPolicy
metadata:
  name: lpp-$inst
  labels:
    fma-e2e-instance: "$inst"
spec:
  enhancedNodeSelector:
    labelSelector:
      matchLabels:
        nvidia.com/gpu.present: "true"
  countForLauncher:
    - launcherConfigName: "$inst"
      launcherCount: 1
---
apiVersion: apps/v1
kind: ReplicaSet
metadata:
  name: my-request-$inst
  labels:
    app: dp-example
    fma-e2e-instance: "$inst"
spec:
  replicas: 1
  selector:
    matchLabels:
      app: dp-example
      instance: "$inst"
  template:
    metadata:
      labels:
        app: dp-example
        instance: "$inst"
      annotations:
        dual-pods.llm-d.ai/admin-port: "8081"
        dual-pods.llm-d.ai/inference-server-config: "inference-server-config-smol-$inst"
    spec:
      ${runtime_class}
      ${node_selector}
      containers:
        - name: inference-server
          image: ${REQUESTER_IMAGE}
          imagePullPolicy: ${pull_policy}
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
EOF
       )
then
    echo inference-server-config-smol-$inst
    echo "$inst"
    echo my-request-$inst
    echo inference-server-config-qwen-$inst
    echo inference-server-config-tinyllama-$inst
    echo lpp-$inst
else
    echo "Failed to create objects" >&2
    echo "$out" >&2
    exit 1
fi
