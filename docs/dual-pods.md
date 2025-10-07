# Dual-Pod Architecture

## Overview

Dual pods is a technique for making model flexibility usable in the
Kubernetes milieu. Model flexibility refers to vLLM sleep/wake and
model swapping. These things do not fit simply and directly into
Kubernetes Pods because each container in a Pod: (1) is allocated a
constant amount of accelerator resources and (2) has a constant
command. Yet clients and users most naturally will use a single
Pod to describe a single desired inference server. The dual-pod
technique has a dichotomy between (1) the server-requesting Pods that
clients/users create and (2) the server-running Pods that actually run
the inference servers.

See [the user interface](../pkg/api/interface.go) and [the stub
interface](../pkt/stub/api/interface.go) for more details of the
technique.

When using vLLM as the inference server code, the server-requesting
Pod has a command conforming to the [`vllm serve`] CLI.
Various dual-pod controllers are possible.
The dual-pod controller that works with just the existing
sleep/wake functionality concludes that to create a server-running Pod
for a particular model, it uses the command `vllm serve <options>`.
The dual-pod controller
that works with the first edition (i.e., launcher based) of model
swapping uses a launcher-specific command to run the launcher. To swap
a model in, the controller issues a POST request (to the launcher)
that includes the model reference and the model-specific flags
according to a pattern fixed at controller development time. To swap a
model out, the controller issues a request that does not include the
model reference nor the model-specific flags.

## Example: vLLM and 1 nvidia GPU

Here is an example using vLLM and a model that runs on a single nvidia
GPU via the nvidia GPU operator. Details here are specific to nvidia
GPUs and software.

Following is what a client might submit to the kube-apiserver in a
request to create a server-requesting Pod.

```yaml
apiVersion: v1
kind: Pod
metadata:
  name: example-request
  annotations:
    dual-pod.llm-d.ai/admin-port: 8001
    dual-pod.llm-d.ai/server-patch: |
      spec:
        containers:
        - name: inference-server
          image: docker.io/vllm/vllm-openai@v0.10.1.1
          command:
          - vllm
          - serve
          - --port=8000
          - /pvcs/local/hf/models--deepseek-ai--DeepSeek-R1-Distill-Qwen-32B/snapshots/711ad2ea6aa40cfca18895e8aca02ab92df1a746
          - --max-model-len=32768
          env:
          - name: VLLM_CACHE_ROOT
            value: /pvcs/shared/vllm
          resources:
            limits:
              cpu: "2"
              memory: 1Gi
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
    image: some-registry/some-namespace/reverse-proxy@v0.1.0
    command:
    - /proxy
    - --relay-port-1=8000
    - --admin-port=8001
    - --metrics-port=8002
    - --debug-port=8003
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

From such a server-requesting Pod, after it is placed on the Node
named "somenode" and started and queried to reveal that the set of
associated GPUs is `{"3"}`, the dual-pod controller would construct
the following to give to the kube-apiserver to create the
server-running Pod.

```yaml
apiVersion: v1
kind: Pod
metadata:
  name: somenode-8-dual
spec:
  affinity:
    nodeAffinity:
      requiredDuringSchedulingIgnoredDuringExecution:
        nodeSelectorTerms:
        - matchExpressions:
          - key: "kubernetes.io/hostname"
            operator: In
            values: ["somenode"]
  containers:
  - name: inference-server
    image: docker.io/vllm/vllm-openai@v0.10.1.1
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
    resources:
      limits:
        nvidia.com/gpu: "0"
        cpu: "2"
        memory: 1Gi
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

Explicitly specifying a quantity of "0" GPUs gets this container
access to all of the GPUs. Setting the `CUDA_VISIBLE_DEVICES` envar
directs the `vllm serve` process to use the indicated one.

The name of this Pod combines the name of the relevant node (which is
presumed to also appear as the value of the hostname label) and the
set of associated GPUs (hexadecimal rendering of bitmask).

## Sequence Flows

### Inference Server Creation Flow

![dual-pod architecture](./dual-pods-arch.drawio.png)

The key steps for requesting an inference server are:

1. An actor (e.g., a Kubernetes controller or an end-user) creates an inference
   server requester pod (see example above).
2. The `inference-server-gpu-allocator` container exposes which accelerators have
   been allocated on the node and marks itself as ready (i.e., its readiness probe
   succeeds).
3. The `inference-server-provider-controller` container watches inference server
   requester pods (i.e., those with the annotation `dual-pod.llm-d.ai/role: requester`)
   and delegates lifecycle events (ie. `create`, `update` and `delete`) to
   the `inference-server-provider` component. Note: multiple implementations of
   the provider component may exists, but only one is active at any given time.
4. The `inference-server-provider` implementation selects an inference server that
   matches the inference server requester pod specification. If no suitable server pod is found,
   it falls back to the `cold-start provider`.
5. The `inference-server-provider` sets the `dual-pod.llm-d.ai/inference-server-ip`
   with the selected server pod's IP address.
6. The `inference-server-requester` containers periodically (TODO: rate?) reads
   the [file containing the `dual-pod.llm-d.ai/inference-server-ip` annotation value](https://kubernetes.io/docs/concepts/storage/volumes/#downwardapi) and when this value is
   not empty anymore, it optionally starts redirecting traffic to the selected
   inference server pod. It then mark itself as ready (i.e., its readiness probe
   succeeds).

### Inference Server Deletion Flow

TDB.
