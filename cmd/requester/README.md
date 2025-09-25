This document shows the steps to exercise the requester and dual-pods controller in a local k8s environment.

Build the requester container image (use your favorate `REQUESTER_IMG_REG`).
```shell
make build-requester REQUESTER_IMG_REG=$REQUESTER_IMG_REG
```

In a 2nd terminal, run the dual-pods controller.
```shell
go run ./cmd/dual-pods-controller/ -v 5
```

Switch back to the 1st terminal, create a server-requesting pod.
```shell
kubectl apply -f - <<EOF
apiVersion: v1
kind: Pod
metadata:
  name: my-request
  annotations:
    dual-pod.llm-d.ai/role: requester
    dual-pod.llm-d.ai/admin-port: "8081"
    dual-pod.llm-d.ai/server-patch: |
      spec:
        containers:
        - name: inference-server
          image: docker.io/vllm/vllm-openai:v0.8.5
          command:
          - vllm
          - serve
          - --port=8000
          - ibm-granite/granite-3.3-2b-instruct
          # - /pvcs/local/hf/models--deepseek-ai--DeepSeek-R1-Distill-Qwen-32B/snapshots/711ad2ea6aa40cfca18895e8aca02ab92df1a746
          - --max-model-len=32768
          env:
          - name: CUDA_VISIBLE_DEVICES
            value: "{{ .GPUIndices }}"
          # - name: VLLM_CACHE_ROOT
          #   value: /pvcs/shared/vllm
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
          # volumeMounts:
          # - name: local
          #   readOnly: true
          #   mountPath: /pvcs/local
        # volumes:
        # - name: local
        #   persistentVolumeClaim:
        #     claimName: {{ .LocalVolume }}
spec:
  containers:
    - name: requester
      image: ${REQUESTER_IMG_REG}/requester:latest
      imagePullPolicy: IfNotPresent
      ports:
        - containerPort: 8080
        - containerPort: 8081
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
In the yaml above, the currently commented lines are for model caching.

Check the allocated GPU.
```console
$ kubectl get po my-request -owide
NAME         READY   STATUS    RESTARTS   AGE   IP           NODE               NOMINATED NODE   READINESS GATES
my-request   0/1     Running   0          12s   10.0.0.111   ip-172-31-58-228   <none>           <none>
$ REQ_IP=10.0.0.111
$ curl $REQ_IP:8081/v1/dual-pod/accelerators
["GPU-b26140c6-bd79-2798-d936-0ed16a4f0733"]
```

Check the controller-created server-running pod.
```console
$ kubectl get po my-request-server -oyaml | yq .metadata
annotations:
  dual-pod.llm-d.ai/role: runner
  kubectl.kubernetes.io/last-applied-configuration: |
    {"apiVersion":"v1","kind":"Pod","metadata":{"annotations":{"dual-pod.llm-d.ai/admin-port":"8081","dual-pod.llm-d.ai/role":"requester","dual-pod.llm-d.ai/server-patch":"spec:\n  containers:\n  - name: inference-server\n    image: docker.io/vllm/vllm-openai:v0.8.5\n    command:\n    - vllm\n    - serve\n    - --port=8000\n    - ibm-granite/granite-3.3-2b-instruct\n    # - /pvcs/local/hf/models--deepseek-ai--DeepSeek-R1-Distill-Qwen-32B/snapshots/711ad2ea6aa40cfca18895e8aca02ab92df1a746\n    - --max-model-len=32768\n    env:\n    - name: CUDA_VISIBLE_DEVICES\n      value: \"{{ .GPUIndices }}\"\n    # - name: VLLM_CACHE_ROOT\n    #   value: /pvcs/shared/vllm\n    resources:\n      limits:\n        cpu: \"2\"\n        memory: 6Gi\n    readinessProbe:\n      httpGet:\n        path: /health\n        port: 8000\n      initialDelaySeconds: 60\n      periodSeconds: 5\n    # volumeMounts:\n    # - name: local\n    #   readOnly: true\n    #   mountPath: /pvcs/local\n  # volumes:\n  # - name: local\n  #   persistentVolumeClaim:\n  #     claimName: {{ .LocalVolume }}\n"},"labels":{"app":"my-request"},"name":"my-request","namespace":"default"},"spec":{"containers":[{"image":"quay.io/my-namespace/requester:latest","imagePullPolicy":"IfNotPresent","name":"requester","ports":[{"containerPort":8080},{"containerPort":8081}],"readinessProbe":{"httpGet":{"path":"/ready","port":8080},"initialDelaySeconds":2,"periodSeconds":5},"resources":{"limits":{"cpu":"1","memory":"250Mi","nvidia.com/gpu":"1"}}}]}}
creationTimestamp: "2025-09-24T01:58:04Z"
name: my-request-server
namespace: default
ownerReferences:
  - apiVersion: v1
    blockOwnerDeletion: true
    controller: true
    kind: Pod
    name: my-request
    uid: 2f8f375b-1b1a-4a31-8215-affe151a0519
resourceVersion: "22030645"
uid: 1de925c4-d29a-4dd3-ab00-bb8b542df469
$ kubectl get po my-request-server -oyaml | yq .spec.containers[0]
command:
  - vllm
  - serve
  - --port=8000
  - ibm-granite/granite-3.3-2b-instruct
  - --max-model-len=32768
env:
  - name: CUDA_VISIBLE_DEVICES
    value: "0"
image: docker.io/vllm/vllm-openai:v0.8.5
imagePullPolicy: IfNotPresent
name: inference-server
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
    cpu: "2"
    memory: 6Gi
    nvidia.com/gpu: "0"
terminationMessagePath: /dev/termination-log
terminationMessagePolicy: File
volumeMounts:
  - mountPath: /var/run/secrets/kubernetes.io/serviceaccount
    name: kube-api-access-lz97w
    readOnly: true
```

Make inference request after the server-running pod becomes ready.
```console
$ kubectl wait pod/my-request-server --for=condition=Ready --timeout=120s
pod/my-request-server condition met
$ kc get po -owide
NAME                READY   STATUS    RESTARTS   AGE     IP           NODE               NOMINATED NODE   READINESS GATES
my-request          1/1     Running   0          4m26s   10.0.0.221   ip-172-31-58-228   <none>           <none>
my-request-server   1/1     Running   0          4m18s   10.0.0.204   ip-172-31-58-228   <none>           <none>
$ curl -s http://10.0.0.204:8000/v1/completions \
  -H "Content-Type: application/json" \
  -d '{
    "model": "ibm-granite/granite-3.3-2b-instruct",
    "prompt": "The capital of France is",
    "max_tokens": 30
  }'
{"id":"cmpl-9b6ccd4b6bc24459bd3d55075f89c349","object":"text_completion","created":1758680479,"model":"ibm-granite/granite-3.3-2b-instruct","choices":[{"index":0,"text":" Paris.\nThe capital of Spain is Madrid.\nThe capital of Italy is Rome.\nThe capital of Germany is Ber","logprobs":null,"finish_reason":"length","stop_reason":null,"prompt_logprobs":null}],"usage":{"prompt_tokens":5,"total_tokens":35,"completion_tokens":30,"prompt_tokens_details":null}}
```

Check the relayed readiness.
```console
$ curl $REQ_IP:8080/ready
OK
```

Check the log of the server-requesting pod.
```console
$ kubectl logs my-request
I0924 02:12:54.268374       1 server.go:73] "starting server" logger="probes-server" port="8080"
I0924 02:12:54.268426       1 server.go:117] "starting server" logger="spi-server" port="8081"
I0924 02:12:54.289478       1 server.go:126] "Got GPU UUIDs" logger="spi-server" uuids=["GPU-b26140c6-bd79-2798-d936-0ed16a4f0733"]
I0924 02:18:34.915124       1 server.go:91] "Setting ready" logger="spi-server" newReady=true
```

Clean up.
```console
$ kubectl delete po my-request
pod "my-request" deleted
```
