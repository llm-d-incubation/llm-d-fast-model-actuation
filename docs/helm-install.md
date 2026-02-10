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
  --version 0.1.0

# Install launcher-populator
helm install launcher-populator \
  oci://ghcr.io/llm-d-incubation/llm-d-fast-model-actuation/charts/launcher-populator \
  --version 0.1.0
```

### Install with Custom Values

```bash
helm install dpctlr \
  oci://ghcr.io/llm-d-incubation/llm-d-fast-model-actuation/charts/dual-pods-controller \
  --version 0.1.0 \
  --set SleeperLimit=3 \
  --set DebugAcceleratorMemory=false
```

Or with a values file:

```bash
helm install dpctlr \
  oci://ghcr.io/llm-d-incubation/llm-d-fast-model-actuation/charts/dual-pods-controller \
  --version 0.1.0 \
  -f custom-values.yaml
```

## Related Documentation

- [Release Process](release-process.md) - How to create and publish releases
- [Testing Release Workflow](testing-release-workflow.md) - Testing in forks and troubleshooting
