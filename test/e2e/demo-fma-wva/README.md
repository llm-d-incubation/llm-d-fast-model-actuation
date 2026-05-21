# FMA + WVA + KEDA + LLM-D-Router Demo on OpenShift

Two scripts to **deploy and tear down a working end-to-end demo of
FMA + WVA + KEDA + llm-d-router in a single OpenShift namespace** —
autoscaling an llm-d workload driven by WVA through KEDA. KEDA must already be installed
on the cluster (see [Prerequisites](#prerequisites)) — the scripts don't install
it.

The scripts install the platform components (controllers, CRDs, RBAC,
llm-d-router) **and** create a concrete workload that exercises them (an
`InferenceServerConfig`, `LauncherConfig`, `LauncherPopulationPolicy`, requester
`Deployment`, and a KEDA `ScaledObject` annotated with `llm-d.ai/managed: "true"`
so WVA discovers it and publishes the `wva_desired_replicas` metric to
Prometheus). KEDA's Prometheus trigger reads that metric and drives the HPA it
creates and manages (`wva-keda-hpa-fma-requester`) to scale the deployment.

| Script | Purpose |
|---|---|
| `setup-fma-wva.sh` | Deploy FMA (CRDs, ValidatingAdmissionPolicies, RBAC, controllers, sample CRs), WVA, llm-d-router, and the demo requester workload |
| `cleanup-fma-wva.sh` | Tear it all back down |

The workload-variant-autoscaler (WVA) repo is cloned automatically — no need
to pre-clone or pass `--wva-repo-path`.

Both scripts use a standard CLI flag interface. Run either with `--help`
for the full list of options.

## Versioning

> **⚠️ The FMA version deployed is _mixed_.** Images come from the published
> release passed via `--fma-version` (required — no default); CRDs, VAPs, and
> the Helm chart come from your **local working tree**. Run this script from a
> checkout of the **same tag** you pass to `--fma-version`. (No single-release
> installer exists yet.)

FMA, WVA, GAIE and llm-d release independently, so incompatibilities are
possible. The defaults pin a known-good combination. If you change one,
test the others alongside it.

| Component | Flag | Default |
|---|---|---|
| All FMA images (dual-pods + launcher-populator controllers, launcher, requester) | `--fma-version` | _(required)_ |
| WVA (published release tag) | `--wva-version` | `v0.8.0` |
| GAIE (Gateway API Inference Extension) | `--gaie-version` | `v1.5.0` |
| llm-d release | `--llm-d-release` | `v0.7.0` |

See the "Pin all components" example below for a fully-pinned invocation.

## Prerequisites

- `oc` authenticated to an OpenShift cluster with NVIDIA GPU nodes
- **KEDA** (the `keda.sh/v1alpha1` `ScaledObject` CRD + controller) installed
  and configured on the cluster, with Prometheus access wired up — the deploy
  script creates a `ScaledObject` but does **not** install or configure KEDA.
- For **deploy** (`setup-fma-wva.sh`): `helm`, `kubectl`, `jq`, `make`, `git`, `yq` ([mikefarah/yq](https://github.com/mikefarah/yq)) on `$PATH`
- For **cleanup** (`cleanup-fma-wva.sh`): `helm`, `kubectl`, `jq` on `$PATH` (plus `git` if `--level all` is used)

## Deploy

`--fma-version` is required — pass a published
[FMA release](https://github.com/llm-d-incubation/llm-d-fast-model-actuation/releases)
(no default; see [Versioning](#versioning)). Examples use `v0.6.2`.

Deploy (default namespace `fma-wva-demo`):

```shell
./test/e2e/demo-fma-wva/setup-fma-wva.sh --fma-version v0.6.2
```

With a custom namespace:

```shell
./test/e2e/demo-fma-wva/setup-fma-wva.sh --fma-version v0.6.2 --namespace my-fma-demo
```

Re-running is safe (idempotent — existing components are skipped). On first run
the WVA repo is cloned to `.wva-checkout/`; pass `--wva-repo-path PATH` to reuse
an existing checkout.

## Tear down

`cleanup-fma-wva.sh` supports three nested levels via `--level` (default
`namespace`):

| `--level` | Removes | Leaves | Use case |
|---|---|---|---|
| `workload` | The demo workload (ScaledObject + KEDA HPA, requester Deployment, FMA CRs, PodMonitor) | The platform (FMA/WVA/EPP controllers) | Reuse the platform with a different workload |
| `namespace` *(default)* | `workload` + all **namespaced** platform (FMA controllers, WVA controller, llm-d-router) | Cluster-scoped objects and the namespace | Clear the namespace without disturbing other cluster users |
| `all` | `namespace` + cluster-scoped RBAC, the `fma-poc=true` node label, and the namespace itself | CRDs only | Full cluster teardown |

```shell
# Default: clear FMA/WVA/EPP from the namespace, leave cluster objects alone
./test/e2e/demo-fma-wva/cleanup-fma-wva.sh --namespace my-fma-demo

# Just the workload, keep the platform running
./test/e2e/demo-fma-wva/cleanup-fma-wva.sh --namespace my-fma-demo --level workload

# Everything (auto-clones the WVA repo to undeploy WVA + llm-d-router)
./test/e2e/demo-fma-wva/cleanup-fma-wva.sh --namespace my-fma-demo --level all
```

Even `--level all` is **not** an exhaustive wipe: CRDs (Gateway API, GAIE, FMA,
WVA) are never removed — they may be shared across namespaces. Delete them by
hand if you want a complete wipe.

## Common flags

| Flag | Default | Used by |
|---|---|---|
| `-n`, `--namespace NAME` | `fma-wva-demo` | both |
| `--level workload\|namespace\|all` | `namespace` | cleanup |
| `--wva-repo-path PATH` | `<repo-root>/.wva-checkout` | both |
| `--wva-repo-url URL` | `https://github.com/llm-d/llm-d-workload-variant-autoscaler` | both |
| `--wva-version TAG` (published release tag) | `v0.8.0` | both |
| `--fma-image-registry URL` | `ghcr.io/llm-d-incubation/llm-d-fast-model-actuation` | deploy |
| `--fma-version VER` | _(required)_ | deploy |
| `--model NAME` | `Qwen/Qwen3-8B` | deploy |
| `--gpu-node NODE` | first node with `nvidia.com/gpu.present=true` | deploy |
| `--hf-token TOKEN` | (unset) | deploy (required for gated models) |

Run `./setup-fma-wva.sh --help` or `./cleanup-fma-wva.sh --help` for the
complete list. Equivalent environment variables (uppercase, underscored —
e.g., `NAMESPACE`, `WVA_VERSION`, `WVA_REPO_PATH`) are also accepted, but flags
take precedence. `--fma-version` is the exception: its env var is `FMA_VERSION`.

## Examples

Pin all components to specific versions. `--fma-version` and `--wva-version`
must be **published release tags** (each is used as an image tag):

```shell
./test/e2e/demo-fma-wva/setup-fma-wva.sh \
  --fma-version v0.6.2 \
  --wva-version v0.8.0 \
  --gaie-version v1.5.0 \
  --llm-d-release v0.7.0
```

Deploy a gated model (`--hf-token` **and** approved access on the model's HF
page are both required):

```shell
./test/e2e/demo-fma-wva/setup-fma-wva.sh \
  --fma-version v0.6.2 \
  --model meta-llama/Llama-3.1-8B-Instruct \
  --hf-token hf_xxx
```

Reuse an existing WVA checkout instead of the auto-clone:

```shell
./test/e2e/demo-fma-wva/setup-fma-wva.sh \
  --fma-version v0.6.2 \
  --wva-repo-path /path/to/my/wva-checkout
```

## Troubleshooting

### ScaledObject not scaling / KEDA-managed HPA shows `<unknown>`
`kubectl get scaledobject fma-requester-scaler -n <ns>` shows the object's
`READY`/`ACTIVE` status; `kubectl get hpa wva-keda-hpa-fma-requester -n <ns>`
(the HPA KEDA creates) shows the metric as `<unknown>/1` in the `TARGETS`
column when KEDA's Prometheus trigger has no value yet. The first 1–2 poll
cycles after deploy usually look like this and resolve on their own (~30–60s).

If it persists longer, walk the chain from WVA out:

1. **WVA discovered the ScaledObject?** `kubectl logs -n <ns> -l app.kubernetes.io/name=workload-variant-autoscaler`
   should mention the ScaledObject name; the controller logs each object it
   picks up via the `llm-d.ai/managed` annotation.
2. **WVA is publishing the metric?** Query Prometheus directly:
   `wva_desired_replicas{variant_name="fma-requester", namespace="<ns>"}`.
   If empty, WVA isn't emitting for this variant.
3. **KEDA's trigger can read it?** Unlike a plain HPA, KEDA's Prometheus
   trigger queries Prometheus **directly** (no prometheus-adapter / external
   metrics API in the path). Check the KEDA operator logs and the ScaledObject
   status for scaler errors:
   ```shell
   kubectl describe scaledobject fma-requester-scaler -n <ns>
   kubectl logs -n <keda-ns> -l app=keda-operator | grep -i fma-requester
   ```
   A `serverAddress`/TLS/auth error here means KEDA can't reach Prometheus —
   fix the trigger's Prometheus access (out of scope for this demo, which
   assumes KEDA is already configured).
4. **Trigger query matches the series?** Compare the ScaledObject trigger's
   `query` (`spec.triggers[0].metadata.query`) against the labels on the actual
   `wva_desired_replicas` series in Prometheus — `variant_name` in particular
   must equal the deployment name (`fma-requester` in this demo).

### Launcher pod missing the `llm-d.ai/variant` label
The `llm-d.ai/variant` label is applied from
`InferenceServerConfig.spec.modelServerConfig.labels` when a requester binds
to a launcher. Unbound (idle) launchers won't carry it. Check the ISC, and
verify the launcher is bound (has the `dual-pods.llm-d.ai/dual` label set).
Don't add the label to `LauncherConfig.spec.podTemplate.metadata.labels` —
it will collide with ISC-applied labels during binding.

### Unknown flag error
The scripts reject unknown flags. Check spelling and run with `--help` for
the canonical flag names.
