# Pool Manager overview

👉 The Pool Manager is a sub-component of the dual-pod controller system. It provisions and manages a pool of idle pods that run only the Launcher process. When requested, it transitions these pods through different lifecycle states (idle → active → sleeping → awake → deleted). This ensures fast activation of server-running vLLM pods while minimizing cold-start latency.

## Sequence Flow

### Example:

- A user creates a server-requesting pod,
- The Dual-Pod Controller watches it and asks Pool Manager for a server-running pod,
- Pool Manager allocates an idle pod from its pool,
- Pool Manager issues POST /v1/vllm to the pod's launcher with server-patch options,
- Launcher starts the vLLM inference server subprocess inside the same pod,
- Pool Manager patches labels so the pod joins an IGW InferencePool,
- Requests may be routed directly to the pod bypassing the launcher,
- On scale-down, Pool Manager may issue DELETE /v1/vllm to the launcher, terminating vLLM before pod deletion.

## Pod States

| State       | Description                                  |
|------------|----------------------------------------------|
| `idle`        | Pod only runs launcher as main process (no model loaded)                 |
| `active`      | Pod has vLLM subprocess actively serving requests  |
| `deleted`     | Pod removed from the pool                   |

📝 Sleeping/awake states will be introduced in future iterations.

---

## Pod labels for EPP InferencePool Recognition

```yaml
metadata:
  labels:
    inference.k8s.io/pool: pool-A
    inference.k8s.io/model: meta-llama/Llama-3.1-8B-Instruct
```

## Pod Spec

```
apiVersion: v1
kind: Pod
metadata:
  generateName: vllm-pool-
  labels:
    inference.k8s.io/pool: pool-A
    inference.k8s.io/model: placeholder   # replaced at activation
    llm-d.ai/role: server-running
  annotations:
    pool.llm-d.ai/state: idle
spec:
  restartPolicy: Always
  containers:
  - name: launcher
    image: yourorg/vllm-launcher:latest
    ports:
    - containerPort: 8000   # admin port for POST/DELETE
    resources:
      limits:
        nvidia.com/gpu: 1   # if GPU required
  # vLLM server process will be started by the launcher
...

```

# Summary

- Pool Manager runs inside the Dual-Pod Controller
- It provisions idle pods running the Launchers 
- Pods are activated via POST (vLLM subprocess started) and torn down via DELETE.
- IGW only sees active inference server pods; Launchers are not visible externally
