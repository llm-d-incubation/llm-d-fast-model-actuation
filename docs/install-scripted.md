# Installing FMA

This document is about scripted installation of FMA.  There are also
documents for [step-by-step installation](install-step-by-step.md) and
[step-by-step removal](uninstall-step-by-step.md).

The install script can install either (a) a given release without
requiring a `git clone` or (b) whatever version is in the local Git
working tree of this repo. Regardless of (a) vs. (b), the version of
FMA being installed must include PR 700; the first release of
sufficient vintage will be 0.6.5-alpha.1.  For case (b), you must have
already built the container images and made them available to the
target Kubernetes cluster. For more on dev/test of a general commit of
the repo, see [README.md#devtest](README.md#devtest).

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

Scripted installation is done by invoking one script. You can invoke
it in curl-to-bash style (example below).

FMA has [a Helm chart](../charts/fma-controllers) that covers all of
the namespace-scoped objects involved in deploying FMA. Instantiation
of that chart is included in the installation script.

That chart deliberately excludes the cluster-scoped objects, for two
reasons: (1) such an object can not be owned by both of two different
users' Helm chart instances and (2) users are not necessarily
authorized to create/update/delete such objects. When the user is not
authorized to create the object then they will have to ask an
administrator to create it for them.

Because of those difficulties, installation of the cluster-scoped
objects is deliberately optional. The script takes a command-line
option for each of the categories of cluster-scoped object. These
categories are discussed [below](#cluster-scoped-objects).

## Prerequisites

To install FMA in a given Kubernetes cluster you will need the
following tools on the machine doing the installation.

- **kubectl**, version 1.32 or later.

- [**helm**](https://github.com/helm/helm), version 3 or 4.

- [**yq**](https://github.com/mikefarah/yq), version 4 or later

- [**jq**](https://github.com/jqlang/jq), version 1.6 or later

- **curl**

- **bash**

In the cluster you will need a namespace in which you have at least
ordinary Kubernetes user authorizations.

You will need your cluster and installation machine to have on-line
access to the OCI registry at `ghcr.io`, or another registry where you
have staged copies of the relevant OCI repositories. Following are the
repositories to stage (if you are doing that).

- `ghcr.io/llm-d-incubation/llm-d-fast-model-actuation/charts/fma-controllers`
- `ghcr.io/llm-d-incubation/llm-d-fast-model-actuation/requester`
- `ghcr.io/llm-d-incubation/llm-d-fast-model-actuation/dual-pods-controller`
- `ghcr.io/llm-d-incubation/llm-d-fast-model-actuation/launcher-populator`

## Cluster-scoped objects

The following subsections describe the various categories of
cluster-scoped objects involved in installing FMA.

### Authorization to read Node objects

The FMA controllers need authorization to get/list/watch Node
objects. Normal Kubernetes users are not authorized to do that.
However, some clusters have already been altered so that any
authorized user (including ServiceAccounts) has this authorization.
If your cluster has not been altered in this way then it is necessary
to create --- or have an administrator create for you --- a
`ClusterRole` that can be bound to the `ServiceAccount` that the FMA
controllers will use and gives this authorization.

### FMA CRDs

FMA defines three [Kubernetes custom
resources](https://v1-34.docs.kubernetes.io/docs/concepts/extend-kubernetes/api-extension/custom-resources/)
using [`CustomResourceDefinition`
objects](https://v1-34.docs.kubernetes.io/docs/tasks/extend-kubernetes/custom-resources/custom-resource-definitions/). The
definition objects are cluster scoped, so you need either: (a) someone
else to have already installed these definitions, (b) authorization
for the installer to install them for you, or (c) to get an
administrator to install them for you. In the last case the
administrator will want to know the following.  The CRD object names
are as follows; each one has the form `$resourcename.$apigroup`.

- `inferenceserverconfigs.fma.llm-d.ai`
- `launcherconfigs.fma.llm-d.ai`
- `launcherpopulationpolicies.fma.llm-d.ai`

The YAML for the `CustomResourceDefinition` objects that define FMA's
custom resources is in [config/crds.yaml](../config/crds.yaml) and can
be referenced directly at GitHub. For example: for release
"0.6.5-alpha.1", this YAML will be available at
`https://raw.githubusercontent.com/llm-d-incubation/llm-d-fast-model-actuation/refs/tags/v0.6.5-alpha.1/config/crds.yaml`.

In case you ask to become authorized to create those objects, there is
a handy YAML file for a `ClusterRole` object that grants this
authority in
[config/fma-cluster-admin/fma-policy-admin-clusterrole.yaml](../config/fma-cluster-admin/fma-crd-admin-clusterrole.yaml).

### Admission policies

FMA uses two `ValidatingAdmissionPolicy` objects and two corresponding
`ValidatingAdmissionPolicyBinding` objects to prevent undesired object
modifications. These objects are cluster scoped, so you need either:
(a) someone else to have already installed these definitions, (b)
authorization for the installer to install them for you, or (c) to get
an administrator to install them for you. In the last case the
administrator will want to know the following. YAML for all four is
found in
[config/validating-admission-policies.yaml](../config/validating-admission-policies.yaml)
and can be referenced directly at GitHub. For example: for release
"0.6.5-alpha.1" this YAML will be at
`https://raw.githubusercontent.com/llm-d-incubation/llm-d-fast-model-actuation/refs/tags/v0.6.5-alpha.1/config/validating-admission-policies.yaml`.

In case you ask to become authorized to create those objects, there is
a handy YAML file for a `ClusterRole` object that grants this
authority in
[config/fma-cluster-admin/fma-policy-admin-clusterrole.yaml](../config/fma-cluster-admin/fma-policy-admin-clusterrole.yaml).

## Staging elsewhere

By default the installer will read (and tell Kubernetes to read)
content from GitHub.com and its OCI registry at ghcr.io. You have the
option to stage the necessary content elsewhere and make the installer
get the content from there.

The relevant OCI images are discussed [above](#prerequisites).

The two files that the installer reads, `crds.yaml` and
`validating-admission-policies.yaml` can be staged to a filesystem
directory that the installer reads from.

## Using the installer script

The install script is at
[scripts/install-fma.sh](../scripts/install-fma.sh). Following is a
synopsis of using it in a curl-to-bash fashion.

```shell
bash <(curl https://raw.githubusercontent.com/llm-d-incubation/llm-d-fast-model-actuation/refs/tags/v$release/scripts/install-fma.sh) OPTIONS
```

You must use exactly one of the following two cases.

1. Specify a release to install (using the option `--release`),
   identified by [semantic version](https://semver.org) without
   leading "v". In this case you may also specify `--oci-registry` if
   you have staged the release's OCI images there.

2. Specify both `--image-tag` and `--oci-registry`, which means to
   install from the Git local working tree under the assumption that
   the container images have been built and are available in the
   target Kubernetes cluster using the values specified here.

The `OPTIONS` are as follows.

- `--release $semver` identifies the release to install. No leading
  "v". This is exclusive with `--image-tag`.

- `--image-tag $imgtag` identifies the container image tag to use when
  constructing references to the images. This is exclusive with
  `--release`.

- `--namespace $nsname` identifies the Kubernetes API object namespace
  to install FMA into. Defaults to the current namespace.

- `--existing-node-view-cluster-role $crname` identifies a
  pre-existing `ClusterRole` that grants authorization to
  get/list/watch `Node` objects. This is exclusive with
  `--ensure-node-view-cluster-role`; if neither appears then the
  installer assumes that some other arrangement has already been made
  that authorizes the FMA controllers to get/list/watch `Node`
  objects.

- `--ensure-node-view-cluster-role $crname` instructs the installer to
  ensure that there exists a `ClusterRole` object with the given name
  and at least the authority to get/list/watch `Node` objects, and to
  use it. This is exclusive with `--existing-node-view-cluster-role`;
  if neither appears then the installer assumes that some other
  arrangement has already been made that authorizes the FMA
  controllers to get/list/watch `Node` objects.

- `--install-crds $trueorfalse` tells the installer whether to install
  the CRD objects. The default is `false`.

- `--install-admission-policies $trueorfalse` tells the installer
  whether to install the admission policy objects. The default is
  `false`.

- `--oci-registry $reg` defines the OCI registry and namespace to use
  in place of "ghcr.io/llm-d-incubation/llm-d-fast-model-actuation".
  Required when `--image-tag` is also specified.

- `--config-dir $pathname` defines the directory from which to read
  `crds.yaml` and/or `validating-admission-policies.yaml`. When
  installing a release, the default is to read them from the right
  release content on GitHub.com. When installing from the local Git
  working tree, the default is "config".

- `--chart-set $path=$val` is for setting any of the other "values" of
  the FMA Helm chart. This uses the same syntax and semantics as in
  the `--set $path=$val` option on `helm install`. May be repeated.

- `--chart-instance-name $name` is the name to use for the FMA Helm
  chart instance. Defaults to "fma".
