The llm-d-fast-model-actuation repository is part of the
[llm-d](https://github.com/llm-d) ecosystem for serving large
language models on Kubernetes. FMA lives in the
[llm-d-incubation](https://github.com/llm-d-incubation) organization,
where new llm-d components are developed before graduation.

This repository contains work on one of the many areas of work that
contribute to fast model actuation. This area concerns exploiting
techniques in which an inference server process dramatically changes
its properties and behavior over time.

There are two sorts of changes contemplated here. Both are currently
realized only for vLLM and nvidia's GPU operator, but we hope that
these ideas can generalize.

1. vLLM level 1 sleep and wake_up. A vLLM instance in level 1 sleep
has its model tensors in main (CPU) memory rather than accelerator
(GPU) memory. While in this state, this instance can not serve
inference requests --- but has freed up accelerator resources for use
by a different instance. But the sleeping instance is still a running
process (e.g., it can still serve administrative requests) as far as
the OS is concerned. And that process is still the main process of a
container in a Pod, as far as Kubernetes is concerned. The process of
waking up the sleeping instance is very fast; for example, taking
about 3 seconds for a model with 64 GiB of tensor data. This behavior
is available in vLLM today.

2. A vllm launcher process. This is a process that can have multiple
child processes, each running a distinct vllm instance (which is
itself the usual parent/child pair of main and engine). The launcher
loads the Python modules, shaving the time for that off of the startup
of each launched vllm instance. The launcher has a network API for
CRUDL of vllm instances.

By reducing the startup latency of vllm instances, these ideas reduce
the latency to change which model a given accelerator (or array of
accelerators) is being used for. We sometimes use the term "model
swapping" to evoke this idea.

A process with sleep/wake and/or launcher functionality does not
easily fit into the Kubernetes milieu. The most obvious and natural
way in Kubernetes to define a desired inference server is to create a
`Pod` object. However, a `Pod` has a static allocation of accelerator
resources and a static command line. That is, the obvious way to
define a `Pod` is such that it serves one fixed model and server
options, with no resource-freeing hiatus. This repository contains a
way of fitting the process flexibility into the Kubernetes milieu. We
call this technique "dual pods". It makes a distinction between (a) a
_server-requesting Pod_, which describes a desired inference server
but does not actually run it, and (b) a _server-providing Pod_, which
actually runs the inference server(s).

The topics above are realized by the following software components.

- A **dual-pods controller**, which manages the server-providing Pods
  in reaction to the server-requesting Pods that other manager(s)
  create and delete. This controller is written in the Go programming
  language and this repository's contents follow the usual conventions
  for one containing Go code.

- A **vLLM instance launcher**, the persistent management process
  mentioned above. This is written in Python and the source code is in
  the [inference_server/launcher](inference_server/launcher)
  directory.

- A **launcher-population controller** (also called the **launcher
  populator** or simply the **populator**), which watches
  LauncherConfig and LauncherPopulationPolicy objects and ensures that
  the right number of launcher pods exist on each node. This
  controller is also written in Go.

These controllers are deployed together via a unified Helm chart at
[charts/fma-controllers](charts/fma-controllers). The chart also
installs the shared RBAC resources and optional ValidatingAdmissionPolicies.

The repository defines three Custom Resource Definitions (CRDs):

- **InferenceServerConfig** — declares the properties of an inference
  server (image, command, resources) that server-providing Pods use.
- **LauncherConfig** — declares the configuration for a launcher
  process (image, resources, ports) that manages vLLM instances.
- **LauncherPopulationPolicy** — declares the desired population of
  launcher pods per node.

These CRD definitions live in [config/crd](config/crd) and the Go
types are in [pkg/api](pkg/api).

The development roadmap started with three milestones and then shifts
to more nuanced "releases". Milestone 2, which introduced vLLM
sleep/wake without the launcher, is finished.  Milestone 3 (AKA minor
release 0.6), which adds the launcher, is essentially finished; we are
finding and fixing bugs in a series of patch releases. The next major
step is minor release 0.7, in which we expect to introduce a TCP
reverse-proxy in the requester Pod, to make it an even better
representative of the inference server.

For further design documentation, see [the docs
directory](docs/README.md).
