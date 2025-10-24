# Dual-Pods Benchmarking Tool
The **Dual-Pods Benchmarking Tool** measures and reports the startup and readiness latency of model-serving pods within the LLM-D Fast Model Actuation workflow.

## Purpose
The goal is to quantify and compare how quickly a model-serving duo (server-requesting and server-providing pods) becomes available under different actuation conditions such as cold starts, wake-ups from a sleeping state, using prewarmed pods, etc. to guide future optimizations for the **Dual-Pods Controller (DPC)**.

## Baseline Startup Latency

**Objective:**
Measure the time from **deployment (server-request submission)** to **dual-pod readiness**.

### Inputs

| Parameter                          | Description                                          |
| ---------------------------------- | -----------------------------------------------------|
| `--namespace`                      | Kubernetes namespace where benchmarking occurs       |
| `--yaml`                           | YAML file describing the dual-pod deployment         |
| `--num-gpus`           | Number of GPUs to request for the server-providing pod           |
| `--num-model-variants` | Number of different model variants to deploy during benchmarking |

### Outputs

| Output                 | Description                                                                |
| ---------------------- | -------------------------------------------------------------------------- |
| `startup_time`         | Total time from deployment to readiness                                    |
| `availability_mode`    | Indicates whether the vLLM instance was started cold or resumed from sleep |

**Example Usage**
```bash
python3 inference_server/benchmark/dualpods_time_logs.py \
  --namespace benchmarking-dev  \
  --yaml deploy/server-request-minimal.yaml
```

**Output Example**

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

| Scenario                      | Description                                                   |
| ----------------------------- | ------------------------------------------------------------- |
| **Cold Start vLLM Instance**  | Measures latency for creating a new vLLM server from scratch (no caching with and without launcher).                 |
| **Wake Up Sleeping Instance** | Measures time to wake-up a sleeping instance (with and without launcher).                                        |
| **Launcher Activation** | Measure end-to-end time from launcher triggering a new instance to full readiness. |
| **Scale-Up Requester Replicas**            | Deploys additional requester-provider pairs to handle increased load and measures incremental activation latency. |
| **Scale-Down Requester Replicas**          | Evaluate teardown and reactivation time when waking-up sleeping instance.                       |
| Coming soon| |
| Coming soon| |

### Next steps

This benchmarking framework may be integrated into an existing or a new LLM-D benchmark harness. It should:

- Continuously measure actuation latency across GPU and node changes in the cluster, plus various model variants.

- Validate improvements across llm-d releases and DPC changes.
