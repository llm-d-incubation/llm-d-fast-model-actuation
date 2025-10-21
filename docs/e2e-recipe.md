# End-to-end test recipe

This is a recipe that a contributor can follow to do end-to-end
testing, using a container registry and GPU-ful Kubernetes cluster
that the contributor is authorized to use.

## Setup

Start by setting the shell variable `CONTAINER_IMG_REG` to the
registry that you intend to use. For example, the following might work
for you.

```shell
CONTAINER_IMG_REG=quay.io/${LOGNAME}/fma
```

Configure kubectl to work with the cluster of your choice.

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
make build-controller CONTAINER_IMG_REG=$CONTAINER_IMG_REG TARGETARCH=amd64
```

For example, it will looks something like the following.

```console
mspreitz@mjs13 llm-d-fast-model-actuation % make build-controller CONTAINER_IMG_REG=$CONTAINER_IMG_REG TARGETARCH=amd64
KO_DOCKER_REPO=quay.io/mspreitz/fma ko build -B ./cmd/dual-pods-controller -t b699bc6 --platform linux/amd64,linux/arm64
2025/10/10 11:51:53 Using base cgr.dev/chainguard/static:latest@sha256:b2e1c3d3627093e54f6805823e73edd17ab93d6c7202e672988080c863e0412b for github.com/llm-d-incubation/llm-d-fast-model-actuation/cmd/dual-pods-controller
...
2025/10/10 11:52:18 Published quay.io/mspreitz/fma/dual-pods-controller:b699bc6@sha256:ef3d14e98c6fc8011c9b6706a280beb4daaeb928f848e2df545ca7eafb1a1908
quay.io/mspreitz/fma/dual-pods-controller:b699bc6@sha256:ef3d14e98c6fc8011c9b6706a280beb4daaeb928f848e2df545ca7eafb1a1908
```

In preparation for usage of the image that you just built, define a
shell variable to hold the tag of the image container just built (you
can see that tag in the last line of the output). Continuing the above
example, that would go as follows.

```shell
CONTROLLER_IMG_TAG=b699bc6 # JUST AN EXAMPLE - USE WHAT YOU BUILT
```

Run the script to populate the `gpu-map` ConfigMap.

```shell
scripts/ensure-nodes-mapped.sh
```

Instantiate the Helm chart for the dual-pods controller. Specify the
tag produced by the build above. Specify the name of the ClusterRole
to use for Node get/list/watch authorization, or omit if not
needed. NOTE: if you have done this before then you will need to
delete the old Helm chart instance before re-making it.

```shell
helm upgrade --install dpctlr charts/dpctlr --set Image="${CONTAINER_IMG_REG}/dual-pods-controller:${CONTROLLER_IMG_TAG}" --set NodeViewClusterRole=vcp-node-viewer
```

## Example 1: cycle server-requesting Pod

Create a ReplicaSet of 1 server-requesting Pod. Following are two
examples. The first is rather minimal. The second uses model staging
and torch.compile caching.

### Simple ReplicaSet

```shell
kubectl apply -f - <<EOF
apiVersion: apps/v1
kind: ReplicaSet
metadata:
  name: my-request
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
              - --max-model-len=32768
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
                  memory: 6Gi
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
              cpu: "1"
              memory: 250Mi
EOF
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
kubectl apply -f - <<EOF
apiVersion: apps/v1
kind: ReplicaSet
metadata:
  name: my-request
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
              - --max-model-len=32768
              env:
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
                  memory: 6Gi
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
```

### Expect a server-running Pod

Expect that soon after the requester in the server-requesting Pod
starts running (NOTE: this is BEFORE the Pod is marked as "ready"),
the dual-pods controller will create the server-running Pod and it
will get scheduled to the same Node as the server-requesting Pod. Its
name will equal the server-requesting Pod's name suffixed with
`-server`.

Expect that once the dual-pods controller starts working on a
server-requesting Pod, the Pod will have an annotation with name
`dual-pod.llm-d.ai/status` and a value reflecting the current status
for that Pod, using [the defined data
structure](../pkg/api/interface.go) (see ServerRequestingPodStatus).

Expect that eventually the server-running Pod gets marked as ready,
and soon after that the server-requesting Pod is marked as ready.

Expect that once the server-running Pod is marked as ready, its log
shows that vLLM has completed starting up.

### Delete server-requesting Pod

Use `kubectl` to delete the ReplicaSet. Expect that the
server-requesting and server-running Pods get deleted.

## Example 2: reflect server-running Pod deletion

Start like example 1, but finish by deleting the server-running Pod
instead of the ReplicaSet. Expect that the server-running and
server-requesting Pods both go away, and then a replacement
server-requesting Pod should appear and get satisfied as in example 1.

## Example 3: deletions while controller is not running

Modify the first two examples by surrounding the `kubectl delete` by
first `helm delete dpctlr` to remove the controller and then, after
the Pod deletion, re-instantiate the controller Helm chart. The right
stuff should finish happening after the second controller starts up.

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
