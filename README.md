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
the OS is concerned. The process of waking up the sleeping instance is
very fast; for example, taking about 3 seconds for a model with 64 GiB
of tensor data. This behavior is available in vLLM today.

2. Model swapping. In model swapping techniques, there is a persistent
management process that can run various subsidiary inference server
processes over time. The management process does basic code loading
and initialization work of the inference server so that this work does
not have to be done at the startup of the inference server process,
reducing that startup latency. The inference servers may be able to
sleep and wake up.

A process with such flexibility does not easily fit into the
Kubernetes milieu. The most obvious and natural way in Kubernetes to
define a desired inference server is to create a `Pod`
object. However, a `Pod` has a static allocation of accelerator
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

- A **launcher-populator** controller, which watches LauncherConfig
  and LauncherPopulationPolicy custom resources and ensures that the
  right number of launcher pods exist on each node. This controller is
  also written in Go.

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

The development roadmap has three milestones. Milestone 2, which
introduced vLLM sleep/wake without the launcher, is finished.
Milestone 3, which adds launcher-based model swapping where a
persistent launcher process manages vLLM instances on each node, is
under implementation.

For further design documentation, see [the docs
directory](docs/README.md).
