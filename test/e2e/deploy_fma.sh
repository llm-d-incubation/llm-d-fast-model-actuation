#!/usr/bin/env bash

# Usage: $0
# Current working directory must be the root of the Git repository.
#
# Deploys the FMA controllers (dual-pods controller + launcher-populator)
# and waits for them to be available.
#
# Required environment variables:
#   FMA_NAMESPACE              - target Kubernetes namespace
#   FMA_CHART_INSTANCE_NAME    - Helm chart instance name
#   CONTAINER_IMG_REG          - container image registry/namespace
#                                (e.g. ghcr.io/llm-d-incubation/llm-d-fast-model-actuation)
#   IMAGE_TAG                  - image tag for all components
#                                (e.g. ref-abcd1234)
#
# Optional environment variables:
#   NODE_VIEW_CLUSTER_ROLE - ClusterRole granting node read access.
#                            If unset, the script creates one named
#                            "${FMA_CHART_INSTANCE_NAME}-node-view".
#                            If set to an existing ClusterRole name, it is
#                            used as-is (no creation).
#                            If set to "none", no ClusterRole is configured.
#   RUNTIME_CLASS_NAME  - if set, adds runtimeClassName to GPU pod specs
#                         (e.g. "nvidia" when the GPU operator requires it)
#   POLICIES_ENABLED    - "true"/"false"; auto-detected if unset
#   FMA_DEBUG            - "true" to enable shell tracing (set -x)
#   HELM_EXTRA_ARGS     - additional Helm arguments appended to the
#                         `helm upgrade --install` invocation
#                         (e.g. "--set global.local=true --set dualPodsController.sleeperLimit=4")

set -euo pipefail
if [ "${FMA_DEBUG:-false}" = "true" ]; then
    set -x
fi

# ---------------------------------------------------------------------------
# Helpers
# ---------------------------------------------------------------------------

step_num=0
total_steps=6

step() {
    step_num=$((step_num + 1))
    echo ""
    echo "========================================"
    echo "[deploy_fma] Step ${step_num}/${total_steps}: $*"
    echo "========================================"
    echo ""
}

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

if [ ${#missing[@]} -gt 0 ]; then
    echo "ERROR: Missing required environment variables: ${missing[*]}" >&2
    exit 1
fi

echo "Configuration:"
echo "  FMA_NAMESPACE:           $FMA_NAMESPACE"
echo "  FMA_CHART_INSTANCE_NAME: $FMA_CHART_INSTANCE_NAME"
echo "  CONTAINER_IMG_REG:       $CONTAINER_IMG_REG"
echo "  IMAGE_TAG:               $IMAGE_TAG"
echo "  NODE_VIEW_CLUSTER_ROLE:  ${NODE_VIEW_CLUSTER_ROLE:-<will create>}"
echo "  RUNTIME_CLASS_NAME:      ${RUNTIME_CLASS_NAME:-<unset>}"
echo "  POLICIES_ENABLED:        ${POLICIES_ENABLED:-<auto-detect>}"
echo "  HELM_EXTRA_ARGS:         ${HELM_EXTRA_ARGS:-<none>}"

# ---------------------------------------------------------------------------
# Step 2: Apply FMA CRDs
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
# Step 3: Create node-viewer ClusterRole
# ---------------------------------------------------------------------------

step "Configure node-viewer ClusterRole"

if [ "${NODE_VIEW_CLUSTER_ROLE:-}" = "none" ]; then
    CLUSTER_ROLE_NAME=""
    echo "Skipped (NODE_VIEW_CLUSTER_ROLE=none)"
elif [ -n "${NODE_VIEW_CLUSTER_ROLE:-}" ]; then
    CLUSTER_ROLE_NAME="${NODE_VIEW_CLUSTER_ROLE}"
    echo "Using existing ClusterRole: $CLUSTER_ROLE_NAME"
else
    CLUSTER_ROLE_NAME="${FMA_CHART_INSTANCE_NAME}-node-view"
    if kubectl get clusterrole "$CLUSTER_ROLE_NAME" &>/dev/null; then
        echo "ClusterRole $CLUSTER_ROLE_NAME already exists, skipping"
    else
        kubectl create clusterrole "$CLUSTER_ROLE_NAME" --verb=get,list,watch --resource=nodes
        echo "ClusterRole $CLUSTER_ROLE_NAME created"
    fi
fi

# ---------------------------------------------------------------------------
# Step 4: Detect and apply ValidatingAdmissionPolicies
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
# Step 5: Deploy FMA controllers via Helm
# ---------------------------------------------------------------------------

step "Deploy FMA controllers via Helm"

HELM_ARGS=(
    --set global.imageRegistry="${CONTAINER_IMG_REG}"
    --set global.imageTag="${IMAGE_TAG}"
)

# Append any caller-supplied Helm arguments (e.g. --set global.local=true)
if [ -n "${HELM_EXTRA_ARGS:-}" ]; then
    read -ra _extra <<< "$HELM_EXTRA_ARGS"
    HELM_ARGS+=("${_extra[@]}")
fi

if [ -n "$CLUSTER_ROLE_NAME" ]; then
    HELM_ARGS+=(--set global.nodeViewClusterRole="${CLUSTER_ROLE_NAME}")
fi

helm upgrade --install "$FMA_CHART_INSTANCE_NAME" charts/fma-controllers \
    -n "$FMA_NAMESPACE" \
    "${HELM_ARGS[@]}"

# ---------------------------------------------------------------------------
# Step 6: Wait for controllers to be ready
# ---------------------------------------------------------------------------

step "Wait for controllers to be ready"

kubectl wait --for=condition=available --timeout=120s \
    deployment "${FMA_CHART_INSTANCE_NAME}-dual-pods-controller" -n "$FMA_NAMESPACE"
kubectl wait --for=condition=available --timeout=120s \
    deployment "${FMA_CHART_INSTANCE_NAME}-launcher-populator" -n "$FMA_NAMESPACE"
echo "Both controllers are available"

echo ""
echo "[deploy_fma] All steps completed successfully"
