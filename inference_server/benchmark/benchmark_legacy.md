# Legacy Benchmark Tooling

The original benchmark implementation in this directory (`benchmark_base.py`, `scenarios.py`,
`kube_ops.py`) predates the llm-d-benchmark integration. It measures L1 actuation metrics
directly via Kubernetes API polling and supports three scenarios:

| Scenario       | What it measures | Matrix mapping (see [benchmark.md](benchmark.md)) |
| -------------- | ---------------- | -------------------------------------------------- |
| `baseline`     | Cold start deploy-to-ready latency | Cold Start columns |
| `scaling`      | Scale up, down to 1, then up again with hit rate tracking | Wake from Sleep columns |
| `new_variant`  | Sequential deployment of multiple model variants | Introducing New Variant row |

It operates in three modes: `kind` (local Kind cluster), `remote` (OpenShift/remote cluster),
and `simulated` (mock mode, no real GPUs). This tooling remains functional for standalone
FMA actuation measurements and serves as a reference for the metrics being integrated into
llm-d-benchmark.

## Baseline Startup Latency

**Objective:**
Measure the time from **deployment (server-request submission)** to **dual-pod readiness**.

### Inputs

| Parameter           | Type   | Required | Default                                 | Description                                                                                            |
| ------------------ | ------ | -------- | --------------------------------------- | ------------------------------------------------------------------------------------------------------ |
| `--namespace`      | `str`  | **Yes**  | ---                                     | Openshift namespace to run benchmark                                      |
| `--yaml`           | `str`  | **Yes**  | ---                                     | Path to the server-requesting YAML template file              |
| `--image`          | `str`  | **Yes*** | ---                                     | Image repository for the requester pod. Required *only if* `CONTAINER_IMG_REG` env var is **not** set |
| `--tag`            | `str`  | **Yes*** | ---                                     | Image tag for the requester pod. Required *only if* `CONTAINER_IMG_VERSION` env var is **not** set    |
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
python3 inference_server/benchmark/benchmark_base.py --namespace <str> --yaml <str> --cleanup <bool,default:True> --iterations <int, default:1> --cluster-domain <str> --model-path <str> --scenario <str, default:scaling> --image <str> --tag <str>
```

<details>
<summary>Output Example (Subject to Change)</summary>

```
2025-12-01 13:59:52,031 - INFO - scale-request-3-1764615426-4pztx-dual-lhv7s:scale-request-3-1764615426-v9jkh bound through a HIT.
2025-12-01 13:59:52,053 - INFO - Checking readiness of Requester Pod:scale-request-3-1764615426-ktcqd
2025-12-01 13:59:52,496 - INFO - Checking readiness of Requester Pod:scale-request-3-1764615426-ktcqd
2025-12-01 13:59:53,930 - INFO - Checking readiness of Requester Pod:scale-request-3-1764615426-ktcqd
2025-12-01 13:59:53,962 - INFO - Checking readiness of Requester Pod:scale-request-3-1764615426-ktcqd
2025-12-01 13:59:53,972 - INFO - Checking readiness of Requester Pod:scale-request-3-1764615426-ktcqd
2025-12-01 13:59:55,900 - INFO - Checking readiness of Requester Pod:scale-request-3-1764615426-ktcqd
2025-12-01 14:00:03,738 - INFO - Checking readiness of Requester Pod:scale-request-3-1764615426-ktcqd
2025-12-01 14:00:33,850 - INFO - Checking readiness of Requester Pod:scale-request-3-1764615426-ktcqd
2025-12-01 14:01:03,904 - INFO - Checking readiness of Requester Pod:scale-request-3-1764615426-ktcqd
2025-12-01 14:01:22,404 - INFO - Checking readiness of Requester Pod:scale-request-3-1764615426-ktcqd
2025-12-01 14:01:22,405 - INFO - Requester Pod:scale-request-3-1764615426-ktcqd ready after 108s on node:fmaas-vllm-d-wv25b-worker-h100-3-hmtwn using GPU:GPU-ca8ae222-50e0-b69e-16f2-e49dac1afe28
2025-12-01 14:01:22,405 - INFO - scale-request-3-1764615426-ktcqd-dual-pptzq:scale-request-3-1764615426-ktcqd bound through a COLD START.
2025-12-01 14:01:22,405 - INFO - All pods {'scale-request-3-1764615426-hvxjg', 'scale-request-3-1764615426-v9jkh', 'scale-request-3-1764615426-ktcqd'} Ready after 108.97s
replicaset.apps "scale-request-3-1764615426" deleted
pod "scale-request-3-1764615426-9hlb2-dual-dgcg2" deleted
pod "scale-request-3-1764615426-hvxjg-dual-59hc8" deleted
pod "scale-request-3-1764615426-4pztx-dual-lhv7s" deleted
pod "scale-request-3-1764615426-ktcqd-dual-pptzq" deleted
2025-12-01 14:01:32,868 - INFO - ---------------------------------------------------------------------

Total Runs: 15
Successful Runs: 15
Failed Runs: 0
Requester Pods
	Min: 9s,
	Max: 318s
	Average: 125.4s
	Median: 115s
Hits: 3/6 (50%)
Hit Wake-up Times
	Min: 9s,
	Max: 18s
	Average: 13.0s
```
</details>
