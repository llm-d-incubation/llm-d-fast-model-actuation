#!/usr/bin/env bash
#
# WVA Stack Deployment Script for E2E Testing
# 
# This script clones the WVA repository and deploys the full stack
# (llm-d infrastructure, WVA controller, Prometheus, and optionally HPA)
# using the official deployment scripts.
#
# Prerequisites:
# - kubectl configured with cluster access
# - helm installed
# - git installed
# - HF_TOKEN environment variable set (for llm-d deployment)
#
# Image Version Overrides:
# This script sets updated image versions to override WVA upstream defaults:
# - LLM_D_RELEASE: v0.6.0 (upstream default: v0.3.0)
# - MODELSERVICE_IMAGE_VERSION: v0.4.11 (upstream default: v0.2.11)
# - LLM_D_SIM_VERSION: v0.8.2 (upstream default: older version)
# You can override these by setting environment variables before running the script.
#
# Usage:
#   ./deploy-wva-stack.sh [options]
#
# Options:
#   -h, --help              Show this help message
#   -c, --cleanup           Clean up existing deployment before installing
#   --cleanup-only          Clean up existing deployment and exit (skip deployment)
#   -d, --delete-repo       Delete WVA repository after deployment
#   --wva-only              Deploy only WVA with Prometheus (skip llm-d)
#   --llmd-only             Deploy only llm-d with Prometheus (skip WVA)
#                           Note: Prometheus is required for llm-d ServiceMonitor/PodMonitor CRDs
#   --with-hpa              Deploy HPA (default: false)
#   --with-va               Deploy VariantAutoscaling (default: false)
#   --kind                  Deploy to Kind cluster (uses kind-emulator scripts)
#   --create-kind           Create Kind cluster before deployment
#   --create-kind-only      Create Kind cluster only (skip deployment)
#   --delete-kind           Delete Kind cluster after deployment
#

set -e  # Exit on error
set -o pipefail  # Exit on pipe failure

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m' # No Color

# Logging functions
log_info() {
    echo -e "${BLUE}[INFO]${NC} $1"
}

log_success() {
    echo -e "${GREEN}[SUCCESS]${NC} $1"
}

log_warning() {
    echo -e "${YELLOW}[WARNING]${NC} $1"
}

log_error() {
    echo -e "${RED}[ERROR]${NC} $1"
}

# Track if script should cleanup on exit
CLEANUP_ON_EXIT=false

# Trap handler for cleanup - only cleanup on successful exit
cleanup_on_exit() {
    # Capture exit code immediately in a variable (not using local to avoid overwriting $?)
    exit_code=$?
    
    # Only cleanup if explicitly enabled AND exit was successful
    if [ "$CLEANUP_ON_EXIT" = true ] && [ $exit_code -eq 0 ]; then
        if [ "$DELETE_REPO_AFTER_DEPLOY" = true ]; then
            log_info "Cleaning up WVA repository..."
            rm -rf "$WORK_DIR"
            log_success "WVA repository removed"
        fi
    elif [ $exit_code -ne 0 ]; then
        log_warning "Script failed with exit code $exit_code - preserving repository for debugging"
        log_info "Repository location: $WORK_DIR"
        log_info "To manually cleanup: rm -rf $WORK_DIR"
    fi
}

# Set trap to call cleanup_on_exit on script exit
trap cleanup_on_exit EXIT

# Script configuration
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
WORK_DIR="${SCRIPT_DIR}/wva-deployment"
WVA_REPO_URL="https://github.com/llm-d/llm-d-workload-variant-autoscaler.git"
WVA_REPO_BRANCH="${WVA_REPO_BRANCH:-main}"
WVA_REPO_DIR="${WORK_DIR}/workload-variant-autoscaler"

# Deployment flags (can be overridden by environment variables or command-line args)
DEPLOY_WVA="${DEPLOY_WVA:-true}"
DEPLOY_LLM_D="${DEPLOY_LLM_D:-true}"
DEPLOY_VA="${DEPLOY_VA:-false}"
DEPLOY_HPA="${DEPLOY_HPA:-false}"
DEPLOY_PROMETHEUS="${DEPLOY_PROMETHEUS:-true}"
DEPLOY_PROMETHEUS_ADAPTER="${DEPLOY_PROMETHEUS_ADAPTER:-true}"

# Cleanup flags
CLEANUP_BEFORE_DEPLOY=false
CLEANUP_ONLY=false
DELETE_REPO_AFTER_DEPLOY=false  # Only delete repo when --delete-repo flag is used

# Kind cluster flags
USE_KIND="${USE_KIND:-false}"
CREATE_KIND_CLUSTER="${CREATE_KIND_CLUSTER:-false}"
CREATE_KIND_ONLY="${CREATE_KIND_ONLY:-false}"
DELETE_KIND_CLUSTER="${DELETE_KIND_CLUSTER:-false}"
KIND_CLUSTER_NAME="${KIND_CLUSTER_NAME:-kind-wva-gpu-cluster}"
KIND_CLUSTER_NODES="${KIND_CLUSTER_NODES:-3}"
KIND_CLUSTER_GPUS="${KIND_CLUSTER_GPUS:-4}"
KIND_CLUSTER_GPU_TYPE="${KIND_CLUSTER_GPU_TYPE:-mix}"

# Parse command-line arguments
while [[ $# -gt 0 ]]; do
    case $1 in
        -h|--help)
            grep '^#' "$0" | grep -v '#!/usr/bin/env' | sed 's/^# //' | sed 's/^#//'
            exit 0
            ;;
        -c|--cleanup)
            CLEANUP_BEFORE_DEPLOY=true
            shift
            ;;
        --cleanup-only)
            CLEANUP_ONLY=true
            shift
            ;;
        -d|--delete-repo)
            DELETE_REPO_AFTER_DEPLOY=true
            shift
            ;;
        --wva-only)
            DEPLOY_WVA=true
            DEPLOY_LLM_D=false
            # Keep Prometheus enabled - WVA needs it for metrics
            shift
            ;;
        --llmd-only)
            DEPLOY_WVA=false
            DEPLOY_LLM_D=true
            # llm-d charts include ServiceMonitor/PodMonitor resources that require
            # Prometheus Operator CRDs, so Prometheus must remain enabled.
            # However, Prometheus Adapter needs the CA cert which is extracted in
            # deploy_wva_prerequisites(), so we need to disable Prometheus Adapter
            # when WVA is not deployed (or extract the cert separately).
            DEPLOY_PROMETHEUS_ADAPTER=false
            log_warning "Prometheus Adapter disabled for --llmd-only (requires WVA prerequisites)"
            shift
            ;;
        --with-hpa)
            DEPLOY_HPA=true
            shift
            ;;
        --with-va)
            DEPLOY_VA=true
            shift
            ;;
        --kind)
            USE_KIND=true
            shift
            ;;
        --create-kind)
            CREATE_KIND_CLUSTER=true
            USE_KIND=true
            shift
            ;;
        --create-kind-only)
            CREATE_KIND_ONLY=true
            CREATE_KIND_CLUSTER=true
            USE_KIND=true
            shift
            ;;
        --delete-kind)
            DELETE_KIND_CLUSTER=true
            shift
            ;;
        *)
            log_error "Unknown option: $1"
            echo "Use --help for usage information"
            exit 1
            ;;
    esac
done

# Validate prerequisites
check_prerequisites() {
    log_info "Checking prerequisites..."
    
    local missing_tools=()
    
    # Base tools
    for tool in kubectl helm git; do
        if ! command -v "$tool" &> /dev/null; then
            missing_tools+=("$tool")
        fi
    done
    
    # Check for kind if using Kind cluster
    if [ "$USE_KIND" = "true" ] || [ "$CREATE_KIND_CLUSTER" = "true" ]; then
        if ! command -v kind &> /dev/null; then
            missing_tools+=("kind")
        fi
        if ! command -v docker &> /dev/null; then
            missing_tools+=("docker")
        fi
    fi
    
    if [ ${#missing_tools[@]} -ne 0 ]; then
        log_error "Missing required tools: ${missing_tools[*]}"
        log_error "Please install the missing tools and try again"
        exit 1
    fi
    
    # Check kubectl connectivity (skip if we're about to create a cluster)
    if [ "$CREATE_KIND_CLUSTER" != "true" ]; then
        if ! kubectl cluster-info &> /dev/null; then
            log_error "Cannot connect to Kubernetes cluster"
            log_error "Please configure kubectl and try again"
            exit 1
        fi
    fi
    
    # Check HF_TOKEN if deploying llm-d (not required for Kind emulator)
    if [ "$DEPLOY_LLM_D" = "true" ] && [ -z "$HF_TOKEN" ] && [ "$USE_KIND" != "true" ]; then
        log_warning "HF_TOKEN not set - llm-d deployment may fail"
        log_warning "Set HF_TOKEN environment variable for production deployments"
    fi
    
    log_success "Prerequisites check passed"
}

# Clone WVA repository
clone_wva_repo() {
    log_info "Cloning WVA repository..."
    
    # Create work directory
    mkdir -p "$WORK_DIR"
    
    # Clone or update repository
    if [ -d "$WVA_REPO_DIR" ]; then
        log_info "WVA repository already exists, pulling latest changes..."
        cd "$WVA_REPO_DIR"
        git fetch origin
        git checkout "$WVA_REPO_BRANCH"
        git pull origin "$WVA_REPO_BRANCH"
    else
        log_info "Cloning WVA repository (branch: $WVA_REPO_BRANCH)..."
        git clone -b "$WVA_REPO_BRANCH" "$WVA_REPO_URL" "$WVA_REPO_DIR"
    fi
    
    log_success "WVA repository ready at: $WVA_REPO_DIR"
}

# Create Kind cluster
create_kind_cluster() {
    log_info "Creating Kind cluster..."
    log_info "Cluster name: $KIND_CLUSTER_NAME"
    log_info "Nodes: $KIND_CLUSTER_NODES"
    log_info "GPUs per node: $KIND_CLUSTER_GPUS"
    log_info "GPU type: $KIND_CLUSTER_GPU_TYPE"
    
    cd "$WVA_REPO_DIR"
    
    # Check if cluster already exists
    if kind get clusters 2>/dev/null | grep -q "^${KIND_CLUSTER_NAME}$"; then
        log_warning "Kind cluster '$KIND_CLUSTER_NAME' already exists"
        log_info "Using existing cluster"
        return
    fi
    
    # Run Kind setup script
    if [ -f "deploy/kind-emulator/setup.sh" ]; then
        log_info "Running Kind cluster setup script..."
        # Export all KIND variables so the setup script can use them
        export CLUSTER_NAME="$KIND_CLUSTER_NAME"
        export KIND_CLUSTER_NAME
        export KIND_CLUSTER_NODES
        export KIND_CLUSTER_GPUS
        export KIND_CLUSTER_GPU_TYPE
        bash "deploy/kind-emulator/setup.sh" \
            -c "$KIND_CLUSTER_NAME" \
            -t "$KIND_CLUSTER_GPU_TYPE" \
            -n "$KIND_CLUSTER_NODES" \
            -g "$KIND_CLUSTER_GPUS"
        log_success "Kind cluster created successfully"
    else
        log_error "Kind setup script not found: deploy/kind-emulator/setup.sh"
        exit 1
    fi
}

# Delete Kind cluster
delete_kind_cluster() {
    log_info "Deleting Kind cluster: $KIND_CLUSTER_NAME"
    
    if kind get clusters 2>/dev/null | grep -q "^${KIND_CLUSTER_NAME}$"; then
        kind delete cluster --name "$KIND_CLUSTER_NAME"
        log_success "Kind cluster deleted"
    else
        log_warning "Kind cluster '$KIND_CLUSTER_NAME' not found"
    fi
}

# Cleanup existing deployment
cleanup_deployment() {
    log_info "Cleaning up existing deployment..."
    
    cd "$WVA_REPO_DIR"
    
    # Detect environment
    local environment="kubernetes"
    if [ "$USE_KIND" = "true" ]; then
        environment="kind-emulator"
    elif kubectl get namespace openshift &> /dev/null; then
        environment="openshift"
    fi
    
    log_info "Environment detected: $environment"
    
    # Set up environment variables required by WVA cleanup scripts
    export ENVIRONMENT="$environment"
    export WVA_PROJECT="$WVA_REPO_DIR"
    export DEPLOY_PROMETHEUS="${DEPLOY_PROMETHEUS:-true}"
    export DEPLOY_PROMETHEUS_ADAPTER="${DEPLOY_PROMETHEUS_ADAPTER:-true}"
    export DEPLOY_LLM_D="${DEPLOY_LLM_D:-true}"
    export DEPLOY_WVA="${DEPLOY_WVA:-true}"
    export DELETE_NAMESPACES="${DELETE_NAMESPACES:-true}"
    export SCALER_BACKEND="${SCALER_BACKEND:-prometheus-adapter}"
    
    # Detect actual llm-d namespace (could be llm-d-sim, llm-d-inference-scheduler, etc.)
    local detected_llmd_ns=$(kubectl get namespace -o name 2>/dev/null | grep 'llm-d' | head -n1 | cut -d'/' -f2)
    if [ -n "$detected_llmd_ns" ]; then
        log_info "Detected llm-d namespace: $detected_llmd_ns"
        export LLMD_NS="$detected_llmd_ns"
    else
        export LLMD_NS="${LLMD_NS:-llm-d-inference-scheduler}"
    fi
    
    # Set other namespace variables
    export MONITORING_NAMESPACE="${MONITORING_NAMESPACE:-workload-variant-autoscaler-monitoring}"
    export WVA_NS="${WVA_NS:-workload-variant-autoscaler-system}"
    export KEDA_NAMESPACE="${KEDA_NAMESPACE:-keda}"
    export WVA_RELEASE_NAME="${WVA_RELEASE_NAME:-workload-variant-autoscaler}"
    export PROMETHEUS_ADAPTER_RELEASE_NAME="${PROMETHEUS_ADAPTER_RELEASE_NAME:-prometheus-adapter}"
    export KEDA_RELEASE_NAME="${KEDA_RELEASE_NAME:-keda}"
    
    # Set llm-d variables
    export WELL_LIT_PATH_NAME="${WELL_LIT_PATH_NAME:-inference-scheduling}"
    export NAMESPACE_SUFFIX="${NAMESPACE_SUFFIX:-inference-scheduler}"
    export LLM_D_PROJECT="${LLM_D_PROJECT:-llm-d}"
    export EXAMPLE_DIR="$WVA_REPO_DIR/$LLM_D_PROJECT/guides/$WELL_LIT_PATH_NAME"
    export GATEWAY_PREREQ_DIR="$WVA_REPO_DIR/$LLM_D_PROJECT/guides/prereq/gateway-provider"
    export GATEWAY_PROVIDER="${GATEWAY_PROVIDER:-istio}"
    export INSTALL_GATEWAY_CTRLPLANE="${INSTALL_GATEWAY_CTRLPLANE:-false}"
    export PROM_CA_CERT_PATH="${PROM_CA_CERT_PATH:-/tmp/prometheus-ca.crt}"
    export PROMETHEUS_CA_CONFIGMAP_NAME="${PROMETHEUS_CA_CONFIGMAP_NAME:-prometheus-ca-cert}"
    
    # Check if cleanup library exists
    local cleanup_lib="deploy/lib/cleanup.sh"
    if [ ! -f "$cleanup_lib" ]; then
        log_error "WVA cleanup library not found at: $cleanup_lib"
        log_error "Cannot proceed with cleanup"
        exit 1
    fi
    
    # Source required libraries from WVA repository in correct order
    log_info "Loading WVA cleanup libraries..."
    local deploy_lib_dir="$WVA_REPO_DIR/deploy/lib"
    
    # Source common functions first (provides log functions, containsElement, etc.)
    if [ -f "$deploy_lib_dir/common.sh" ]; then
        # shellcheck source=/dev/null
        source "$deploy_lib_dir/common.sh"
    else
        log_warning "common.sh not found, some functions may be missing"
    fi
    
    # Source all required libraries for cleanup in dependency order
    local required_libs=(
        "scaler_runtime.sh"                # Provides stop_apiservice_guard
        "deploy_prometheus_kube_stack.sh"  # Provides undeploy_prometheus_kube_stack
        "infra_wva.sh"                     # Provides delete_namespaces_kube_like
        "kube_like_adapter.sh"             # Provides wrapper functions (undeploy_prometheus_stack, delete_namespaces)
        "infra_monitoring.sh"              # Provides monitoring-related functions
    )
    
    for lib in "${required_libs[@]}"; do
        if [ -f "$deploy_lib_dir/$lib" ]; then
            # shellcheck source=/dev/null
            source "$deploy_lib_dir/$lib"
        else
            log_warning "Library not found: $lib (some cleanup functions may fail)"
        fi
    done
    
    # Source cleanup library last (depends on functions from other libraries)
    # shellcheck source=/dev/null
    source "$cleanup_lib"
    
    # Define delete_namespaces function (environment-specific, not in shared libs)
    delete_namespaces() {
        delete_namespaces_kube_like
    }
    
    # Run the cleanup function from WVA repository
    log_info "Running WVA cleanup function..."
    echo ""
    echo "=========================================="
    echo "WVA Cleanup Output:"
    echo "=========================================="
    
    # Disable exit on error temporarily to capture cleanup result
    set +e
    cleanup
    local cleanup_exit_code=$?
    set -e
    
    echo ""
    echo "=========================================="
    
    if [ $cleanup_exit_code -eq 0 ]; then
        log_success "Cleanup completed"
    else
        log_warning "Cleanup completed with warnings (exit code: $cleanup_exit_code)"
        log_info "Some resources may not have been found or were already deleted"
    fi
}

# Check if Prometheus is already installed cluster-wide
check_existing_prometheus() {
    log_info "Checking for existing Prometheus installation..."
    
    # Check for Prometheus Operator CRDs (most reliable indicator)
    if kubectl get crd prometheuses.monitoring.coreos.com &> /dev/null; then
        log_warning "Existing Prometheus Operator detected (CRD: prometheuses.monitoring.coreos.com)"
        log_warning "Disabling Prometheus deployment to avoid conflicts"
        DEPLOY_PROMETHEUS=false
        DEPLOY_PROMETHEUS_ADAPTER=false
        export DEPLOY_PROMETHEUS DEPLOY_PROMETHEUS_ADAPTER
        return 0
    fi
    
    # Check for Prometheus services in common namespaces
    if kubectl get svc --all-namespaces -l app.kubernetes.io/name=prometheus -o name 2>/dev/null | grep -q .; then
        log_warning "Existing Prometheus service detected"
        log_warning "Disabling Prometheus deployment to avoid conflicts"
        DEPLOY_PROMETHEUS=false
        DEPLOY_PROMETHEUS_ADAPTER=false
        export DEPLOY_PROMETHEUS DEPLOY_PROMETHEUS_ADAPTER
        return 0
    fi
    
    log_info "No existing Prometheus installation detected - will deploy Prometheus"
    return 1
}

# Deploy WVA stack
deploy_wva_stack() {
    log_info "Deploying WVA stack..."
    
    # Check for existing Prometheus installation (unless explicitly disabled or using Kind)
    if [ "$DEPLOY_PROMETHEUS" = "true" ] && [ "$USE_KIND" != "true" ]; then
        check_existing_prometheus
    fi
    
    log_info "Configuration:"
    log_info "  DEPLOY_WVA: $DEPLOY_WVA"
    log_info "  DEPLOY_LLM_D: $DEPLOY_LLM_D"
    log_info "  DEPLOY_VA: $DEPLOY_VA"
    log_info "  DEPLOY_HPA: $DEPLOY_HPA"
    log_info "  DEPLOY_PROMETHEUS: $DEPLOY_PROMETHEUS"
    log_info "  DEPLOY_PROMETHEUS_ADAPTER: $DEPLOY_PROMETHEUS_ADAPTER"
    log_info "  USE_KIND: $USE_KIND"
    
    cd "$WVA_REPO_DIR"
    
    # Detect environment
    local environment="kubernetes"
    if [ "$USE_KIND" = "true" ]; then
        environment="kind-emulator"
        log_info "Using Kind emulator environment"
    elif kubectl get namespace openshift &> /dev/null; then
        environment="openshift"
        log_info "Detected OpenShift environment"
    else
        log_info "Detected Kubernetes environment"
    fi
    
    # Export deployment flags
    export DEPLOY_WVA
    export DEPLOY_LLM_D
    export DEPLOY_VA
    export DEPLOY_HPA
    export DEPLOY_PROMETHEUS
    export DEPLOY_PROMETHEUS_ADAPTER
    export ENVIRONMENT="$environment"
    
    # Export WVA_PROJECT to point to the repository root
    export WVA_PROJECT="$WVA_REPO_DIR"
    
    # Export KIND cluster configuration (for kind-emulator environment)
    if [ "$USE_KIND" = "true" ]; then
        export CLUSTER_NAME="$KIND_CLUSTER_NAME"
        export KIND_CLUSTER_NAME
        export KIND_CLUSTER_NODES
        export KIND_CLUSTER_GPUS
        export KIND_CLUSTER_GPU_TYPE
        log_info "Exporting KIND cluster configuration: $KIND_CLUSTER_NAME"
    fi
    
    # Export image versions to override WVA defaults
    # The upstream WVA install script defaults to older versions that may cause
    # compatibility issues. These exports ensure we use the latest stable releases.
    # Users can override these by setting environment variables before running this script.
    
    # llm-d release version (upstream default: v0.3.0, latest: v0.6.0)
    # This controls which version of the llm-d repository is cloned
    export LLM_D_RELEASE="${LLM_D_RELEASE:-v0.6.0}"
    log_info "Using llm-d release: $LLM_D_RELEASE"
    
    # Model service image version (upstream default: v0.2.11, latest: v0.4.11)
    # This is the image used for serving models in production environments
    export MODELSERVICE_IMAGE_VERSION="${MODELSERVICE_IMAGE_VERSION:-v0.4.11}"
    log_info "Using modelservice image version: $MODELSERVICE_IMAGE_VERSION"
    
    # llm-d-sim (inference simulator) version for Kind emulator (upstream default: older, latest: v0.8.2)
    # This is used instead of real model serving in Kind emulator environments
    export LLM_D_SIM_VERSION="${LLM_D_SIM_VERSION:-v0.8.2}"
    log_info "Using llm-d-sim version: $LLM_D_SIM_VERSION"
    
    # Export HF_TOKEN if set (or use dummy for Kind)
    if [ -n "$HF_TOKEN" ]; then
        export HF_TOKEN
    elif [ "$USE_KIND" = "true" ]; then
        export HF_TOKEN="dummy-token"
        log_info "Using dummy HF_TOKEN for Kind emulator"
    fi
    
    # Use the main install script which handles environment detection
    local install_script="deploy/install.sh"
    if [ ! -f "$install_script" ]; then
        log_error "Install script not found: $install_script"
        exit 1
    fi
    
    log_info "Running main install script: $install_script"
    log_info "Working directory: $WVA_REPO_DIR"
    log_info "Environment: $environment"
    echo ""
    echo "=========================================="
    echo "Install Script Output:"
    echo "=========================================="
    
    # Run install script with output visible
    if bash "$install_script"; then
        echo ""
        echo "=========================================="
        log_success "WVA stack deployment complete"
    else
        echo ""
        echo "=========================================="
        log_error "Install script failed with exit code: $?"
        exit 1
    fi
}

# Verify deployment
verify_deployment() {
    log_info "Verifying deployment..."
    
    local all_ready=true
    
    # Check WVA namespace
    if [ "$DEPLOY_WVA" = "true" ]; then
        log_info "Checking WVA controller..."
        if kubectl get deployment -n workload-variant-autoscaler-system workload-variant-autoscaler-controller-manager &> /dev/null; then
            if kubectl wait --for=condition=Available deployment/workload-variant-autoscaler-controller-manager \
                -n workload-variant-autoscaler-system --timeout=60s &> /dev/null; then
                log_success "WVA controller is ready"
            else
                log_warning "WVA controller is not ready yet"
                all_ready=false
            fi
        else
            log_warning "WVA controller deployment not found"
            all_ready=false
        fi
    fi
    
    # Check llm-d namespace
    if [ "$DEPLOY_LLM_D" = "true" ]; then
        log_info "Checking llm-d infrastructure..."
        local llmd_ns=$(kubectl get namespace -o name | grep 'llm-d' | head -n1 | cut -d'/' -f2)
        if [ -n "$llmd_ns" ]; then
            log_success "llm-d namespace found: $llmd_ns"
        else
            log_warning "llm-d namespace not found"
            all_ready=false
        fi
    fi
    
    # Check Prometheus
    if [ "$DEPLOY_PROMETHEUS" = "true" ]; then
        log_info "Checking Prometheus..."
        if kubectl get statefulset -n workload-variant-autoscaler-monitoring prometheus-kube-prometheus-stack-prometheus &> /dev/null; then
            log_success "Prometheus is deployed"
        else
            log_warning "Prometheus not found"
            all_ready=false
        fi
    fi
    
    if [ "$all_ready" = true ]; then
        log_success "All components verified successfully"
    else
        log_warning "Some components are not ready yet - check logs for details"
    fi
}

# Print deployment summary
print_summary() {
    echo ""
    log_success "=========================================="
    log_success "WVA Stack Deployment Summary"
    log_success "=========================================="
    echo ""
    log_info "Deployed components:"
    [ "$DEPLOY_WVA" = "true" ] && echo "  ✓ WVA Controller"
    [ "$DEPLOY_LLM_D" = "true" ] && echo "  ✓ llm-d Infrastructure"
    [ "$DEPLOY_VA" = "true" ] && echo "  ✓ VariantAutoscaling"
    [ "$DEPLOY_HPA" = "true" ] && echo "  ✓ HorizontalPodAutoscaler"
    [ "$DEPLOY_PROMETHEUS" = "true" ] && echo "  ✓ Prometheus"
    [ "$DEPLOY_PROMETHEUS_ADAPTER" = "true" ] && echo "  ✓ Prometheus Adapter"
    echo ""
    log_info "Useful commands:"
    echo "  # View WVA logs"
    echo "  kubectl logs -n workload-variant-autoscaler-system -l control-plane=controller-manager -f"
    echo ""
    echo "  # View llm-d resources"
    echo "  kubectl get all -n \$(kubectl get ns -o name | grep llm-d | head -n1 | cut -d'/' -f2)"
    echo ""
    echo "  # View VariantAutoscaling resources"
    echo "  kubectl get variantautoscalings.llmd.ai -A"
    echo ""
    echo "  # View HPA resources"
    echo "  kubectl get hpa -A"
    echo ""
    log_success "=========================================="
}

# Main execution
main() {
    # Check if this is create-kind-only mode
    if [ "$CREATE_KIND_ONLY" = true ]; then
        log_info "Creating Kind cluster only (skipping deployment)..."
        log_info "Work directory: $WORK_DIR"
        
        # Check prerequisites
        check_prerequisites
        
        # Clone repository (needed for Kind setup scripts)
        clone_wva_repo
        
        # Create Kind cluster
        create_kind_cluster
        
        # Enable cleanup on successful exit
        CLEANUP_ON_EXIT=true
        
        log_success "Kind cluster created successfully!"
        log_info "Cluster name: $KIND_CLUSTER_NAME"
        log_info "To deploy to this cluster, run: ./deploy-wva-stack.sh --kind"
        return
    fi
    
    # Check if this is cleanup-only mode (explicit --cleanup-only flag)
    if [ "$CLEANUP_ONLY" = true ]; then
        # This is cleanup-only mode - skip deployment
        log_info "Starting WVA stack cleanup..."
        log_info "Work directory: $WORK_DIR"
        
        # Check prerequisites
        check_prerequisites
        
        # Clone repository (needed for cleanup scripts)
        clone_wva_repo
        
        # Cleanup deployment
        cleanup_deployment
        
        # Delete Kind cluster if requested
        if [ "$DELETE_KIND_CLUSTER" = "true" ]; then
            delete_kind_cluster
        fi
        
        # Enable cleanup on successful exit
        CLEANUP_ON_EXIT=true
        
        log_success "Cleanup completed successfully!"
        return
    fi
    
    # Normal deployment mode
    log_info "Starting WVA stack deployment..."
    log_info "Work directory: $WORK_DIR"
    
    # Check prerequisites
    check_prerequisites
    
    # Clone repository first (needed for Kind cluster creation)
    clone_wva_repo
    
    # Create Kind cluster if requested
    if [ "$CREATE_KIND_CLUSTER" = "true" ]; then
        create_kind_cluster
    fi
    
    # Cleanup before deploy if requested
    if [ "$CLEANUP_BEFORE_DEPLOY" = true ]; then
        cleanup_deployment
    fi
    
    # Deploy stack
    deploy_wva_stack
    
    # Verify deployment
    verify_deployment
    
    # Delete Kind cluster if requested (unusual but supported)
    if [ "$DELETE_KIND_CLUSTER" = "true" ]; then
        delete_kind_cluster
    fi
    
    # Print summary
    print_summary
    
    # Enable cleanup on successful exit
    CLEANUP_ON_EXIT=true
    
    log_success "Deployment script completed successfully!"
}

# Run main function
main "$@"