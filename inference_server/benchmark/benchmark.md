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

## Baseline Startup Latency

**Objective:**
Measure the time from **deployment (server-request submission)** to **dual-pod readiness**.

### Inputs

| Parameter           | Type   | Required | Default                                 | Description                                                                                            |
| ------------------ | ------ | -------- | --------------------------------------- | ------------------------------------------------------------------------------------------------------ |
| `--namespace`      | `str`  | **Yes**  | —                                       | Openshift namespace to run benchmark                                      |
| `--yaml`           | `str`  | **Yes**  | —                                       | Path to the server-requesting YAML template file              |
| `--image`          | `str`  | **Yes*** | —                                       | Image repository for the requester pod. Required *only if* `CONTAINER_IMG_REG` env var is **not** set |
| `--tag`            | `str`  | **Yes*** | —                                       | Image tag for the requester pod. Required *only if* `CONTAINER_IMG_VERSION` env var is **not** set    |
| `--cleanup`        | `bool` | No       | `True`                                  | Whether to clean up created resources after the benchmark                                             |
| `--iterations`     | `int`  | No       | `1`                                     | Number of times to run each benchmark scenario                                                        |
| `--cluster-domain` | `str`  | No       | `fmaas-platform-eval.fmaas.res.ibm.com` | Cluster domain for Prometheus GPU metrics query                                                           |
| `--model-path`     | `str`  | No       | `None`                                  | Path to JSON file containing models for scenario (used only in the `new_variant` scenario).                           |
| `--scenario`       | `str`  | No       | `"scaling"`                             | Benchmark scenario to run: `baseline`, `scaling`, or `new_variant`.                                    |


### Outputs

| Output                 | Description                                                                |
| ---------------------- | -------------------------------------------------------------------------- |
| `startup_time`         | Total time from deployment to readiness                                    |
| `availability_mode`    | Indicates whether the vLLM instance was started cold or resumed from sleep |

**Example Usage**
```bash
python3 inference_server/benchmark/bechmark_base.py --namespace <str> --yaml <str> --cleanup <bool,default:True> --iterations <int, default:1> --cluster-domain <str> --model-path <str> --scenario <str, default:scaling> --image <str> --tag <str>
```

**Output Example (Subject to Change)**

```
replicaset.apps/my-request created

Applying deploy/server-request-minimal.yaml...

2025-10-22 14:39:59,678 - INFO - Waiting for server-requesting pod to appear...
2025-10-22 14:39:59,763 - INFO - Requester pod detected: my-request-qwhpb
2025-10-22 14:39:59,763 - INFO - Waiting for server-providing pod to relay readiness to requester...
2025-10-22 14:39:59,763 - INFO - Waiting for both pods: my-request-qwhpb-server, my-request-qwhpb
2025-10-23 14:46:25,759 - INFO - my-request-qwhpb-server is Ready at 386.00s
2025-10-22 14:46:28,967 - INFO - my-request-qwhpb is Ready at 389.21s
2025-10-22 14:46:28,968 - INFO - ✅ Both pods Ready after 389.21s
2025-10-22 14:46:28,971 - INFO - 🚀 Metric #1: Time from server-requesting pod apply to dual pods ready is 389.64 seconds
```

## Benchmarking Scenarios (WIP)

| Scenario                      | Description                                                                                                                                           |
| ----------------------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------- |
| **Fast Replica Scale Up**     | As a ModelService Owner, I can scale up the number of active replicas for a variant from 0 to 1 or 1 to 2 with minimal latency |
| **Introducing New Variant**   | As a ModelService Owner, I can deploy a newly released variant in anticipation of user requests |
| **Free Up Cluster Resources** | As a Cluster Operator, I can reduce/deactivate resource intensive variants to make space for numerous smaller model variants |
| **Resource Request Justification** | As a Workload Owner, I can stress-test my namespace's resources to justify more resource requests (routes, gateways, GPUs) from cluster operator |
| **Maintenance Planning**      | As a Cluster Operator, I can validate the cluster performance is similar or better after node maintenance schedules and upgrades |


## Benchmarking Matrix (WIP)

| Scenario                      | Cold Start (No Launcher)  | Cold Start (w/ Launcher)  | Caching (No Launcher) | Caching (w/ Launcher) | Scale Up (No Sleep) | Scale Up (Sleep + GPU Hit/Bind) |
| ----------------------------- | ------------------------- | ------------------------- | --------------------  | --------------------- | ------------------- | ------------------------------- |
| **Introducing New Variant**   |                           |                           |                       |                       |                     |                                 |
| **Fast Replica Scale Up**     |                           |                           |                       |                       |                     |                                 |
| **Free Up Cluster Resources** |                           |                           |                       |                       |                     |                                 |
| **Resource Request Justification** |                      |                           |                       |                       |                     |                                 |
| **Maintenance Planning**      |                           |                           |                       |                       |                     |                                 |


### Next steps

This benchmarking framework may be integrated into an existing or a new LLM-D benchmark harness. It should:

- Continuously measure actuation latency across GPU and node changes in the cluster, plus various model variants.

- Validate improvements across llm-d releases and DPC changes.
