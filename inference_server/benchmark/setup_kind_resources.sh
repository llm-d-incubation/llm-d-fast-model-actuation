#!/usr/bin/env bash

# This script draws most of its inspiration and content from: https://github.com/llm-d-incubation/llm-d-fast-model-actuation/blob/main/test/e2e/run.sh
# It enables the launching of a kind cluster and loading of images within it
# to run the benchmark for fast model actuation Go modules.

# The Current working directory must be the root of the repo to take
# advantage of the `make` commands and other automation.

set -x


# Set up the kind cluster.
clusterName="$1"
printf "Setting up cluster: %s\n" "$clusterName"
requesterImageTag="$2"
printf "RequesterImage Tag: %s\n" "$requesterImageTag"

# Build the container images for local testing.
make build-test-requester-local IMAGE_TAG="$requesterImageTag"
make build-test-server-local IMAGE_TAG="$requesterImageTag"
make build-controller-local IMAGE_TAG="$requesterImageTag"

# Recreate the cluster if it already exists.
kind delete cluster --name "$clusterName"
kind create cluster --name "$clusterName" --config test/e2e/kind-config.yaml
kubectl wait --for=create sa default
kubectl wait --for condition=Ready node fmatest-control-plane
kubectl wait --for condition=Ready node fmatest-worker
kubectl wait --for condition=Ready node fmatest-worker2

# Display health, prove we don't have https://kind.sigs.k8s.io/docs/user/known-issues/#pod-errors-due-to-too-many-open-files
kubectl get pods -A -o wide

kubectl create clusterrole node-viewer --verb=get,list,watch --resource=nodes

kubectl apply -f - <<EOF
apiVersion: rbac.authorization.k8s.io/v1
kind: Role
metadata:
  name: testreq
rules:
- apiGroups:
  - ""
  resourceNames:
  - gpu-map
  - gpu-allocs
  resources:
  - configmaps
  verbs:
  - update
  - patch
  - get
  - list
  - watch
- apiGroups:
  - ""
  resources:
  - configmaps
  verbs:
  - create
- apiGroups:
  - ""
  resources:
  - pods
  verbs:
  - get
  - list
  - watch
EOF

kubectl create rolebinding testreq --role=testreq --serviceaccount=$(kubectl get sa default -o jsonpath={.metadata.namespace}):testreq

kubectl create sa testreq
kubectl create cm gpu-map
kubectl get nodes -o name | sed 's%^node/%%' | while read node; do
    kubectl label node $node nvidia.com/gpu.present=true nvidia.com/gpu.product=NVIDIA-L40S nvidia.com/gpu.count=2 --overwrite=true
    kubectl patch node $node --subresource status -p '{"status": {"capacity": {"nvidia.com/gpu": 2}, "allocatable": {"nvidia.com/gpu": 2} }}'
done

# Load the container images into the kind cluster
make load-test-requester-local CLUSTER_NAME="$clusterName" IMAGE_TAG="$requesterImageTag"
make load-test-server-local CLUSTER_NAME="$clusterName" IMAGE_TAG="$requesterImageTag"
make load-controller-local CLUSTER_NAME="$clusterName" IMAGE_TAG="$requesterImageTag"

# Wait a few seconds for all machinery to come online before issuing requests.
sleep 10
