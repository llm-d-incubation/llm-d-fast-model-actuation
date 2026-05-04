#!/usr/bin/env bash

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

# When a node is specified, pin the ReplicaSet's pods to it.
if [ -n "$node_name" ]; then
    node_selector="nodeSelector:
        kubernetes.io/hostname: \"$node_name\""
else
    node_selector=""
fi

inst=$(date +%d-%H-%M-%S)
requester_img=$(make echo-var VAR=TEST_REQUESTER_IMG)
launcher_img=$(make echo-var VAR=TEST_LAUNCHER_IMG)
if out=$(kubectl apply "${ns_flag[@]}" -f - 2>&1 <<EOF
apiVersion: fma.llm-d.ai/v1alpha1
kind: InferenceServerConfig
metadata:
  name: inference-server-config-smol-$inst
  labels:
    instance: "$inst"
    fma-e2e-instance: "$inst"
spec:
  modelServerConfig:
    port: 8005
    options: "--model HuggingFaceTB/SmolLM2-360M-Instruct"
    env_vars:
      VLLM_SERVER_DEV_MODE: "1"
      VLLM_LOGGING_LEVEL: "DEBUG"
      VLLM_CPU_KVCACHE_SPACE: "1" # GiB, helpful for small models to reduce CPU memory usage during testing
    labels:
      e2e-test.fma.llm-d.ai/isc-label: test-value
    annotations:
      e2e-test.fma.llm-d.ai/isc-annotation: test-value
  launcherConfigName: launcher-config-$inst
---
apiVersion: fma.llm-d.ai/v1alpha1
kind: InferenceServerConfig
metadata:
  name: inference-server-config-qwen-$inst
  labels:
    instance: "$inst"
    fma-e2e-instance: "$inst"
spec:
  modelServerConfig:
    port: 8006
    options: "--model Qwen/Qwen2.5-0.5B-Instruct"
    env_vars:
      VLLM_SERVER_DEV_MODE: "1"
      VLLM_LOGGING_LEVEL: "DEBUG"
      VLLM_CPU_KVCACHE_SPACE: "1" # GiB, helpful for small models to reduce CPU memory usage during testing
    labels:
      e2e-test.fma.llm-d.ai/isc-label: test-value
    annotations:
      e2e-test.fma.llm-d.ai/isc-annotation: test-value
  launcherConfigName: launcher-config-$inst
---
apiVersion: fma.llm-d.ai/v1alpha1
kind: InferenceServerConfig
metadata:
  name: inference-server-config-tinyllama-$inst
  labels:
    instance: "$inst"
    fma-e2e-instance: "$inst"
spec:
  modelServerConfig:
    port: 8007
    options: "--model TinyLlama/TinyLlama-1.1B-Chat-v1.0"
    env_vars:
      VLLM_SERVER_DEV_MODE: "1"
      VLLM_LOGGING_LEVEL: "DEBUG"
      VLLM_CPU_KVCACHE_SPACE: "1" # GiB, helpful for small models to reduce CPU memory usage during testing
    labels:
      e2e-test.fma.llm-d.ai/isc-label: test-value
    annotations:
      e2e-test.fma.llm-d.ai/isc-annotation: test-value
  launcherConfigName: launcher-config-$inst
---
apiVersion: fma.llm-d.ai/v1alpha1
kind: LauncherConfig
metadata:
  name: launcher-config-$inst
  labels:
    instance: "$inst"
    fma-e2e-instance: "$inst"
spec:
  maxSleepingInstances: 1
  podTemplate:
    metadata:
      labels:
        e2e-test.fma.llm-d.ai/template-label: from-launcher-config
      annotations:
        e2e-test.fma.llm-d.ai/template-annotation: from-launcher-config
    spec:
      serviceAccount: testlauncher
      containers:
        - name: inference-server
          image: $launcher_img
          imagePullPolicy: Never
          command:
          - /bin/bash
          - "-c"
          args:
          - |
            python3 launcher.py \
            --mock-gpus \
            --host 0.0.0.0 \
            --port 8001 \
            --log-level info
          env:
            - name: NODE_NAME
              valueFrom:
                fieldRef: { fieldPath: spec.nodeName }
            - name: NAMESPACE
              valueFrom:
                fieldRef: { fieldPath: metadata.namespace }
---
apiVersion: fma.llm-d.ai/v1alpha1
kind: LauncherPopulationPolicy
metadata:
  name: lpp-$inst
  labels:
    instance: "$inst"
    fma-e2e-instance: "$inst"
spec:
  enhancedNodeSelector:
    labelSelector:
      matchLabels:
        nvidia.com/gpu.present: "true"
  countForLauncher:
    - launcherConfigName: launcher-config-$inst
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
      containers:
        - name: inference-server
          image: $requester_img
          imagePullPolicy: IfNotPresent
          command:
          - /ko-app/test-requester
          - --node=\$(NODE_NAME)
          - --pod-uid=\$(POD_UID)
          - --namespace=\$(NAMESPACE)
          - -v=5
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
      ${node_selector}
      serviceAccount: testreq
EOF
        )
then
    # output to be parsed by caller, e.g. the e2e test script
    echo inference-server-config-smol-$inst
    echo launcher-config-$inst
    echo my-request-$inst
    echo inference-server-config-qwen-$inst
    echo inference-server-config-tinyllama-$inst
    echo lpp-$inst
else
    echo Failed to create objects >&2
    echo "$out" >&2
    exit 1
fi
