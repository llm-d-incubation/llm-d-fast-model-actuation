# What is the Pool Manager and what does it do?

👉 A component that provisions and manages vLLM pods within the Dual-Pod (DP) Controller architecture. Can be a Kubernetes native service that maintains a pool of idle pods running just the launcher, pre-labeled for IGW’s InferencePool, and transitions them through some different lifecycle states (idle → active → sleeping → deleted) to ensure fast activation of server-running vLLM pods while minimizing cold-start latency.

Flow may look like this for create:

- EPP scheduler creates a server-requesting pod,
- DP Controller watches it and asks the pool manager for a server-running pod,
- Pool Manager allocates an idle pod and issues POST /v1/vllm to its launcher,
- Launcher starts the vLLM inference server inside the same pod,
- Pool Manager patches labels to add server-running pod to IGW InferencePool,
- Queries may be routed directly to the inference server pod (without launcher),
- When scaling down, pool manager may issue a DELETE /v1/vllm to terminate the inference server before pod deletion.

Queries can now reach the inference server directly.

## Pod States (WIP)

Idle state at startup — the pod should boot with just the launcher (no model loaded). When dual-pod requests for an endpoint and Pool Manager assigns one, it calls POST /vllm inside the pod to start vllm serve. Once active, the pod becomes a valid serving endpoint in the InferencePool.


| State       | Description                                  |
|------------|----------------------------------------------|
| `idle`        | Pod only runs launcher                  |
| `active`      | Pod has vLLM actively serving requests  |
| `deleted`     | Pod removed from pool                   |

📝 Sleeping/wake logic will be introduced in future iterations but is not part of this version.
---

## Pod Requirements

### Labels for EPP InferencePool Recognition

```yaml
metadata:
  labels:
    inference.k8s.io/pool: pool-A
    inference.k8s.io/model: meta-llama/Llama-3.1-8B-Instruct
```

Pool manager ensures there enough idle pods exist in the pool so EPP can always schedule.

### Pod spec: (WIP)

Each vLLM pod contains:

 - Launcher - receives POST/DELETE to start/stop inference server

 - Inference Server (vLLM) – started dynamically by Launcher on POST..

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
  # vLLM container will be started by the launcher
...

```

# Summary

- Pool Manager runs inside the Dual-Pod Controller
- It provisions idle pods with Launchers
- It transitions pods via POST → active and DELETE → deleted
- IGW only sees active inference server pods; Launchers are not visible externally
