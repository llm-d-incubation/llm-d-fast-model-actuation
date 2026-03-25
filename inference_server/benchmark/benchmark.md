# Dual-Pods Benchmarking Tool
The **Dual-Pods Benchmarking Tool** measures and reports the startup and readiness
latency of model-serving pods within the LLM-D Fast Model Actuation workflow.

## Purpose
The goal is to quantify and compare how quickly a model-serving duo (server-requesting
and server-providing pods) becomes available under different actuation conditions such
as cold starts, wake-ups from a sleeping state, using prewarmed pods, etc. These metrics
will guide future optimizations for the **Dual-Pods Controller (DPC)**. Ultimately, the goal
is *high predictability*, which is defined as achieving close to 100% hit rate of awakening
available, sleeping pods on cluster GPUs as function of total inference server
requests for common user scenarios.

## Measurement Layers

FMA benchmarking uses a layered measurement model. Layer 1 is what FMA benchmarks own
directly. Layer 2 bridges actuation to inference readiness. Layer 3 is out of FMA's
direct scope but is referenced for completeness and handoff to other frameworks.

| Layer | Focus | Metrics | Measured By |
| ----- | ----- | ------- | ----------- |
| **L1: Actuation** | Pod readiness | T_actuation (deploy-to-ready), T_wake (sleep-to-serving), Hit_rate (% GPU hits), T_launcher (instance creation) | llm-d-benchmark new harness |
| **L2: Inference Readiness** | First response | T_first_token (ready-to-first-response), T_e2e (request-to-first-response) | FMA + llm-d-benchmark nop/inference-perf harness |
| **L3: Steady-State** | Throughput/latency | TPOT, throughput, queue depth, KV cache usage, replica stability | llm-d-benchmark / WVA |

**Metric definitions:**

- **T_actuation**: Time from server-request ReplicaSet creation to all dual-pods reporting ready.
- **T_wake**: Time from a sleeping vLLM instance receiving a wake signal to serving inference requests. A subset of T_actuation when a GPU hit occurs.
- **Hit_rate**: Percentage of scale-up events that land on a node with a sleeping pod (GPU hit) vs. requiring a cold start (GPU miss).
- **T_launcher**: Time for the launcher to create a new vLLM instance (including module preloading benefit).
- **T_first_token**: Time from pod readiness to first successful inference response (time-to-first-token).
- **T_e2e**: Total time from server-request submission to first successful inference response (T_actuation + T_first_token).

## Benchmarking Scenarios

| Scenario                           | Description                                                                                                                            |
| ---------------------------------- | -------------------------------------------------------------------------------------------------------------------------------------- |
| **Fast Replica Scale Up**          | As a ModelService Owner, I can scale up the number of active replicas for a variant from 0 to 1 or 1 to N with minimal latency         |
| **Introducing New Variant**        | As a ModelService Owner, I can deploy a newly released variant in anticipation of user requests                                         |
| **Free Up Cluster Resources**      | As a Cluster Operator, I can reduce/deactivate resource intensive variants to make space for numerous smaller model variants             |
| **Resource Request Justification** | As a Workload Owner, I can stress-test my namespace's resources to justify more resource requests (routes, gateways, GPUs) from cluster operator |
| **Maintenance Planning**           | As a Cluster Operator, I can validate the cluster performance is similar or better after node maintenance schedules and upgrades         |


## Benchmarking Matrix

Cell annotations indicate which measurement layers apply:
- **L1** -- Layer 1 actuation metrics (T_actuation, T_wake, Hit_rate, T_launcher)
- **L1+L2** -- Actuation metrics plus inference readiness (T_first_token, T_e2e)
- **P** -- Planned but not yet implemented
- **--** -- Not applicable to this combination

| Scenario                           | Cold Start (Standalone) | Cold Start (Launcher) | Wake from Sleep (GPU Hit) | Wake from Sleep (GPU Miss) | Model Swap (Launcher) | Cached Model (PVC) | Simulated (Mock GPUs) |
| ---------------------------------- | :---------------------: | :-------------------: | :-----------------------: | :------------------------: | :-------------------: | :-----------------: | :-------------------: |
| **Fast Replica Scale Up**          | L1                      | L1+L2                 | L1+L2                     | L1                         | --                    | L1+L2               | L1                    |
| **Introducing New Variant**        | L1                      | L1+L2                 | --                        | --                         | L1+L2                 | L1+L2               | L1                    |
| **Free Up Cluster Resources**      | --                      | P                     | P                         | --                         | P                     | --                  | P                     |
| **Resource Request Justification** | L1                      | L1                    | L1                        | L1                         | P                     | L1                  | L1                    |
| **Maintenance Planning**           | L1                      | L1+L2                 | L1+L2                     | L1                         | P                     | L1+L2               | --                    |


### Scenario Rationale

| FMA Scenario                       | Why Included | llm-d-benchmark Applicability |
| ---------------------------------- | ------------ | ----------------------------- |
| **Fast Replica Scale Up**          | Core FMA value proposition: how fast can the DPC bring replicas online? Directly measures the benefit of sleep/wake and launcher preloading over cold starts. | `fma.sh` scenario with varying `LLMDBENCH_VLLM_COMMON_REPLICAS`; `inference-scheduling` guide with `-t fma` |
| **Introducing New Variant**        | Tests the full FMA deployment path: InferenceServerConfig + LauncherConfig + LauncherPopulationPolicy creation, followed by requester ReplicaSet. Captures model download, launcher instance creation, and dual-pod binding. | `fma.sh` with `LLMDBENCH_DEPLOY_MODEL_LIST` variations; nop harness for pure actuation timing |
| **Free Up Cluster Resources**      | Validates the reverse path: sleeping/deactivating variants. Important for cluster operators managing GPU capacity across tenants. Measures GPU release latency. | FMA-specific: scale down + verify GPU release via Prometheus. Not yet in llm-d-benchmark |
| **Resource Request Justification** | Stress-tests namespace resources across multiple concurrent models/variants to produce data for capacity planning and resource justification to cluster operators. | `fma.sh` with multi-model list; DoE experiment with replica/model treatments |
| **Maintenance Planning**           | Regression baseline: run the same scenarios before and after node maintenance or upgrades. Detects performance regressions in actuation latency. | Any guide scenario as regression baseline with `-t fma`; compare pre/post results |

### Actuation Condition Rationale

| Condition                      | What It Tests | llm-d-benchmark Config |
| ------------------------------ | ------------- | ---------------------- |
| **Cold Start (Standalone)**    | No launcher, no sleeping pods. Raw Kubernetes deploy-to-ready latency. Serves as the baseline for all other conditions. | `-t standalone` comparison baseline |
| **Cold Start (w/ Launcher)**   | Launcher is present and pre-loads vLLM Python modules. Measures the launcher's contribution to reducing cold start latency vs. standalone. | `-t fma` with default `LLMDBENCH_FMA_LAUNCHER_*` env vars |
| **Wake from Sleep (GPU Hit)**  | A sleeping vLLM instance exists on the correct GPU. Measures the sleep-to-wake latency, which is the best-case actuation path. | `-t fma` with `LLMDBENCH_VLLM_COMMON_ENABLE_SLEEP_MODE=true`, `LLMDBENCH_FMA_DUAL_POD_SLEEPER_LIMIT>=1` |
| **Wake from Sleep (GPU Miss)** | A sleeping pod exists but on the wrong GPU or node. Requires cold start despite available sleeping capacity. Measures the miss penalty and validates DPC scheduling decisions. | `-t fma`, requires multi-node cluster with asymmetric GPU placement |
| **Model Swap (Launcher)**      | The launcher swaps the model on an existing vLLM instance without restarting the process. Tests the launcher's dynamic model management capability. | `-t fma`, sequential model deployment via `07_deploy_fma_models.py` |
| **Cached Model (PVC)**         | Model weights are pre-cached on a PersistentVolumeClaim, eliminating download time. Isolates the non-download portion of actuation latency. | `-t fma` with `LLMDBENCH_VLLM_COMMON_EXTRA_PVC_NAME` + `LLMDBENCH_VLLM_COMMON_VLLM_CACHE_ROOT` (configured in `fma.sh`) |
| **Simulated (Mock GPUs)**      | Uses `llm-d-inference-sim` image or launcher GPU mock mode. No real GPUs required. For CI pipelines and scenario prototyping. | `simulated-accelerators` guide or launcher `--mock-mode` |


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
