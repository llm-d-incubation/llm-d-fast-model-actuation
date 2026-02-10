# Helm Chart Installation

This guide covers installing Fast Model Actuation (FMA) Helm charts.

## Available Charts

- **dual-pods-controller**: Main FMA dual pod controller
- **launcher-populator**: Launcher population controller

## Installation

### Install from GHCR

```bash
# Install dual-pods-controller
helm install dpctlr \
  oci://ghcr.io/llm-d-incubation/llm-d-fast-model-actuation/charts/dual-pods-controller \
  --version 0.3.0

# Install launcher-populator
helm install launcher-populator \
  oci://ghcr.io/llm-d-incubation/llm-d-fast-model-actuation/charts/launcher-populator \
  --version 0.3.0
```

### Install with Custom Values

```bash
helm install dpctlr \
  oci://ghcr.io/llm-d-incubation/llm-d-fast-model-actuation/charts/dual-pods-controller \
  --version 0.3.0 \
  --set SleeperLimit=3 \
  --set DebugAcceleratorMemory=false
```

Or with a values file:

```bash
helm install dpctlr \
  oci://ghcr.io/llm-d-incubation/llm-d-fast-model-actuation/charts/dual-pods-controller \
  --version 0.3.0 \
  -f custom-values.yaml
```

## Release Process

For tag `v0.3.1`, the workflow publishes:

**Container Images:**
- `ghcr.io/llm-d-incubation/llm-d-fast-model-actuation/dual-pods-controller:v0.3.1`
- `ghcr.io/llm-d-incubation/llm-d-fast-model-actuation/launcher-populator:v0.3.1`
- `ghcr.io/llm-d-incubation/llm-d-fast-model-actuation/launcher:v0.3.1`
- `ghcr.io/llm-d-incubation/llm-d-fast-model-actuation/requester:v0.3.1`

**Helm Charts:**
- `dual-pods-controller:0.3.1` (references controller image `v0.3.1`)
- `launcher-populator:0.3.1` (references populator image `v0.3.1`)
