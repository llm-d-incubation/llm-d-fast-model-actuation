# Fast Model Actuation with Process Flexibility and Dual Pods

Model flexibility refers to using vLLM sleep/wake and/or model
swapping (e.g., [launcher based model-swapping](launcher.md)).  Dual
pods is a technique for making model flexibility usable in the
Kubernetes milieu.

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

The dual pods technnique takes advantage of something that we have
observed: for nodes that run inference servers, just one resource
determines what Pods can run, when and where: the accelerators (GPUs);
CPU and main memory are always sufficient when the constraints on
accelerator usage are met. The dual pods technique also relies on the
fact that we can construct Pods that have access to all of the
accelerators on their node while being accounted --- in the Kubernetes
scheduler and kubelet --- as consuming none of them. The
server-requesting Pod's resource requirements consist of the
accelerator usage of the inference server, and a small amount of CPU
and main memory. The server-providing Pod's `.spec` has resource
requirements that hold the CPU and main memory needed to run the
inference server, and zero accelerators. The container that actually
runs vLLM has an environment variable setting that directs vLLM to use
the accelerators that were chosen by the Kubernetes scheduler and
kubelet for the server-requesting Pod.

The server-requesting Pod (a) has a container --- described as the
_requester_ container --- that is part of the implementation of the
dual-pods technique, (b) does _not_ have a container that runs vLLM,
and (c) has an annotation that contains a patch that, roughly
speaking (there is a precise statement below), changes the
server-requesting Pod into the server-providing Pod.

Kubernetes (in its scheduler and kubelets) allocates and assigns
accelerators to the server-requesting Pods as normal, but the
requester container in those Pods only reports on those
assignments. The server-providing Pods adopt those assignments to
actually use the accelerators to run the inference servers.

Clients/users of the dual pods technique create and delete
server-requesting Pods roughly as they would if those Pods ran the
inference servers. There is a dual pods controller that manages the
server-providing Pods in reaction to the server-requesting Pods, and
relays some state from the server-providing Pods back to the
server-requesting Pods.

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
  variants.

- **llm-d administrator**. Deploys and configures llm-d on the cluster.

- **Inference client**. Outside the cluster. Submits inference
  requests, consumes responses. Judges SLO attainment.

## Design

Note: this document currently focuses on the design for the second of
three milestones.

Defining limitation of milestone 2: No use of the launcher. Each
server-providing Pod runs just one vLLM instance.

### Drawing

#### Milestone 2

![Boxes and arrows illustrating the milestone 2 design here](llm-d-fma-arch-m2.drawio.svg)

#### Milestone 3

![Boxes and arrows illustrating the milestone 3 design here](llm-d-fma-arch-m3.drawio.svg)

### Dual Pods API

We defined an API that is hoped to work for both milestone 2 and
milestone 3. That API is in [pkg/api](../pkg/api) and in this
section. The patch defined in the server-providing Pod converts the
server-requesting Pod's `.metadata.labels` and `.spec` into those of a
_nominal_ server-providing Pod and defines the annotations of that
Pod. This nominal server-providing Pod could satisfactorily run the
inference server. However, the dual-pods controller is allowed to
create different sorts of server-providing Pods that, ultimately, run
inference servers according to the nominal server-providing Pod.

The dual-pods controller that works with just the existing sleep/wake
functionality concludes that to create a server-providing Pod for a
particular model, it uses the nominal server-providing PodSpec,
labels, and annotations directly. A dual-pods controller that works
with the launcher-based model swapping will create a server-providing
Pod that runs the launcher. To swap a model in, the controller issues
a request (to the launcher) that includes the command line arguments,
environment variable settings, and assigned accelerator set (the ones
assigned to the server-requesting Pod) for running `vllm serve`. To
swap a model out, the controller issues a request that does not
include those details.

### Scenarios

The outer product of

1. (scaling)

    a. Scale out an existing set. Note: this is not something with an
      SLO? Or do we evaluate this as a matter of shifting from failing
      SLO to passing (due to added capacity)?
    b. Scale set out from zero

2. (single GPU vs. not)

    a. Resource request/limit is 1 GPU
    b. Resource request/limit is multiple GPUs

3. (wake or not)

    a. There is a sleeping vLLM instance that can be woken and used
    b. A new vLLM instance must be created; in milestone 3 this further divides into:
        i. An existing launcher can be used
        ii. A new launcher has to be created

4. (resource reclamation)

    a. No vLLM instances need to be deleted first
    b. One vLLM instance needs to be deleted first
    c. Multiple vLLM instances need to be deleted first (can only
       happen when multiple GPUs are needed for the new vLLM instance)

### What goes in a server-requesting Pod

Here is an example.

```yaml
apiVersion: v1
kind: Pod
metadata:
  name: example-request
  annotations:
    dual-pod.llm-d.ai/admin-port: "8082"
    dual-pod.llm-d.ai/server-patch: |
      metadata:
        labels: {
          "model-reg": "ibm-granite",
          "model-repo": "granite-3.3-2b-instruct"}
      spec:
        containers:
        - name: inference-server
          image: docker.io/vllm/vllm-openai@v0.10.2
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

### The corresponding nominal server-providing Pod

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
    image: docker.io/vllm/vllm-openai@v0.10.2
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

### The dual-pods controller data and logic

The dual-pods controller works entirely within the confines of one
given Kubernetes API object namespace.

The dual-pods controller follows the usual style for a Go-based
Kubernetes controller: informers and a workqueue. To minimize
chattiness, the controller does all of its reading through the
informers.

The central item of interest in the controller is an inference server,
identified by the UID of its server-requesting Pod.

This controller's workqueue is hierarchical. An entry may refer to the
`gpu-map` ConfigMap or to a Node. The controller's internal data for
a Node consists of:

- a map from inference server ID to internal data about that inference
  server; and
- a set of references to inference servers on that node that need work.

The reason for the hierarchy is as follows. Recall that a workqueue
excludes concurrent work on a given object reference in the queue. For
a given Node, the controller works on just one inference server at a
time. That is because enforcing the budget on GPU memory for sleeping
vLLM instances may involve processing any or all of the sleeping
inference servers on the same node.

The binding between inference server and server-providing Pod is
stored, first and foremost, in an annotation on the server-providing
Pod. It is critical that the authoritative record of this relationship
be in an ACID database. This design uses the Kubernetes API service,
which can do an atomic transaction on only a single Kubernetes API
object. Secondarily, the dual-pods controller's informer on Pods
maintains an index that allows looking up the server-providing Pod(s)
(the controller intends there to be at most one!) that are bound to a
given inference server.

The controller syncs a given inference server according to the
following principles.

It is only when the server-requesting Pod does not exist and there is
no bound server-providing Pod that the controller can forget about a
given inference server.

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

When the server-requesting Pod is absent or in the process of being
deleted, and there is a bound server-providing pod, the controller
unbinds the server-providing Pod. The controller first considers
whether the server-providing Pod appears to be broken; if so then the
controller initiates deletion of the server-providing Pod.

Unbinding the server-providing Pod consists of the following. If the
controller knows an IP address for that Pod and does not know that it
is asleep then the controller will make the HTTP request that puts the
Pod to sleep. The controller updates the Pod object to remove the
controller's finalizer and the annotation that declares the binding.

Apart from the above relaying of deletions, the controller does no
more work if the Node is absent or in the process of deletion.

Once the server-requesting Pod has an IP address, the controller can
do the HTTP request to the requester container to retrieve the list of
assigned GPU UUIDs. Once this is successfully done, the controller
remembers it and does not repeat it.

Controlling sleep/wake is secondary to binding. An unbound
server-providing Pod is asleep. The transitions between asleep and
awake happen while bound: wake up ASAP after binding, go to sleep just
before unbinding.

When both server-requesting and server-providing Pods exist and are
not in the process of being deleted, the controller will relay
readiness from the provider to the requester if it has not already
done so for the current state.

There is a possible timing splinter, where a server-requesting Pod
gets scheduled to a Node and starts running but then the Node becomes
unschedulable before a server-providing can be created and scheduled
there. In this case the dual-pods controller initiates deletion of the
server-requesting Pod, because it is stuck.

The controller's Pod informer maintains an index on Pods by a hash of
characteristics of the nominal server-providing Pod. These
characteristics include all that are relevant to the question of
whether this Pod, if it has a sleeping vLLM instance, would be correct
to wake and bind to a given server-requesting Pod. When the controller
is faced with the question of whether there is a suitable sleeping
vLLM instance to be woken and bound, this index provides the answer.

If there is an unsatisfied server-requesting Pod, and no suitable
sleeping vLLM instance to wake up and bind, then it is time to create
a new server-providing Pod.

Just before creating a new server-providing Pod, the controller
enforces the budget on accelerator memory for sleeping vLLM instances
(see below).

When making a new vLLM instance: the Kubernetes scheduler and kubelet
have already assured that there is no other server-requesting Pod
using any of those accelerators, and the behavior of this controller
means that consequently there is no awake vLLM instance using any of
those accelerators. The controller creates the new vLLM instance by
creating a new server-providing Pod. This Pod uses the
CUDA_VISIBLE_DEVICES environment variable to convey the assigned set
of accelerators.

#### Respecting the accelerator memory budget

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

#### Readiness Relay

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

#### GPU UUID vs. Index

The GPU assignment query from the dual-pods controller to the
server-requesting Pod returns a list of GPU UUIDs. The controller
translates this to a list of GPU indices to put in the
CUDA_VISIBLE_DEVICES envar of the server-providing Pod. To support
that translation, we use a ConfigMap named "gpu-map". There is [a
script](../scripts/ensure-nodes-mapped.sh) that ensures that the
ConfigMap is populated with the needed information. The dual-pods
controller reads the mapping from GPU UUID to index from that
ConfigMap.

This will change in milestone 3. The launcher will read the
UUIDs of the GPUs on its node, and the request to launch a vLLM
instance will carry the list of assigned GPU UUIDs. The launcher will
translate from UUID to index and put the list of indices in the vLLM
container's CUDA_VISIBLE_DEVICES.
