# Fast Model Actuation with Process Flexibility and Dual Pods

Fast model actuation involves using either or both (a) vLLM sleep/wake
and/or (b) [launcher](launcher.md) processes. Both of those make a
process a flexible platform rather than a unit of workload. This is a
conceptual mismatch for Kubernetes, and the _dual pods_ technique is a
way of bridging that mismatch --- making such flexible processes
usable in Kubernetes, llm-d in particular.

## Introduction to dual pods

Kubernetes is based on making a Pod do both of two things: (1)
describe some application workload and (b) describe some containerized
OS-level processes. The presumption is that these two things are the
same. This runs into trouble when the OS-level processes implement a
higher-level platform with its own abstraction(s) for describing
applications. The process flexibility in fast model actuation creates
such a situation.

The dual pods technique has a dichotomy between (1) the
server-requesting Pods that clients/users create to describe the
desired inference servers and (2) the server-providing Pods that
actually run the inference servers.

The dual pods technique takes advantage of something that we have
observed in practice: for nodes that run inference servers, only one
resource is constrained enough to actually affect scheduling --- the
accelerators (GPUs). CPU and main memory are always (well, almost
always) sufficient when the constraints on accelerator usage are
met. The dual pods technique also relies on the fact that we can
construct Pods that have access to all of the accelerators on their
node while being accounted --- in the Kubernetes scheduler and kubelet
--- as consuming none of them. The server-requesting Pod's resource
requirements consist of the accelerator usage of the inference server,
and a small amount of CPU and main memory. The server-providing Pod's
`.spec` has resource requirements that hold the CPU and main memory
needed to run the inference server(s), and zero accelerators. The dual
pods implementation gives to the process that actually runs vLLM an
environment variable setting that directs vLLM and the nvidia runtime
to use the accelerators that were chosen by the Kubernetes scheduler
and kubelet for the server-requesting Pod.

The server-requesting Pod:

- has a container --- which we call the _requester_ container --- that
  is part of the implementation of the dual-pods technique,

- does _not_ have a container that runs vLLM, and

- has an annotation that provides what the FMA controllers need to
  bridge from server-requesting Pod to server-providing Pod.

Kubernetes (in its Pod scheduler and kubelets) allocates and assigns
accelerators to the server-requesting Pods as normal, but the
requester container in those Pods only reports on those
assignments. The server-providing Pods adopt those assignments to
actually use the accelerators to run the inference servers.

Clients/users of the dual pods technique create and delete
server-requesting Pods roughly as they would if those Pods ran the
inference servers. There is a dual-pods controller that manages the
server-providing Pods in reaction to the server-requesting Pods, and
relays some state from the server-providing Pods back to the
server-requesting Pods.

A launcher process is _preparation_ for running vLLM, and can be
created proactively. FMA defines `LauncherPopulationPolicy` objects
that direct proactive maintenance of a population of launchers. FMA
includes a controller that does the prescribed maintenance.

The dual pods technique involves allocating some amount of each GPU's
memory to sleeping vLLM instances and the rest to awake vLLM
instances. Model variant deployers (see next section) are told how
much GPU memory their vLLM instances may use.

At present, we have only worked on the dual pods technique for pre-DRA
style resource statements. In other words, usage of
https://github.com/kubernetes/api/blob/v0.34.2/core/v1/types.go#L2847
is not yet supported.

## Personas

- **Cluster administrator**. Installs and configures nvidia
  components.

- **Model variant deployer**. Deploys a horizontally scalable set of
  server-requesting Pods for each model variant that shall be on the
  cluster. Also creates an
  [InferencePool](https://github.com/kubernetes-sigs/gateway-api-inference-extension/blob/main/api/v1/inferencepool_types.go)
  object and associated llm-d objects for each of those model
  variants. Also creates, when launchers are used, the
  `InferenceServerConfig`, `LauncherConfig`, and
  `LauncherPopulationPolicy` objects that direct FMA's construction of
  server-providing Pods.

- **llm-d administrator**. Deploys and configures llm-d on the cluster.

- **Inference client**. Outside the cluster. Submits inference
  requests, consumes responses. Judges SLO attainment.

## Scope Notes

Note: this document covers the design for both milestones 2 and 3.
Milestone 2 used vLLM sleep/wake but no launchers.
Milestone 3 involves both sleep/wake and launchers.

A deployment of FMA operates within one Kubernetes API object
namespace. The custom resources defined by FMA are namespaced.  Sadly,
`CustomResourceDefinition` objects and some others defined by
Kubernetes are cluster-scoped.

## Drawing

### Direct Providers (Milestone 2)

![Boxes and arrows illustrating the milestone 2 design here](llm-d-fma-arch-m2.drawio.svg)

### Using Launchers (Milestone 3)

![Boxes and arrows illustrating the milestone 3 design here](llm-d-fma-arch-m3.drawio.svg)

## Dual Pods API

The API is in [pkg/api](../pkg/api) and in this section.

### Direct Providers API

A server-requesting Pod that requests using only sleep/wake (no
launcher) does so by having an annotation whose name (key) is
`dual-pods.llm-d.ai/server-patch` and whose value is a patch string
(of the Kubernetes "strategic merge" type) that converts the
server-requesting Pod's `.metadata.labels` and `.spec` into those of a
_nominal_ server-providing Pod and defines the annotations of that
Pod.

The dual-pods controller further customizes the nominal
server-providing Pod to make it run on the same Node as the
server-requesting Pod and use the same accelerators.

### Launcher-based API

A server-requesting Pod that requests using a launcher as well as
sleep/wake does so by having an annotation whose name (key) is
`dual-pods.llm-d.ai/inference-server-config` and whose value is the
name of an
[`InferenceServerConfig`](../api/fma/v1alpha1/inferenceserverconfig_types.go)
object. That object provides the parameters of vLLM and also the name
of a [`LauncherConfig`](../api/fma/v1alpha1/launcherconfig_types.go)
object. That object, in turn, holds the template to use when creating
the launcher Pods.

Additionally,
[`LauncherPopulationPolicy`](../api/fma/v1alpha1/launcherpopulationpolicy_types.go)
objects call for a proactive population of launchers, as follows.

- Each `LauncherPopulationPolicy` has a node selector --- a predicate
  that tests whether that policy should apply to a given Node. This is
  implicitly applied to all Nodes to find the relevant ones.

- Each `LauncherPopulationPolicy` defines a partial map from
  `LauncherConfig` to a desired number of launcher Pods.

- All the `LauncherPopulationPolicy` objects together collectively
  define a map, called `PopulationPolicy`, from (Node, LauncherConfig)
  to count. For a given (N, C) this count is the maximum of the counts
  prescribed by the `LauncherPopulationPolicy` objects that select N
  and declare a count for C (zero if there are none).

- The collective meaning of all the `LauncherPopulationPolicy` objects
  and all the server-requesting Pods is that for a given (Node,
  LauncherConfig) the number of launchers that should exist is the
  larger of
    (a) what `PopulationPolicy` says for that pair, and
    (b) the number needed to satisfy the existing server-requesting Pods.

### User-initiated changes to relevant API objects

FMA is written in the usual Kubernetes style: it primarily reacts to
create/update/delete of the Kubernetes API objects that are its
inputs, in an eventually-consistent way. The input objects can be
created in any order; FMA will react at any given time as best it can
to the inputs that currently exist. This leaves a few more questions,
addressed in the following subsections.

#### What happens when a server-requesting Pod changes what it is requesting?

FMA uses Kubernetes admission control (a `ValidatingAdmissionPolicy`
and a `ValidatingAdmissionPolicyBinding`) to suppress such changes
while the server-requesting Pod is bound to a server-providing Pod.

#### What happens to existing launcher Pods when a LauncherConfig changes?

Each corresponding launcher Pod that is not bound and was created from
the wrong LauncherConfig spec is deleted. Then the launcher population
controller will create a replacement if that is still
appropriate. While the LauncherConfig object does not exist, FMA does
not consider any corresponding launcher to be wrong.

#### What happens to existing uses when an InferenceServerConfigSpec changes?

Each vLLM instance that is unbound and created from the wrong
InferenceServerConfigSpec contents is deleted.

#### What happens if somebody changes one of the annotations that our implementation uses to link things together?

(You may not have known that there are such things. They are not
user-serviceable parts.)

FMA uses Kubernetes admission control (a `ValidatingAdmissionPolicy`
and a `ValidatingAdmissionPolicyBinding`) to suppress such changes.

## Scenarios

The outer product of

1. (scaling)

    a. Scale out an existing non-empty set.
    b. Create a non-empty set (equivalently: Scale set out from zero).

2. (single GPU vs. not)

    a. Resource request/limit is 1 GPU
    b. Resource request/limit is multiple GPUs

3. (activation style)

    - **hot-start**: There is a sleeping vLLM instance that can be woken and used
    - **direct cold-start**: A new direct server-providing Pod must be created
    - **warm-start**: A suitable launcher exists but must create a new vLLM instance
    - **launcher cold-start**: A new launcher must be created and create a new vLLM instance

4. (vLLM instance reclamation)

    - No vLLM instances need to be deleted first
    - One vLLM instance needs to be deleted first
    - Multiple vLLM instances need to be deleted first

5. (launcher reclamation)

    - No launchers need to be deleted first
    - One launcher needs to be deleted first
    - Multiple launchers need to be deleted first

## What goes in a direct server-requesting Pod

Here is an example of a server-requesting Pod that requests a direct provider.

```yaml
apiVersion: v1
kind: Pod
metadata:
  name: example-request
  annotations:
    dual-pods.llm-d.ai/admin-port: "8082"
    dual-pods.llm-d.ai/server-patch: |
      metadata:
        labels: {
          "model-reg": "ibm-granite",
          "model-repo": "granite-3.3-2b-instruct"}
      spec:
        containers:
        - name: inference-server
          image: docker.io/vllm/vllm-openai:v0.23.0
          command:
          - vllm
          - serve
          - --port=8000
          - --model=ibm-granite/granite-3.3-2b-instruct
          - --enable-sleep-mode
          - --max-model-len=32768
          - --gpu-memory-utilization=0.8
          env:
          - name: VLLM_SERVER_DEV_MODE
            value: "1"
          - name: VLLM_CACHE_ROOT
            value: /tmp
          - name: FLASHINFER_WORKSPACE_BASE
            value: /tmp
          - name: XDG_CONFIG_HOME
            value: /tmp
          - name: XDG_CACHE_HOME
            value: /tmp
          - name: TRITON_HOME
            value: /tmp
          readinessProbe:
            httpGet:
              path: /health
              port: 8000
            initialDelaySeconds: 60
            periodSeconds: 5
          resources:
            limits:
              cpu: "2"
              memory: 9Gi
spec:
  affinity:
    nodeAffinity:
      requiredDuringSchedulingIgnoredDuringExecution:
        nodeSelectorTerms:
        - matchExpressions:
          - key: "nvidia.com/gpu.product"
            operator: In
            values: ["NVIDIA-A100-SXM4-80GB"]
  containers:
  - name: inference-server
    image: some-registry/some-namespace/requester@v0.1.0
    command:
    - /app/requester
    - --probes-port=8081
    - --spi-port=8082
    - --metrics-port=8083
    - --debug-port=8084
    readinessProbe:
      httpGet:
        path: /ready
        port: 8081
      initialDelaySeconds: 2
      periodSeconds: 5
    resources:
      limits:
        nvidia.com/gpu: "1"
        cpu: "200m"
        memory: 250Mi
```

Following are some features to note.

- The container that runs the requester must be named
  "inference-server", and this will also be the name of the container
  that runs vLLM. This simple way of identifying the relevant
  container could be changed if we found a need.

- The server patch is in strategic merge patch format.

- The Pod labels that match the right InferencePool go on the nominal
  server-providing Pod and not on the server-requesting Pod.

- When the server-requesting Pod is part of a set (e.g.: ReplicaSet,
  StatefulSet, LeaderWorkerSet), the server patch must also change the
  Pod's labels in such a way that the nominal server-providing Pod
  does not match the Pod label selector in the definition of the
  set. We want the set controller to see and manage only the
  server-requesting Pods.

- While this simple example downloads the model from HuggingFace, you
  could use other techniques. Most of the features of a Kubernetes Pod
  are available, including usage of storage volumes.

- Explicit settings of the environment variables VLLM_CACHE_ROOT,
  FLASHINFER_WORKSPACE_BASE, XDG_CONFIG_HOME, XDG_CACHE_HOME, and
  TRITON_HOME are needed when running as an unprivileged user in an
  OpenShift cluster; in this case the home directory inside the
  container is the un-writable root directory `/`.

- The server-providing Pod needs enough main memory to hold the model
  tensors that are off-loaded from the GPU memory when the vLLM
  instance goes to sleep. This off-loading and memory usage happens
  when the instance is first put to sleep, so an inadequate setting
  only causes a problem at this late time.

- The server-requesting Pod needs very little CPU and main memory. It
  needs to state the inference server's requirement for accelerators,
  so that they get allocated and assigned to the requester container
  --- even though it will not actually use them.

## The corresponding nominal server-providing Pod

Here is an example of what the dual-pods controller would derive.

```yaml
apiVersion: v1
kind: Pod
metadata:
  name: example-request-dual-xyzzy
spec:
  nodeSelector: { "kubernetes.io/hostname": "somenode" }
  containers:
  - name: inference-server
    image: docker.io/vllm/vllm-openai:v0.23.0
    command:
    - vllm
    - serve
    - --port=8000
    - --model=ibm-granite/granite-3.3-2b-instruct
    - --enable-sleep-mode
    - --max-model-len=32768
    - --gpu-memory-utilization=0.8
    env:
    - name: VLLM_SERVER_DEV_MODE
      value: "1"
    - name: VLLM_CACHE_ROOT
      value: /tmp
    - name: FLASHINFER_WORKSPACE_BASE
      value: /tmp
    - name: XDG_CONFIG_HOME
      value: /tmp
    - name: XDG_CACHE_HOME
      value: /tmp
    - name: TRITON_HOME
      value: /tmp
    - name: CUDA_VISIBLE_DEVICES
      value: "3"
    readinessProbe:
      httpGet:
        path: /health
        port: 8000
      initialDelaySeconds: 60
      periodSeconds: 5
    resources:
      limits:
        nvidia.com/gpu: "0"
        cpu: "2"
        memory: 9Gi
```

## Launcher-based Pods

Following is an example of a `ReplicaSet` of server-requesting pods.

```yaml
apiVersion: apps/v1
kind: ReplicaSet
metadata:
  name: example-requesters
  labels:
    app: dp-example
spec:
  replicas: 1
  selector:
    matchLabels:
      app: dp-example
  template:
    metadata:
      labels:
        app: dp-example
      annotations:
        dual-pods.llm-d.ai/inference-server-config: example
    spec:
      nodeSelector:
        nvidia.com/gpu.family: hopper
      containers:
        - name: inference-server
          image: ghcr.io/llm-d-incubation/llm-d-fast-model-actuation/requester:v0.6.2
          imagePullPolicy: IfNotPresent
          ports:
          - name: probes
            containerPort: 8080
          - name: spi
            containerPort: 8081
          readinessProbe:
            httpGet:
              path: /ready
              port: 8080
            initialDelaySeconds: 2
            periodSeconds: 5
          resources:
            limits:
              nvidia.com/gpu: "1"
              cpu: "200m"
              memory: 250Mi
```

Following is an example of what the referenced config object might
look like.

```yaml
apiVersion: fma.llm-d.ai/v1alpha1
kind: InferenceServerConfig
metadata:
  name: example
spec:
  modelServerConfig:
    port: 8005
    options: >-
      --model TinyLlama/TinyLlama-1.1B-Chat-v1.0
      --enable-sleep-mode
      --gpu-memory-utilization 0.85
    env_vars:
      VLLM_SERVER_DEV_MODE: "1"
      VLLM_LOGGING_LEVEL: "DEBUG"
      USER: inductor-fantasy
    labels:
      e2e-test.fma.llm-d.ai/isc-label: test-value
    annotations:
      e2e-test.fma.llm-d.ai/isc-annotation: test-value
  launcherConfigName: example
```

The capping of the GPU memory utilization at 0.85 leaves room for a
few vLLM instances that are sleeping. Note that the default is 0.9,
deliberately leaving room because vLLM's control over its GPU memory
usage is imprecise. To calculate the right utilization cap for your
usage, measure the amount of GPU memory that remains in use while the
GPU has one sleeping vLLM instance and no awake one. Pick a number of
sleeping vLLM instances that you want to allow to coexist with one
awake instance. Multiply by the amount of GPU memory that each
sleeping instance takes, and convert to a fraction of the GPU's total
memory. Subtract that from 0.95, and you will probably be good.

The configuration objects exhibited here and the FMA container images
have been engineered to be easily usable on OpenShift without special
security-context privileges. In particular, no particular userid or
username is specified anywhere that OpenShift can see it.

The setting of the environment variable "USER" to some value (it does
not really matter what the value is, as long as it is syntactically
valid) is necessary to cope with overzealous caching by the
Inductor. That thing insists on having a distinct cache for every
user, and insists on determining the user name. Most of the cache
configuration is in the `LauncherConfig` (see next), but the `USER`
hack is buried here so that it does not confuse OpenShift or the OS.

Following is an example of what a launcher config object looks like.

```yaml
apiVersion: fma.llm-d.ai/v1alpha1
kind: LauncherConfig
metadata:
  name: example
spec:
  maxInstances: 2
  podTemplate:
    metadata:
      labels:
        example.com/template-label: example-from-lc
      annotations:
        example.com/template-annotation: Example from LC
    spec:
      serviceAccountName: example
      containers:
        - name: inference-server
          image: ghcr.io/llm-d-incubation/llm-d-fast-model-actuation/launcher:v0.6.2
          imagePullPolicy: IfNotPresent
          command:
          - /app/launcher.py
          - --log-level=info
          env:
          - name: HF_HOME
            value: "/tmp"
          - name: VLLM_CACHE_ROOT
            value: "/tmp"
          - name: FLASHINFER_WORKSPACE_BASE
            value: "/tmp"
          - name: TRITON_CACHE_DIR
            value: "/tmp"
          - name: XDG_CACHE_HOME
            value: "/tmp"
          - name: XDG_CONFIG_HOME
            value: "/tmp"
          resources:
            limits:
              ephemeral-storage: "4.5Gi"
```

The settings of various environment variables to refer to `/tmp`
configures the locations of the various caches that vLLM maintains on
the filesystem.

TODO: exmplain the ephemeral-storage setting.

Following is an example launcher population policy object.

```yaml
apiVersion: fma.llm-d.ai/v1alpha1
kind: LauncherPopulationPolicy
metadata:
  name: example
spec:
  enhancedNodeSelector:
    labelSelector:
      matchLabels:
        nvidia.com/gpu.family: hopper
  countForLauncher:
    - launcherConfigName: example
      launcherCount: 8
```

In this example the desired number of launchers is 8, which is what
you would typically use when the nodes have 8 GPUs.

## Observability

### FYI labels and annotations

The FMA controllers put some labels and annotations on Pods purely for
the sake of observability; these labels and annotations are not
consumed by the FMA controllers. They are as follows.

- annotation **dual-pods.llm-d.ai/accelerators**. Put on both
  requester and provider Pods. Value is a comma-separated list of GPU
  UUIDs.

- label **dual-pods.llm-d.ai/dual**. Put on both requester and
  provider Pods. Value is the name of the dual Pod.

- label **dual-pods.llm-d.ai/sleeping**. Maintained on the provider
  Pod, reflecting what the controller knows about the sleep/wake state
  of the vLLM instance(s) there. The value is "true" or "false",
  despite there being a relevant third state: starting up. In the case
  of a launcher: the value is "false" when the controller knows that
  there is an awake (and ready to serve inference requests) vLLM
  instance, "true" otherwise.

- label **dual-pods.llm-d.ai/instance**. Appears on a requester Pod
  while it is bound to a launcher. Value is the ID that FMA uses to
  distinguish the relevant vLLM instance from others in the same
  launcher.

### Kubernetes Event objects

TODO: write this

### Prometheus metrics

TODO: write this

## Notes on the dual-pods controller

The dual-pods controller follows the usual style for a Go-based
Kubernetes controller: informers and a workqueue. To minimize
chattiness, the controller does all of its reading through the
informers.

The central item of interest in the controller is an inference server,
identified by the UID of its server-requesting Pod.

The binding between inference server and server-providing Pod is
stored, first and foremost, in an annotation on the server-providing
Pod. It is critical that the authoritative record of this relationship
be in an ACID database. This design uses the Kubernetes API service,
which can do an atomic transaction on only a single Kubernetes API
object. Secondarily, the dual-pods controller's informer on Pods
maintains an index that allows looking up the server-providing Pod(s)
(the controller intends there to be at most one!) that are bound to a
given inference server.

A given server-requesting Pod is bound to a server-providing Pod at
most once. If trouble develops this may signal a condition that the
client/user may need to be aware of and so is reflected back by the
controller deleting the server-requesting Pod.

The controller maintains a finalizer on the server-requesting Pod, so
that its deletion can be delayed until the server-providing Pod is
asleep or gone. This is to stop higher level processes that monitor
only the server-requesting Pod from assuming that the inference server
is gone before it actually is (gone or asleep).

An _exogenous_ deletion of the server-providing Pod is one initiated
by something other than the dual-pods controller. This is unusual but
can happen, typically because some resource contention has led to the
Pod's eviction. In this case the dual-pods controller relays that to
become deletion of the server-requesting Pod (removing the
controller's own finalizer there, of course). The controller remembers
in its internal data for the inference server that it has initiated
deletion of this Pod, to avoid redundant or incorrect work in
subsequent syncs before the deletion has been reflected into the Pod
informer's local cache.

The dual-pods controller maintains a finalizer on the server-providing
Pod while it is bound to a server-requesting Pod, so that the
controller can tell the difference between two states: (1) the
server-requesting Pod exists but no server-providing Pod has ever been
bound, and (2) there was a bound server-providing Pod but it was
deleted while the controller was not running. The controller needs to
treat these two states differently, relaying the deletion of (2) back
to the server-requesting Pod but not deleting the server-requesting
Pod in state (1).

Controlling sleep/wake is secondary to binding. An unbound
server-providing Pod is asleep. The transitions between asleep and
awake happen while bound: wake up ASAP after binding, go to sleep just
before unbinding.

When both server-requesting and server-providing Pods exist and are
not in the process of being deleted, the controller will relay
readiness from the provider to the requester if it has not already
done so for the current state.

Just before creating a new server-providing Pod, the controller
enforces the budget on accelerator memory for sleeping vLLM instances
(see below).

TODO: remaining updates for milestone 3

When making a new vLLM instance: the Kubernetes scheduler and kubelet
have already assured that there is no other server-requesting Pod
using any of those accelerators, and the behavior of this controller
means that consequently there is no awake vLLM instance using any of
those accelerators. The controller creates the new vLLM instance by
creating a new server-providing Pod. This Pod uses the
CUDA_VISIBLE_DEVICES environment variable to convey the assigned set
of accelerators.

### Respecting the accelerator memory budget

At present the dual-pods controller uses a simple approximation: a
limit on the number of sleeping vLLM instances, which must be obeyed
whenever there is an awake vLLM instance (using the same GPU). In our
experience the amount of GPU memory used by a sleeping vLLM instance
does not vary very much. Multiplying the observed upper limit, for a
given type of GPU, by limit on number of sleeping vLLM instances gives
the GPU memory reserved for sleeping instances.

The controller enforces the limit on the number of sleeping vLLM
instances just before creating a new instance. The enforcement
consists of deleting instances as needed, least recently used
first. This very simple policy is our starting point. More complex
policies could be developed, based on observed need.

### Readiness Relay

The relay of readiness goes as follows.

- The requester container in the server-requesting pod can be sent an
  HTTP POST request (see [the SPI](../pkg/spi/interface.go)) that
  conveys the boolean value for readiness of the real inference server
  container.

- When dual-pods controller knows that the server-providing Pod is
  ready (as reported through an informer on those Pods), the
  controller tells the requester that the inference server is ready
  (if the controller has not already done so).

- When dual-pods controller knows that the server-providing Pod is not
  ready (as reported through an informer on those Pods), the
  controller tells the requester that the inference server is not
  ready (if the controller has not already done so).

### GPU UUID vs. Index

The GPU assignment query from the dual-pods controller to the
server-requesting Pod returns a list of GPU UUIDs.

When the server-providing Pod is a launcher (milestone 3), the
launcher reads the UUIDs of the GPUs on its node, and the request to
launch a vLLM instance carries the list of assigned GPU UUIDs. The
launcher translates from UUID to index and puts the list of indices in
the vLLM container's CUDA_VISIBLE_DEVICES.

In the direct provider case (milestone 2), the dual-pods controller
translates the GPU UUID list to a list of GPU indices to put in the
CUDA_VISIBLE_DEVICES envar of the server-providing Pod. To support
that translation, we use a ConfigMap named "gpu-map". There is [a
script](../scripts/ensure-nodes-mapped.sh) that ensures that the
ConfigMap is populated with the needed information. The dual-pods
controller reads the mapping from GPU UUID to index from that
ConfigMap.
