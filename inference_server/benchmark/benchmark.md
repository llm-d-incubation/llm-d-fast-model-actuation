# Dual-Pods Benchmarking Tool
The **Dual-Pods Benchmarking Tool** measures and reports the startup and readiness
latency of model-serving pods within the LLM-D Fast Model Actuation workflow.

## Purpose
The goal is to quantify and compare how quickly a model-serving duo (server-requesting
and server-providing pods) becomes available under three different actuation conditions
in order of decreasing latency:

- **Cold start**: creating a new vLLM instance without using a launcher
- **Warm start**: creating a new vLLM instance in an existing launcher pod
- **Hot start**: waking a sleeping vLLM instance on an existing launcher pod

These metrics will guide future optimizations for the **Dual-Pods Controller (DPC)**. Ultimately, the goal
is *high predictability*, which is defined as achieving close to 100% hit rate of awakening
available, sleeping pods on cluster GPUs as a function of total inference server
requests for common user scenarios.

FMA benchmarking is also intended to work alongside the
[Workload Variant Autoscaler (WVA)](https://github.com/llm-d/llm-d-workload-variant-autoscaler):
some benchmarking scenarios may involve WVA-triggered scaling, and we want to
demonstrate FMA working with WVA as an integrated system.

## Measurement Layers

FMA benchmarking uses a layered measurement model. Layer 1 is what FMA benchmarks own
directly. Layer 2 bridges actuation to inference readiness (i.e., latency for streaming
inference requests to receive their first response chunk in an FMA-enabled context). Layer 3 is out of FMA's
direct scope but is referenced for completeness and handoff to other frameworks.

| Layer | Focus | Metrics | Measured By |
| ----- | ----- | ------- | ----------- |
| **L1: Actuation** | Requester pod readiness | T_actuation (requester creation to readiness), T_wake (DPC wakes sleeping vLLM instance), Hit_rate (GPU hits), T_launcher (launcher creates new vLLM instance) | llm-d-benchmark new harness |
| **L2: Inference Readiness** | First inference response | T_e2e (requester creation to first inference response), T_first_token (requester ready to first inference response) | llm-d-benchmark nop/inference-perf harness |
| **L3: Steady-State** | Throughput/latency | T_actuation (requester creation to readiness), TPOT (time per output token), throughput, queue depth, KV cache usage, replica stability | llm-d-benchmark / WVA |

**Metric definitions:**

- **T_actuation**: Time from requester pod creation (ReplicaSet scale-up) to requester pod readiness (`/ready` probe passes), which implies the DPC has bound the requester to a server-providing pod and the vLLM instance is serving.
- **T_wake**: Request-response time for the DPC's `/wake_up` call to a sleeping vLLM instance on the server-providing pod. A part of T_actuation when a hot start occurs.
- **Hit_rate**: Fraction of server-requesting Pods that get satisfied by waking a sleeping vLLM instance.
- **T_launcher**: Time from the launcher receiving a create request to the new vLLM instance reporting healthy. Includes the benefit of vLLM module preloading.
- **T_e2e**: Total time from requester pod creation to first successful inference response. Spans the full path: requester scheduling, DPC binding, instance wake-up or launcher instance creation, vLLM ready, first inference (T_actuation + T_first_token).
- **T_first_token**: Time from requester pod readiness to receiving the first streamed token from the server-providing pod's vLLM instance (time-to-first-token, post-actuation). Requires streaming inference requests.

## Benchmarking Scenarios

| Scenario                           | Description                                                                                                                            |
| ---------------------------------- | -------------------------------------------------------------------------------------------------------------------------------------- |
| **Fast Replica Scale Up**          | As a ModelService Owner, I can scale up the number of active replicas for a variant from 0 to 1 or 1 to N with minimal latency         |
| **Introducing New Variant**        | As a ModelService Owner, I can deploy a newly released variant in anticipation of user requests                                         |
| **Resource Scaling and Stress Test** | As a Workload Owner, I can stress-test my namespace's resource capacity at scale (many models, many requesters) for numerous purposes (e.g., management reports, load characterization, future resource requests) |
| **Maintenance Planning**           | As a Cluster Operator, I can validate the cluster performance is similar or better after node maintenance schedules and upgrades         |


## Benchmarking Matrix

### Actuation Paths (columns)

The columns represent the different paths FMA can take to satisfy a new server-requesting pod,
using the team's established terminology:

| Actuation Path            | What It Measures | llm-d-benchmark Config |
| ------------------------- | ---------------- | ---------------------- |
| **Cold Start**            | No launcher, no sleeping pods. Raw Kubernetes deploy-to-ready latency (non-FMA baseline, or FMA milestone 2 without launcher). | `-t standalone` comparison baseline |
| **Warm Start**            | A launcher pod exists (pre-created by the launcher population controller) but no sleeping instance is available on the assigned GPU. Launcher creates a new vLLM instance with the benefit of module preloading. | `-t fma` with default `LLMDBENCH_FMA_LAUNCHER_*` env vars |
| **Hot Start**             | A sleeping vLLM instance exists on the correct GPU. DPC sends `/wake_up`. Best-case actuation path. | `-t fma` with `LLMDBENCH_VLLM_COMMON_ENABLE_SLEEP_MODE=true`, `LLMDBENCH_FMA_DUAL_POD_SLEEPER_LIMIT>=1` |

> **Note on simulation:** Any of the above paths can be exercised with mock GPUs
> (`llm-d-inference-sim` image or launcher `--mock-mode`) for CI pipelines and scenario
> prototyping. Simulation is an orthogonal testing mode, not a separate actuation path.
>
> **Note on caching:** Model tensor caching (via PVC) and CUDA graph compilation caching
> are orthogonal to the actuation paths above. Either or both can be enabled for any path
> besides Hot Start (where the instance is already loaded). Caching configuration is
> controlled via `LLMDBENCH_VLLM_COMMON_EXTRA_PVC_NAME` and `LLMDBENCH_VLLM_COMMON_VLLM_CACHE_ROOT`
> in the `fma.sh` scenario.

### Matrix

Cell annotations indicate which measurement layers apply:
- **L1** -- Layer 1 actuation metrics (T_actuation, T_wake, Hit_rate, T_launcher)
- **L1+L2** -- Actuation metrics plus inference readiness (T_first_token, T_e2e)
- **L1+L3** -- Actuation metrics plus steady-state performance (TPOT, throughput, queue depth, KV cache, replica stability)
- **L1+L2+L3** -- All three layers
- **--** -- Not applicable to this combination

| Scenario                           | Cold Start | Warm Start | Hot Start |
| ---------------------------------- | :--------: | :--------: | :-------: |
| **Fast Replica Scale Up**          | L1+L2      | L1+L2      | L1+L2     |
| **Introducing New Variant**        | L1+L2      | L1+L2      | --        |
| **Resource Scaling and Stress Test** | L1+L3      | L1+L3      | L1+L3     |
| **Maintenance Planning**           | L1+L2+L3   | L1+L2+L3   | L1+L2+L3  |


### Scenario Rationale

| FMA Scenario                       | Why Included | llm-d-benchmark Applicability |
| ---------------------------------- | ------------ | ----------------------------- |
| **Fast Replica Scale Up**          | Core FMA value proposition: how fast can the DPC bring replicas online? Directly measures the benefit of sleep/wake and launcher preloading over cold starts. | `fma.sh` scenario with varying `LLMDBENCH_VLLM_COMMON_REPLICAS`; `inference-scheduling` guide with `-t fma` |
| **Introducing New Variant**        | Measures actuation latency for a previously unseen model (cold cache, no sleeping instances for this model). Assumes the launcher population controller (LPC) has already created the needed launchers. Captures the "day 1" deployment experience. | `fma.sh` with `LLMDBENCH_DEPLOY_MODEL_LIST` variations; nop harness for pure actuation timing |
| **Resource Scaling and Stress Test** | Stress-tests namespace resource capacity at scale (many models, many requesters) for performance characterization, cost analysis, and capacity planning. | `fma.sh` with multi-model list; DoE experiment with replica/model treatments |
| **Maintenance Planning**           | Regression baseline: run the same scenarios before and after node maintenance or upgrades. Detects performance regressions in actuation latency. | Any guide scenario as regression baseline with `-t fma`; compare pre/post results |

### Actuation Path Rationale

| Actuation Path   | Why Included |
| ---------------- | ------------ |
| **Cold Start**   | Baseline without FMA (or FMA milestone 2 without launcher). Establishes the raw Kubernetes deploy-to-ready latency that all FMA paths should improve upon. |
| **Warm Start**   | Measures the launcher's contribution when no sleeping instance is available. LPC has pre-created launcher pods, and the launcher creates a new vLLM instance with module preloading benefit. |
| **Hot Start**    | Best-case FMA path. DPC sends `/wake_up` to a sleeping vLLM instance on the correct GPU. Measures sleep-to-wake latency. |


## Integration Strategy

### Current State

FMA is being integrated as a third deploy method (`-t fma`) in [llm-d-benchmark](https://github.com/llm-d/llm-d-benchmark),
alongside `standalone` and `modelservice`. This work is tracked on the
[`fma` branch](https://github.com/manoelmarques/llm-d-benchmark/tree/fma) and includes:

- **`scenarios/examples/fma.sh`** -- Scenario configuration with sleep mode enabled, model caching PVC, and FMA image references.
- **`setup/steps/07_deploy_fma_models.py`** -- Standup step that deploys InferenceServerConfig, LauncherConfig, LauncherPopulationPolicy, and requester ReplicaSet CRs. Installs FMA CRDs and the `fma-controllers` Helm chart. Waits for dual-pod controller and launcher-populator readiness.
- **`setup/env.sh`** -- 35+ new `LLMDBENCH_FMA_*` environment variables covering chart version, image registry/tags, dual-pod configuration, launcher configuration, and requester resource limits.
- **`setup/run.sh`** -- FMA endpoint discovery via Kubernetes service labels (`stood-up-via=fma`).
- **`setup/teardown.sh`** -- Ordered teardown: FMA custom resources first, then wait for the dual-pods controller to remove finalizers, then uninstall the FMA Helm release.

The existing llm-d-benchmark harnesses (nop, inference-perf, vllm-benchmark) can run after
FMA standup to measure L2 and L3 metrics.

### Next Steps

1. **Upstream the `fma` branch** into llm-d-benchmark, aligning with the declarative
   Python architecture in [PR #848](https://github.com/llm-d/llm-d-benchmark/pull/848).
2. **Add FMA-specific experiment YAML** for Design of Experiments (DoE) treatments:
   replica count, sleep mode on/off, sleeper limit, model variant combinations.
3. **Add actuation-specific metrics collection** in the nop harness: T_actuation, T_wake,
   Hit_rate parsed from FMA pod events and DPC logs.
4. **Consider Grafana integration** for visual actuation metrics (scale-up latency
   dashboards), following the pattern in [WVA PR #900](https://github.com/llm-d/llm-d-workload-variant-autoscaler/pull/900).
5. **Maintain framework-agnostic interface**: the FMA benchmark lifecycle (deploy, measure,
   teardown) should remain pluggable into other benchmarking frameworks beyond llm-d-benchmark.


## Legacy Benchmark Tooling

See [benchmark_legacy.md](benchmark_legacy.md) for documentation on the original `benchmark_base.py` tool.
