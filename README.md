The llm-d-fast-model-actuation repository contains work on one of the
many areas of work that contribute to fast model actuation. This area
concerns exploiting techniques in which an inference server process
dramatically changes its properties and behavior over time.

There are two sorts of changes contemplated here.

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
reducing that startup latency.

A process with such flexibility does not easily fit into the
Kubernetes milieu. The most obvious and natural way in Kubernetes to
define a desired inference server is to create a `Pod`
object. However, a `Pod` has a static allocation of accelerator
resources and a static command line. That is, the obvious way to
define a `Pod` is such that it serves one fixed model and server
options, with no resource-freeing hiatus. This repository contains
way(s) of fitting the process flexibility into the Kubernetes milieu.

The topics above are divided into subdirectories of this repo as follows.

- [inference_server](inference_server) is about a particular model
  swapping technique. This technique's management process is developed
  here and uses vLLM without changing it.

- [dual-pods](dual-pods) is about one technique for fitting process
  flexilibility into the Kuberntes milieu. In this techique, clients
  and users create single-purpose `Pod` objects (called
  "server-requesting Pods") in the usual Kubernetes way. Behind the
  scenes, there are other `Pod` objects (called "server-running Pods")
  that run the flexible processes. A new controller manages the
  server-running Pods to implement the behavior specified by the
  server-requesting Pods.
