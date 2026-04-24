# WVA Stack Deployment Script

This directory contains a deployment script for setting up the Workload-Variant-Autoscaler (WVA) stack for E2E testing.

## Table of Contents

- [Overview](#overview)
- [Prerequisites](#prerequisites)
- [Quick Start](#quick-start)
- [Configuration](#configuration)
- [Usage Examples](#usage-examples)
- [Verification](#verification)
- [Viewing Logs](#viewing-logs)
- [Cleanup](#cleanup)
- [Kind Cluster Configuration](#kind-cluster-configuration)

## Overview

The `deploy-wva-stack.sh` script automates the deployment of:
- WVA Controller
- llm-d Infrastructure
- Prometheus Monitoring
- Prometheus Adapter
- Optional: VariantAutoscaling (VA)
- Optional: HorizontalPodAutoscaler (HPA)

The script clones the official WVA repository from GitHub and uses the deployment scripts provided there.

## Prerequisites

- `kubectl` configured with cluster access
- `helm` installed
- `git` installed
- `HF_TOKEN` environment variable set (for llm-d deployment)

## Quick Start

### Basic Deployment (WVA + llm-d + Prometheus)

**On existing Kubernetes cluster:**
```bash
export HF_TOKEN="hf_xxxxx"
./deploy-wva-stack.sh
```

**On Kind cluster (emulated GPUs):**
```bash
export KIND_CLUSTER_NAME="kind-fma-cluster"
./deploy-wva-stack.sh --kind
```

This deploys:
- ✅ WVA Controller
- ✅ llm-d Infrastructure (with simulator for Kind)
- ✅ Prometheus
- ✅ Prometheus Adapter
- ❌ VariantAutoscaling (opt-in)
- ❌ HPA (opt-in)

### Deployment with HPA and VA

**On existing cluster:**
```bash
export HF_TOKEN="hf_xxxxx"
./deploy-wva-stack.sh --with-hpa --with-va
```

**On Kind cluster:**
```bash
export KIND_CLUSTER_NAME="kind-fma-cluster"
./deploy-wva-stack.sh --kind --with-hpa --with-va
```

### Create Kind Cluster and Deploy

```bash
# Create Kind cluster and deploy full stack
./deploy-wva-stack.sh --create-kind --with-hpa --with-va
```

### Create Kind Cluster Only (No Deployment)

```bash
# Create Kind cluster with custom configuration (no deployment)
export KIND_CLUSTER_NAME="kind-fma-cluster"
export KIND_CLUSTER_NODES=4
export KIND_CLUSTER_GPUS=2
export KIND_CLUSTER_GPU_TYPE="nvidia"
./deploy-wva-stack.sh --create-kind-only

# Later, deploy to the existing cluster
# IMPORTANT: Must export KIND_CLUSTER_NAME to match your cluster
export KIND_CLUSTER_NAME="kind-fma-cluster"
./deploy-wva-stack.sh --kind --with-hpa
```

**Note:** When deploying to an existing Kind cluster with `--kind`, you must export `KIND_CLUSTER_NAME` to match your cluster name (default: `kind-wva-gpu-cluster`).

### WVA Only (No llm-d)

```bash
./deploy-wva-stack.sh --wva-only
```

### llm-d Only (No WVA)

```bash
export HF_TOKEN="hf_xxxxx"
./deploy-wva-stack.sh --llmd-only
```

## Configuration

### Environment Variables

You can configure the deployment using environment variables:

```bash
# Deployment flags
export DEPLOY_WVA=true                    # Deploy WVA controller (default: true)
export DEPLOY_LLM_D=true                  # Deploy llm-d infrastructure (default: true)
export DEPLOY_VA=false                    # Deploy VariantAutoscaling (default: false)
export DEPLOY_HPA=false                   # Deploy HPA (default: false)
export DEPLOY_PROMETHEUS=true             # Deploy Prometheus (default: true)
export DEPLOY_PROMETHEUS_ADAPTER=true     # Deploy Prometheus Adapter (default: true)

# WVA repository configuration
export WVA_REPO_BRANCH=main               # WVA repository branch (default: main)

# HuggingFace token (required for llm-d)
export HF_TOKEN="hf_xxxxx"

# Run deployment
./deploy-wva-stack.sh
```

### Command-Line Options

```bash
./deploy-wva-stack.sh [options]

Options:
  -h, --help              Show help message
  -c, --cleanup           Clean up existing deployment before installing
  --cleanup-only          Clean up existing deployment and exit (skip deployment)
  -d, --delete-repo       Delete WVA repository after deployment
  --wva-only              Deploy only WVA (skip llm-d)
  --llmd-only             Deploy only llm-d (skip WVA)
  --with-hpa              Deploy HPA (default: false)
  --with-va               Deploy VariantAutoscaling (default: false)
  --kind                  Deploy to Kind cluster (uses kind-emulator scripts)
  --create-kind           Create Kind cluster before deployment
  --create-kind-only      Create Kind cluster only (skip deployment)
  --delete-kind           Delete Kind cluster after deployment
```

## Usage Examples

### Example 1: Full Stack with HPA for Testing

```bash
export HF_TOKEN="hf_xxxxx"
export DEPLOY_VA=true
export DEPLOY_HPA=true
./deploy-wva-stack.sh
```


### Example 2: Infrastructure Only

```bash
export HF_TOKEN="hf_xxxxx"
export DEPLOY_VA=false
export DEPLOY_HPA=false
./deploy-wva-stack.sh
```


### Example 3: Kind Cluster with Custom Configuration

```bash
# Create Kind cluster with custom settings
export KIND_CLUSTER_NAME="kind-fma-cluster"
export KIND_CLUSTER_NODES=4
export KIND_CLUSTER_GPUS=2
export KIND_CLUSTER_GPU_TYPE="nvidia"
./deploy-wva-stack.sh --create-kind --with-hpa
```

### Example 4: Custom WVA Branch

```bash
export WVA_REPO_BRANCH=feature/new-feature
export HF_TOKEN="hf_xxxxx"
./deploy-wva-stack.sh
```

## Verification

After deployment, verify the components:

```bash
# Check WVA controller
kubectl get pods -n workload-variant-autoscaler-system

# Check llm-d resources
kubectl get all -n $(kubectl get ns -o name | grep llm-d | head -n1 | cut -d'/' -f2)

# Check VariantAutoscaling resources
kubectl get variantautoscalings.llmd.ai -A

# Check HPA resources
kubectl get hpa -A

# Check Prometheus
kubectl get pods -n workload-variant-autoscaler-monitoring
```

## Viewing Logs

```bash
# WVA controller logs
kubectl logs -n workload-variant-autoscaler-system \
  -l control-plane=controller-manager -f

# llm-d logs (example for decode deployment)
kubectl logs -n llm-d-inference-scheduler \
  deployment/ms-inference-scheduling-llm-d-modelservice-decode -f
```

## Cleanup

### Clean Up and Redeploy

The `--cleanup` flag removes the existing deployment **before** installing again:

```bash
# Clean up and redeploy with default settings
./deploy-wva-stack.sh --cleanup

# Clean up and redeploy with HPA and VA
./deploy-wva-stack.sh --cleanup --with-hpa --with-va
```

### Clean Up Only (No Redeployment)

The `--cleanup-only` flag removes the deployment and exits without redeploying:

```bash
# Remove deployment only
./deploy-wva-stack.sh --cleanup-only

# Remove deployment and Kind cluster
./deploy-wva-stack.sh --cleanup-only --delete-kind
```

## Kind Cluster Configuration

When using `--create-kind` or `--create-kind-only`, you can customize the cluster configuration:

### Environment Variables

```bash
export KIND_CLUSTER_NAME="kind-fma-cluster"        # Default: kind-wva-gpu-cluster
export KIND_CLUSTER_NODES=4                  # Default: 3
export KIND_CLUSTER_GPUS=2                   # Default: 4 (GPUs per node)
export KIND_CLUSTER_GPU_TYPE="nvidia"        # Default: mix (nvidia|amd|intel|mix)
```

### Usage Examples

**Create cluster and deploy immediately:**
```bash
export KIND_CLUSTER_NAME="kind-fma-cluster"
export KIND_CLUSTER_NODES=4
export KIND_CLUSTER_GPUS=2
export KIND_CLUSTER_GPU_TYPE="nvidia"
./deploy-wva-stack.sh --create-kind --with-hpa
```

**Create cluster only (for later deployment):**
```bash
export KIND_CLUSTER_NAME="kind-fma-cluster"
export KIND_CLUSTER_NODES=4
export KIND_CLUSTER_GPUS=2
export KIND_CLUSTER_GPU_TYPE="nvidia"
./deploy-wva-stack.sh --create-kind-only
```

### Cluster Features

The Kind cluster includes:
- Emulated GPU resources (nvidia.com/gpu, amd.com/gpu, intel.com/gpu)
- HPAScaleToZero feature gate enabled (for scale-to-zero testing)
- llm-d inference simulator (no real model loading)

## Known Issues

### `--llmd-only --kind` Requires WVA Image

Due to a limitation in the WVA upstream repository (`deploy/kind-emulator/install.sh`, line 131), the Kind emulator deployment unconditionally tries to pull the WVA controller image even when `DEPLOY_WVA=false`. As a workaround, authenticate with ghcr.io before running `./deploy-wva-stack.sh --llmd-only --kind`, or use a real Kubernetes cluster instead of Kind for llm-d-only deployments.
