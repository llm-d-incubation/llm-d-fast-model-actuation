Build and push gpu-allocator container image (use your favorate `ALLOCATOR_IMG_REG`).
```shell
make build-gpu-allocator ALLOCATOR_IMG_REG=quay.io/junatibm
make push-gpu-allocator ALLOCATOR_IMG_REG=quay.io/junatibm
```

Create a pod to test the gpu-allocator container.
```shell
kubectl apply -f - <<EOF
apiVersion: v1
kind: Pod
metadata:
  name: my-server-request
  labels:
    app: my-server-request
spec:
  containers:
    - name: gpu-allocator
      image: quay.io/junatibm/gpu-allocator:latest
      imagePullPolicy: Always
      ports:
        - containerPort: 8080
      readinessProbe:
        httpGet:
          path: /readyz
          port: 8080
        initialDelaySeconds: 2
        periodSeconds: 5
        failureThreshold: 3
      resources:
        limits:
          nvidia.com/gpu: "1"
          cpu: "1"
          memory: 250Mi
EOF
```

Check the readiness and exposed GPU.
```console
(vllm) ubuntu@ip-172-31-58-228:~/llm-d-fast-model-actuation$ kubectl get po my-server-request -owide
NAME                READY   STATUS    RESTARTS   AGE   IP           NODE               NOMINATED NODE   READINESS GATES
my-server-request   1/1     Running   0          57s   10.0.0.106   ip-172-31-58-228   <none>           <none>
(vllm) ubuntu@ip-172-31-58-228:~/llm-d-fast-model-actuation$ thepodip=10.0.0.106
(vllm) ubuntu@ip-172-31-58-228:~/llm-d-fast-model-actuation$ curl $thepodip:8080/readyz
ready(vllm) ubuntu@ip-172-31-58-228:~/llm-d-fast-model-actuation$
(vllm) ubuntu@ip-172-31-58-228:~/llm-d-fast-model-actuation$ curl $thepodip:8080/dual-pod/accelerators
["GPU-b88683d2-a275-15af-5633-d569397bd622"]
(vllm) ubuntu@ip-172-31-58-228:~/llm-d-fast-model-actuation$
```
