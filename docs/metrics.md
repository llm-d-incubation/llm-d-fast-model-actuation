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
Recall that a workqueue item may be enqueued multiple times before it
is worked on. That first of _those_ enqueue actions is the one
referenced here; the others are no-ops. Note well: the purpose of the
qualifier here is to distinguish among the possibly-many times that an
item is enqueued _before it is worked on_. For an item that is worked
on multiple times, _each_ work is preceded by a "unique" enqueue of
that item. This is **NOT** about the first time _ever_ that an item is
enqueued.

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

## FMA requester-provider binding

### fma_duality

Vector of gauges. Value is 1.0 while a server-requesting Pod is bound
to a vllm instance in a launcher, set to 0.0 when those two get
unbound.

Labels are as follows.

- `exported_namespace`: Kube API namespace involved
- `requester_name`: name of the server-requesting Pod
- `exported_pod`: name of the launcher Pod
- `exported_container`: name of the container in the launcher Pod
- `instance_id`: the launcher-local identifier of the vllm-instance
- `UUID`: of the GPU. Multiple timeseries when multiple GPUs are involved.
- `node`: name of the Node involved

This metric can be used to effectively do "joins" in PromQL. PromQL
does not really have joins, and this hack is nowhere near as flexible
as an SQL join. Following is an example of how this metric can be used
to associate a DCGM metric about GPUs to the server-requesting Pod.

```
fma_duality * on(UUID) group_left(exported_namspace,exported_pod) DCGM_FI_DEV_FB_USED{exported_namespace!=""}
```

The `{exported_namespace!=""}` qualifier filters out the time series
that DCGM produces when a GPU is not bound to any Pod.

The `group_left` gets more labels into the result.

## FMA actuation latencies

### fma_actuation_seconds

Vector of histograms: Time from requester CreationTimestamp to
completion of readiness relay. Here "completion" means the dual-pods
controller received the reply to its request to the requester to
become ready. Counted when the controller receives that reply.

Labels are as follows.

- `exported_namespace`: Kube namespace of requester and provider
- `path`: one of "cold", "warm", or "hot"
- `instancesDeleted`: decimal representation of number of vllm instances
  deleted for any reason in the course of this actuation.
- `isc_name`: name of the relevant InferenceServerConfig object

### fma_wake_seconds

Vector of histograms: Latency of `/wake_up` call from DPC to vllm.

Labels are as follows.

- `exported_namespace`: Kube namespace of requester and provider
- `isc_name`: name of the relevant InferenceServerConfig object
- `success`: "true" if no error, "false" otherwise. Here "error" means
  a failure to construct the HTTP request message, send it, or receive
  the response message. The HTTP "status" code is not germane.

### fma_launcher_create_seconds

Vector of histograms: Latency of kube API call to create launcher.
This is only the time to get the Kube apiserver to create the Pod _API
object_; the actual construction by of a running Pod, as well as the
scheduling by the Kube Pod scheduler, are not included here.

Labels are as follows.

- `exported_namespace`: Kube namespace of requester and provider
- `lcfg_name`: name of the relevant LauncherConfig object
- `success`: "true" if no error, "false" otherwise. Here "error" means
  a failure to construct the HTTP request message, send it, or receive
  the response message. The HTTP "status" code is not germane.

### fma_instance_create_seconds

Vector of histograms: Latency of DPC call to launcher to create vllm instance.

Labels are as follows.

- `exported_namespace`: Kube namespace of requester and provider
- `isc_name`: name of the relevant InferenceServerConfig object
- `success`: "true" if no error, "false" otherwise. Here "error" means
  a failure to construct the HTTP request message, send it, or receive
  the response message. The HTTP "status" code is not germane.
