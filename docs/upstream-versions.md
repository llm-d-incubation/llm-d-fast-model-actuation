# Upstream Dependency Version Tracking

> This file is the source of truth for the [upstream dependency monitor](../.github/workflows/upstream-monitor.md) workflow.
> Add your project's key upstream dependencies below. The monitor runs daily and creates GitHub issues when breaking changes are detected.

## Dependencies

| Dependency | Current Pin | Pin Type | File Location | Upstream Repo |
|-----------|-------------|----------|---------------|---------------|
| **Go (language version)** | `1.25.0` | version | `go.mod` line 3 | [golang/go](https://github.com/golang/go) |
| **k8s.io/api** | `v0.34.10` | tag | `go.mod` line 8 | [kubernetes/api](https://github.com/kubernetes/api) |
| **k8s.io/apimachinery** | `v0.34.10` | tag | `go.mod` line 9 | [kubernetes/apimachinery](https://github.com/kubernetes/apimachinery) |
| **k8s.io/client-go** | `v0.34.10` | tag | `go.mod` line 10 | [kubernetes/client-go](https://github.com/kubernetes/client-go) |
| **k8s.io/component-base** | `v0.34.10` | tag | `go.mod` line 11 | [kubernetes/component-base](https://github.com/kubernetes/component-base) |
| **sigs.k8s.io/controller-runtime** | `v0.22.5` | tag | `go.mod` line 14 | [kubernetes-sigs/controller-runtime](https://github.com/kubernetes-sigs/controller-runtime) |
| **github.com/prometheus/client_golang** | `v1.24.1` | tag | `go.mod` line 6 | [prometheus/client_golang](https://github.com/prometheus/client_golang) |
| **vllm/vllm-openai** | `v0.23.0` | tag | `cmd/requester/README.md`; `dockerfiles/Dockerfile.launcher.benchmark` line 1 (`BASE_IMAGE`) | [vllm-project/vllm](https://github.com/vllm-project/vllm) |
| **vllm (CPU build)** | `v0.23.0` | tag | `dockerfiles/Dockerfile.launcher.cpu` line 5 (`VLLM_VERSION`) | [vllm-project/vllm](https://github.com/vllm-project/vllm) |
| **nvidia/cuda** | `12.8.0-base-ubuntu22.04` | tag | `dockerfiles/Dockerfile.requester` line 31 | [NVIDIA CUDA](https://hub.docker.com/r/nvidia/cuda) |
| **projectquay/golang (builder image)** | `1.26` | tag | `dockerfiles/Dockerfile.requester` line 2 | [projectquay/golang](https://quay.io/repository/projectquay/golang) |
