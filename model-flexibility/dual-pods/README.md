# Dual Pods

Dual pods is a technique for making model flexibility usable in the
Kubernetes milieu. Model flexibility refers to vLLM sleep/wake and
model swapping. These things do not fit simply and directly into
Kubernetes Pods because each container in a Pod: (1) is allocated a
constant amount of accelerator resources and (2) has a constant image
and command. Yet clients and users most naturally will use a single
Pod to describe a single desired inference server. The dual-pod
technique has a dichotomy between (1) the server-requesting Pods that
clients/users create and (2) the server-running Pods that actually run
the inference servers.

See [the interface declarations](pkg/api) for more details of the
technique.

## Example: vLLM sleep/wake and 1 nvidia GPU

Here is an example using vLLM level 1 sleep/wake and a model that runs
on a single nvidia GPU via the nvidia GPU operator. Details here are
specific to nvidia GPUs and software.

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
          - deepseek-ai/DeepSeek-R1-Distill-Qwen-32B
          - --max-model-len=32768
          env:
          - name: VLLM_CACHE_ROOT
            value: /pvcs/shared/vllm
          resources:
            limits:
              cpu: "2"
              memory: 1Gi
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
    - deepseek-ai/DeepSeek-R1-Distill-Qwen-32B
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
  volumes:
  - name: shared
    persistentVolumeClaim:
      claimName: shared
```

Explicitly specifying a quantity of "0" GPUs gets this container
access to all of the GPUs. Setting the `CUDA_VISIBLE_DEVICES` envar
directs the `vllm serve` process to use the indicated one.

The name of this Pod combines the name of the relevant node (which is
presumed to also appear as the value of the hostname label) and the
set of associated GPUs (hexadecimal rendering of bitmask).
