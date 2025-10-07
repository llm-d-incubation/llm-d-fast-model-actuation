# Dual Pods for Fast Model Actuation

## Background

Dual pods is a technique for making model flexibility usable in the
Kubernetes milieu. Model flexibility refers to vLLM sleep/wake and
model swapping. These things do not fit simply and directly into
Kubernetes Pods because each container in a Pod: (1) is allocated a
constant amount of accelerator resources and (2) has a constant
command. Yet clients and users most naturally will use a single Pod to
describe a single desired inference server. The dual-pod technique has
a dichotomy between (1) the server-requesting Pods that clients/users
create and (2) the server-running Pods that actually run the inference
servers.

When using vLLM as the inference server code: the server-requesting
Pod has a container that is actually a bit of dual-pod technology, and
an annotation that contains a patch that changes the Pod's labels,
annotations, and spec to those for actually running vLLM. Various
dual-pod controllers are possible.

We defined an API that is independent of which controller (technique)
is used. That API is in [pkg/api](../pkg/api) and here. The patch
defined in the server-running Pod converts the server-requesting Pod's
PodSpec and labels into those of a _nominal_ server-running Pod and
defines the annotations of that Pod. This nominal server-running Pod
could satisfactorily run the inference server. However, the dual-pod
controller is allowed to create different sorts of server-running Pods
that, ultimately, run inference servers according to the nominal
server-running PodSpec, labels, and annotations.

The dual-pod controller that works with just the existing sleep/wake
functionality concludes that to create a server-running Pod for a
particular model, it uses the nominal server-running PodSpec, labels,
and annotations directly. A dual-pod controller that works with the
launcher-based model swapping creates a server-running Pod that runs
the launcher. To swap a model in, the controller issues a POST request
(to the launcher) that includes the command line arguments,
environment variable settings, and assigned accelerator set for
running `vllm serve`. To swap a model out, the controller issues a
request that does not include those details.

## Personas

- **Cluster administrator**. Installs and configures nvidia
  components. Partitions GPU-bearing nodes of the cluster between
  those used for dual pods and those NOT.

- **Model variant deployer**. Deploys a horizontally scalable set of
  server-requesting Pods for each model variant that shall be on the
  cluster.

- **llm-d administrator**. Deploys and configures llm-d on the cluster.

- **Inference client**. Outside the cluster. Submits inference
  requests, consumes responses. Judges SLO attainment.

## Design

Note: this document currently describes the design for the first of
three milestones.

### Drawing

(To be redrawn for Milestone 1; currently, this is the picture for Milestone 3).

![Boxes and arrows illustrating the design here](llm-d-fma-arch.drawio.png)

[//]: # "See the adjacent .drawio file for the source"

### Introduction

(to be written)

Defining limitation of milestone 1: There is a 1:1 correspondence between server-requesting Pod and server-running Pod.

### Scenarios

The outer product of

1. (scaling)

    a. Scale out an existing set. Note: this is not something with an
      SLO? Or do we evaluate this as a matter of shifting from failing
      SLO to passing (due to added capacity)?
    b. Scale set out from zero

2. (GPU single vs. not)

    a. Resource request/limit is 1 GPU
    b. Resource request/limit is multiple GPUs

### What goes in a server-requesting Pod

Here is an example.

```yaml
apiVersion: v1
kind: Pod
metadata:
  name: example-request
  annotations:
    dual-pod.llm-d.ai/admin-port: 8082
    dual-pod.llm-d.ai/server-patch: |
      spec:
        containers:
        - name: inference-server
          image: docker.io/vllm/vllm-openai@v0.10.2
          command:
          - vllm
          - serve
          - --port=8000
          - /pvcs/local/hf/models--deepseek-ai--DeepSeek-R1-Distill-Qwen-32B/snapshots/711ad2ea6aa40cfca18895e8aca02ab92df1a746
          - --max-model-len=32768
          env:
          - name: VLLM_CACHE_ROOT
            value: /pvcs/shared/vllm
          readinessProbe:
            httpGet:
              path: /readyz
              port: 8000
          resources:
            limits:
              cpu: "2"
              memory: 6Gi
          volumeMounts:
          - name: local
            readOnly: true
            mountPath: /pvcs/local
        volumes:
        - name: local
          persistentVolumeClaim:
            claimName: {{ .LocalVolume }}
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
    resources:
      limits:
        nvidia.com/gpu: "1"
        cpu: "1"
        memory: 250Mi
    volumeMounts:
    - name: shared
      mountPath: /pvcs/shared
  volumes:
  - name: shared
    persistentVolumeClaim:
      claimName: shared
```

### What goes in a server-running Pod

Here is a corresponding example.

```yaml
apiVersion: v1
kind: Pod
metadata:
  name: example-request-dual
spec:
  nodeSelector: { "kubernetes.io/hostname": "somenode" }
  containers:
  - name: inference-server
    image: docker.io/vllm/vllm-openai@v0.10.2
    command:
    - vllm
    - serve
    - --port=8000
    - /pvcs/local/hf/models--deepseek-ai--DeepSeek-R1-Distill-Qwen-32B/snapshots/711ad2ea6aa40cfca18895e8aca02ab92df1a746
    - --max-model-len=32768
    env:
    - name: VLLM_CACHE_ROOT
      value: /pvcs/shared/vllm
    - name: CUDA_VISIBLE_DEVICES
      value: "3"
    readinessProbe:
      httpGet:
        path: /readyz
        port: 8000
    resources:
      limits:
        nvidia.com/gpu: "0"
        cpu: "2"
        memory: 6Gi
    volumeMounts:
    - name: shared
      mountPath: /pvcs/shared
    - name: local
      readOnly: true
      mountPath: /pvcs/local
  volumes:
  - name: shared
    persistentVolumeClaim:
      claimName: shared
  - name: local
    persistentVolumeClaim:
      claimName: somenode-local
```

### The dual-pods controller data and logic

The mutable internal state of the controller includes the following.

- A set of existing vLLM instances. Each runs a particular model, with
  other command line parameters and some environment variable
  settings. Each instance is in a Pod, on one Node, and uses a set of
  particular accelerators on that Node.

- A set of server-running Pods. Each is running one of the
  aforementioned vLLM instances.

- A set of server-requesting Pods that are bound to Nodes. Each such
  Pod specifies: model, other command-line parameters, some
  environment variable settings, and a Node. After its stub has been
  queried, this Pod is also known to specify a particular set of
  accelerators on the Node. This server-requesting Pod may be bound
  (here, in this data structure) to a vLLM instance.

When the server-requesting Pod is bound to a Node that is absent or in the process of being deleted, the dual-pods controller has nothing left to do and the following logic is irrelevant.
When a server-requesting Pod is bound to a server-running Pod that is in the process of being deleted, the controller (a) ensures that its finalizer is not on the server-running Pod and (b) ensures that the server-requesting Pod is being deleted. (The controller creates server-running Pods with its finalizer on them, so that they cannot evaporate without this interaction with the controller.)
When, for a given server-requesting Pod, (a) the assigned set of accelerators is not known and (b) the stub container is running (without regard to whether the container is marked as "ready"), the dual-pod controller tries until successful to query for the set of assigned accelerators.
When there is a server-requesting Pod that has a known set of accelerators but is not bound (in the controller's internal state) to an existing vLLM instance in a server-running Pod that exists, it is time to do something about that. There is only one case: creating a new vLLM instance.
However, if the Node is unschedulable then it is impossible to make the new vLLM instance and this should be reflected back to the user/client by deleting the server-requesting Pod. Otherwise the following logic applies.
When making a new vLLM instance: the Kubernetes scheduler and kubelet have already assured that there is no other server-requesting Pod using any of those accelerators, and the behavior of this controller means that consequently there is no vLLM instance using any of those accelerators. The controller creates the new vLLM instance by creating a new server-running Pod. This Pod uses the CUDA_VISIBLE_DEVICES environment variable to convey the assigned set of accelerators. The controller also sets up the relay of readiness from the vLLM instance to the server-requesting Pod's inference-server container, as mentioned below.
When there is a vLLM instance and its server-requesting Pod is non-existent or being deleted, the dual-pod controller deletes that instance. This is done by (1) ensuring that the controller's finalizer is not on the server-running Pod and (2) ensuring that Pod is being deleted. In this situation, the readiness relay is moot.

#### Readiness Relay

The relay of readiness goes as follows.

- The stub in the server-requesting pod can be sent an HTTP POST
  request that conveys the boolean value for readiness of the real
  inference server container.

- When dual-pod controller knows that the server-running Pod is ready
  (as reported through an informer on those Pods), the controller
  tells the stub that the inference server is ready (if the controller
  has not already done so).

- When dual-pod controller knows that the server-running Pod is not
  ready (as reported through an informer on those Pods), the
  controller tells the stub that the inference server is not ready (if
  the controller has not already done so).

#### GPU UUID vs. Index

The GPU assignment query from the dual-pod controller to the
serve-requesting Pod returns a list of GPU UUIDs. The controller
translates this to a list of GPU indices to put in the
CUDA_VISIBLE_DEVICES envar or the server-running Pod. To support that
translation, we use a ConfigMap named "gpu-map". There is [a
script](../scripts/ensure-nodes-mapped.sh) that ensures that the
ConfigMap is populated with the needed information. The dual-pods
controller reads the mapping from GPU UUID to index from that
ConfigMap.
