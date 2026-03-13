#!/usr/bin/env bash

# Usage: $0 [--standalone]
# Current working directory must be the root of the Git repository.
#
# Deploys the FMA controllers (dual-pods controller + launcher-populator),
# creates test objects (ISC, LC, LPP, ReplicaSet), and verifies that pods
# are created, bound, and ready.
#
# By default the script assumes it runs inside the ci-e2e-openshift.yaml
# workflow, which has already created the namespace, GHCR pull secret, and
# patched the default ServiceAccount.  Pass --standalone to have the script
# create those resources itself (useful for kind clusters or manual runs).
#
# Required environment variables:
#   FMA_NAMESPACE       - target Kubernetes namespace
#   FMA_RELEASE_NAME    - Helm release name
#   CONTROLLER_IMAGE    - dual-pods controller container image
#   REQUESTER_IMAGE     - requester container image
#   LAUNCHER_IMAGE      - launcher container image
#
# Additional env vars required when --standalone:
#   CR_USER             - container registry username (for pull secret)
#   CR_TOKEN            - container registry token
#
# Optional environment variables:
#   CLUSTER_TYPE        - if "pokprod", adds runtimeClassName: nvidia
#   POLICIES_ENABLED    - "true"/"false"; auto-detected if unset
#   LIMIT               - timeout in seconds for wait loops (default: 600)
#
# Outputs (written to $GITHUB_OUTPUT when set, and exported):
#   INST, ISC, LC, LPP, RS

set -euo pipefail
set -x

# ---------------------------------------------------------------------------
# Helpers
# ---------------------------------------------------------------------------

step_num=0
total_steps=15

step() {
    step_num=$((step_num + 1))
    echo ""
    echo "========================================"
    echo "[deploy_fma] Step ${step_num}/${total_steps}: $*"
    echo "========================================"
    echo ""
}

output() {
    local key="$1" val="$2"
    export "$key=$val"
    if [ -n "${GITHUB_OUTPUT:-}" ]; then
        echo "${key}=${val}" >> "$GITHUB_OUTPUT"
    fi
}

wait_loop() {
    local description="$1"
    local check_cmd="$2"
    local limit="${LIMIT:-600}"
    local elapsed=0
    local start
    start=$(date)

    echo "Waiting for: ${description} (limit: ${limit}s)"
    while true; do
        if eval "$check_cmd"; then
            echo "OK: ${description} (after ${elapsed}s)"
            return 0
        fi
        if (( elapsed >= limit )); then
            echo "TIMEOUT: ${description} (from $start to $(date))" >&2
            return 1
        fi
        sleep 5
        elapsed=$(( elapsed + 5 ))
    done
}

# ---------------------------------------------------------------------------
# Parse flags
# ---------------------------------------------------------------------------

STANDALONE=false
for arg in "$@"; do
    case "$arg" in
        --standalone) STANDALONE=true ;;
        *) echo "Unknown argument: $arg" >&2; exit 1 ;;
    esac
done

# ---------------------------------------------------------------------------
# Step 1: Validate required environment variables
# ---------------------------------------------------------------------------

step "Validate required environment variables"

missing=()
for var in FMA_NAMESPACE FMA_RELEASE_NAME CONTROLLER_IMAGE REQUESTER_IMAGE LAUNCHER_IMAGE; do
    if [ -z "${!var:-}" ]; then
        missing+=("$var")
    fi
done

if [ "$STANDALONE" = true ]; then
    for var in CR_USER CR_TOKEN; do
        if [ -z "${!var:-}" ]; then
            missing+=("$var")
        fi
    done
fi

if [ ${#missing[@]} -gt 0 ]; then
    echo "ERROR: Missing required environment variables: ${missing[*]}" >&2
    exit 1
fi

echo "Configuration:"
echo "  FMA_NAMESPACE:    $FMA_NAMESPACE"
echo "  FMA_RELEASE_NAME: $FMA_RELEASE_NAME"
echo "  CONTROLLER_IMAGE: $CONTROLLER_IMAGE"
echo "  REQUESTER_IMAGE:  $REQUESTER_IMAGE"
echo "  LAUNCHER_IMAGE:   $LAUNCHER_IMAGE"
echo "  STANDALONE:       $STANDALONE"
echo "  CLUSTER_TYPE:     ${CLUSTER_TYPE:-<unset>}"
echo "  POLICIES_ENABLED: ${POLICIES_ENABLED:-<auto-detect>}"
echo "  LIMIT:            ${LIMIT:-600}"

# ---------------------------------------------------------------------------
# Step 2: (standalone only) Create namespace, pull secret, patch SA
# ---------------------------------------------------------------------------

step "Standalone setup (namespace, pull secret)"

if [ "$STANDALONE" = true ]; then
    # Clean up existing namespace if present
    if kubectl get namespace "$FMA_NAMESPACE" &>/dev/null; then
        echo "Namespace $FMA_NAMESPACE exists, deleting..."
        # Remove dual-pods finalizers to unblock deletion
        for pod in $(kubectl get pods -n "$FMA_NAMESPACE" -o jsonpath='{.items[*].metadata.name}' 2>/dev/null); do
            all_finalizers=$(kubectl get pod "$pod" -n "$FMA_NAMESPACE" \
                -o jsonpath='{range .metadata.finalizers[*]}{@}{"\n"}{end}' 2>/dev/null || true)
            if echo "$all_finalizers" | grep -q '^dual-pods\.llm-d\.ai/'; then
                keep_entries=$(echo "$all_finalizers" \
                    | grep -v '^dual-pods\.llm-d\.ai/' \
                    | awk 'NR>1{printf ","} {printf "\"%s\"", $0}')
                kubectl patch pod "$pod" -n "$FMA_NAMESPACE" --type=merge \
                    -p="{\"metadata\":{\"finalizers\":[${keep_entries}]}}" 2>/dev/null || true
            fi
        done
        kubectl delete namespace "$FMA_NAMESPACE" --ignore-not-found --timeout=120s || true
        # Wait for full deletion
        while kubectl get namespace "$FMA_NAMESPACE" &>/dev/null; do
            echo "  Waiting for namespace deletion..."
            sleep 2
        done
    fi

    echo "Creating namespace $FMA_NAMESPACE..."
    kubectl create namespace "$FMA_NAMESPACE"

    echo "Creating GHCR pull secret..."
    kubectl create secret docker-registry ghcr-pull-secret \
        --docker-server=ghcr.io \
        --docker-username="$CR_USER" \
        --docker-password="$CR_TOKEN" \
        -n "$FMA_NAMESPACE"

    kubectl patch serviceaccount default -n "$FMA_NAMESPACE" \
        -p '{"imagePullSecrets": [{"name": "ghcr-pull-secret"}]}'
    echo "Standalone setup complete"
else
    echo "Skipped (not standalone — workflow handles namespace/secrets)"
fi

# ---------------------------------------------------------------------------
# Step 3: Apply FMA CRDs
# ---------------------------------------------------------------------------

step "Apply FMA CRDs"

CRD_NAMES=""
for crd_file in config/crd/*.yaml; do
    crd_name=$(kubectl apply --dry-run=client -f "$crd_file" -o jsonpath='{.metadata.name}')
    CRD_NAMES="$CRD_NAMES $crd_name"
    if kubectl get crd "$crd_name" &>/dev/null; then
        echo "  CRD $crd_name already exists, skipping"
    else
        echo "  Applying $crd_file ($crd_name)"
        kubectl apply --server-side -f "$crd_file"
    fi
done

echo "Waiting for CRDs to become Established..."
for crd_name in $CRD_NAMES; do
    kubectl wait --for=condition=Established "crd/$crd_name" --timeout=120s
done
echo "All CRDs established"

# ---------------------------------------------------------------------------
# Step 4: Create node-viewer ClusterRole
# ---------------------------------------------------------------------------

step "Create node-viewer ClusterRole"

CLUSTER_ROLE_NAME="${FMA_RELEASE_NAME}-node-view"
if kubectl get clusterrole "$CLUSTER_ROLE_NAME" &>/dev/null; then
    echo "ClusterRole $CLUSTER_ROLE_NAME already exists, skipping"
else
    kubectl create clusterrole "$CLUSTER_ROLE_NAME" --verb=get,list,watch --resource=nodes
    echo "ClusterRole $CLUSTER_ROLE_NAME created"
fi

# ---------------------------------------------------------------------------
# Step 5: Detect and apply ValidatingAdmissionPolicies
# ---------------------------------------------------------------------------

step "ValidatingAdmissionPolicies"

if [ -z "${POLICIES_ENABLED:-}" ]; then
    POLICIES_ENABLED=false
    if kubectl api-resources --api-group=admissionregistration.k8s.io -o name 2>/dev/null \
       | grep -q 'validatingadmissionpolicies'; then
        POLICIES_ENABLED=true
    fi
    echo "Auto-detected POLICIES_ENABLED=$POLICIES_ENABLED"
fi

if [ "$POLICIES_ENABLED" = "true" ]; then
    echo "Applying ValidatingAdmissionPolicy resources..."
    kubectl apply -f config/validating-admission-policies/
else
    echo "ValidatingAdmissionPolicy not supported or disabled, skipping"
fi

# ---------------------------------------------------------------------------
# Step 6: Deploy FMA controllers via Helm
# ---------------------------------------------------------------------------

step "Deploy FMA controllers via Helm"

echo "  Release:   $FMA_RELEASE_NAME"
echo "  Namespace: $FMA_NAMESPACE"
echo "  Image:     $CONTROLLER_IMAGE"

helm upgrade --install "$FMA_RELEASE_NAME" charts/fma-controllers \
    -n "$FMA_NAMESPACE" \
    --set global.imageRegistry="${CONTROLLER_IMAGE%/dual-pods-controller:*}" \
    --set global.imageTag="${CONTROLLER_IMAGE##*:}" \
    --set global.nodeViewClusterRole="${CLUSTER_ROLE_NAME}" \
    --set dualPodsController.sleeperLimit=2 \
    --set global.local=false \
    --set dualPodsController.debugAcceleratorMemory=false \
    --set launcherPopulator.enabled=true

# ---------------------------------------------------------------------------
# Step 7: Wait for controllers to be ready
# ---------------------------------------------------------------------------

step "Wait for controllers to be ready"

kubectl wait --for=condition=available --timeout=120s \
    deployment "${FMA_RELEASE_NAME}-dual-pods-controller" -n "$FMA_NAMESPACE"
echo ""
echo "=== Dual-Pod Controller ==="
kubectl get pods -n "$FMA_NAMESPACE" -l app.kubernetes.io/component=dual-pods-controller
kubectl get deployment "${FMA_RELEASE_NAME}-dual-pods-controller" -n "$FMA_NAMESPACE"

kubectl wait --for=condition=available --timeout=120s \
    deployment "${FMA_RELEASE_NAME}-launcher-populator" -n "$FMA_NAMESPACE"
echo ""
echo "=== Launcher Populator ==="
kubectl get pods -n "$FMA_NAMESPACE" -l app.kubernetes.io/component=launcher-populator
kubectl get deployment "${FMA_RELEASE_NAME}-launcher-populator" -n "$FMA_NAMESPACE"

# ---------------------------------------------------------------------------
# Step 8: Verify controller health
# ---------------------------------------------------------------------------

step "Verify controller health"

POD_NAME=$(kubectl get pods -n "$FMA_NAMESPACE" \
    -l app.kubernetes.io/name=fma-controllers,app.kubernetes.io/component=dual-pods-controller \
    -o jsonpath='{.items[0].metadata.name}')

if [ -z "$POD_NAME" ]; then
    echo "ERROR: No controller pod found" >&2
    exit 1
fi

echo "Controller pod: $POD_NAME"

PHASE=$(kubectl get pod "$POD_NAME" -n "$FMA_NAMESPACE" -o jsonpath='{.status.phase}')
if [ "$PHASE" != "Running" ]; then
    echo "ERROR: Controller pod is in phase $PHASE, expected Running" >&2
    kubectl describe pod "$POD_NAME" -n "$FMA_NAMESPACE"
    exit 1
fi

RESTARTS=$(kubectl get pod "$POD_NAME" -n "$FMA_NAMESPACE" \
    -o jsonpath='{.status.containerStatuses[0].restartCount}')
if [ "$RESTARTS" -gt 0 ]; then
    echo "WARNING: Controller has restarted $RESTARTS time(s)"
fi

echo ""
echo "=== Controller Logs (last 50 lines) ==="
kubectl logs "$POD_NAME" -n "$FMA_NAMESPACE" --tail=50

# Check for fatal/panic in logs
FATAL_LINES=$(kubectl logs "$POD_NAME" -n "$FMA_NAMESPACE" 2>&1 \
    | grep -E "^F[0-9]{4} |^panic:" | head -5) || true
if [ -n "$FATAL_LINES" ]; then
    echo "ERROR: Controller logs contain FATAL or panic messages:" >&2
    echo "$FATAL_LINES" >&2
    exit 1
fi
echo "Controller health check passed"

# ---------------------------------------------------------------------------
# Step 9: Create test service account
# ---------------------------------------------------------------------------

step "Create test service account"

kubectl create sa testreq -n "$FMA_NAMESPACE" || true
kubectl patch serviceaccount testreq -n "$FMA_NAMESPACE" \
    -p '{"imagePullSecrets": [{"name": "ghcr-pull-secret"}]}'
echo "Service account testreq ready"

# ---------------------------------------------------------------------------
# Step 10: Create test objects (ISC, LC, LPP, ReplicaSet)
# ---------------------------------------------------------------------------

step "Create test objects"

INST=$(date +%d-%H-%M-%S)
echo "Instance ID: $INST"

RUNTIME_CLASS=""
if [ "${CLUSTER_TYPE:-}" = "pokprod" ]; then
    RUNTIME_CLASS="runtimeClassName: nvidia"
fi

kubectl apply -n "$FMA_NAMESPACE" -f - <<EOF
apiVersion: fma.llm-d.ai/v1alpha1
kind: InferenceServerConfig
metadata:
  name: inference-server-config-${INST}
spec:
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
      description: "E2E test InferenceServerConfig"
  launcherConfigName: launcher-config-${INST}
---
apiVersion: fma.llm-d.ai/v1alpha1
kind: LauncherConfig
metadata:
  name: launcher-config-${INST}
spec:
  maxSleepingInstances: 3
  podTemplate:
    spec:
      ${RUNTIME_CLASS}
      imagePullSecrets:
        - name: ghcr-pull-secret
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
  name: lpp-${INST}
spec:
  enhancedNodeSelector:
    labelSelector:
      matchLabels:
        nvidia.com/gpu.present: "true"
  countForLauncher:
    - launcherConfigName: launcher-config-${INST}
      launcherCount: 1
---
apiVersion: apps/v1
kind: ReplicaSet
metadata:
  name: my-request-${INST}
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
        instance: "${INST}"
      annotations:
        dual-pods.llm-d.ai/admin-port: "8081"
        dual-pods.llm-d.ai/inference-server-config: "inference-server-config-${INST}"
    spec:
      ${RUNTIME_CLASS}
      imagePullSecrets:
        - name: ghcr-pull-secret
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
      serviceAccount: testreq
EOF

ISC="inference-server-config-${INST}"
LC="launcher-config-${INST}"
LPP="lpp-${INST}"
RS="my-request-${INST}"

output "INST" "$INST"
output "ISC" "$ISC"
output "LC" "$LC"
output "LPP" "$LPP"
output "RS" "$RS"

echo "Test objects created: ISC=$ISC LC=$LC LPP=$LPP RS=$RS"

# ---------------------------------------------------------------------------
# Step 11: Wait for launcher pod creation
# ---------------------------------------------------------------------------

step "Wait for launcher pod creation"

# Count schedulable GPU nodes — launcher-populator creates one per node
GPU_NODES=$(kubectl get nodes -l nvidia.com/gpu.present=true --field-selector spec.unschedulable!=true -o name | wc -l | tr -d ' ')
echo "Expecting launcher-populator to create $GPU_NODES launcher(s)"

wait_loop "launcher pod(s) created" \
    '[ "$(kubectl get pods -n "$FMA_NAMESPACE" -l "dual-pods.llm-d.ai/launcher-config-name=$LC" -o json 2>/dev/null | jq ".items | length")" -ge "$GPU_NODES" ]'

kubectl get pods -n "$FMA_NAMESPACE" -l "dual-pods.llm-d.ai/launcher-config-name=$LC" -o wide

# ---------------------------------------------------------------------------
# Step 12: Wait for launcher pod(s) Ready
# ---------------------------------------------------------------------------

step "Wait for launcher pod(s) Ready"

# Launcher image is ~20GB, allow extra time for uncached pulls
kubectl wait pods --for=condition=Ready -n "$FMA_NAMESPACE" \
    -l "dual-pods.llm-d.ai/launcher-config-name=$LC" --timeout="${LIMIT:-600}s"
echo "All launcher pods Ready"

# ---------------------------------------------------------------------------
# Step 13: Verify bidirectional launcher↔requester binding
# ---------------------------------------------------------------------------

step "Verify launcher-requester binding"

# Find the requester pod
echo "Waiting for requester pod..."
wait_loop "requester pod exists" \
    '[ "$(kubectl get pods -n "$FMA_NAMESPACE" -l "app=dp-example,instance=$INST" -o json 2>/dev/null | jq ".items | length")" -ge 1 ]'

REQUESTER=$(kubectl get pods -n "$FMA_NAMESPACE" -l "app=dp-example,instance=$INST" -o json | jq -r '.items[0].metadata.name')
echo "Requester pod: $REQUESTER"

# Verify launcher→requester binding
echo "Verifying launcher-to-requester binding..."
wait_loop "launcher bound to requester" \
    '[ -n "$(kubectl get pods -n "$FMA_NAMESPACE" -l "dual-pods.llm-d.ai/launcher-config-name=$LC,dual-pods.llm-d.ai/dual=$REQUESTER" -o json | jq -r ".items[0].metadata.name // empty")" ]'

LAUNCHER=$(kubectl get pods -n "$FMA_NAMESPACE" \
    -l "dual-pods.llm-d.ai/launcher-config-name=$LC,dual-pods.llm-d.ai/dual=$REQUESTER" \
    -o json | jq -r '.items[0].metadata.name')
echo "Launcher bound to requester: $LAUNCHER -> $REQUESTER"

# Verify requester→launcher binding (reverse)
echo "Verifying requester-to-launcher binding..."
wait_loop "requester bound to launcher" \
    '[ "$(kubectl get pod "$REQUESTER" -n "$FMA_NAMESPACE" -o json | jq -r ".metadata.labels[\"dual-pods.llm-d.ai/dual\"] // empty")" = "$LAUNCHER" ]'
echo "Requester bound to launcher: $REQUESTER -> $LAUNCHER"

# ---------------------------------------------------------------------------
# Step 14: Wait for requester Ready
# ---------------------------------------------------------------------------

step "Wait for requester Ready"

kubectl wait --for=condition=Ready "pod/$REQUESTER" -n "$FMA_NAMESPACE" --timeout=120s
echo "Requester pod Ready"

# ---------------------------------------------------------------------------
# Step 15: Final status
# ---------------------------------------------------------------------------

step "Deployment complete"

echo ""
echo "=== All pods in namespace ==="
kubectl get pods -n "$FMA_NAMESPACE" -o wide --show-labels
echo ""
echo "=== Test object names ==="
echo "  INST=$INST"
echo "  ISC=$ISC"
echo "  LC=$LC"
echo "  LPP=$LPP"
echo "  RS=$RS"
echo "  LAUNCHER=$LAUNCHER"
echo "  REQUESTER=$REQUESTER"
echo ""
echo "[deploy_fma] All steps completed successfully"
