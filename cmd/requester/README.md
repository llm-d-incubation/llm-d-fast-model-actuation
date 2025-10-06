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

Build the dual-pods controller image. Omit TARGETARCH if not cross-compiling.

```shell
make build-controller CONTAINER_IMG_REG=$CONTAINER_IMG_REG TARGETARCH=amd64
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
$ kubectl get po my-request -owide
NAME         READY   STATUS    RESTARTS   AGE     IP           NODE               NOMINATED NODE   READINESS GATES
my-request   1/1     Running   0          6m27s   10.0.0.218   ip-172-31-58-228   <none>           <none>
$ REQ_IP=10.0.0.148
$ curl $REQ_IP:8081/v1/dual-pod/accelerators
["GPU-7450f677-9aa8-0150-8b11-68727d721976"]
```

Check the controller-created server-running pod.
```console
$ kubectl get po my-request-server -oyaml | yq .metadata
annotations:
  dual-pod.llm-d.ai/role: runner
  kubectl.kubernetes.io/last-applied-configuration: |
    {"apiVersion":"v1","kind":"Pod","metadata":{"annotations":{"dual-pod.llm-d.ai/admin-port":"8081","dual-pod.llm-d.ai/role":"requester","dual-pod.llm-d.ai/server-patch":"spec:\n  containers:\n  - name: inference-server\n    image: docker.io/vllm/vllm-openai:v0.8.5\n    command:\n    - vllm\n    - serve\n    - --port=8000\n    - /pvcs/local/default/vcp/hf/models--ibm-granite--granite-3.3-2b-instruct/snapshots/c4179de4bf66635b0cf11f410a73ebf95f85d506\n    - --max-model-len=32768\n    env:\n    - name: CUDA_VISIBLE_DEVICES\n      value: \"{{ .GPUIndices }}\"\n    - name: VLLM_CACHE_ROOT\n      value: /pvcs/shared/vcp/vllm\n    resources:\n      limits:\n        cpu: \"2\"\n        memory: 6Gi\n    readinessProbe:\n      httpGet:\n        path: /health\n        port: 8000\n      initialDelaySeconds: 60\n      periodSeconds: 5\n    volumeMounts:\n    - name: local\n      readOnly: true\n      mountPath: /pvcs/local\n    - name: shared\n      mountPath: /pvcs/shared\n  volumes:\n  - name: local\n    persistentVolumeClaim:\n      claimName: {{ .LocalVolume }}\n  - name: shared\n    persistentVolumeClaim:\n      claimName: {{ .SharedVolume }}\n"},"name":"my-request","namespace":"default"},"spec":{"containers":[{"image":"quay.io/my-namespace/requester:latest","imagePullPolicy":"IfNotPresent","name":"inference-server","ports":[{"containerPort":8080},{"containerPort":8081}],"readinessProbe":{"httpGet":{"path":"/ready","port":8080},"initialDelaySeconds":2,"periodSeconds":5},"resources":{"limits":{"cpu":"1","memory":"250Mi","nvidia.com/gpu":"1"}}}]}}
creationTimestamp: "2025-09-26T09:02:07Z"
name: my-request-server
namespace: default
ownerReferences:
  - apiVersion: v1
    blockOwnerDeletion: true
    controller: true
    kind: Pod
    name: my-request
    uid: 15f63f7e-42ad-42b8-96a2-bf66f00cac26
resourceVersion: "22498428"
uid: 3e978564-d557-4f67-8f31-03f924d3686e
$ kubectl get po my-request-server -oyaml | yq .spec.containers[0]
command:
  - vllm
  - serve
  - --port=8000
  - /pvcs/local/default/vcp/hf/models--ibm-granite--granite-3.3-2b-instruct/snapshots/c4179de4bf66635b0cf11f410a73ebf95f85d506
  - --max-model-len=32768
env:
  - name: CUDA_VISIBLE_DEVICES
    value: "0"
  - name: VLLM_CACHE_ROOT
    value: /pvcs/shared/vcp/vllm
image: docker.io/vllm/vllm-openai:v0.8.5
imagePullPolicy: IfNotPresent
name: inference-server
ports:
  - containerPort: 8080
    protocol: TCP
  - containerPort: 8081
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
  - mountPath: /pvcs/local
    name: local
    readOnly: true
  - mountPath: /pvcs/shared
    name: shared
  - mountPath: /var/run/secrets/kubernetes.io/serviceaccount
    name: kube-api-access-5kwgp
    readOnly: true
```

Check the relayed readiness.
```console
$ kubectl wait pod/my-request-server --for=condition=Ready --timeout=120s
pod/my-request-server condition met
$ curl $REQ_IP:8080/ready
OK
```

Make an inference request.
```console
$ kc get po -owide
NAME                              READY   STATUS    RESTARTS   AGE   IP           NODE               NOMINATED NODE   READINESS GATES
ip-172-31-58-228-na               1/1     Running   0          44h   10.0.0.45    ip-172-31-58-228   <none>           <none>
my-request                        1/1     Running   0          11m   10.0.0.218   ip-172-31-58-228   <none>           <none>
my-request-server                 1/1     Running   0          11m   10.0.0.136   ip-172-31-58-228   <none>           <none>
vcp-model-ctlr-67c7c8dd8b-kwrjv   1/1     Running   0          44h   10.0.0.8     ip-172-31-58-228   <none>           <none>
$ curl -s http://10.0.0.136:8000/v1/completions \
  -H "Content-Type: application/json" \
  -d '{
    "model": "/pvcs/local/default/vcp/hf/models--ibm-granite--granite-3.3-2b-instruct/snapshots/c4179de4bf66635b0cf11f410a73ebf95f85d506",
    "prompt": "The capital of France is",
    "max_tokens": 30
  }'
{"id":"cmpl-f67701446b8a439dbceea4963fd9c0a2","object":"text_completion","created":1758878077,"model":"/pvcs/local/default/vcp/hf/models--ibm-granite--granite-3.3-2b-instruct/snapshots/c4179de4bf66635b0cf11f410a73ebf95f85d506","choices":[{"index":0,"text":" Paris. It is also the most populous city in the European Union.\n\nParis is known for its rich history, iconic","logprobs":null,"finish_reason":"length","stop_reason":null,"prompt_logprobs":null}],"usage":{"prompt_tokens":5,"total_tokens":35,"completion_tokens":30,"prompt_tokens_details":null}}
```

Check the log of the server-requesting pod.
```console
$ kubectl logs my-request
I0926 09:02:06.183389       1 server.go:64] "starting server" logger="probes-server" port="8080"
I0926 09:02:06.204610       1 server.go:83] "Got GPU UUIDs" logger="spi-server" uuids=["GPU-7450f677-9aa8-0150-8b11-68727d721976"]
I0926 09:02:06.204710       1 server.go:122] "starting server" logger="spi-server" port="8081"
I0926 09:02:07.028900       1 server.go:91] "Setting ready" logger="spi-server" newReady=false
I0926 09:02:07.037853       1 server.go:91] "Setting ready" logger="spi-server" newReady=false
I0926 09:02:08.043815       1 server.go:91] "Setting ready" logger="spi-server" newReady=false
I0926 09:03:24.058287       1 server.go:91] "Setting ready" logger="spi-server" newReady=true
```

Clean up.
```console
$ kubectl delete po my-request
pod "my-request" deleted
$ kubectl get po # a few moments later
NAME                              READY   STATUS    RESTARTS   AGE
ip-172-31-58-228-na               1/1     Running   0          44h
vcp-model-ctlr-67c7c8dd8b-kwrjv   1/1     Running   0          44h
```
