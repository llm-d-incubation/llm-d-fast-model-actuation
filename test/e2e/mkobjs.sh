#!/usr/bin/env bash

inst=$(date +%d-%H-%M-%S)
server_img=$(make echo-var VAR=TEST_SERVER_IMG)
requester_img=$(make echo-var VAR=TEST_REQUESTER_IMG)
launcher_img=$(make echo-var VAR=TEST_LAUNCHER_IMG)
if out=$(kubectl apply -f - 2>&1 <<EOF
apiVersion: fma.llm-d.ai/v1alpha1
kind: InferenceServerConfig
metadata:
  name: inference-server-config-smol-$inst
  labels:
    instance: "$inst"
spec:
  modelServerConfig:
    port: 8005
    options: "--model HuggingFaceTB/SmolLM2-360M-Instruct"
    env_vars:
      VLLM_SERVER_DEV_MODE: "1"
      VLLM_USE_V1: "1"
      VLLM_LOGGING_LEVEL: "DEBUG"
      VLLM_CPU_KVCACHE_SPACE: "1" # GiB
    labels:
      component: inference
    annotations:
      description: "Example InferenceServerConfig"
  launcherConfigName: launcher-config-$inst
---
apiVersion: fma.llm-d.ai/v1alpha1
kind: InferenceServerConfig
metadata:
  name: inference-server-config-qwen-$inst
  labels:
    instance: "$inst"
spec:
  modelServerConfig:
    port: 8006
    options: "--model Qwen/Qwen2.5-0.5B-Instruct"
    env_vars:
      VLLM_SERVER_DEV_MODE: "1"
      VLLM_USE_V1: "1"
      VLLM_LOGGING_LEVEL: "DEBUG"
      VLLM_CPU_KVCACHE_SPACE: "1" # GiB
    labels:
      component: inference
    annotations:
      description: "Example InferenceServerConfig"
  launcherConfigName: launcher-config-$inst
---
apiVersion: fma.llm-d.ai/v1alpha1
kind: InferenceServerConfig
metadata:
  name: inference-server-config-tinyllama-$inst
  labels:
    instance: "$inst"
spec:
  modelServerConfig:
    port: 8007
    options: "--model TinyLlama/TinyLlama-1.1B-Chat-v1.0"
    env_vars:
      VLLM_SERVER_DEV_MODE: "1"
      VLLM_USE_V1: "1"
      VLLM_LOGGING_LEVEL: "DEBUG"
      VLLM_CPU_KVCACHE_SPACE: "1" # GiB
    labels:
      component: inference
    annotations:
      description: "Example InferenceServerConfig"
  launcherConfigName: launcher-config-$inst
---
apiVersion: fma.llm-d.ai/v1alpha1
kind: LauncherConfig
metadata:
  name: launcher-config-$inst
  labels:
    instance: "$inst"
spec:
  maxSleepingInstances: 1
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
apiVersion: apps/v1
kind: ReplicaSet
metadata:
  name: my-request-$inst
  labels:
    app: dp-example
spec:
  replicas: 1
  selector:
    matchLabels:
      app: dp-example
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
      # nodeName: fmatest-worker # try fixed node for the consistency of value of dual-pods.llm-d.ai/launcher-config-hash annotation
EOF
        )
then
    # output to be parsed by caller, e.g. the e2e test script
    echo inference-server-config-smol-$inst
    echo launcher-config-$inst
    echo my-request-$inst
    echo inference-server-config-qwen-$inst
    echo inference-server-config-tinyllama-$inst
else
    echo Failed to create objects >&2
    echo "$out" >&2
    exit 1
fi
