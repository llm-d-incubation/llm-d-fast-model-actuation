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

All three paths below use the same
`config/grafana/dashboards/fma-operations.json`. The dashboard uses the classic
Grafana JSON model and does not require a plugin.

### Existing Grafana

In Grafana, open **Dashboards > New > Import**, upload the dashboard JSON, and
select **Import**. Select the Prometheus data source in the dashboard's
**Prometheus** variable.

### Grafana Operator

If the cluster already has a `Grafana` instance managed by the
[Grafana Operator](https://grafana.github.io/grafana-operator/docs/examples/dashboard/),
create a `GrafanaDashboard` in the same namespace or in a separate namespace.
Set the selector key and value to a label on that `Grafana` object, and set the
data source to the UID or name of an existing Prometheus data source in that
instance:

```sh
GRAFANA_NAMESPACE=monitoring
DASHBOARD_NAMESPACE=monitoring
GRAFANA_LABEL_KEY=dashboards
GRAFANA_LABEL_VALUE=grafana
PROMETHEUS_DATASOURCE=prometheus

# Match these values to `kubectl -n "$GRAFANA_NAMESPACE" get grafana --show-labels`
# and the Prometheus data source configured in that Grafana instance.
jq -n \
  --arg namespace "$DASHBOARD_NAMESPACE" \
  --arg grafanaNamespace "$GRAFANA_NAMESPACE" \
  --arg labelKey "$GRAFANA_LABEL_KEY" \
  --arg labelValue "$GRAFANA_LABEL_VALUE" \
  --arg datasource "$PROMETHEUS_DATASOURCE" \
  --rawfile dashboard config/grafana/dashboards/fma-operations.json \
  '{apiVersion:"grafana.integreatly.org/v1beta1", kind:"GrafanaDashboard",
    metadata:{name:"fma-operations", namespace:$namespace},
    spec:{instanceSelector:{matchLabels:{($labelKey):$labelValue}},
      allowCrossNamespaceImport:($namespace != $grafanaNamespace),
      variables:[{name:"DS_PROMETHEUS", value:$datasource}],
      json:$dashboard}}' | kubectl apply -f -
```

Set `DASHBOARD_NAMESPACE` to the namespace where you can create dashboard
resources. When it differs from `GRAFANA_NAMESPACE`, the command enables
`allowCrossNamespaceImport` so the operator can import the dashboard into the
selected `Grafana` instance.

The operator's `spec.variables` selects the default value of the dashboard's
Prometheus data source variable. Check the `GrafanaDashboard` status and open
**FMA Operations** in Grafana after it syncs.

### Grafana on a laptop

For a local trial without a Kubernetes-hosted Grafana, run
[Grafana in Docker](https://grafana.com/docs/grafana/latest/setup-grafana/installation/docker/):

```sh
docker run --rm -d --name fma-grafana -p 3000:3000 \
  --add-host=host.docker.internal:host-gateway grafana/grafana:13.2.1
```

Open <http://localhost:3000>, add a Prometheus data source under
**Connections > Data sources**, then follow **Existing Grafana** above. Use a
Prometheus URL reachable from inside the container, not `localhost` on the
laptop. If Prometheus is reachable on the laptop, use
`http://host.docker.internal:9090`; adjust the port if needed. The panels need
FMA metrics in that Prometheus instance;
importing the JSON alone does not create metrics. Stop the trial with
`docker stop fma-grafana`.

After import, select the target namespace. The InferenceServerConfig,
LauncherConfig, and Node variables can narrow an investigation without editing
PromQL.

The actuation and launcher latency panels include the first observation for a
new label set, before `increase()` has enough samples. The path mix also includes
that first observation when the label set did not exist at the start of the
selected time range.

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
