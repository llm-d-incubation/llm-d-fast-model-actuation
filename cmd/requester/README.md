This document shows the steps to exercise the requester and dual-pods controller
in a local k8s environment with model `ibm-granite/granite-3.3-2b-instruct`
cached on local PV in the cluster.

Build and push the requester container image (use your favorate
`CONTAINER_IMG_REG`) with a command like the following. You can omit
the `TARGETARCH` if the runtime ISA matches your build time ISA.

```shell
make build-requester CONTAINER_IMG_REG=$CONTAINER_IMG_REG TARGETARCH=amd64
make push-requester  CONTAINER_IMG_REG=$CONTAINER_IMG_REG
```

Build the dual-pods controller image.

```shell
make build-controller CONTAINER_IMG_REG=$CONTAINER_IMG_REG
```

**NOTE**: The instructions here are about local testing. To build and
  publish images for others to use the following commands, with
  judicious choices for the shell variables therein.

```shell
make build-and-push-requester CONTAINER_IMG_REG=$CONTAINER_IMG_REG REQUESTER_IMG_TAG=$YOUR_DESIRED_TAG

make build-controller CONTAINER_IMG_REG=$CONTAINER_IMG_REG CONTROLLER_IMG_TAG=$YOUR_DESIRED_TAG
```

**END OF NOTE**

Run the script to populate the `gpu-map` ConfigMap.

```shell
scripts/ensure-nodes-mapped.sh
```

Instantiate the Helm chart for the dual-pods controller. Specify the tag produced by the build above. Specify the name of the ClusterRole to use for Node get/list/watch authorization, or omit if not needed.

```shell
helm upgrade --install dpctlr charts/dpctlr --set Image="${CONTAINER_IMG_REG}/dual-pods-controller:9010ece" --set NodeViewClusterRole=vcp-node-viewer
```

Create a ReplicaSet of 1 server-requesting Pod.

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
        dual-pods.llm-d.ai/admin-port: "8081"
        dual-pods.llm-d.ai/server-patch: |
          metadata:
            labels: {
              "model-reg": "ibm-granite",
              "model-repo": "granite-3.3-2b-instruct",
              "app": null}
          spec:
            containers:
            - name: inference-server
              image: docker.io/vllm/vllm-openai:v0.10.2
              imagePullPolicy: IfNotPresent
              command:
              - vllm
              - serve
              - --port=8000
              - --model=ibm-granite/granite-3.3-2b-instruct
              - --max-model-len=32768
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
          command:
          - /app/requester
          - --logtostderr=false
          - --log_file=/tmp/requester.log
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
```

Or, if you had caching working, something like the following.

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
        dual-pods.llm-d.ai/admin-port: "8081"
        dual-pods.llm-d.ai/server-patch: |
          metadata:
            labels: {
              "model-reg": "ibm-granite",
              "model-repo": "granite-3.3-2b-instruct",
              "app": null}
          spec:
            containers:
            - name: inference-server
              image: docker.io/vllm/vllm-openai:v0.10.2
              imagePullPolicy: IfNotPresent
              command:
              - vllm
              - serve
              - --port=8000
              - /pvcs/local/vcp/hf/models--ibm-granite--granite-3.3-2b-instruct/snapshots/707f574c62054322f6b5b04b6d075f0a8f05e0f0
              - --max-model-len=32768
              env:
              - name: VLLM_CACHE_ROOT
                value: /pvcs/shared/vcp/vllm
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
                subPath: vcp-mspreitz
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

Check the allocated GPU.
```console
$ kubectl get po -o wide
NAME                          READY   STATUS              RESTARTS   AGE     IP           NODE               NOMINATED NODE   READINESS GATES
dpctlr-78494ffcc7-p58tc       1/1     Running             0          7m58s   10.0.0.218   ip-172-31-58-228   <none>           <none>
my-request-5n2m6              0/1     Running             0          8m36s   10.0.0.134   ip-172-31-58-228   <none>           <none>
my-request-5n2m6-dual-2wn7w   0/1     ContainerCreating   0          40s     <none>       ip-172-31-58-228   <none>           <none>
$ REQ_IP=10.0.0.134
$ curl $REQ_IP:8081/v1/dual-pods/accelerators
["GPU-0d1d8df2-4bc7-98fe-1d41-a5d13a5866d1"]
```

Check the controller-created server-running pod.
```console
$ kubectl get po my-request-5n2m6-dual-2wn7w -oyaml | yq .metadata
annotations:
  dual-pods.llm-d.ai/accelerators: GPU-0d1d8df2-4bc7-98fe-1d41-a5d13a5866d1
  dual-pods.llm-d.ai/nominal: 7O/T2msskzUzLzemXvw/fN3RGTPg9zqr8cpOyRy3VbU
  dual-pods.llm-d.ai/requester: 811f8932-b608-416c-8a90-589b39ed9bde my-request-5n2m6
creationTimestamp: "2025-11-21T23:30:32Z"
finalizers:
  - dual-pods.llm-d.ai/provider
generateName: my-request-5n2m6-dual-
labels:
  dual-pods.llm-d.ai/dual: my-request-5n2m6
  dual-pods.llm-d.ai/sleeping: "false"
  model-reg: ibm-granite
  model-repo: granite-3.3-2b-instruct
name: my-request-5n2m6-dual-2wn7w
namespace: default
resourceVersion: "24430539"
uid: b7aced71-3137-4205-9f50-86eb73f92a61
$ kubectl get po my-request-5n2m6-dual-2wn7w -oyaml | yq .spec.containers[0]
command:
  - vllm
  - serve
  - --port=8000
  - --model=ibm-granite/granite-3.3-2b-instruct
  - --max-model-len=32768
env:
  - name: CUDA_VISIBLE_DEVICES
    value: "0"
image: docker.io/vllm/vllm-openai:v0.10.2
imagePullPolicy: IfNotPresent
name: inference-server
ports:
  - containerPort: 8080
    name: probes
    protocol: TCP
  - containerPort: 8081
    name: spi
    protocol: TCP
readinessProbe:
  failureThreshold: 3
  httpGet:
    path: /health
    port: 8000
    scheme: HTTP
  initialDelaySeconds: 60
  periodSeconds: 5
  successThreshold: 1
  timeoutSeconds: 1
resources:
  limits:
    cpu: "2"
    memory: 6Gi
    nvidia.com/gpu: "0"
  requests:
    cpu: "1"
    memory: 250Mi
    nvidia.com/gpu: "0"
terminationMessagePath: /dev/termination-log
terminationMessagePolicy: File
volumeMounts:
  - mountPath: /var/run/secrets/kubernetes.io/serviceaccount
    name: kube-api-access-9ncxb
    readOnly: true
```

Check the relayed readiness.
```console
$ kubectl wait pod/my-request-5n2m6-dual-2wn7w --for=condition=Ready --timeout=120s
pod/my-request-5n2m6-dual-2wn7w condition met
$ curl $REQ_IP:8080/ready
OK
```

Make an inference request.
```console
$ kubectl get po -owide
NAME                          READY   STATUS    RESTARTS   AGE     IP           NODE               NOMINATED NODE   READINESS GATES
dpctlr-78494ffcc7-p58tc       1/1     Running   0          16m     10.0.0.218   ip-172-31-58-228   <none>           <none>
my-request-5n2m6              1/1     Running   0          17m     10.0.0.134   ip-172-31-58-228   <none>           <none>
my-request-5n2m6-dual-2wn7w   1/1     Running   0          9m10s   10.0.0.145   ip-172-31-58-228   <none>           <none>
$ curl -s http://10.0.0.145:8000/v1/completions \
  -H "Content-Type: application/json" \
  -d '{
    "model": "ibm-granite/granite-3.3-2b-instruct",
    "prompt": "The capital of France is",
    "max_tokens": 30
  }'
{"id":"cmpl-cfe04f79eb904748891561c76ae29986","object":"text_completion","created":1763768447,"model":"ibm-granite/granite-3.3-2b-instruct","choices":[{"index":0,"text":" Paris, which is known for its rich history, cultural landmarks, and iconic architecture like the Eiffel Tower and Notre","logprobs":null,"finish_reason":"length","stop_reason":null,"token_ids":null,"prompt_logprobs":null,"prompt_token_ids":null}],"service_tier":null,"system_fingerprint":null,"usage":{"prompt_tokens":5,"total_tokens":35,"completion_tokens":30,"prompt_tokens_details":null},"kv_transfer_params":null}
```

Check the log of the server-requesting pod.
```console
$ kubectl logs my-request-5n2m6
I1121 23:22:48.119370       1 server.go:64] "starting server" logger="probes-server" port="8080"
I1121 23:22:48.142001       1 server.go:84] "Got GPU UUIDs" logger="spi-server" uuids=["GPU-0d1d8df2-4bc7-98fe-1d41-a5d13a5866d1"]
I1121 23:22:48.142119       1 server.go:171] "starting server" logger="spi-server" port="8081"
I1121 23:30:32.039243       1 server.go:139] "Setting ready" logger="spi-server" newReady=false
I1121 23:37:13.904292       1 server.go:139] "Setting ready" logger="spi-server" newReady=true
```

Clean up.
```console
$ kubectl delete rs my-request
replicaset.apps "my-request" deleted
$ kubectl delete po my-request-5n2m6-dual-2wn7w
pod "my-request-5n2m6-dual-2wn7w" deleted
$ helm delete dpctlr
release "dpctlr" uninstalled
```
