#!/usr/bin/env bash

set -e
set -x

mapkey='dual-pod.llm-d.ai/gpu-map'
mapkeye='dual-pod\.llm-d\.ai/gpu-map'

for node in $(kubectl get node -l nvidia.com/gpu.present=true -o name | sed 's$node/$$'); do
    echo "Considering $node"
    got=$(kubectl get node $node -o jsonpath="{.metadata.annotations.$mapkeye}")
    if [ -n "$got" ]; then continue; fi
    kubectl create -f - <<EOF
apiVersion: v1
kind: Pod
metadata:
  name: ${node}-map
spec:
  restartPolicy: OnFailure
  containers:
  - name: c1
    image: docker.io/vllm/vllm-openai
    command: [ "nvidia-smi", "--query-gpu=index,uuid", "--format=csv,noheader"]
    resources:
      limits:
        nvidia.com/gpu: "0"
      requests:
        cpu: "2"
        memory: 1Gi
        nvidia.com/gpu: "0"
  nodeSelector:
    kubernetes.io/hostname: "$node"
EOF
    kubectl wait pod/${node}-map --for='jsonpath={.status.phase}'=Succeeded
    map=$(kubectl logs ${node}-map | sort -n -t, -k1 | while read index id; do echo -n " \"$id\": $index"; done)
    kubectl annotate node $node "${mapkey}={${map%,}}"
done
