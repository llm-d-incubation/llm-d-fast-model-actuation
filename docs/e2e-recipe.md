# End-to-end test recipe

This is a recipe that a contributor can follow to do end-to-end
testing, using a container registry and GPU-ful Kubernetes cluster
that the contributor is authorized to use.

## Setup

Configure kubectl to work with the cluster of your choice.

Run the script to populate the `gpu-map` ConfigMap.

```shell
scripts/ensure-nodes-mapped.sh
```

Set the shell variable `CONTAINER_IMG_REG` to the registry that you
intend to use. For example, the following might work for you.

```shell
CONTAINER_IMG_REG=quay.io/${LOGNAME}/fma
```

Build and push the requester container image with a command like the
following. You can omit the `TARGETARCH` if the runtime ISA matches
your build time ISA.

```shell
make build-requester CONTAINER_IMG_REG=$CONTAINER_IMG_REG TARGETARCH=amd64
make push-requester  CONTAINER_IMG_REG=$CONTAINER_IMG_REG
```

Build the dual-pods controller image. Omit TARGETARCH if not
cross-compiling. NOTE: you will need a local Go development
environment, including [ko](https://ko.build/).

```shell
make build-controller CONTAINER_IMG_REG=$CONTAINER_IMG_REG
```

For example, it will looks something like the following.

```console
user@machine llm-d-fast-model-actuation % make build-controller CONTAINER_IMG_REG=$CONTAINER_IMG_REG
KO_DOCKER_REPO=quay.io/user/fma ko build -B ./cmd/dual-pods-controller -t 88ce25e --platform all
...
2025/10/30 15:58:05 Published quay.io/user/fma/dual-pods-controller:88ce25e@sha256:0fdc3415247e47e742305738b50777ad78a8b8ceb502977b1e6ab3e8bee3c52a
quay.io/user/fma/dual-pods-controller:88ce25e@sha256:0fdc3415247e47e742305738b50777ad78a8b8ceb502977b1e6ab3e8bee3c52a

```

In preparation for usage of the image that you just built, define a
shell variable to hold the tag of the image container just built (you
can see that tag in the last line of the output). Continuing the above
example, that would go as follows.

```shell
CONTROLLER_IMG_TAG=b699bc6 # JUST AN EXAMPLE - USE WHAT YOU BUILT
```

Instantiate the Helm chart for the dual-pods controller. Specify the
tag produced by the build above. Specify the name of the ClusterRole
to use for Node get/list/watch authorization, or omit if not
needed. Adjust the SleeperLimit setting to your liking (the default is
2).

NOTE: if you have done this before then you will need to delete the
old Helm chart instance before re-making it.

```shell
helm upgrade --install dpctlr charts/dpctlr --set Image="${CONTAINER_IMG_REG}/dual-pods-controller:${CONTROLLER_IMG_TAG}" --set NodeViewClusterRole=vcp-node-viewer --set SleeperLimit=1
```

Finally, define a shell function that creates a new ReplicaSet whose
members will not match members of other invocations of this same shell
function. Following are two examples. The first is rather minimal. The
second uses model staging and torch.compile caching.

Following are some things to keep in mind about these definitions.

- It is critical that the server patch change the label set to not
  match the selector of the ReplicaSet.

- Make sure that the (main, not GPU) memory limit stated in the
  resources section is big enough to contain the off-loaded model
  tensors (for when the inference server is put to sleep).

- Limit the GPU memory utilization to leave enough room for the number
  of sleeping servers that you configured the dual-pods controller to
  allow.

- You may need to add a `vllm serve` argument for `--kv-cache-memory`.
  I do not understand why the default gets into trouble, but I see it
  doing that. Look in the startup log from vllm for a statement like
  the following (showing that it is using too much memory for kv
  cache): "Free memory on device (43.9/44.39 GiB) on startup. Desired
  GPU memory utilization is (0.8, 35.51 GiB). Actual usage is 4.74 GiB
  for weight, 0.23 GiB for peak activation, 0.02 GiB for non-torch
  memory, and 0.62 GiB for CUDAGraph memory. Replace
  gpu_memory_utilization config with `--kv-cache-memory=31966072012`
  to fit into requested memory, or `--kv-cache-memory=40974729216` to
  fully utilize gpu memory. Current kv cache memory in use is
  32786058444 bytes".

### Simple ReplicaSet

```shell
function mkrs() {
kubectl apply -f - <<EOF
apiVersion: apps/v1
kind: ReplicaSet
metadata:
  name: my-request-$(date +%H-%M-%S)
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
        instance: "$(date +%H-%M-%S)"
      annotations:
        dual-pod.llm-d.ai/admin-port: "8081"
        dual-pod.llm-d.ai/server-patch: |
          metadata:
            labels: {
              "model-reg": "ibm-granite",
              "model-repo": "granite-3.3-2b-instruct",
              "app": null}
          spec:
            containers:
            - name: inference-server
              image: docker.io/vllm/vllm-openai:v0.10.2
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
              resources:
                limits:
                  cpu: "2"
                  memory: 9Gi
              readinessProbe:
                httpGet:
                  path: /health
                  port: 8000
                initialDelaySeconds: 60
                periodSeconds: 5
    spec:
      containers:
        - name: inference-server
          image: ${CONTAINER_IMG_REG}/requester:latest
          imagePullPolicy: Always
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
EOF
}
```

### ReplicaSet using model staging and torch.compile caching

This example supposes model staging and torch.compile caching. It
supposes that, for each Node capable of running the model, the model
has been staged to a file (in a subdirectory specific to the user, to
finesse OpenShift access control issues) in a PVC (whose name includes
the Node's name) dedicated to holding staged models for that
Node. This example also supposes that the torch.compile cache is
shared throughout the cluster in a shared PVC.

```shell
function mkrs() {
kubectl apply -f - <<EOF
apiVersion: apps/v1
kind: ReplicaSet
metadata:
  name: my-request-$(date +%H-%M-%S)
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
        instance: "$(date +%H-%M-%S)"
      annotations:
        dual-pod.llm-d.ai/admin-port: "8081"
        dual-pod.llm-d.ai/server-patch: |
          metadata:
            labels: {
              "model-reg": "ibm-granite",
              "model-repo": "granite-3.3-2b-instruct",
              "app": null}
          spec:
            containers:
            - name: inference-server
              image: docker.io/vllm/vllm-openai:v0.10.2
              command:
              - vllm
              - serve
              - --port=8000
              - /pvcs/local/vcp/hf/models--ibm-granite--granite-3.3-2b-instruct/snapshots/707f574c62054322f6b5b04b6d075f0a8f05e0f0
              - --enable-sleep-mode
              - --max-model-len=32768
              - --gpu-memory-utilization=0.8
              env:
              - name: VLLM_SERVER_DEV_MODE
                value: "1"
              - name: VLLM_CACHE_ROOT
                value: /pvcs/shared/vcp/vllm
              - name: FLASHINFER_WORKSPACE_BASE
                value: /pvcs/shared/vcp/vllm
              - name: XDG_CONFIG_HOME
                value: /tmp
              - name: TRITON_HOME
                value: /tmp
              resources:
                limits:
                  cpu: "2"
                  memory: 9Gi
              readinessProbe:
                httpGet:
                  path: /health
                  port: 8000
                initialDelaySeconds: 60
                periodSeconds: 5
              volumeMounts:
              - name: local
                readOnly: true
                mountPath: /pvcs/local
                subPath: vcp-${LOGNAME}
              - name: shared
                mountPath: /pvcs/shared
            volumes:
            - name: local
              persistentVolumeClaim:
                claimName: vcp-local-{{ .NodeName }}
    spec:
      containers:
        - name: inference-server
          image: ${CONTAINER_IMG_REG}/requester:latest
          imagePullPolicy: Always
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
              cpu: "1"
              memory: 250Mi
      volumes:
      - name: shared
        persistentVolumeClaim:
          claimName: vcp-cephfs-shared
EOF
}
```


## Example 1: cycle server-requesting Pod

Create a ReplicaSet of 1 server-requesting Pod.

```shell
mkrs
```

### Expect a server-running Pod

Expect that soon after the requester in the server-requesting Pod
starts running (NOTE: this is BEFORE the Pod is marked as "ready"),
the dual-pods controller will create the server-running Pod and it
will get scheduled to the same Node as the server-requesting Pod. Its
name will equal the server-requesting Pod's name suffixed with
`-dual-` and some letters and numbers.

Expect that once the dual-pods controller starts working on a
server-requesting Pod, the Pod will have an annotation with name
`dual-pod.llm-d.ai/status` and a value reflecting the current status
for that Pod, using [the defined data
structure](../pkg/api/interface.go) (see ServerRequestingPodStatus).

Expect that eventually the server-running Pod gets marked as ready,
and soon after that the server-requesting Pod is marked as ready.

Expect that once the server-running Pod is marked as ready, its log
shows that vLLM has completed starting up.

FYI, the following commands produce Pod listings that show FYI
information tacked on by the dual-pods controller.

```shell
kubectl get pods -o 'custom-columns=NAME:.metadata.name,PHASE:.status.phase,COND2:.status.conditions[2].type,VAL2:.status.conditions[2].status,DUAL:.metadata.labels.dual-pods\.llm-d\.ai/dual,GPUS:.metadata.annotations.dual-pods\.llm-d\.ai/accelerators,SLEEPING:.metadata.labels.dual-pods\.llm-d\.ai/sleeping'


kubectl get pods -L dual-pods.llm-d.ai/dual,dual-pods.llm-d.ai/sleeping
```

The following command will query Prometheus metrics from the nvidia
infrastructure in an OpenShift cluster to produce a listing that shows
a little information about each GPU. But beware, experience shows that
the "Assoc" field is flaky, even without the subterfuge of the
dual-pods controller in action. This requires you to set a shell
variable named `cluster_domain` to the domain used for external access
to applications in your cluster (i.e., where the Ingress/Route
listener is listening).

```shell
curl -sSkG \
  -H "Authorization: Bearer $(oc whoami -t)" \
  --data-urlencode query=DCGM_FI_DEV_FB_USED \
  https://prometheus-k8s-openshift-monitoring.apps.$cluster_domain/api/v1/query \
  | jq -c '.data.result[] | {Hostname: .metric.Hostname, GPU: .metric.gpu, ID: .metric.UUID, Assoc: .metric.exported_namespace!=null, Mem: .value[1]}' | sort
```

### Delete server-requesting Pod

Use `kubectl scale --replicas=0` to scale the ReplicaSet down to 0
replicas. Expect that the server-requesting and server-running Pods
get deleted.

## Example 2: reflect server-running Pod deletion

Start like example 1, but finish by deleting the server-running Pod
instead of the server-requesting one. Expect that the server-running
and server-requesting Pods both go away, and then a replacement
server-requesting Pod should appear and get satisfied as in example 1.

## Example 3: deletions while controller is not running

Modify the first two examples by surrounding the pod deletion by first
`helm delete dpctlr` to remove the controller and then, after the Pod
deletion, re-instantiate the controller Helm chart. Remember when
using `kubectl delete` that it will hang until the controller is
re-instantiated to remove the controller's finalizer on the Pod,
unless you use `kubectl delete --wait=false`. The right stuff should
finish happening after the second controller starts up.

## Example 4: create the gpu-map too late

Like example 1 but start with the ConfigMap named `gpu-map` not
existing (delete it if you already have it). After creating the
ReplicaSet and waiting a while for the controller to do as much as it
will, expect that there is no server-running Pod. Examine the
controller's log to see that it has stopped making progress. Then run
the script to create and populate the `gpu-map` ConfigMap. After it
finishes, the controller should soon create the server-running Pod.

## Example 5: Node cordon

Setup and create the ReplicaSet. Then `kubectl cordon` the node where
the dual pods are. Observe that nothing changes. Prod the system,
e.g., by adding unimportant annotations to the pods. Observe that
still nothing happens.

If you are fast, try doing `kubectl cordon` between (a) the time when
the server-requesting Pod gets scheduled and (b) the time when the
server-running Pod gets created. If you can do that, observe that the
dual-pods controller deletes the server-requesting Pod.

## Example 6: Node deletion

Setup and create the dual pods. Then delete the Node that they are
running on. Observe that eventually both Pods go away.

## Example 7: Sleep and wake

Whenever using sleep and wake, you have responsibility for not
overloading the GPU memory. Plan a division of GPU memory between
awake inference servers and sleeping ones. Thus far, we find that a
sleeping one takes between 1 and 2 GiB of GPU memory. To be safe, plan
on 2 GiB. When your limit on the number of sleeping instances is `N`
(this is the value that you supplied for `SleeperLimit` above when
instantiating the Helm chart), you are thus planning on up to `N * 2
GiB` of memory being used by them. Consequently, you must configure
each inference server on a GPU with `M GiB` of memory to use no more
than `M - N * 2` GiB of memory.

Testing wake-up is a little challenging when there are many GPUs
available, because the likelihood of re-use is low. You can
artificially improve your odds by pinning the server-requesting Pod to
a node with few GPUs. For example, I did this by adding the following
to the PodSpec in the ReplicaSet; I was using a cluster that had only
one un-allocated GPU of that type.

```yaml
      nodeSelector: { "nvidia.com/gpu.product": "NVIDIA-L40S" }
```

Follow example 1, up through getting the server-running Pod.

Next, scale the ReplicaSet down to zero replicas.

```shell
kubectl scale rs/my-request --replicas=0
```

Expect that the server-requesting Pod goes away and the server-running
Pod continues to exist, with no finalizer. Expect that if you HTTP GET
its `/is_sleeping` path, the response says that it is indeed
sleeping. Examine the dual-pod controller's log.

```shell
kubectl logs deploy/dpctlr > /tmp/dpctlr.log
```

Expect to find a message "Unbound server-running Pod", and later a
message "End of life of inference server".

Next, scale the ReplicaSet back up to 1 replica. Expect to find a new
server-requesting Pod, with a different name than before. Get the
latest controller log. Now the question is, did the new
server-requesting Pod get assigned the same GPU as the original?  Look
for log message "Found GPUs", and look at the value of "gpuUUIDs". If
it is NOT the same in the two log messages, the two server-requesting
Pods did NOT get the same GPU assigned. Try again.

If the two sever-requesting Pods got the same GPU assigned, expect the
existing server-running Pod to be woken up and used. Look for a log
message "Bound server-running Pod", and expect that no new
server-running Pod is created. Expect a new "Successfully relayed the
readiness" log message in the dual-pods controller log.

## Example 8: Exercise sleeper limit

Repeatedly create a server-requesting Pod, wait for its server-running
Pod to appear and become ready, then delete the sever-requesting Pod,
observe that the server-running Pod remains. With the dual-pods
controller configured with a sleeper limit of N, build up N+1
server-running Pods (all with sleeping vllm) using some particular
GPU. Look in the dual-pod controller's log to see which GPU each
runner uses. Next, create one more server-requesting Pod that gets
bound to the same GPU. Observe that exactly 1 of the old
server-running Pods gets delete --- the oldest one.

Or, for more fun, before going past N+1, make a server-requesting Pod
that causes the oldest runner to be re-used. Then delete that
requester. Then force a deletion; observe that the deled one is the
least recently used.
