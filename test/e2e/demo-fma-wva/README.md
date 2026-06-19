# FMA + WVA Demo on OpenShift

Two scripts to deploy and tear down a working end-to-end demo of FMA + WVA + llm-d in a
single OpenShift namespace. The scripts install the platform components
(controllers, CRDs, RBAC, EPP/Gateway) **and** create a concrete workload
that exercises them (an `InferenceServerConfig`, `LauncherConfig`,
`LauncherPopulationPolicy`, requester `Deployment`, and a
`HorizontalPodAutoscaler` annotated with `llm-d.ai/managed: "true"` so WVA
discovers it and publishes the `wva_desired_replicas` external metric the
HPA consumes to scale the deployment).

| Script | Purpose |
|---|---|
| `demo-fma-wva-ocp.sh` | Deploy FMA (CRDs, ValidatingAdmissionPolicies, RBAC, controllers, sample CRs), WVA, EPP/Gateway, and the demo requester workload |
| `cleanup-fma-wva.sh` | Tear it all back down |

The workload-variant-autoscaler (WVA) repo is cloned automatically — no need
to pre-clone or pass `--wva-repo-path`.

Both scripts use a standard CLI flag interface. Run either with `--help`
for the full list of options.

## Versioning

> **This script consumes _published_ FMA releases only.** Even though it
> lives inside the FMA repo, it pulls FMA images by tag (e.g.
> `v0.6.0-alpha.13`) — it does **not** build from the local source tree.
> Don't change `--fma-image-tag` to a tag that hasn't been pushed yet;
> doing so couples the FMA repo to itself in a circular way and breaks
> reproducibility.

FMA, WVA, and GIE/llm-d release independently, so incompatibilities are
possible. The defaults pin a known-good combination. If you change one,
test the others alongside it.

| Component | Flag | Default |
|---|---|---|
| FMA controllers + launcher/requester images | `--fma-image-tag` | `v0.6.0-alpha.13` |
| WVA (git ref + controller image tag, kept in sync) | `--wva-version` | `v0.8.0-rc4` |
| GIE (Gateway API Inference Extension) | `--gaie-version` | `v1.5.0` |
| llm-d release (EPP/Gateway) | `--llm-d-release` | `v0.7.0` |

See the "Pin all components" example below for a fully-pinned invocation.

## Prerequisites

- `oc` authenticated to an OpenShift cluster with GPU nodes
- For **deploy** (`demo-fma-wva-ocp.sh`): `helm`, `kubectl`, `jq`, `make`, `git`, `yq` ([mikefarah/yq](https://github.com/mikefarah/yq)) on `$PATH`
- For **cleanup** (`cleanup-fma-wva.sh`): `helm`, `kubectl`, `jq` on `$PATH` (plus `git` if `--full-cleanup` is used)

## Deploy

Default deploy (uses namespace `fma-wva-demo`):

```shell
./test/e2e/demo-fma-wva/demo-fma-wva-ocp.sh
```

Pick your own namespace:

```shell
./test/e2e/demo-fma-wva/demo-fma-wva-ocp.sh --namespace my-fma-demo
```

The script is idempotent — re-running it skips components that already exist.
On first run it clones the WVA repo to `.wva-checkout/` at the repo root and
reuses it on subsequent runs. Pass `--wva-repo-path PATH` to use a different
location (e.g., a shared checkout outside this repo).

`--namespace` and `--wva-repo-path` are just two of many flags. See
[Common flags](#common-flags) for the most-used ones, or run
`./test/e2e/demo-fma-wva/demo-fma-wva-ocp.sh --help` for the complete list.

## Tear down

By default cleans up FMA / WVA objects but leaves the namespace, CRDs,
WVA controller, and EPP/Gateway in place:

```shell
./test/e2e/demo-fma-wva/cleanup-fma-wva.sh --namespace my-fma-demo
```

Full cleanup (also removes the namespace, the `fma-poc=true` label that the
deploy script applied to the GPU node, the WVA controller, and EPP/Gateway):

```shell
./test/e2e/demo-fma-wva/cleanup-fma-wva.sh --namespace my-fma-demo --full-cleanup
```

CRDs (Gateway API, GAIE, FMA, WVA) are never removed — they may be shared
across namespaces. Delete them by hand if you want a complete wipe.

## Common flags

| Flag | Default | Used by |
|---|---|---|
| `-n`, `--namespace NAME` | `fma-wva-demo` | both |
| `-f`, `--full-cleanup` | (off) | cleanup |
| `--wva-repo-path PATH` | `<repo-root>/.wva-checkout` | both |
| `--wva-repo-url URL` | `https://github.com/llm-d/llm-d-workload-variant-autoscaler` | both |
| `--wva-version REF` | `v0.8.0-rc4` | both |
| `--fma-image-registry URL` | `ghcr.io/llm-d-incubation/llm-d-fast-model-actuation` | deploy |
| `--fma-image-tag TAG` | `v0.6.0-alpha.13` | deploy |
| `--model NAME` | `HuggingFaceTB/SmolLM2-360M-Instruct` | deploy |
| `--gpu-node NODE` | first node with `nvidia.com/gpu.present=true` | deploy |
| `--hf-token TOKEN` | (unset) | deploy (if model is gated) |

Run `./demo-fma-wva-ocp.sh --help` or `./cleanup-fma-wva.sh --help` for the
complete list. Equivalent environment variables (uppercase, underscored —
e.g., `NAMESPACE`, `IMAGE_TAG`, `WVA_VERSION`, `WVA_REPO_PATH`) are also
accepted for backward compatibility, but flags take precedence.

## Examples

Pin a specific WVA version (branch, tag, or commit SHA):

```shell
# Tag
./test/e2e/demo-fma-wva/demo-fma-wva-ocp.sh --wva-version v0.3.0
# Commit SHA (full or abbreviated)
./test/e2e/demo-fma-wva/demo-fma-wva-ocp.sh --wva-version a1b2c3d4
```

Use a WVA fork:

```shell
./test/e2e/demo-fma-wva/demo-fma-wva-ocp.sh \
  --wva-repo-url https://github.com/myorg/wva-fork \
  --wva-version feature-branch
```

Deploy a different model:

```shell
./test/e2e/demo-fma-wva/demo-fma-wva-ocp.sh \
  --model meta-llama/Llama-3.1-8B-Instruct \
  --hf-token hf_xxx
```

Use an existing WVA checkout instead of the auto-clone:

```shell
./test/e2e/demo-fma-wva/demo-fma-wva-ocp.sh \
  --wva-repo-path /path/to/my/wva-checkout
```

Pin all components (FMA + WVA + GIE + llm-d) to specific versions:

```shell
./test/e2e/demo-fma-wva/demo-fma-wva-ocp.sh \
  --fma-image-tag v0.6.0-alpha.13 \
  --wva-version v0.3.0 \
  --gaie-version v1.5.0 \
  --llm-d-release v0.7.0
```

## Troubleshooting

### HPA `TARGETS` column shows `<unknown>` for `wva_desired_replicas`
`kubectl get hpa fma-requester-hpa` shows the metric as `<unknown>/1 (avg)`
in the `TARGETS` column when the external-metrics API has no value for the
HPA's selector. The first 1–2 reconcile cycles after deploy usually look
like this and resolve on their own (~30–60s).

If it persists longer, walk the chain from WVA out:

1. **WVA discovered the HPA?** `kubectl logs -n <ns> -l app.kubernetes.io/name=workload-variant-autoscaler`
   should mention the HPA name; the controller logs each HPA it picks up via
   the `llm-d.ai/managed` annotation.
2. **WVA is publishing the metric?** Query Prometheus directly:
   `wva_desired_replicas{variant_name="fma-requester", exported_namespace="<ns>"}`.
   If empty, WVA isn't emitting for this variant.
3. **prometheus-adapter exposes it as an external metric?** Check two
   layers — registration *and* a queryable value for your namespace:
   ```shell
   # 3a. Metric registered in the external-metrics API (adapter rule exists)
   kubectl get --raw "/apis/external.metrics.k8s.io/v1beta1" | grep wva_desired_replicas

   # 3b. Value actually retrievable for your HPA's labels
   kubectl get --raw "/apis/external.metrics.k8s.io/v1beta1/namespaces/<ns>/wva_desired_replicas?labelSelector=variant_name=fma-requester,exported_namespace=<ns>"
   ```
   3a passing without 3b means the adapter knows the metric name but no
   series matches your selector — usually a `variant_name` or
   `exported_namespace` label mismatch (see step 4).
4. **HPA's matchLabels match?** Compare the HPA's
   `spec.metrics[0].external.metric.selector.matchLabels` against the labels
   on the actual `wva_desired_replicas` series in Prometheus — `variant_name`
   in particular must equal the deployment name (`fma-requester` in this
   demo).

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
