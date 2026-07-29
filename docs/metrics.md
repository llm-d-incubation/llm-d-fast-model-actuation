# Prometheus Metrics

## From libraries

- Go language runtime metrics (`go_...`)
- Go process metrics (`process_...`)
- Kubernetes REST client metrics (`rest_client_...`)
- Kubernetes workqueue metrics (`workqueue_...`)

See [the Kubernetes metrics
documentation](https://v1-34.docs.kubernetes.io/docs/reference/instrumentation/metrics/)
for information about those.

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

### fma_launcher_pod_count

Vector of gauges: Number of launcher Pods for each LauncherConfig and
lifecycle phase. This metric deliberately has no Node label, so its
cardinality does not grow with the number of Nodes.

Labels are as follows.

- `lcfg_name`: name of the relevant LauncherConfig
- `phase`: one of the following:
  - `bound`: assigned to a server-requesting Pod
  - `unbound`: uses the current launcher template, is not bound, and is either
    Ready or has not reached a stuck threshold
  - `stuck_scheduling`: uses the current launcher template, is unbound and
    not scheduled, and has reached the configured scheduling threshold,
    measured from Pod creation
  - `stuck_starting`: uses the current launcher template, is unbound,
    scheduled but not Ready, and has reached the configured starting
    threshold, measured from scheduling
  - `stale`: is unbound and was created from a superseded launcher template

The launcher-populator's `--stuck-scheduling-threshold` and
`--stuck-starting-threshold` flags configure the two stuck thresholds.
When the controller observes a retained launcher in either stuck phase without
the stuck label, it publishes a `LauncherStuck` Warning Event and sets the
`dual-pods.llm-d.ai/launcher-stuck=true` label. The label makes the current
condition discoverable after the Event expires and suppresses further Events
while the Pod remains stuck. If the launcher recovers, the controller removes
the label.

The Event is emitted before the label Patch is attempted. Because they are
separate Kubernetes API transactions, a controller failure between them or a
failed Patch can produce a duplicate Event on a later reconcile. This ordering
deliberately favors a possible duplicate over losing the Event entirely.

A stuck launcher remains in place and continues to count toward the desired
launcher population. The controller does not automatically replace or retry
it; users decide how to respond using the metric, Event, and label.

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
fma_duality * on(UUID) group_left(exported_namespace,exported_pod) DCGM_FI_DEV_FB_USED{exported_namespace!=""}
```

The `{exported_namespace!=""}` qualifier filters out the time series
that DCGM produces when a GPU is not bound to any Pod.

The `group_left` gets more labels into the result.

## FMA actuation latencies

### fma_actuation_seconds

Vector of histograms: Time from start of the requester container to
completion of readiness relay. Here "completion" means the dual-pods
controller received the reply to its request to the requester to
become ready. Counted when the controller receives that reply.

Labels are as follows.

- `exported_namespace`: Kube namespace of requester and provider
- `path`: one of "cold", "warm", or "hot"
- `instancesDeleted`: decimal representation of number of vllm instances
  deleted for any reason in the course of this actuation.
- `isc_name`: name of the relevant InferenceServerConfig object

### fma_http_latency_seconds

Vector of histograms: Latency of HTTP calls. Currently, only calls
from the dual-pods controller are instrumented.

Labels are as follows.

- `purpose`: short token identifying the purpose of the call
- `method`: the HTTP method
- `exported_namespace`: Kube namespace of requester and provider
- `isc_name`: name of the relevant InferenceServerConfig object
- `status_code`: if an HTTP response was not received then "0",
  otherwise the decimal representation of the returned HTTP status
  code.

The possible values of the `purpose` label are as follows.

- `create_instance`
- `delete_all_instances`
- `delete_instance`
- `get_accel_memory_usage`
- `get_gpu_uuids`
- `get_health`
- `get_instance_state`
- `list_instances`
- `list_instance_ids`
- `query_sleeping`
- `relay_ready`
- `relay_unready`
- `sleep`
- `wake`

**NOTE**: For every observation entered into this HistogramVec there is an associated log statement. It is at `V(5)`. At the time of this writing it appears in the `doHTTP` function in https://github.com/llm-d-incubation/llm-d-fast-model-actuation/blob/main/pkg/controller/dual-pods/inference-server.go and reads as follows.

```go
logger.V(5).Info("HTTP call done", "purpose", purpose, "method", method, "url", url, "httpCallStartTime", httpCallStartTime.Format(time.RFC3339Nano), "latencySecs", latencySecs, "statusCode", statusCode, "err", err)
```

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
