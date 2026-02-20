# Upstream Dependency Version Tracking

> This file is the source of truth for the [upstream dependency monitor](../.github/workflows/upstream-monitor.md) workflow.
> Add your project's key upstream dependencies below. The monitor runs daily and creates GitHub issues when breaking changes are detected.

## Dependencies

| Dependency | Current Pin | Pin Type | File Location | Upstream Repo |
|-----------|-------------|----------|---------------|---------------|
| **Go** | `1.24.2` | version | `go.mod` line 3 | [golang/go](https://github.com/golang/go) |
| **k8s.io/api** | `v0.34.0` | tag | `go.mod` line 7 | [kubernetes/api](https://github.com/kubernetes/api) |
| **k8s.io/apimachinery** | `v0.34.0` | tag | `go.mod` line 8 | [kubernetes/apimachinery](https://github.com/kubernetes/apimachinery) |
| **k8s.io/client-go** | `v0.34.0` | tag | `go.mod` line 9 | [kubernetes/client-go](https://github.com/kubernetes/client-go) |
| **sigs.k8s.io/controller-runtime** | `v0.22.1` | tag | `go.mod` line 12 | [kubernetes-sigs/controller-runtime](https://github.com/kubernetes-sigs/controller-runtime) |
| **vllm/vllm-openai** | `v0.10.2` | tag | `cmd/requester/README.md` | [vllm-project/vllm](https://github.com/vllm-project/vllm) |
| **vllm (CPU build)** | `v0.15.0` | tag | `dockerfiles/Dockerfile.launcher.cpu` | [vllm-project/vllm](https://github.com/vllm-project/vllm) |
| **nvidia/cuda** | `12.8.0-base-ubuntu22.04` | tag | `dockerfiles/Dockerfile.requester` | [NVIDIA CUDA](https://hub.docker.com/r/nvidia/cuda) |
