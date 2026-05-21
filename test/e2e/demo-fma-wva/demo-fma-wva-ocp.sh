#!/usr/bin/env bash

# Deploys FMA + WVA + llm-d components in the same namespace.
#
# Idempotent: checks each component before deploying, skips if already present.
# Can be invoked from any working directory — the script resolves the FMA repo
# root from its own location.
#
# Prerequisites:
#   - This repo (llm-d-incubation/llm-d-fast-model-actuation) cloned locally
#   - oc authenticated to an OpenShift cluster with GPU nodes
#   - helm, kubectl, make, git, jq, yq (mikefarah/yq) on $PATH
#   - Container images already pushed to registry (see --fma-image-registry)
#
# The workload-variant-autoscaler (WVA) repo is auto-cloned to --wva-repo-path
# if not already present. To use an existing checkout, pass --wva-repo-path
# /path/to/checkout.
#
# Run with --help for the full list of flags.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/../../.." && pwd)"

# ----------------------------------------------------------------------------
# CLI parsing — flags are the primary interface; matching env vars are honored
# as a fallback for backward compatibility but flags take precedence.
# ----------------------------------------------------------------------------
usage() {
    cat <<EOF
Usage: $(basename "$0") [OPTIONS]

Deploys FMA + WVA + llm-d components in the same namespace.

FMA / cluster options:
  -n, --namespace NAME              Target namespace (default: fma-wva-demo)
      --fma-image-registry URL      FMA image registry
                                    (default: ghcr.io/llm-d-incubation/llm-d-fast-model-actuation)
      --fma-image-tag TAG           FMA image tag (default: v0.6.0-alpha.13)
      --fma-launcher-image IMG      Launcher image
                                    (default: <fma-image-registry>/launcher:<fma-image-tag>)
      --fma-requester-image IMG     Requester image
                                    (default: <fma-image-registry>/requester:<fma-image-tag>)
      --model NAME                  vLLM model (default: HuggingFaceTB/SmolLM2-360M-Instruct)
      --gpu-node NODE               Node for LPP (default: first node with
                                    nvidia.com/gpu.present=true)
      --hf-token TOKEN              HuggingFace token (for gated models)
      --runtime-class NAME          RuntimeClass for GPU pods (default: nvidia;
                                    pass an empty string to omit)
      --wait-requester-timeout DUR  How long to wait for Deployment/fma-requester
                                    to become Available (default: 600s; covers
                                    cold-cluster image+model fetch). Accepts
                                    any kubectl --timeout value (e.g., 90s, 5m).

WVA options:
      --wva-version REF             WVA version. Used as both the git ref to
                                    check out (branch, tag, or commit SHA) and
                                    the controller image tag (default: v0.8.0-rc4)
      --wva-repo-path PATH          Path to WVA repo (default: <repo-root>/.wva-checkout)
      --wva-repo-url URL            WVA git URL
                                    (default: https://github.com/llm-d/llm-d-workload-variant-autoscaler)
      --wva-image-repo URL          WVA image repository
                                    (default: ghcr.io/llm-d/llm-d-workload-variant-autoscaler)
      --controller-instance NAME    WVA controller instance name (default: fma-wva)
      --monitoring-namespace NAME   Monitoring namespace
                                    (default: openshift-user-workload-monitoring)
      --llm-d-release VER           llm-d release version (default: v0.7.0)
      --gaie-version VER            GAIE version (default: v1.5.0)

  -h, --help                        Show this help and exit

Every flag above also has a matching environment variable (the flag's
long-form name in upper snake case — e.g. --namespace → NAMESPACE,
--wva-repo-path → WVA_REPO_PATH). Env vars are accepted for backward
compatibility; flags take precedence when both are set.
EOF
}

# Seed defaults from env vars (so existing callers using env vars still work).
NAMESPACE="${NAMESPACE:-fma-wva-demo}"
CONTAINER_IMG_REG="${CONTAINER_IMG_REG:-ghcr.io/llm-d-incubation/llm-d-fast-model-actuation}"
IMAGE_TAG="${IMAGE_TAG:-v0.6.0-alpha.13}"
LAUNCHER_IMAGE="${LAUNCHER_IMAGE:-}"
REQUESTER_IMAGE="${REQUESTER_IMAGE:-}"
MODEL="${MODEL:-HuggingFaceTB/SmolLM2-360M-Instruct}"
GPU_NODE="${GPU_NODE:-}"
HF_TOKEN="${HF_TOKEN:-}"
RUNTIME_CLASS_NAME="${RUNTIME_CLASS_NAME-nvidia}"
WAIT_REQUESTER_TIMEOUT="${WAIT_REQUESTER_TIMEOUT:-600s}"

WVA_REPO_PATH="${WVA_REPO_PATH:-$REPO_ROOT/.wva-checkout}"
WVA_REPO_URL="${WVA_REPO_URL:-https://github.com/llm-d/llm-d-workload-variant-autoscaler}"
# Single WVA version knob — used as both the git ref (for the source-based
# install scripts that the WVA repo provides) and the controller image tag.
WVA_VERSION="${WVA_VERSION:-v0.8.0-rc4}"
WVA_IMAGE_REPO="${WVA_IMAGE_REPO:-ghcr.io/llm-d/llm-d-workload-variant-autoscaler}"
CONTROLLER_INSTANCE="${CONTROLLER_INSTANCE:-fma-wva}"
MONITORING_NAMESPACE="${MONITORING_NAMESPACE:-openshift-user-workload-monitoring}"
LLM_D_RELEASE="${LLM_D_RELEASE:-v0.7.0}"
GAIE_VERSION="${GAIE_VERSION:-v1.5.0}"

# Prometheus + adapter are NOT deployed by this script; OpenShift's built-in
# user-workload monitoring is assumed. Hard-coded; not user-configurable.
DEPLOY_PROMETHEUS=false
DEPLOY_PROMETHEUS_ADAPTER=false

# Helper for required-arg flags
need_arg() {
    [[ $# -ge 2 ]] || { echo "ERROR: $1 requires an argument" >&2; exit 2; }
}

while [[ $# -gt 0 ]]; do
    case "$1" in
        -n|--namespace)
            need_arg "$@"; NAMESPACE="$2"; shift 2 ;;
        --fma-image-registry)
            need_arg "$@"; CONTAINER_IMG_REG="$2"; shift 2 ;;
        --fma-image-tag)
            need_arg "$@"; IMAGE_TAG="$2"; shift 2 ;;
        --fma-launcher-image)
            need_arg "$@"; LAUNCHER_IMAGE="$2"; shift 2 ;;
        --fma-requester-image)
            need_arg "$@"; REQUESTER_IMAGE="$2"; shift 2 ;;
        --model)
            need_arg "$@"; MODEL="$2"; shift 2 ;;
        --gpu-node)
            need_arg "$@"; GPU_NODE="$2"; shift 2 ;;
        --hf-token)
            need_arg "$@"; HF_TOKEN="$2"; shift 2 ;;
        --runtime-class)
            need_arg "$@"; RUNTIME_CLASS_NAME="$2"; shift 2 ;;
        --wait-requester-timeout)
            need_arg "$@"; WAIT_REQUESTER_TIMEOUT="$2"; shift 2 ;;
        --wva-version)
            need_arg "$@"; WVA_VERSION="$2"; shift 2 ;;
        --wva-repo-path)
            need_arg "$@"; WVA_REPO_PATH="$2"; shift 2 ;;
        --wva-repo-url)
            need_arg "$@"; WVA_REPO_URL="$2"; shift 2 ;;
        --wva-image-repo)
            need_arg "$@"; WVA_IMAGE_REPO="$2"; shift 2 ;;
        --controller-instance)
            need_arg "$@"; CONTROLLER_INSTANCE="$2"; shift 2 ;;
        --monitoring-namespace)
            need_arg "$@"; MONITORING_NAMESPACE="$2"; shift 2 ;;
        --llm-d-release)
            need_arg "$@"; LLM_D_RELEASE="$2"; shift 2 ;;
        --gaie-version)
            need_arg "$@"; GAIE_VERSION="$2"; shift 2 ;;
        -h|--help)
            usage; exit 0 ;;
        *)
            echo "ERROR: Unknown option: $1" >&2
            usage >&2
            exit 2 ;;
    esac
done

# Compute derived defaults after parsing (so --fma-image-registry / --fma-image-tag
# can flow into LAUNCHER_IMAGE / REQUESTER_IMAGE if those weren't set explicitly).
LAUNCHER_IMAGE="${LAUNCHER_IMAGE:-${CONTAINER_IMG_REG}/launcher:${IMAGE_TAG}}"
REQUESTER_IMAGE="${REQUESTER_IMAGE:-${CONTAINER_IMG_REG}/requester:${IMAGE_TAG}}"

# WVA's source ref and controller image tag are coupled — both come from the
# single --wva-version knob.
WVA_REPO_REF="$WVA_VERSION"
WVA_IMAGE_TAG="$WVA_VERSION"

if [ ! -d "$WVA_REPO_PATH/.git" ]; then
    if [ -d "$WVA_REPO_PATH" ] && [ -n "$(ls -A "$WVA_REPO_PATH" 2>/dev/null)" ]; then
        echo "ERROR: $WVA_REPO_PATH exists but is not a git checkout. Remove it or set WVA_REPO_PATH to a different location." >&2
        exit 1
    fi
    echo "  WVA repo not found at $WVA_REPO_PATH — cloning $WVA_REPO_URL ($WVA_REPO_REF)..."
    mkdir -p "$(dirname "$WVA_REPO_PATH")"
    # Try a shallow clone first (works for branches + tags). If that fails,
    # the ref is likely a commit SHA, which `--branch` doesn't accept; fall
    # back to a full clone followed by checkout.
    if ! git clone --depth 1 --branch "$WVA_REPO_REF" "$WVA_REPO_URL" "$WVA_REPO_PATH" 2>/dev/null; then
        echo "  Shallow clone by --branch failed; assuming WVA_REPO_REF is a commit SHA."
        rm -rf "$WVA_REPO_PATH"
        git clone "$WVA_REPO_URL" "$WVA_REPO_PATH"
        git -C "$WVA_REPO_PATH" checkout "$WVA_REPO_REF"
    fi
else
    echo "  Using existing WVA repo at $WVA_REPO_PATH"
fi

step_num=0
total_steps=7

step() {
    step_num=$((step_num + 1))
    echo ""
    echo "========================================"
    echo "  Step ${step_num}/${total_steps}: $*"
    echo "========================================"
    echo ""
}


step "Namespace, ServiceAccounts, RBAC"

if ! kubectl get ns "$NAMESPACE" &>/dev/null; then
    kubectl create namespace "$NAMESPACE"
    echo "  Created namespace $NAMESPACE"
else
    echo "  Namespace $NAMESPACE exists"
fi
kubectl label namespace "$NAMESPACE" openshift.io/user-monitoring=true --overwrite >/dev/null
echo "  Ensured monitoring label openshift.io/user-monitoring=true on $NAMESPACE"

# Ensure the ServiceAccount idempotently.
kubectl create sa testlauncher -n "$NAMESPACE" --dry-run=client -o yaml \
    | kubectl apply -f - >/dev/null
echo "  Ensured ServiceAccount testlauncher"

# Apply the Role idempotently.
kubectl apply -n "$NAMESPACE" -f - <<'EOF' >/dev/null
apiVersion: rbac.authorization.k8s.io/v1
kind: Role
metadata:
  name: testlauncher
rules:
- apiGroups: [""]
  resources: [pods]
  verbs: [get, patch]
EOF
echo "  Ensured Role testlauncher"

# Apply the RoleBinding idempotently.
kubectl apply -n "$NAMESPACE" -f - <<EOF >/dev/null
apiVersion: rbac.authorization.k8s.io/v1
kind: RoleBinding
metadata:
  name: testlauncher
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: Role
  name: testlauncher
subjects:
- kind: ServiceAccount
  name: testlauncher
  namespace: ${NAMESPACE}
EOF
echo "  Ensured RoleBinding testlauncher"


step "FMA CRDs and Controllers (via deploy_fma.sh)"

# Helm release (chart instance) name. The chart itself is `fma-controllers`;
# this is the name passed to `helm install/upgrade --install`, which prefixes
# resources created by the chart (e.g., the dual-pods-controller Deployment).
FMA_RELEASE="fma"
if kubectl get deployment "${FMA_RELEASE}-dual-pods-controller" -n "$NAMESPACE" &>/dev/null; then
    echo "  FMA controllers already deployed"
else
    echo "  Deploying FMA CRDs and controllers via deploy_fma.sh..."
    (
        cd "$REPO_ROOT"
        FMA_NAMESPACE="$NAMESPACE" \
        FMA_CHART_INSTANCE_NAME="$FMA_RELEASE" \
        CONTAINER_IMG_REG="$CONTAINER_IMG_REG" \
        IMAGE_TAG="$IMAGE_TAG" \
        NODE_VIEW_CLUSTER_ROLE=create/please \
        RUNTIME_CLASS_NAME="$RUNTIME_CLASS_NAME" \
        "$SCRIPT_DIR/../deploy_fma.sh"
    )
fi

echo "  Verifying FMA CRDs..."
kubectl get crd | grep -F "fma.llm-d.ai" || echo "  WARNING: FMA CRDs not found"

echo "  Verifying FMA controllers..."
# `kubectl get -l ...` exits 0 with empty output when nothing matches, so we
# check for non-empty `-o name` output to detect missing resources reliably.
if kubectl get deployment -n "$NAMESPACE" -l app.kubernetes.io/part-of=fma -o name 2>/dev/null | grep -q .; then
    kubectl get deployment -n "$NAMESPACE" -l app.kubernetes.io/part-of=fma
else
    echo "  WARNING: FMA controllers not found"
fi


step "WVA Deployment (via make deploy-wva-on-openshift)"

if [ -n "$(kubectl get deployment -n "$NAMESPACE" -l app.kubernetes.io/name=workload-variant-autoscaler -o name 2>/dev/null)" ]; then
    echo "  WVA controller already deployed"
else
    echo "  Deploying WVA to namespace $NAMESPACE..."
    (
        cd "$WVA_REPO_PATH"
        # Same-name variables can simply be exported (no need to repeat the
        # value). The renamed/derived ones still need an assignment.
        export HF_TOKEN DEPLOY_PROMETHEUS DEPLOY_PROMETHEUS_ADAPTER \
               MONITORING_NAMESPACE CONTROLLER_INSTANCE \
               WVA_IMAGE_REPO WVA_IMAGE_TAG
        export LLMD_NS="$NAMESPACE"
        export WVA_NS="$NAMESPACE"
        export IMG="${WVA_IMAGE_REPO}:${WVA_IMAGE_TAG}"
        
        echo "  Running: make deploy-wva-on-openshift"
        make deploy-wva-on-openshift
    )
    echo "  WVA deployed successfully"
fi

echo "  Verifying WVA deployment..."
if kubectl get deployment -n "$NAMESPACE" -l app.kubernetes.io/name=workload-variant-autoscaler -o name 2>/dev/null | grep -q .; then
    kubectl get deployment -n "$NAMESPACE" -l app.kubernetes.io/name=workload-variant-autoscaler
else
    echo "  WARNING: WVA controller not found"
fi


step "llm-d EPP Installation (via install-epp.sh)"

if [ -n "$(kubectl get inferencepool -n "$NAMESPACE" -o name 2>/dev/null)" ]; then
    echo "  llm-d EPP already deployed"
else
    echo "  Deploying llm-d EPP to namespace $NAMESPACE..."
    (
        cd "$WVA_REPO_PATH"
        export LLMD_NS="$NAMESPACE"
        export ENVIRONMENT=openshift
        export LLM_D_RELEASE="$LLM_D_RELEASE"
        export GAIE_VERSION="$GAIE_VERSION"
        
        echo "  Running: ./deploy/install-epp.sh"
        ./deploy/install-epp.sh
    )
    echo "  llm-d EPP deployed successfully"
fi

echo "  Verifying llm-d EPP..."
if kubectl get inferencepool -n "$NAMESPACE" -o name 2>/dev/null | grep -q .; then
    kubectl get inferencepool -n "$NAMESPACE"
else
    echo "  WARNING: InferencePool not found"
fi
if kubectl get gateway -n "$NAMESPACE" -o name 2>/dev/null | grep -q .; then
    kubectl get gateway -n "$NAMESPACE"
else
    echo "  WARNING: Gateway not found"
fi


step "FMA-specific objects (ISC, LauncherConfig, LPP, Deployment)"

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
kubectl label node "$GPU_NODE" fma-poc=true --overwrite=true 2>/dev/null
echo "  Labeled $GPU_NODE with fma-poc=true"

if kubectl get inferenceserverconfig isc-smol -n "$NAMESPACE" &>/dev/null; then
    echo "  FMA objects already exist"
else
    echo "  Creating FMA objects..."
    # Render the runtimeClassName line conditionally — empty value means
    # "omit the field entirely" rather than producing 'runtimeClassName: ""'.
    if [ -n "$RUNTIME_CLASS_NAME" ]; then
        runtime_class_line="runtimeClassName: ${RUNTIME_CLASS_NAME}"
    else
        runtime_class_line=""
    fi
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
      VLLM_SERVER_DEV_MODE: "1"
      VLLM_LOGGING_LEVEL: "DEBUG"
    labels:
      llm-d.ai/inference-serving: "true"
      llm-d.ai/guide: "optimized-baseline"
      llm-d.ai/model: "SmolLM2-360M-Instruct"
      llm-d.ai/variant: wva-fma-va
    annotations:
      description: "FMA ISC - ${MODEL}"
  launcherConfigName: lc-fma
---
apiVersion: fma.llm-d.ai/v1alpha1
kind: LauncherConfig
metadata:
  name: lc-fma
spec:
  maxInstances: 1
  podTemplate:
    spec:
      ${runtime_class_line}
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
---
apiVersion: fma.llm-d.ai/v1alpha1
kind: LauncherPopulationPolicy
metadata:
  name: lpp-fma
spec:
  enhancedNodeSelector:
    labelSelector:
      matchLabels:
        fma-poc: "true"
        nvidia.com/gpu.present: "true"
  countForLauncher:
    - launcherConfigName: lc-fma
      launcherCount: 1
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: fma-requester
  labels:
    app: fma-requester
spec:
  replicas: 1
  selector:
    matchLabels:
      app: fma-requester
  template:
    metadata:
      labels:
        app: fma-requester
        llm-d.ai/variant: wva-fma-va
      annotations:
        dual-pods.llm-d.ai/admin-port: "8081"
        dual-pods.llm-d.ai/inference-server-config: "isc-smol"
    spec:
      ${runtime_class_line}
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

# Create PodMonitor for vLLM instances in launcher pods
if kubectl get podmonitor fma-vllm-monitor -n "$NAMESPACE" &>/dev/null; then
    echo "  PodMonitor for vLLM instances in launcher pods already exists"
else
    echo "  Creating PodMonitor for vLLM instances in launcher pods ..."


kubectl apply -f - <<EOF
apiVersion: monitoring.coreos.com/v1
kind: PodMonitor
metadata:
  name: fma-vllm-monitor
  namespace: ${NAMESPACE}
  labels:
    app: llm-inference
spec:
  selector:
    matchLabels:
      llm-d.ai/guide: "optimized-baseline"
  podMetricsEndpoints:
  - interval: 30s
    path: /metrics
    relabelings:
    # Only scrape the inference-server container (not state-change-reflector etc.)
    - action: keep
      regex: inference-server
      sourceLabels: [__meta_kubernetes_pod_container_name]
    # Force address to <pod-ip>:8000 — the launcher's vLLM /metrics port.
    - action: replace
      regex: (.+)
      replacement: \$1:8000
      sourceLabels: [__meta_kubernetes_pod_ip]
      targetLabel: __address__
    # Surface the variant label so WVA can correlate metrics to the VA.
    - action: replace
      sourceLabels: [__meta_kubernetes_pod_label_llm_d_ai_variant]
      targetLabel: llm_d_ai_variant
EOF
    echo "  PodMonitor created"
fi


step "WVA Objects (VariantAutoscaling, HPA)"

# Wait for the requester Deployment to become Available — i.e., the
# dual-pods controller has bound the requester to a launcher AND the bound
# pod's readiness probe is passing. On a cold cluster this includes pulling
# the (multi-GB CUDA) vLLM image and fetching model weights from HuggingFace,
# which typically takes 5–10 minutes for moderate models.
echo "  Waiting up to ${WAIT_REQUESTER_TIMEOUT} for Deployment/fma-requester to become Available..."
kubectl wait --for=condition=Available deployment/fma-requester \
    -n "$NAMESPACE" --timeout="$WAIT_REQUESTER_TIMEOUT" 2>/dev/null || \
    echo "  Deployment/fma-requester is not Available within ${WAIT_REQUESTER_TIMEOUT} — proceeding anyway"

if kubectl get variantautoscaling wva-fma-va -n "$NAMESPACE" &>/dev/null; then
    echo "  WVA objects already exist"
else
    echo "  Creating WVA VariantAutoscaling..."
    kubectl apply -f - <<EOF
apiVersion: llmd.ai/v1alpha1
kind: VariantAutoscaling
metadata:
  labels:
    wva.llmd.ai/controller-instance: ${CONTROLLER_INSTANCE}
    inference.optimization/acceleratorName: nvidia-gpu
  name: wva-fma-va
  namespace: ${NAMESPACE}
spec:
  maxReplicas: 2
  minReplicas: 0
  modelID: ${MODEL}
  scaleTargetRef:
    apiVersion: apps/v1
    kind: Deployment
    name: fma-requester
  variantCost: "10.0"
EOF
    echo "  VariantAutoscaling created"
fi

if kubectl get hpa wva-fma-hpa -n "$NAMESPACE" &>/dev/null; then
    echo "  WVA HPA already exists"
else
    echo "  Creating WVA HorizontalPodAutoscaler..."
    kubectl apply -f - <<EOF
apiVersion: autoscaling/v2
kind: HorizontalPodAutoscaler
metadata:
  name: wva-fma-hpa
  namespace: ${NAMESPACE}
spec:
  scaleTargetRef:
    apiVersion: apps/v1
    kind: Deployment
    name: fma-requester
  maxReplicas: 6
  #minReplicas: 0  # Scale to zero is an alpha feature
  behavior:
    scaleUp:
      stabilizationWindowSeconds: 0  # Tune based on your needs
      policies:
      - type: Pods
        value: 10
        periodSeconds: 15
    scaleDown:
      stabilizationWindowSeconds: 0  # Tune based on your needs
      policies:
      - type: Pods
        value: 10
        periodSeconds: 15
  metrics:
  - type: External
    external:
      metric:
        name: wva_desired_replicas
        selector:
          matchLabels:
            variant_name: wva-fma-va
            exported_namespace: ${NAMESPACE}
            controller_instance: ${CONTROLLER_INSTANCE}
      target:
        type: AverageValue
        averageValue: "1"
EOF
    echo "  HPA created"
fi


step "Validation"

echo "  Waiting for requester and launcher pods..."
kubectl wait --for=condition=Ready pod \
    -l app=fma-requester -n "$NAMESPACE" --timeout=300s 2>/dev/null || true

echo ""
echo "  --- FMA Controllers ---"
kubectl get deployment -n "$NAMESPACE" -l app.kubernetes.io/part-of=fma 2>/dev/null || true

echo ""
echo "  --- WVA Controller ---"
kubectl get deployment -n "$NAMESPACE" -l app.kubernetes.io/name=workload-variant-autoscaler 2>/dev/null || true

echo ""
echo "  --- FMA Launcher Pods ---"
kubectl get pods -n "$NAMESPACE" \
    -l app.kubernetes.io/component=launcher \
    -L dual-pods.llm-d.ai/dual,dual-pods.llm-d.ai/sleeping 2>/dev/null || true

echo ""
echo "  --- FMA CRDs ---"
kubectl get crd | grep -F "fma.llm-d.ai" 2>/dev/null || true

echo ""
echo "  --- FMA Custom Resources ---"
kubectl get inferenceserverconfig,launcherconfig,launcherpopulationpolicy -n "$NAMESPACE" 2>/dev/null || true

echo ""
echo "  --- WVA Custom Resources ---"
kubectl get variantautoscaling,hpa -n "$NAMESPACE" 2>/dev/null || true

echo ""
echo "  --- Monitoring Resources ---"
kubectl get podmonitor,servicemonitor -n "$NAMESPACE" 2>/dev/null || true

echo ""
echo "  --- llm-d EPP Resources ---"
kubectl get inferencepool,gateway,httproute -n "$NAMESPACE" 2>/dev/null || true

echo ""
echo "========================================"
echo "  Deployment Complete!"
echo "========================================"
echo ""
echo "  Namespace:           $NAMESPACE"
echo "  GPU Node:            $GPU_NODE"
echo "  Model:               $MODEL"
echo "  Controller Instance: $CONTROLLER_INSTANCE"
echo ""
echo "  Components installed:"
echo "    ✓ FMA CRDs and Controllers"
echo "    ✓ WVA Controller (${WVA_IMAGE_REPO}:${WVA_IMAGE_TAG})"
echo "    ✓ llm-d EPP (Gateway API + InferencePool)"
echo "    ✓ FMA objects (ISC, LauncherConfig, LPP, Deployment, PodMonitor)"
echo "    ✓ WVA objects (VariantAutoscaling, HPA)"
echo ""
echo "  Next steps:"
echo "    - Check WVA metrics: kubectl get --raw /apis/external.metrics.k8s.io/v1beta1"
echo "    - Check HPA status: kubectl get hpa wva-fma-hpa -n $NAMESPACE"
echo "    - Monitor pods: kubectl get pods -n $NAMESPACE -w"
echo "    - View WVA logs: kubectl logs -n $NAMESPACE -l app.kubernetes.io/name=workload-variant-autoscaler"
echo "    - View FMA logs: kubectl logs -n $NAMESPACE -l app.kubernetes.io/part-of=fma"
echo ""