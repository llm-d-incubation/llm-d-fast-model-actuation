# Prometheus Metrics

## From libraries

- Go language runtime metrics (`go_...`)
- Go process metrics (`process_...`)
- Kubernetes REST client metrics (`rest_client_...`)
- Kubernetes workqueue metrics (`workqueue_...`)

## Dual-pods controller inner work queue

### fma_dpc_innerqueue_adds_total

Vector of counters: Number of unique adds to the queue.

Labels are as follows.

- `node`: name of Node

### fma_dpc_innerqueue_depth

Vector of gauges: Number of items in the queue.

Labels are as follows.

- `node`: name of Node

### fma_dpc_innerqueue_queue_duration_seconds

Vector of classic histograms: Time from unique enqueue to the dequeue.
Recall that a workqueue item may be enqueued multiple times before
it is worked on. That first enqueue action is the one referenced here;
the others are no-ops.

Labels are as follows.

- `node`: name of Node

### fma_dpc_innerqueue_retries_total

Vector of counters: Total number of retries handled by queue.

Labels are as follows.

- `node`: name of Node

### fma_dpc_innerqueue_work_duration_seconds

Vector of histograms: Time spent syncing (working on one item).

Labels are as follows.

- `node`: name of Node

## FMA workload characterization

### fma_requester_count

Vector of gauges: Number of server-requesting Pods.

Labels are as follows.

- `isc_name`: Name of the relevant InferenceServerConfig

### fma_isc_count

Vector of gauges: Number of InferenceServerConfig objects.

Labels are as follows.

- `launcher_config_name`: Name of the relevant LauncherConfig

## FMA actuation latencies

### fma_actuation_seconds

Vector of histograms: Time from requester CreationTimestamp to
completion of readiness relay. Here "completion" means the dual-pods
controller received the reply to its request to the requester to
become ready. Counted when the controller receives that reply.

Labels are as follows.

- `namespace`
- `path`: one of "cold", "warm", or "hot"
- `instancesDeleted`: decimal representation of number of vllm instances
  deleted for any reason in the course of this actuation.
- `isc_name`: name of the relevant InferenceServerConfig object

### fma_wake_seconds

Vector of histograms: Latency of `/wake_up` call from DPC to vllm.

Labels are as follows.

- `namespace`
- `isc_name`: name of the relevant InferenceServerConfig object
- `success`: "true" if no error, "false" otherwise

### fma_launcher_create_seconds

Vector of histograms: Latency of kube API call to create launcher.

Labels are as follows.

- `namespace`
- `lcfg_name`: name of the relevant LauncherConfig object
- `success`: "true" if no error, "false" otherwise

### fma_instance_create_seconds

Vector of histograms: Latency of DPC call to launcher to create vllm instance.

Labels are as follows.

- `namespace`
- `isc_name`: name of the relevant InferenceServerConfig object
- `success`: "true" if no error, "false" otherwise
