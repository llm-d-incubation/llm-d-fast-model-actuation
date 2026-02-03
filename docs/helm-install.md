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
  -f values.yaml
```

## Chart Versions

### Development Versions

Continuous builds from main branch:
- Format: `0.3.0-d1a7c8f` (version + git hash)
- Published automatically on every push to main that modifies charts

### Release Versions

Official releases:
- Format: `0.3.0`
- Published when a GitHub release is created or tag matching `v*` is pushed
- Updates Chart.yaml with release version
