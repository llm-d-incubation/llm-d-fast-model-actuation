#!/usr/bin/env bash

# Deploys FMA + HPA + EPP + Gateway on an OCP cluster with real GPUs.
#
# Idempotent: checks each component before deploying, skips if already present.
# Run from the root of the llm-d-fast-model-actuation repository.
# Deploys the version of FMA that is checked out locally.
#
# Prerequisites:
#   - This repo (llm-d-incubation/llm-d-fast-model-actuation) cloned locally
#   - oc CLI authenticated to an OCP cluster with GPU nodes
#   - helm, helmfile, kubectl, jq, yq installed
#   - Container images already pushed to registry (see CONTAINER_IMG_REG)
#   - llm-d/guides repo cloned locally (for EPP/Gateway via helmfile)
#
# Required environment variables:
#   LLM_D_GUIDES_DIR  - path to llm-d/guides repo (contains optimized-baseline Helm values and prereqs)
#
# Optional environment variables (with defaults):
#   NAMESPACE          - target namespace (default: fma-hpa)
#   CONTAINER_IMG_REG  - image registry (default: ghcr.io/llm-d-incubation/llm-d-fast-model-actuation)
#   IMAGE_TAG          - image tag (default: v0.6.0-alpha.12, latest release)
#   LAUNCHER_IMAGE     - launcher image (default: $CONTAINER_IMG_REG/launcher:$IMAGE_TAG)
#   REQUESTER_IMAGE    - requester image (default: $CONTAINER_IMG_REG/requester:$IMAGE_TAG)
#   MODEL              - vLLM model (default: HuggingFaceTB/SmolLM2-360M-Instruct)
#   GPU_NODE           - node for LPP (default: first node with nvidia.com/gpu.present=true)
#   HPA_MAX_REPLICAS   - max HPA replicas (default: 4)
#   PROM_ADAPTER_NS    - prometheus-adapter namespace (default: openshift-user-workload-monitoring)

set -euo pipefail

NAMESPACE="${NAMESPACE:-fma-hpa}"
CONTAINER_IMG_REG="${CONTAINER_IMG_REG:-ghcr.io/llm-d-incubation/llm-d-fast-model-actuation}"
IMAGE_TAG="${IMAGE_TAG:-v0.6.0-alpha.12}"
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
    echo "  Installing Gateway API CRDs v1.5.1..."
    kubectl apply -k "https://github.com/kubernetes-sigs/gateway-api/config/crd?ref=v1.5.1"
fi

if kubectl get crd inferencepools.inference.networking.x-k8s.io &>/dev/null; then
    echo "  GAIE CRDs: present"
else
    echo "  Installing GAIE CRDs v1.5.0..."
    kubectl apply -k "https://github.com/kubernetes-sigs/gateway-api-inference-extension/config/crd?ref=v1.5.0"
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

step "EPP + Gateway (optimized-baseline)"

if [ -z "${LLM_D_GUIDES_DIR:-}" ]; then
    echo "  ERROR: LLM_D_GUIDES_DIR not set. Set it to deploy EPP+Gateway." >&2
    echo "  Example: export LLM_D_GUIDES_DIR=/path/to/llm-d/guides" >&2
    exit 1
fi

GAIE_VERSION="${GAIE_VERSION:-v1.5.0}"

# Deploy agentgateway control plane in agentgateway-system (cluster-scoped prereq)
if kubectl get deployment agentgateway -n agentgateway-system &>/dev/null; then
    echo "  agentgateway: already deployed"
else
    echo "  Deploying agentgateway control plane..."
    helmfile apply -f "${LLM_D_GUIDES_DIR}/prereq/gateway-provider/agentgateway.helmfile.yaml"
fi

# Deploy Gateway object (OCP-specific: uses AgentgatewayParameters for compatible security context)
if kubectl get gateway llm-d-inference-gateway -n "$NAMESPACE" &>/dev/null; then
    echo "  Gateway llm-d-inference-gateway: already exists"
else
    echo "  Creating Gateway (OCP)..."
    kubectl apply -n "$NAMESPACE" -k "${LLM_D_GUIDES_DIR}/recipes/gateway/agentgateway-openshift/"
fi

# Deploy InferencePool + EPP via Helm (optimized-baseline)
if kubectl get inferencepool -n "$NAMESPACE" -o name 2>/dev/null | grep -q .; then
    echo "  InferencePool: already exists"
else
    echo "  Deploying InferencePool + EPP (GAIE ${GAIE_VERSION})..."
    helm upgrade --install fma-hpa \
        oci://registry.k8s.io/gateway-api-inference-extension/charts/inferencepool \
        -f "${LLM_D_GUIDES_DIR}/recipes/scheduler/base.values.yaml" \
        -f "${LLM_D_GUIDES_DIR}/optimized-baseline/scheduler/optimized-baseline.values.yaml" \
        --set provider.name=agentgateway \
        --set experimentalHttpRoute.enabled=true \
        --set experimentalHttpRoute.inferenceGatewayName=llm-d-inference-gateway \
        -n "$NAMESPACE" --version "$GAIE_VERSION"
fi

echo "  Waiting for EPP to be ready..."
kubectl rollout status deployment -n "$NAMESPACE" \
    -l app.kubernetes.io/name=inference-scheduler --timeout=120s 2>/dev/null || true

echo "  Verifying Gateway and EPP..."
kubectl get gateway -n "$NAMESPACE" --no-headers 2>/dev/null || echo "  WARNING: no Gateway found"
kubectl get inferencepool -n "$NAMESPACE" --no-headers 2>/dev/null || echo "  WARNING: no InferencePool found"

# EPP metrics auth: the EPP validates scrape tokens via TokenReview, so its SA
# needs the ability to create tokenreviews and subjectaccessreviews.
EPP_SA=$(kubectl get deploy -n "$NAMESPACE" -l app.kubernetes.io/name="${NAMESPACE}-epp" \
    -o jsonpath='{.items[0].spec.template.spec.serviceAccountName}' 2>/dev/null || echo "${NAMESPACE}-epp")
CRB_NAME="${NAMESPACE}-${EPP_SA}"
if kubectl get clusterrolebinding "$CRB_NAME" &>/dev/null; then
    echo "  EPP metrics RBAC: already exists"
else
    echo "  Creating EPP metrics RBAC (ClusterRole + ClusterRoleBinding)..."
    kubectl apply -f - <<EOF
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: ${CRB_NAME}
rules:
- apiGroups: ["authentication.k8s.io"]
  resources: ["tokenreviews"]
  verbs: ["create"]
- apiGroups: ["authorization.k8s.io"]
  resources: ["subjectaccessreviews"]
  verbs: ["create"]
- nonResourceURLs: ["/metrics"]
  verbs: ["get"]
---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRoleBinding
metadata:
  name: ${CRB_NAME}
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: ClusterRole
  name: ${CRB_NAME}
subjects:
- kind: ServiceAccount
  name: ${EPP_SA}
  namespace: ${NAMESPACE}
EOF
fi

# ServiceMonitor + token secret for Prometheus to scrape EPP metrics
if kubectl get servicemonitor "${NAMESPACE}-epp-monitor" -n "$NAMESPACE" &>/dev/null; then
    echo "  ServiceMonitor: already exists"
else
    echo "  Creating ServiceMonitor and metrics token secret..."
    kubectl apply -n "$NAMESPACE" -f - <<EOF
apiVersion: v1
kind: Secret
metadata:
  name: ${EPP_SA}-metrics-reader-secret
  annotations:
    kubernetes.io/service-account.name: ${EPP_SA}
type: kubernetes.io/service-account-token
---
apiVersion: monitoring.coreos.com/v1
kind: ServiceMonitor
metadata:
  name: ${NAMESPACE}-epp-monitor
spec:
  endpoints:
    - authorization:
        credentials:
          key: token
          name: ${EPP_SA}-metrics-reader-secret
      interval: 10s
      path: /metrics
      port: http-metrics
  jobLabel: ${EPP_SA}
  namespaceSelector:
    matchNames:
      - ${NAMESPACE}
  selector:
    matchLabels:
      app.kubernetes.io/name: ${EPP_SA}
EOF
fi

# =========================================================================
# Step 4: FMA controllers (via deploy_fma.sh)
# =========================================================================

step "FMA controllers"

FMA_CHART="fma"
if kubectl get deployment "${FMA_CHART}-dual-pods-controller" -n "$NAMESPACE" &>/dev/null; then
    echo "  FMA controllers already deployed"
else
    echo "  Deploying FMA controllers via deploy_fma.sh..."
    (
        cd "$REPO_ROOT"
        FMA_NAMESPACE="$NAMESPACE" \
        FMA_CHART_INSTANCE_NAME="$FMA_CHART" \
        CONTAINER_IMG_REG="$CONTAINER_IMG_REG" \
        IMAGE_TAG="$IMAGE_TAG" \
        NODE_VIEW_CLUSTER_ROLE=create/please \
        RUNTIME_CLASS_NAME=nvidia \
        HELM_EXTRA_ARGS="--set launcherPopulator.enabled=true" \
        "$SCRIPT_DIR/../deploy_fma.sh"
    )
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
      llm-d.ai/guide: "optimized-baseline"
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

PROM_ADAPTER_REL="${PROM_ADAPTER_REL:-prometheus-adapter}"

if ! helm status "$PROM_ADAPTER_REL" -n "$PROM_ADAPTER_NS" &>/dev/null; then
    echo "  WARNING: prometheus-adapter Helm release '$PROM_ADAPTER_REL' not found in $PROM_ADAPTER_NS."
    echo "  HPA will show <unknown> until adapter rules are configured."
else
    # Resolve the actual InferencePool name and EPP service name in the cluster.
    # The rules must point at THESE — not at a generic fallback — otherwise
    # PromQL returns no data and the HPA shows <unknown>.
    POOL_NAME=$(kubectl get inferencepool -n "$NAMESPACE" \
        -o jsonpath='{.items[0].metadata.name}' 2>/dev/null || true)
    EPP_JOB=$(kubectl get svc -n "$NAMESPACE" \
        -l inferencepool="$POOL_NAME" \
        -o jsonpath='{.items[0].metadata.name}' 2>/dev/null || true)
    # Fallback: derive from helm release name (matches chart's default service naming)
    [ -z "$POOL_NAME" ] && POOL_NAME="fma-hpa"
    [ -z "$EPP_JOB" ] && EPP_JOB="${POOL_NAME}-epp"
    echo "  Detected InferencePool='$POOL_NAME', EPP job='$EPP_JOB'"

    CURRENT_VALUES=$(mktemp)
    NEW_VALUES=$(mktemp)
    NEW_RULES=$(mktemp)
    trap 'rm -f "$CURRENT_VALUES" "$NEW_VALUES" "$NEW_RULES"' RETURN

    helm get values "$PROM_ADAPTER_REL" -n "$PROM_ADAPTER_NS" > "$CURRENT_VALUES"

    # Determine if the existing FMA rules already point to the current pool/job.
    # If so, nothing to do. If they exist but are stale (different pool/job), or
    # don't exist at all, we'll regenerate them below.
    EXISTING_QS=$(yq eval '.rules.external[] | select(.name.as == "epp_queue_size") | .metricsQuery' "$CURRENT_VALUES" 2>/dev/null || echo "")
    EXISTING_RR=$(yq eval '.rules.external[] | select(.name.as == "epp_running_requests") | .metricsQuery' "$CURRENT_VALUES" 2>/dev/null || echo "")

    EXPECTED_QS_FRAG="name=\"${POOL_NAME}\""
    EXPECTED_RR_FRAG="job=\"${EPP_JOB}\""

    if [ -n "$EXISTING_QS" ] && [ -n "$EXISTING_RR" ] \
       && echo "$EXISTING_QS" | grep -q "$EXPECTED_QS_FRAG" \
       && echo "$EXISTING_RR" | grep -q "$EXPECTED_RR_FRAG"; then
        echo "  EPP rules already present and pointing to current pool/job — nothing to do."
    else
        if [ -n "$EXISTING_QS" ] || [ -n "$EXISTING_RR" ]; then
            echo "  EPP rules present but stale (pointing to old pool/job) — replacing..."
        else
            echo "  Adding EPP External Metrics rules to prometheus-adapter..."
        fi

        # Build the two new FMA rules (as a YAML array, for yq to load+append).
        cat > "$NEW_RULES" <<RULES_EOF
- seriesQuery: 'inference_pool_average_queue_size'
  resources:
    overrides:
      namespace:
        resource: "namespace"
    namespaced: false
  name:
    as: "epp_queue_size"
  metricsQuery: 'sum(inference_pool_average_queue_size{name="${POOL_NAME}"})'
- seriesQuery: 'inference_objective_running_requests'
  resources:
    overrides:
      namespace:
        resource: "namespace"
    namespaced: false
  name:
    as: "epp_running_requests"
  metricsQuery: 'sum(inference_objective_running_requests{namespace="${NAMESPACE}",job="${EPP_JOB}"})'
RULES_EOF

        # Strip any old FMA rules (preserves WVA and any other tenant's rules),
        # then append the freshly-generated FMA rules.
        yq eval 'del(.rules.external[] | select(.name.as == "epp_queue_size" or .name.as == "epp_running_requests"))' \
            "$CURRENT_VALUES" \
            | NEW_RULES_FILE="$NEW_RULES" yq eval '.rules.external = ((.rules.external // []) + load(env(NEW_RULES_FILE)))' - \
            > "$NEW_VALUES"

        # Resolve the chart from the existing release so we don't depend on a specific repo alias
        CHART_NAME=$(helm get metadata "$PROM_ADAPTER_REL" -n "$PROM_ADAPTER_NS" -o json 2>/dev/null \
            | yq -r '.chart' 2>/dev/null || echo "prometheus-adapter")

        helm upgrade "$PROM_ADAPTER_REL" "prometheus-community/${CHART_NAME}" \
            -n "$PROM_ADAPTER_NS" \
            -f "$NEW_VALUES"
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
