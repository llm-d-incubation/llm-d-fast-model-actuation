#!/usr/bin/env bash

# Deploys FMA + HPA + EPP + Gateway on an OCP cluster with real GPUs.
#
# Idempotent: checks each component before deploying, skips if already present.
# Run from the root of the llm-d-fast-model-actuation repository.
#
# Prerequisites:
#   - oc CLI authenticated to an OCP cluster with GPU nodes
#   - helm, helmfile, kubectl, jq, yq installed
#   - Container images already pushed to registry (see CONTAINER_IMG_REG)
#   - llm-d/guides repo cloned locally (for EPP/Gateway via helmfile)
#
# Required environment variables:
#   LLM_D_GUIDES_DIR  - path to llm-d/guides repo (contains inference-scheduling helmfile)
#
# Optional environment variables (with defaults):
#   NAMESPACE          - target namespace (default: fma-hpa)
#   CONTAINER_IMG_REG  - image registry (default: quay.io/diego_castan)
#   IMAGE_TAG          - image tag (default: f323a8f, the last known-good build)
#   LAUNCHER_IMAGE     - launcher image (default: $CONTAINER_IMG_REG/launcher:$IMAGE_TAG)
#   REQUESTER_IMAGE    - requester image (default: $CONTAINER_IMG_REG/requester:$IMAGE_TAG)
#   MODEL              - vLLM model (default: HuggingFaceTB/SmolLM2-360M-Instruct)
#   GPU_NODE           - node for LPP (default: first node with nvidia.com/gpu.present=true)
#   HPA_MAX_REPLICAS   - max HPA replicas (default: 4)
#   PROM_ADAPTER_NS    - prometheus-adapter namespace (default: openshift-user-workload-monitoring)

set -euo pipefail

NAMESPACE="${NAMESPACE:-fma-hpa}"
CONTAINER_IMG_REG="${CONTAINER_IMG_REG:-quay.io/diego_castan}"
IMAGE_TAG="${IMAGE_TAG:-f323a8f}"
LAUNCHER_IMAGE="${LAUNCHER_IMAGE:-${CONTAINER_IMG_REG}/launcher:${IMAGE_TAG}}"
REQUESTER_IMAGE="${REQUESTER_IMAGE:-${CONTAINER_IMG_REG}/requester:${IMAGE_TAG}}"
MODEL="${MODEL:-HuggingFaceTB/SmolLM2-360M-Instruct}"
GPU_NODE="${GPU_NODE:-}"
HPA_MAX_REPLICAS="${HPA_MAX_REPLICAS:-4}"
PROM_ADAPTER_NS="${PROM_ADAPTER_NS:-openshift-user-workload-monitoring}"

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"

step_num=0
total_steps=8

step() {
    step_num=$((step_num + 1))
    echo ""
    echo "========================================"
    echo "  Step ${step_num}/${total_steps}: $*"
    echo "========================================"
    echo ""
}

check_skip() {
    echo "  -> Already exists, skipping"
}

# =========================================================================
# Step 1: Namespace + RBAC
# =========================================================================

step "Namespace, ServiceAccounts, RBAC"

if kubectl get ns "$NAMESPACE" &>/dev/null; then
    echo "  Namespace $NAMESPACE exists"
else
    kubectl create ns "$NAMESPACE"
    echo "  Created namespace $NAMESPACE"
fi

for sa in testreq testlauncher; do
    if kubectl get sa "$sa" -n "$NAMESPACE" &>/dev/null; then
        echo "  SA $sa exists"
    else
        kubectl create sa "$sa" -n "$NAMESPACE"
        echo "  Created SA $sa"
    fi
done

if kubectl get role testreq -n "$NAMESPACE" &>/dev/null; then
    echo "  RBAC roles exist"
else
    kubectl apply -n "$NAMESPACE" -f - <<'EOF'
apiVersion: rbac.authorization.k8s.io/v1
kind: Role
metadata:
  name: testreq
rules:
- apiGroups: ["fma.llm-d.ai"]
  resources: [inferenceserverconfigs, launcherconfigs]
  verbs: [get, list, watch]
- apiGroups: [""]
  resourceNames: [gpu-allocs]
  resources: [configmaps]
  verbs: [update, patch, get, list, watch]
- apiGroups: [""]
  resources: [configmaps]
  verbs: [create]
- apiGroups: [""]
  resources: [pods]
  verbs: [get, list, watch]
---
apiVersion: rbac.authorization.k8s.io/v1
kind: Role
metadata:
  name: testlauncher
rules:
- apiGroups: [""]
  resources: [pods]
  verbs: [get, patch]
EOF
    kubectl create rolebinding testreq \
        --role=testreq --serviceaccount="${NAMESPACE}:testreq" \
        -n "$NAMESPACE" 2>/dev/null || true
    kubectl create rolebinding testlauncher \
        --role=testlauncher --serviceaccount="${NAMESPACE}:testlauncher" \
        -n "$NAMESPACE" 2>/dev/null || true
    echo "  Created RBAC roles and bindings"
fi


# =========================================================================
# Step 2: CRDs (Gateway API, GAIE, FMA)
# =========================================================================

step "CRDs"

if kubectl get crd gateways.gateway.networking.k8s.io &>/dev/null; then
    echo "  Gateway API CRDs: present"
else
    echo "  Installing Gateway API CRDs..."
    kubectl apply -f https://github.com/kubernetes-sigs/gateway-api/releases/download/v1.2.1/standard-install.yaml
fi

if kubectl get crd inferencepools.inference.networking.x-k8s.io &>/dev/null; then
    echo "  GAIE CRDs: present"
else
    echo "  Installing GAIE CRDs..."
    kubectl apply -f https://github.com/kubernetes-sigs/gateway-api-inference-extension/releases/download/v0.4.0/manifests.yaml
fi

if kubectl get crd inferenceserverconfigs.fma.llm-d.ai &>/dev/null; then
    echo "  FMA CRDs: present"
else
    echo "  Installing FMA CRDs..."
    for crd_file in "$REPO_ROOT"/config/crd/*.yaml; do
        kubectl apply --server-side --force-conflicts -f "$crd_file"
    done
fi

# =========================================================================
# Step 3: EPP + Gateway
# =========================================================================

step "EPP + Gateway (Inference scheduling)"

if kubectl get inferencepool -n "$NAMESPACE" &>/dev/null 2>&1 && \
   [ "$(kubectl get inferencepool -n "$NAMESPACE" -o name 2>/dev/null | wc -l)" -gt 0 ]; then
    echo "  InferencePool exists in $NAMESPACE"
else
    if [ -z "${LLM_D_GUIDES_DIR:-}" ]; then
        echo "  ERROR: LLM_D_GUIDES_DIR not set. Set it to deploy EPP+Gateway." >&2
        echo "  Example: export LLM_D_GUIDES_DIR=/path/to/llm-d/guides" >&2
        exit 1
    fi
    echo "  Deploying infra (Gateway + HTTPRoute)..."
    pushd "${LLM_D_GUIDES_DIR}/inference-scheduling" >/dev/null
    NAMESPACE="$NAMESPACE" helmfile apply -e agentgateway -n "$NAMESPACE" \
        --skip-diff-on-install -l "name=infra-inference-scheduling"
    echo "  Deploying EPP (InferencePool + Endpoint Picker)..."
    NAMESPACE="$NAMESPACE" helmfile apply -e agentgateway -n "$NAMESPACE" \
        --skip-diff-on-install -l "name=gaie-inference-scheduling"
    popd >/dev/null
fi

# Enable flowControl featureGate in EPP if not already enabled
EPP_CM=$(kubectl get cm -n "$NAMESPACE" -l app.kubernetes.io/name=inference-scheduler \
    -o jsonpath='{.items[0].metadata.name}' 2>/dev/null || true)
if [ -n "$EPP_CM" ]; then
    if kubectl get cm "$EPP_CM" -n "$NAMESPACE" -o yaml 2>/dev/null | grep -q "flowControl"; then
        echo "  EPP flowControl: already enabled"
    else
        echo "  Enabling flowControl featureGate in EPP..."
        kubectl patch configmap "$EPP_CM" -n "$NAMESPACE" --type merge -p '{
            "data": {
                "default-plugins.yaml": "apiVersion: inference.networking.x-k8s.io/v1alpha1\nkind: EndpointPickerConfig\nfeatureGates:\n- flowControl\nplugins:\n- type: queue-scorer\n- type: kv-cache-utilization-scorer\n- type: prefix-cache-scorer\nschedulingProfiles:\n- name: default\n  plugins:\n    - pluginRef: queue-scorer\n      weight: 2\n    - pluginRef: kv-cache-utilization-scorer\n      weight: 2\n    - pluginRef: prefix-cache-scorer\n      weight: 3\n"
            }
        }'
        kubectl rollout restart deployment -n "$NAMESPACE" \
            -l app.kubernetes.io/name=inference-scheduler
        echo "  Waiting for EPP rollout..."
        kubectl rollout status deployment -n "$NAMESPACE" \
            -l app.kubernetes.io/name=inference-scheduler --timeout=120s
    fi
fi

# SCC for gateway service account
GW_SA=$(kubectl get sa -n "$NAMESPACE" -o name 2>/dev/null | grep -m1 gateway | sed 's|serviceaccount/||' || true)
if [ -n "$GW_SA" ]; then
    oc adm policy add-scc-to-user anyuid -z "$GW_SA" -n "$NAMESPACE" 2>/dev/null || true
    oc adm policy add-scc-to-user privileged -z "$GW_SA" -n "$NAMESPACE" 2>/dev/null || true
    echo "  SCC granted to gateway SA: $GW_SA"
    # Restart gateway if it's not running
    GW_DEPLOY=$(kubectl get deploy -n "$NAMESPACE" -o name 2>/dev/null | grep -m1 gateway || true)
    if [ -n "$GW_DEPLOY" ]; then
        GW_READY=$(kubectl get "$GW_DEPLOY" -n "$NAMESPACE" -o jsonpath='{.status.readyReplicas}' 2>/dev/null || echo "0")
        if [ "${GW_READY:-0}" = "0" ]; then
            echo "  Gateway not ready, restarting..."
            kubectl rollout restart "$GW_DEPLOY" -n "$NAMESPACE"
            kubectl rollout status "$GW_DEPLOY" -n "$NAMESPACE" --timeout=120s
        fi
    fi
fi

echo "  Verifying Gateway and EPP..."
kubectl get gateway -n "$NAMESPACE" --no-headers 2>/dev/null || echo "  WARNING: no Gateway found"
kubectl get inferencepool -n "$NAMESPACE" --no-headers 2>/dev/null || echo "  WARNING: no InferencePool found"

# =========================================================================
# Step 4: FMA controllers (via deploy_fma.sh)
# =========================================================================

step "FMA controllers"

FMA_CHART="fma"
if kubectl get deployment "${FMA_CHART}-dual-pods-controller" -n "$NAMESPACE" &>/dev/null; then
    echo "  FMA controllers already deployed"
else
    echo "  Deploying FMA controllers via deploy_fma.sh..."
    cd "$REPO_ROOT"
    FMA_NAMESPACE="$NAMESPACE" \
    FMA_CHART_INSTANCE_NAME="$FMA_CHART" \
    CONTAINER_IMG_REG="$CONTAINER_IMG_REG" \
    IMAGE_TAG="$IMAGE_TAG" \
    NODE_VIEW_CLUSTER_ROLE=create/please \
    RUNTIME_CLASS_NAME=nvidia \
    HELM_EXTRA_ARGS="--set launcherPopulator.enabled=true" \
    "$SCRIPT_DIR/deploy_fma.sh"
fi

# =========================================================================
# Step 5: FMA objects (ISC, LauncherConfig, LPP, ReplicaSet)
# =========================================================================

step "FMA objects (ISC, LauncherConfig, LPP, ReplicaSet)"

# Pick a GPU node for the LPP
if [ -z "$GPU_NODE" ]; then
    GPU_NODE=$(kubectl get nodes -l nvidia.com/gpu.present=true \
        -o jsonpath='{.items[0].metadata.name}' 2>/dev/null || true)
    if [ -z "$GPU_NODE" ]; then
        echo "  ERROR: No GPU node found. Set GPU_NODE manually." >&2
        exit 1
    fi
fi
echo "  Using GPU node: $GPU_NODE"

# Label the chosen node for LPP selector
kubectl label node "$GPU_NODE" fma-hpa-poc=true --overwrite=true 2>/dev/null
echo "  Labeled $GPU_NODE with fma-hpa-poc=true"

if kubectl get inferenceserverconfig isc-smol -n "$NAMESPACE" &>/dev/null; then
    echo "  FMA objects already exist"
else
    echo "  Creating FMA objects..."
    kubectl apply -n "$NAMESPACE" -f - <<EOF
apiVersion: fma.llm-d.ai/v1alpha1
kind: InferenceServerConfig
metadata:
  name: isc-smol
spec:
  modelServerConfig:
    port: 8000
    options: "--model ${MODEL} --enable-sleep-mode"
    env_vars:
      VLLM_USE_V1: "1"
      VLLM_LOGGING_LEVEL: "DEBUG"
      HF_HOME: "/tmp"
      VLLM_CACHE_ROOT: "/tmp"
      FLASHINFER_WORKSPACE_BASE: "/tmp"
      TRITON_CACHE_DIR: "/tmp"
      XDG_CACHE_HOME: "/tmp"
      XDG_CONFIG_HOME: "/tmp"
    labels:
      llm-d.ai/inference-serving: "true"
      llm-d.ai/guide: "inference-scheduling"
      llm-d.ai/model: "SmolLM2-360M-Instruct"
    annotations:
      description: "FMA ISC for HPA demo - ${MODEL}"
  launcherConfigName: lc-hpa
---
apiVersion: fma.llm-d.ai/v1alpha1
kind: LauncherConfig
metadata:
  name: lc-hpa
spec:
  maxSleepingInstances: 0
  podTemplate:
    spec:
      runtimeClassName: nvidia
      serviceAccountName: testlauncher
      containers:
        - name: inference-server
          image: ${LAUNCHER_IMAGE}
          imagePullPolicy: Always
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
  name: lpp-hpa
spec:
  enhancedNodeSelector:
    labelSelector:
      matchLabels:
        fma-hpa-poc: "true"
  countForLauncher:
    - launcherConfigName: lc-hpa
      launcherCount: 1
---
apiVersion: apps/v1
kind: ReplicaSet
metadata:
  name: fma-requester
  labels:
    app: fma-hpa-requester
spec:
  replicas: 1
  selector:
    matchLabels:
      app: fma-hpa-requester
  template:
    metadata:
      labels:
        app: fma-hpa-requester
      annotations:
        dual-pods.llm-d.ai/admin-port: "8081"
        dual-pods.llm-d.ai/inference-server-config: "isc-smol"
    spec:
      runtimeClassName: nvidia
      serviceAccountName: testreq
      containers:
        - name: inference-server
          image: ${REQUESTER_IMAGE}
          imagePullPolicy: Always
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
    echo "  FMA objects created"
fi

# =========================================================================
# Step 6: HPA
# =========================================================================

step "HPA"

if kubectl get hpa fma-hpa -n "$NAMESPACE" &>/dev/null; then
    echo "  HPA fma-hpa already exists"
else
    echo "  Creating HPA targeting ReplicaSet/fma-requester..."
    kubectl apply -n "$NAMESPACE" -f - <<EOF
apiVersion: autoscaling/v2
kind: HorizontalPodAutoscaler
metadata:
  name: fma-hpa
spec:
  scaleTargetRef:
    apiVersion: apps/v1
    kind: ReplicaSet
    name: fma-requester
  minReplicas: 1
  maxReplicas: ${HPA_MAX_REPLICAS}
  metrics:
  - type: External
    external:
      metric:
        name: epp_queue_size
      target:
        type: Value
        value: "5"
  - type: External
    external:
      metric:
        name: epp_running_requests
      target:
        type: AverageValue
        averageValue: "10"
  behavior:
    scaleUp:
      stabilizationWindowSeconds: 0
      policies:
      - type: Percent
        value: 100
        periodSeconds: 15
    scaleDown:
      stabilizationWindowSeconds: 300
      policies:
      - type: Percent
        value: 100
        periodSeconds: 15
EOF
    echo "  HPA created"
fi

# =========================================================================
# Step 7: prometheus-adapter rules
# =========================================================================

step "prometheus-adapter External Metrics rules"

ADAPTER_CM=$(kubectl get cm -n "$PROM_ADAPTER_NS" \
    -l app.kubernetes.io/name=prometheus-adapter \
    -o jsonpath='{.items[0].metadata.name}' 2>/dev/null || true)

if [ -z "$ADAPTER_CM" ]; then
    echo "  WARNING: prometheus-adapter ConfigMap not found in $PROM_ADAPTER_NS"
    echo "  HPA will show <unknown> until adapter rules are configured."
else
    ADAPTER_CONFIG=$(kubectl get cm "$ADAPTER_CM" -n "$PROM_ADAPTER_NS" \
        -o jsonpath='{.data.config\.yaml}' 2>/dev/null || true)

    if echo "$ADAPTER_CONFIG" | grep -q "epp_running_requests"; then
        echo "  EPP rules already present in prometheus-adapter"
    else
        echo "  Adding EPP External Metrics rules to prometheus-adapter..."
        # Determine the EPP job name and InferencePool name from the cluster
        EPP_JOB=$(kubectl get svc -n "$NAMESPACE" \
            -l app.kubernetes.io/name=inference-scheduler \
            -o jsonpath='{.items[0].metadata.name}' 2>/dev/null || echo "gaie-inference-scheduling-epp")
        POOL_NAME=$(kubectl get inferencepool -n "$NAMESPACE" \
            -o jsonpath='{.items[0].metadata.name}' 2>/dev/null || echo "gaie-inference-scheduling")

        cat > /tmp/adapter-epp-values.yaml <<VALEOF
rules:
  external:
    - seriesQuery: 'wva_desired_replicas{variant_name!="",exported_namespace!=""}'
      resources:
        overrides:
          exported_namespace:
            resource: namespace
          variant_name:
            resource: deployment
      name:
        matches: "^wva_desired_replicas"
        as: "wva_desired_replicas"
      metricsQuery: 'wva_desired_replicas{<<.LabelMatchers>>}'
    - seriesQuery: 'inference_extension_flow_control_queue_size'
      resources:
        overrides:
          namespace:
            resource: "namespace"
        namespaced: false
      name:
        as: "epp_queue_size"
      metricsQuery: 'sum(inference_extension_flow_control_queue_size{namespace="${NAMESPACE}",inference_pool="${POOL_NAME}"})'
    - seriesQuery: 'inference_objective_running_requests'
      resources:
        overrides:
          namespace:
            resource: "namespace"
        namespaced: false
      name:
        as: "epp_running_requests"
      metricsQuery: 'sum(inference_objective_running_requests{namespace="${NAMESPACE}",job="${EPP_JOB}"})'
VALEOF
        helm upgrade prometheus-adapter prometheus-community/prometheus-adapter \
            -n "$PROM_ADAPTER_NS" --reuse-values \
            -f /tmp/adapter-epp-values.yaml
        rm -f /tmp/adapter-epp-values.yaml
        echo "  Waiting for prometheus-adapter to restart..."
        kubectl rollout status deployment -n "$PROM_ADAPTER_NS" \
            -l app.kubernetes.io/name=prometheus-adapter --timeout=120s 2>/dev/null || true
        echo "  prometheus-adapter rules updated"
    fi
fi

# =========================================================================
# Step 8: Validation
# =========================================================================

step "Validation"

echo "  Waiting for requester and launcher pods..."
kubectl wait --for=condition=Ready pod \
    -l app=fma-hpa-requester -n "$NAMESPACE" --timeout=300s 2>/dev/null || true

echo ""
echo "  --- Pods ---"
kubectl get pods -n "$NAMESPACE" \
    -L dual-pods.llm-d.ai/dual,dual-pods.llm-d.ai/sleeping --no-headers 2>/dev/null || true

echo ""
echo "  --- HPA ---"
kubectl get hpa -n "$NAMESPACE" 2>/dev/null || true

echo ""
echo "  --- External Metrics API ---"
kubectl get --raw "/apis/external.metrics.k8s.io/v1beta1/namespaces/${NAMESPACE}/epp_queue_size" 2>/dev/null \
    | jq -r '.items[0].value // "not available"' 2>/dev/null \
    && echo "  (epp_queue_size OK)" \
    || echo "  epp_queue_size: not available yet (may take a few minutes)"

kubectl get --raw "/apis/external.metrics.k8s.io/v1beta1/namespaces/${NAMESPACE}/epp_running_requests" 2>/dev/null \
    | jq -r '.items[0].value // "not available"' 2>/dev/null \
    && echo "  (epp_running_requests OK)" \
    || echo "  epp_running_requests: not available yet (may take a few minutes)"

echo ""
echo "========================================"
echo "  Deployment complete!"
echo "========================================"
echo ""
echo "  Namespace:  $NAMESPACE"
echo "  GPU Node:   $GPU_NODE"
echo "  Model:      $MODEL"
echo "  HPA:        fma-hpa (max ${HPA_MAX_REPLICAS} replicas)"
echo ""
echo "  Next steps:"
echo "    Terminal 2: ./test/e2e/demo-fma-hpa-monitor.sh"
echo "    Terminal 3: ./test/e2e/demo-fma-hpa-loadgen.sh"
echo ""
