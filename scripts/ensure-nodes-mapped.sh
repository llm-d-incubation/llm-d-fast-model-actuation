#!/usr/bin/env bash

# Usage: $0
# Working directory is irrelevant.

# Purpose: ensure that a ConfigMap named "gpu-map" exists in the current
# namespace and has a data item for every node with an nvidia GPU.
# The value supplied by this script for a given node is the JSON
# for a map from GPU UUID to GPU index.

set -e

if ! kubectl get cm gpu-map &> /dev/null; then
    kubectl create cm gpu-map
fi

kubectl delete pods -l app=gather-gpu-map

for node in $(kubectl get node -l nvidia.com/gpu.present=true -o name | sed 's$node/$$'); do
    echo "Considering $node"
    got=$(kubectl get cm gpu-map -o jsonpath="{.data.${node}}")
    if [ -n "$got" ]; then continue; fi
    kubectl create -f - <<EOF
apiVersion: v1
kind: Pod
metadata:
  name: ${node}-map
  labels:
    app: gather-gpu-map
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
    kubectl delete pod ${node}-map
    map_qq=$(sed 's/"/\\\"/g' <<<$map)
    kubectl patch cm gpu-map -p "{\"data\": {\"${node}\": \"{${map_qq%,}}\"}}"
done
