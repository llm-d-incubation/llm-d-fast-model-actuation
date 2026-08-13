# Step-by-step installation of a release of FMA

This document is about step-by-step installation of a given release of
FMA. For dev/test of a general commit of the repo, see
[README.md#devtest](README.md#devtest).

FMA, like llm-d, is primarily confined to operate in one Kubernetes
API object namespace. However, installing FMA _does_ involve _some_
cluster-scoped objects. That makes installing FMA tricky, for two
reasons. One is potential interference between different users
independently (e.g., using different releases) using FMA in the same
cluster. The other is the fact that creating cluster-scoped objects
requires more authorization than the base usually given to Kubernetes
users.

To minimize conflicts between independent FMA users in the same
cluster, FMA uses namespace-scoped objects wherever possible. Where
cluster-scoped objects are required, they are changed rarely and
carefully. See the [document about cluster
sharing](cluster-sharing.md).

FMA has [a Helm chart](../charts/fma-controllers) that covers all of
the namespace-scoped objects involved in deploying FMA. Instantiation
of that chart is discussed [below](#instantiating-the-chart).

That chart deliberately excludes the cluster-scoped objects, for two
reasons: (1) such an object can not be owned by both of two different
users' Helm chart instances and (2) users are not necessarily
authorized to create/update/delete such objects. Installing these
objects is discussed [below](#cluster-scoped-objects).

## Prerequisites

To install FMA in a given Kubernetes cluster you will need the
following tools on the machine doing the installation.

- **kubectl**, version 1.32 or later.

- [**helm**](https://github.com/helm/helm), version 3 or 4.

- [**yq**](https://github.com/mikefarah/yq), version 4 or later

- [**jq**](https://github.com/jqlang/jq), version 1.6 or later

In the cluster you will need a namespace in which you have at least
ordinary Kubernetes user authorizations.

You will need your cluster and installation machine to have on-line
access to the OCI registry at `ghcr.io`, or another registry where you
have staged copies of the relevant OCI repositories. Following are the
repositories to stage (if you are doing that).

- `ghcr.io/llm-d-incubation/llm-d-fast-model-actuation/charts/fma-controllers`
- `ghcr.io/llm-d-incubation/llm-d-fast-model-actuation/requester`
- `ghcr.io/llm-d-incubation/llm-d-fast-model-actuation/launcher`
- `ghcr.io/llm-d-incubation/llm-d-fast-model-actuation/dual-pods-controller`
- `ghcr.io/llm-d-incubation/llm-d-fast-model-actuation/launcher-populator`

## Cluster-scoped objects

The artifacts for a release include the Helm chart but do not include
the cluster-scoped objects. To get the available sources (YAML files),
you need to `git clone` this repo and `git checkout` the desired
release.

The following subsections describe the various categories of
cluster-scoped objects that you will need to create or find already
existing when installing FMA.

### Authorization to read Node objects

The FMA controllers need authorization to get/list/watch Node
objects. If your cluster does not authorize every authenticated client
to get/list/watch Node objects then you will need to (a) create a
`ClusterRole` object that grants such authorization and (b) include a
setting of `global.nodeViewClusterRole` to that name when
instantiating the FMA Helm chart.

### FMA CRDs

The YAML for the `CustomResourceDefinition` objects that define FMA's
custom resources are in (a) files in [config/crd](../config/crd), in
releases up to and including 0.6.5, and (b) one file
[config/crds.yaml](../config/crds.yaml), in releases from
0.6.5-alpha.1 onward. Those objects need to get created in the
cluster. For example, by `kubectl apply`. If you are not authorized to
do that then ask a cluster administrator to do it for you or to grant
you authorization to do it yourself; there is an example `ClusterRole`
for the latter in
[config/fma-cluster-admin/fma-crd-admin-clusterrole.yaml](config/fma-cluster-admin/fma-crd-admin-clusterrole.yaml).

If you are automating creation of these `CustomResourceDefinition`
objects, note that merely getting the object created is not enough for
it to take full effect. The Kubernetes apiservers take a little time
to digest the contents of those objects. If you need to automate
waiting for that to happen, wait for each of them to have an entry in
`.status.conditions` with type "Established" and value "true". The
names of these objects are as follows.

- `inferenceserverconfigs.fma.llm-d.ai`
- `launcherconfigs.fma.llm-d.ai`
- `launcherpopulationpolicies.fma.llm-d.ai`

### Admission policies

FMA uses two `ValidatingAdmissionPolicy` objects and two corresponding
`ValidatingAdmissionPolicyBinding` objects to prevent undesired object
modifications. YAML for all four is found in (a) files in
[config/validating-admission-policies](../config/validating-admission-policies),
in releases up to and including 0.6.5, and (b) one file
[config/validating-admission-policies.yaml](../config/validating-admission-policies.yaml),
from release 0.6.5-alpha.1 onward.  Those objects need to get created
in the cluster. For example, by `kubectl apply`. If you are not
authorized to do that then ask a cluster administrator to do it for
you or to grant you authorization to do it yourself; there is an
example `ClusterRole` for the latter in
[config/fma-cluster-admin/fma-policy-admin-clusterrole.yaml](config/fma-cluster-admin/fma-policy-admin-clusterrole.yaml).

## Instantiating the chart

Instantiate the Helm chart
`ghcr.io/llm-d-incubation/llm-d-fast-model-actuation/charts/fma-controllers`,
using your chosen `$release` (without leading "v") as the chart
version, into your chosen Kubernetes namespace. For a full listing of
the Helm chart "values" that you can set, see the chart's
[values.yaml](../charts/fma-controllers/values.yaml). Prominent among
them are the following.

- `global.imageTag` **required**. Set this to `v$release` (e.g.,
  `v0.6.3`).

- `global.imageRegistry` **optional**. If you have staged the images
  to a different OCI registry, the value here should be what replaces
  "ghcr.io/llm-d-incubation/llm-d-fast-model-actuation" in the image
  references.

- `global.nodeViewClusterRole` **optional**. The name of the
  [aforementioned](#authorization-to-read-node-objects) `ClusterRole`,
  if any is needed.

- `global.coverdataInspectorImage` **optional**. The Helm chart
  collects coverage data from the FMA controllers into a Kubernetes
  `PersistentVolume`, and creates a 0-replica `Deployment` of Pods
  that you can use to inspect and `kubectl cp` that data. If you want
  to scale that `Deployment` up from 0 to 1 without your cluster
  needing do pull a container image from Docker Hub then set this
  value to a different image to use in those utility Pods.

The following command is a very simple example.

```bash
helm install -n myns fma-example \
    ghcr.io/llm-d-incubation/llm-d-fast-model-actuation/charts/fma-controllers \
    --version 0.6.3 \
    --set global.imageTag=v0.6.3 \
    --set global.nodeViewClusterRole=node-view
```
