This document shows the steps to exercise the requester in a local k8s environment.

Build the requester container image (use your favorate `REQUESTER_IMG_REG`).
```shell
REQUESTER_IMG_REG=quay.io/my-namespace
make build-requester REQUESTER_IMG_REG=$REQUESTER_IMG_REG
```

Create a pod.
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
    - name: requester
      image: ${REQUESTER_IMG_REG}/requester:latest
      imagePullPolicy: IfNotPresent
      ports:
        - containerPort: 8080
        - containerPort: 8081
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

Check the allocated GPU.
```console
(vllm) ubuntu@ip-172-31-58-228:~/llm-d-fast-model-actuation$ kubectl get po my-server-request -owide
NAME                READY   STATUS    RESTARTS   AGE   IP           NODE               NOMINATED NODE   READINESS GATES
my-server-request   0/1     Running   0          26s   10.0.0.233   ip-172-31-58-228   <none>           <none>
(vllm) ubuntu@ip-172-31-58-228:~/llm-d-fast-model-actuation$ thepodip=10.0.0.233
(vllm) ubuntu@ip-172-31-58-228:~/llm-d-fast-model-actuation$ curl $thepodip:8081/v1/dual-pod/accelerators
["GPU-845e0388-9896-3a61-8ac9-3643833770d2"]
```

Mock the relayed readiness.
```console 
(vllm) ubuntu@ip-172-31-58-228:~/llm-d-fast-model-actuation$ curl $thepodip:8080/ready
Service Unavailable
(vllm) ubuntu@ip-172-31-58-228:~/llm-d-fast-model-actuation$ curl -X PUT "http://$thepodip:8081/ip"   -H "Content-Type: text/plain"   --data "1.2.3.4"
OK
(vllm) ubuntu@ip-172-31-58-228:~/llm-d-fast-model-actuation$ curl $thepodip:8080/ready
OK
(vllm) ubuntu@ip-172-31-58-228:~/llm-d-fast-model-actuation$ curl -X DELETE "http://$thepodip:8081/ip"
(vllm) ubuntu@ip-172-31-58-228:~/llm-d-fast-model-actuation$ curl $thepodip:8080/ready
Service Unavailable
```

Show the log of the pod.
```console
(vllm) ubuntu@ip-172-31-58-228:~/llm-d-fast-model-actuation$ kubectl logs my-server-request
I0918 05:42:24.411639       1 server.go:117] "starting server" logger="spi-server" port="8081"
I0918 05:42:24.411640       1 server.go:73] "starting server" logger="probes-server" port="8080"
I0918 05:42:24.431092       1 server.go:126] "Got GPU UUIDs" logger="spi-server" uuids=["GPU-7d4a903d-6045-642b-12fd-db4207cd82c4"]
I0918 05:44:06.689257       1 server.go:62] "received IP, setting readiness to true" logger="probes-server" ip="1.2.3.4"
I0918 05:44:22.127723       1 server.go:58] "received empty IP, setting readiness to false" logger="probes-server"
```

Clean up.
```console
(vllm) ubuntu@ip-172-31-58-228:~/llm-d-fast-model-actuation$ kubectl delete po my-server-request
pod "my-server-request" deleted
```
