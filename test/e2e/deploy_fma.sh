#!/usr/bin/env bash

# Usage: $0 [--standalone]
# Current working directory must be the root of the Git repository.
#
# Deploys the FMA controllers (dual-pods controller + launcher-populator)
# and waits for them to be available.
#
# By default the script assumes it runs inside the ci-e2e-openshift.yaml
# workflow, which has already created the namespace, GHCR pull secret, and
# patched the default ServiceAccount.  Pass --standalone to have the script
# create those resources itself (useful for kind clusters or manual runs).
#
# Required environment variables:
#   FMA_NAMESPACE       - target Kubernetes namespace
#   FMA_CHART_INSTANCE_NAME    - Helm release name
#   CONTAINER_IMG_REG   - container image registry/namespace
#                         (e.g. ghcr.io/llm-d-incubation/llm-d-fast-model-actuation)
#   IMAGE_TAG           - image tag for all components
#                         (e.g. ref-abcd1234)
#
# Additional env vars required when --standalone:
#   CR_USER             - container registry username (for pull secret)
#   CR_TOKEN            - container registry token
#
# Optional environment variables:
#   RUNTIME_CLASS_NAME  - if set, adds runtimeClassName to GPU pod specs
#                         (e.g. "nvidia" when the GPU operator requires it)
#   POLICIES_ENABLED    - "true"/"false"; auto-detected if unset
#   LIMIT               - timeout in seconds for wait loops (default: 600)
set -euo pipefail
set -x

# ---------------------------------------------------------------------------
# Helpers
# ---------------------------------------------------------------------------

step_num=0
total_steps=7

step() {
    step_num=$((step_num + 1))
    echo ""
    echo "========================================"
    echo "[deploy_fma] Step ${step_num}/${total_steps}: $*"
    echo "========================================"
    echo ""
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
for var in FMA_NAMESPACE FMA_CHART_INSTANCE_NAME CONTAINER_IMG_REG IMAGE_TAG; do
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

# Derive individual image references from registry + tag
CONTROLLER_IMAGE="${CONTAINER_IMG_REG}/dual-pods-controller:${IMAGE_TAG}"
REQUESTER_IMAGE="${CONTAINER_IMG_REG}/requester:${IMAGE_TAG}"
LAUNCHER_IMAGE="${CONTAINER_IMG_REG}/launcher:${IMAGE_TAG}"

echo "Configuration:"
echo "  FMA_NAMESPACE:    $FMA_NAMESPACE"
echo "  FMA_CHART_INSTANCE_NAME: $FMA_CHART_INSTANCE_NAME"
echo "  CONTAINER_IMG_REG: $CONTAINER_IMG_REG"
echo "  IMAGE_TAG:        $IMAGE_TAG"
echo "  CONTROLLER_IMAGE: $CONTROLLER_IMAGE"
echo "  REQUESTER_IMAGE:  $REQUESTER_IMAGE"
echo "  LAUNCHER_IMAGE:   $LAUNCHER_IMAGE"
echo "  STANDALONE:       $STANDALONE"
if [ "$STANDALONE" = true ]; then
    echo "  CR_USER:          ${CR_USER}"
    echo "  CR_TOKEN:         ${CR_TOKEN:0:4}****"
fi
echo "  RUNTIME_CLASS_NAME: ${RUNTIME_CLASS_NAME:-<unset>}"
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

CLUSTER_ROLE_NAME="${FMA_CHART_INSTANCE_NAME}-node-view"
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

echo "  Release:   $FMA_CHART_INSTANCE_NAME"
echo "  Namespace: $FMA_NAMESPACE"
echo "  Image:     $CONTROLLER_IMAGE"

helm upgrade --install "$FMA_CHART_INSTANCE_NAME" charts/fma-controllers \
    -n "$FMA_NAMESPACE" \
    --set global.imageRegistry="${CONTAINER_IMG_REG}" \
    --set global.imageTag="${IMAGE_TAG}" \
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
    deployment "${FMA_CHART_INSTANCE_NAME}-dual-pods-controller" -n "$FMA_NAMESPACE"
kubectl wait --for=condition=available --timeout=120s \
    deployment "${FMA_CHART_INSTANCE_NAME}-launcher-populator" -n "$FMA_NAMESPACE"
echo "Both controllers are available"

echo ""
echo "[deploy_fma] All steps completed successfully"
