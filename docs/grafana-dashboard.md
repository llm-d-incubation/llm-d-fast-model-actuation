# Grafana dashboard

The [FMA Operations dashboard](../config/grafana/dashboards/fma-operations.json)
turns the metrics documented in [Prometheus Metrics](metrics.md) into one
operational view. It is designed to answer four questions in order:

1. Are the FMA controllers and workload objects healthy?
2. Are actuations completing, and which path is slow?
3. Is the controller retrying or waiting on Kubernetes or HTTP calls?
4. Which bound GPU is under memory pressure, when DCGM metrics are available?

## Prerequisites

- Grafana with a Prometheus data source.
- Prometheus scraping both FMA controller Pods on their named `metrics` port
  (TCP 8002).
- The scrape target must retain the standard `namespace` and `pod` target
  labels. Keep `honorLabels` disabled so the workload namespace exported by
  FMA remains available as `exported_namespace`.
- Optional GPU panels require `DCGM_FI_DEV_FB_USED` and
  `DCGM_FI_DEV_FB_FREE`, with the GPU identifier in the `UUID` label.

If the Prometheus Operator is installed, save the following as
`fma-podmonitor.yaml`, replace both placeholders, and apply it:

```yaml
apiVersion: monitoring.coreos.com/v1
kind: PodMonitor
metadata:
  name: fma-controllers
  namespace: REPLACE_WITH_FMA_NAMESPACE
  labels:
    release: REPLACE_WITH_PROMETHEUS_RELEASE
spec:
  namespaceSelector:
    matchNames:
      - REPLACE_WITH_FMA_NAMESPACE
  selector:
    matchLabels:
      scrape: "true"
  podMetricsEndpoints:
    - port: metrics
      interval: 15s
      honorLabels: false
```

The `release` label must match the PodMonitor selector of your Prometheus
installation. Some installations use another label or select all PodMonitors;
adjust only that label to match the local Prometheus configuration.

Verify the scrape before importing the dashboard:

```promql
up{pod=~".*(dual-pods-controller|launcher-populator).*"}
```

Both controller targets should return `1`.

## Import

In Grafana, open **Dashboards > New > Import**, upload
`config/grafana/dashboards/fma-operations.json`, select the Prometheus data
source, and select **Import**. The dashboard uses the classic Grafana JSON
model and does not require a Grafana plugin.

After import, select the target namespace. The InferenceServerConfig,
LauncherConfig, and Node variables can narrow an investigation without editing
PromQL.

## Reading the dashboard

| Symptom | Start with | Follow with |
| --- | --- | --- |
| A requester takes too long to become Ready | Actuation latency by path | HTTP latency by purpose, DPC queue latency |
| Launchers exist but cannot be reused | Current launcher phases | Launcher phase history, controller restarts |
| A launcher API call is flaky | Launcher create API failures | Launcher create API latency, controller logs |
| A binding retries or fails | DPC retries | HTTP failures grouped by purpose and status |
| A wake or create operation hits GPU pressure | GPU framebuffer utilization | Active binding topology, then DCGM and requester logs |

The red `stuck_scheduling`, `stuck_starting`, and `stale` launcher series are
states that need investigation. A non-zero HTTP status code outside the `2xx`
range is an application response; status code `0` means that no HTTP response
was received.

The GPU panels are intentionally optional. An empty GPU panel means the DCGM
metrics or matching `UUID` labels are absent; it does not make the FMA-only
panels invalid. GPU framebuffer values are device-level measurements. If more
than one active FMA binding refers to the same GPU, the joined result can appear
once per binding and must not be summed as physical GPU capacity.

## Compatibility

The dashboard queries only metrics listed in [Prometheus Metrics](metrics.md)
plus the two optional DCGM metrics above. If a deployment predates one of those
FMA metrics, the corresponding panel remains empty while the rest of the
dashboard continues to work.
