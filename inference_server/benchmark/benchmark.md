# Dual-Pods Benchmarking Tool
The **Dual-Pods Benchmarking Tool** measures and reports the startup and readiness
latency of model-serving pods within the LLM-D Fast Model Actuation workflow.

## Purpose
The goal is to quantify and compare how quickly a model-serving duo (server-requesting
and server-providing pods) becomes available under four different actuation conditions
in order of decreasing latency:

- **Cold start**: creating a new vLLM instance without using a launcher
- **Luke warm start**: DPC creates a new launcher pod, then the launcher creates a new vLLM instance
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
| **L1: Actuation** | Requester pod readiness | T_actuation (requester creation to readiness), T_wake (DPC wakes sleeping vLLM instance), Hit_rate (GPU hits), T_luke_warm (DPC creates launcher pod then vLLM instance), T_launcher (launcher creates new vLLM instance) | llm-d-benchmark new harness |
| **L2: Inference Readiness** | First inference response | T_e2e (requester creation to first inference response), T_first_token (requester ready to first inference response) | llm-d-benchmark nop/inference-perf harness |
| **L3: Steady-State** | Throughput/latency | TPOT (time per output token), throughput, queue depth, KV cache usage, replica stability | llm-d-benchmark / WVA |

**Metric definitions:**

- **T_actuation**: Time from requester pod creation (ReplicaSet scale-up) to requester pod readiness (`/ready` probe passes), which implies the DPC has bound the requester to a server-providing pod and the vLLM instance is serving. Spans different sub-components depending on the actuation path: hot start (T_wake), warm start (T_launcher), or luke warm start (T_luke_warm).
- **T_wake**: Request-response time for the DPC's `/wake_up` call to a sleeping vLLM instance on the server-providing pod. A part of T_actuation when a hot start occurs.
- **Hit_rate**: Fraction of server-requesting Pods that get satisfied by waking a sleeping vLLM instance.
- **T_luke_warm**: Time from the DPC requesting launcher pod creation to the new vLLM instance reporting healthy. Covers the full luke warm start span: launcher pod scheduling, launcher readiness, DPC reconciliation, and vLLM instance creation. Measured end-to-end because the boundary between launcher readiness and instance creation is not directly observable from outside the DPC.
- **T_launcher**: Time from the launcher receiving a create request to the new vLLM instance reporting healthy. Includes the benefit of vLLM module preloading. Applies to the warm start path, where a launcher pod already exists.
- **T_e2e**: Total time from requester pod creation to first successful inference response (T_actuation + T_first_token). Spans the full actuation and inference readiness path.
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

| Actuation Path            | What It Measures | Why Included | llm-d-benchmark Config |
| ------------------------- | ---------------- | ------------ | ---------------------- |
| **Cold Start**            | No launcher, no sleeping pods. Raw Kubernetes deploy-to-ready latency (non-FMA baseline, or FMA milestone 2 without launcher). | Establishes the baseline that all FMA paths should improve upon. | `-t standalone` comparison baseline |
| **Luke Warm Start**       | No launcher pod on the assigned GPU. The DPC creates a new launcher pod, then the launcher creates a new vLLM instance. | Worst-case FMA path, relevant for dynamic situations such as LauncherConfig rollouts or newly added nodes where the LPC has not yet populated launchers. | `-t fma` with LauncherPopulationPolicy that does not cover the target GPU node |
| **Warm Start**            | A launcher pod exists (pre-created by the LPC) but no sleeping instance is available on the assigned GPU. Launcher creates a new vLLM instance with module preloading. | Measures the launcher's contribution when no sleeping instance is available. | `-t fma` with default `LLMDBENCH_FMA_LAUNCHER_*` env vars |
| **Hot Start**             | A sleeping vLLM instance exists on the correct GPU. DPC sends `/wake_up`. | Best-case FMA path. Measures sleep-to-wake latency. | `-t fma` with `LLMDBENCH_VLLM_COMMON_ENABLE_SLEEP_MODE=true`, `LLMDBENCH_FMA_DUAL_POD_SLEEPER_LIMIT>=1` |

> **Note on naming:** "Cold FMA Start" was considered as an alternative to "Luke Warm
> Start", but the latter, though informal, was preferred for consistency with the
> existing hot/warm/cold temperature metaphor.
>
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
- **L1** -- Layer 1 actuation metrics (T_actuation, T_wake, Hit_rate, T_luke_warm, T_launcher)
- **L1+L2** -- Actuation metrics plus inference readiness (T_first_token, T_e2e)
- **L1+L3** -- Actuation metrics plus steady-state performance (TPOT, throughput, queue depth, KV cache, replica stability)
- **L1+L2+L3** -- All three layers
- **--** -- Not applicable to this combination

| Scenario                           | Cold Start | Luke Warm Start | Warm Start | Hot Start |
| ---------------------------------- | :--------: | :-------------: | :--------: | :-------: |
| **Fast Replica Scale Up**          | L1+L2      | L1+L2           | L1+L2      | L1+L2     |
| **Introducing New Variant**        | L1+L2      | L1+L2           | L1+L2      | --        |
| **Resource Scaling and Stress Test** | L1+L3    | L1+L3           | L1+L3      | L1+L3     |
| **Maintenance Planning**           | L1+L2+L3   | L1+L2+L3        | L1+L2+L3   | L1+L2+L3  |


### Scenario Rationale

| FMA Scenario                       | Why Included | llm-d-benchmark Applicability |
| ---------------------------------- | ------------ | ----------------------------- |
| **Fast Replica Scale Up**          | Core FMA value proposition: how fast can the DPC bring replicas online? Directly measures the benefit of sleep/wake and launcher preloading over cold starts. | `fma.sh` scenario with varying `LLMDBENCH_VLLM_COMMON_REPLICAS`; `inference-scheduling` guide with `-t fma` |
| **Introducing New Variant**        | Measures actuation latency for a previously unseen model (cold cache, no sleeping instances for this model). Assumes the launcher population controller (LPC) has already created the needed launchers. Captures the "day 1" deployment experience. | `fma.sh` with `LLMDBENCH_DEPLOY_MODEL_LIST` variations; nop harness for pure actuation timing |
| **Resource Scaling and Stress Test** | Stress-tests namespace resource capacity at scale (many models, many requesters) for performance characterization, cost analysis, and capacity planning. | `fma.sh` with multi-model list; DoE experiment with replica/model treatments |
| **Maintenance Planning**           | Regression baseline: run the same scenarios before and after node maintenance or upgrades. Detects performance regressions in actuation latency. | Any guide scenario as regression baseline with `-t fma`; compare pre/post results |

## Integration Strategy

### Current State

FMA is being integrated as a third deploy method (`-t fma`) in [llm-d-benchmark](https://github.com/llm-d/llm-d-benchmark),
alongside `standalone` and `modelservice`. The active upstream PR is
[llm-d/llm-d-benchmark#900](https://github.com/llm-d/llm-d-benchmark/pull/900),
which builds on the earlier `fma` branch work and aligns with the declarative Python
architecture from [PR #848](https://github.com/llm-d/llm-d-benchmark/pull/848). Key components:

- **`step_06_fma_deploy.py`** -- Standup step that installs FMA CRDs, ClusterRole, and
  the `fma-controllers` Helm chart, then applies a rendered deployment YAML containing
  InferenceServerConfig, LauncherConfig, LauncherPopulationPolicy, and a ReplicaSet
  (starting at 0 replicas). Waits for dual-pod controller and launcher-populator readiness.
- **`fma_functions.py`** -- FMA benchmarking logic in the nop harness: scales the ReplicaSet
  0->1, watches for requester pods to become Ready and receive the `dual-pods.llm-d.ai/dual`
  label, discovers launcher pods, and measures TTFT (time-to-first-token) via streaming
  `/v1/completions` requests. Iterates per `fma.iterations` config.
- **`config/scenarios/examples/fma.yaml`** -- Example FMA scenario configuration.
- **`config/templates/values/defaults.yaml`** -- 60+ FMA-related defaults covering chart
  version, image registry/tags, CRD URLs, dual-pod configuration, launcher configuration,
  and requester resource limits.
- **`nop-analyze_results.py`** -- Analysis script that outputs FMA-specific metrics: TTRR
  (time to requester ready), TTRD (time to requester dual-labeled), TTFT, and TTRD+TTFT.
- **Teardown** -- Ordered cleanup: FMA custom resources (ReplicaSet, InferenceServerConfig,
  LauncherConfig, LauncherPopulationPolicy) first, then the FMA Helm release.

The existing llm-d-benchmark harnesses (nop, inference-perf, vllm-benchmark) can run after
FMA standup to measure L2 and L3 metrics.

### Integration Phases

The following phases describe a concrete plan to evolve PR #900 into full coverage of the
benchmarking matrix above. Each phase builds on the previous one.

**Phase 1: Actuation path classification and Hit_rate**

PR #900 already watches pod events and queries the launcher API (`inspect_vllm_instances`),
but does not classify which actuation path the DPC took. This phase adds classification
logic to `fma_functions.py`:

- Compare the launcher pod's creation timestamp against the requester pod's creation
  timestamp. If the launcher was created *after* the requester, it is a luke warm start.
- For remaining cases, check whether the vLLM instance was woken from sleep (hot start)
  or newly created (warm start). The launcher's `/v2/vllm/instances` API returns instance
  status, and sleep/wake metrics are already parsed from launcher logs by `nop_functions.py`.
- Compute Hit_rate as the fraction of hot starts per scaling operation.
- Report the actuation path classification alongside the existing TTRR/TTRD/TTFT metrics.

**Phase 2: Per-path timing metrics**

Once actuation paths are classified, isolate the path-specific timing components:

- **T_wake** (hot): measure the `/wake_up` round-trip, approximated by the requester pod's
  transition from creation to Ready on known-hot actuations.
- **T_launcher** (warm): time between DPC binding and vLLM instance readiness, approximated
  by (requester dual-label timestamp - launcher pod Ready timestamp). Includes some DPC
  reconciliation overhead.
- **T_luke_warm** (luke warm): end-to-end from launcher pod creation timestamp to vLLM
  instance healthy, measured as a single span since the internal DPC boundary is not
  directly observable from outside.

**Phase 3: Multi-replica and scenario coverage**

PR #900 currently scales 0->1->0 per iteration. This phase adds support for scaling
0->N to cover the "Fast Replica Scale Up" (1->N) and "Resource Scaling and Stress Test"
scenarios:

- Add `fma.replicas` config alongside the existing `fma.iterations`.
- Extend `benchmark_fma()` to scale to N replicas and collect per-pod actuation path
  classifications and timing.

**Phase 4: Configurable actuation paths via scenario YAML**

PR #900's FMA deployment template (`24_fma-deployment.yaml.j2`) currently hardcodes vLLM
options (`--enable-sleep-mode`, `--max-model-len`, `--gpu-memory-utilization`,
`--tensor-parallel-size`). This phase templatizes those options so different actuation
paths can be exercised from scenario config:

- Templatize vLLM flags in the InferenceServerConfig spec from scenario YAML
  (analogous to how the standalone template uses `standalone.vllm.additionalFlags`).
- Add example scenario variants: `fma-hot.yaml` (sleep mode on, pre-populated launchers),
  `fma-warm.yaml` (sleeper limit 0 to force warm starts), standalone baseline for cold
  start comparison.
- Add FMA-specific DoE experiment YAML for treatment combinations: replica count, sleep
  mode on/off, sleeper limit, model variant.

**Phase 5: Reporting and visualization (optional)**

- Extend the nop analysis script to produce per-path timing breakdowns and Hit_rate
  summaries.
- Consider Grafana dashboards for actuation latency over time.

Throughout all phases, the FMA benchmark lifecycle (deploy, measure, teardown) should
remain framework-agnostic and pluggable into benchmarking frameworks beyond llm-d-benchmark.


## Legacy Benchmark Tooling

See [benchmark_legacy.md](benchmark_legacy.md) for documentation on the original `benchmark_base.py` tool.
