# Fast Model Actuation with Process Flexibility and Dual Pods

## Introduction

Model flexibility refers to using vLLM sleep/wake and model swapping.
Dual pods is a technique for making model flexibility usable in the
Kubernetes milieu.

Kubernetes is based on making a Pod do both of two things: (1)
describe some application workload and (b) describe some containerized
OS-level processes. The presumption is that these two things are the
same. This runs into trouble when the OS-level processes implement a
higher-level platform with its own abstraction(s) for describing
applications. The process flexibility in fast model actuation creates
such a situation.

The dual-pods technique has a dichotomy between (1) the
server-requesting Pods that clients/users create to describe the
desired inference servers and (2) the server-providing Pods that
actually run the inference servers.

The server-requesting Pod (a) has a container --- described as the
_requester_ container --- that is part of the implementation of the
dual-pods technique, (b) does _not_ have a container that runs vLLM,
and (c) has an annotation that contains a patch that changes the Pod's
labels and spec to those for actually running vLLM and not running the
requester container.

Kubernetes (in its scheduler and kubelets) allocates and assigns
accelerators to the server-requesting Pods as normal, but the
requester container in those Pods only reports on those
assignments. The server-providing Pods adopt those assignments to
actually use the accelerators to run the inference servers.

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
environment variable settings, and assigned accelerator set for
running `vllm serve`. To swap a model out, the controller issues a
request that does not include those details.

## Personas

- **Cluster administrator**. Installs and configures nvidia
  components.

- **Model variant deployer**. Deploys a horizontally scalable set of
  server-requesting Pods for each model variant that shall be on the
  cluster. Also creates an
  [InferencePool](https://github.com/kubernetes-sigs/gateway-api-inference-extension/blob/main/api/v1/inferencepool_types.go)
  object for each of those model variants.

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
  that runs vLLM. (TODO: really doc the interface of the requester.)

- The server patch is in strategic merge patch format.

- The Pod labels that match the right InferencePool go on the
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

- The server-requesting Pod needs very little CPU and main
  memory. However, it needs the accelerators.

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

The mutable internal state of the controller includes the following.

- A set of existing vLLM instances. Each runs a particular model, with
  other command line parameters and some environment variable
  settings. Each instance is in a Pod, on one Node, and uses a set of
  particular accelerators on that Node.

- A set of server-providing Pods. Each is running one of the
  aforementioned vLLM instances.

- A set of server-requesting Pods that are bound to Nodes. Each such
  Pod specifies: model, other command-line parameters, some
  environment variable settings, and a Node. After its stub has been
  queried, this Pod is also known to specify a particular set of
  accelerators on the Node. This server-requesting Pod may be bound
  (here, in this data structure) to a vLLM instance.

When the server-requesting Pod is bound to a Node that is absent or in
the process of being deleted, the dual-pods controller has nothing
left to do and the following logic is irrelevant.

When a server-requesting Pod is bound to a server-providing Pod that is
in the process of being deleted, the controller (a) ensures that its
finalizer is not on the server-providing Pod and (b) ensures that the
server-requesting Pod is being deleted. (The controller creates
server-providing Pods with its finalizer on them, so that they cannot
evaporate without this interaction with the controller.)

When, for a given server-requesting Pod, (a) the assigned set of
accelerators is not known and (b) the stub container is running
(without regard to whether the container is marked as "ready"), the
dual-pods controller tries until successful to query for the set of
assigned accelerators.

TODO: Update the following from milestone 1 to milestone 2

When there is a server-requesting Pod that has a known set of
accelerators but is not bound (in the controller's internal state) to
an existing vLLM instance in a server-providing Pod that exists, it is
time to do something about that. There is only one case: creating a
new vLLM instance.

However, if the Node is unschedulable then it is impossible to make
the new vLLM instance and this should be reflected back to the
user/client by deleting the server-requesting Pod. Otherwise the
following logic applies.

When making a new vLLM instance: the Kubernetes scheduler and kubelet
have already assured that there is no other server-requesting Pod
using any of those accelerators, and the behavior of this controller
means that consequently there is no vLLM instance using any of those
accelerators. The controller creates the new vLLM instance by creating
a new server-providing Pod. This Pod uses the CUDA_VISIBLE_DEVICES
environment variable to convey the assigned set of accelerators. The
controller also sets up the relay of readiness from the vLLM instance
to the server-requesting Pod's inference-server container, as
mentioned below.

When there is a vLLM instance and its server-requesting Pod is
non-existent or being deleted, the dual-pod controller deletes that
instance. This is done by (1) ensuring that the controller's finalizer
is not on the server-providing Pod and (2) ensuring that Pod is being
deleted. In this situation, the readiness relay is moot.


#### Readiness Relay

The relay of readiness goes as follows.

- The stub in the server-requesting pod can be sent an HTTP POST
  request that conveys the boolean value for readiness of the real
  inference server container.

- When dual-pods controller knows that the server-providing Pod is ready
  (as reported through an informer on those Pods), the controller
  tells the stub that the inference server is ready (if the controller
  has not already done so).

- When dual-pods controller knows that the server-providing Pod is not
  ready (as reported through an informer on those Pods), the
  controller tells the stub that the inference server is not ready (if
  the controller has not already done so).

#### GPU UUID vs. Index

The GPU assignment query from the dual-pods controller to the
serve-requesting Pod returns a list of GPU UUIDs. The controller
translates this to a list of GPU indices to put in the
CUDA_VISIBLE_DEVICES envar of the server-providing Pod. To support that
translation, we use a ConfigMap named "gpu-map". There is [a
script](../scripts/ensure-nodes-mapped.sh) that ensures that the
ConfigMap is populated with the needed information. The dual-pods
controller reads the mapping from GPU UUID to index from that
ConfigMap.
