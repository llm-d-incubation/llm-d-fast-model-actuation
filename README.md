The llm-d-fast-model-actuation repository contains work on one of the
many areas of work that contribute to fast model actuation. This area
concerns exploiting techniques in which an inference server process
dramatically changes its properties and behavior over time.

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

The topics above are realized by two software components, as follows.

- A vLLM instance launcher, the persistent management process
  mentioned above. This is written in Python and the source code is in
  the [inference_server/launcher](inference_server/launcher)
  directory.

- A "dual-pods" controller, which manages the server-providing Pods
  in reaction to the server-requesting Pods that other manager(s)
  create and delete. This controller is written in the Go programming
  language and this repository's contents follow the usual conventions
  for one containing Go code.

We are currently in the midst of a development roadmap with three
milestones. We are currently polishing off milestone 2, which involves
using vLLM sleep/wake but not the launcher. The final milestone, 3,
adds the use of the launcher.

**NOTE**: we are in the midst of a terminology shift, from
  "server-running Pod" to "server-providing Pod".

For further design documentation, see [the docs
directory](docs/README.md).
