#!/usr/bin/env bash

# Usage: $0
# Current working directory must be the root of the Git repository.
# This script tests launcher-based server-providing pods independently.
#
# Required tools: kubectl, helm, jq, yq (https://github.com/mikefarah/yq).

set -euo pipefail

set -x

nl=$'\n'

function clear_img_repo() (
    set +o pipefail
    docker images --format "table {{.Repository}}\t{{.Tag}}\t{{.CreatedAt}}" $1 | fgrep -v '<none>' | grep -vw REPOSITORY | while read name tag rest; do
	docker rmi $name:$tag
    done
)

: Build the container images, no push

clear_img_repo ko.local/test-requester
clear_img_repo my-registry/my-namespace/test-requester
clear_img_repo my-registry/my-namespace/test-launcher
clear_img_repo ko.local/dual-pods-controller
clear_img_repo my-registry/my-namespace/dual-pods-controller
clear_img_repo ko.local/launcher-populator
clear_img_repo my-registry/my-namespace/launcher-populator
make build-test-requester-local
make build-test-launcher-local
make build-controller-local
make build-populator-local

: Set up the kind cluster

kind delete cluster --name fmatest
kind create cluster --name fmatest --config - <<EOF
kind: Cluster
apiVersion: kind.x-k8s.io/v1alpha4
nodes:
- role: control-plane
- role: worker
EOF

kubectl wait --for=create sa default
kubectl wait --for condition=Ready node fmatest-control-plane
kubectl wait --for condition=Ready node fmatest-worker

# Display health, prove we don't have https://kind.sigs.k8s.io/docs/user/known-issues/#pod-errors-due-to-too-many-open-files
kubectl get pods -A -o wide

kubectl create clusterrole node-viewer --verb=get,list,watch --resource=nodes

kubectl create -f ./config/crd/

kubectl apply -f - <<EOF
apiVersion: rbac.authorization.k8s.io/v1
kind: Role
metadata:
  name: testreq
rules:
- apiGroups:
  - "fma.llm-d.ai"
  resources:
  - inferenceserverconfigs
  - launcherconfigs
  verbs:
  - get
  - list
  - watch
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

kubectl apply -f - <<EOF
apiVersion: rbac.authorization.k8s.io/v1
kind: Role
metadata:
  name: testlauncher
rules:
- apiGroups:
  - ""
  resourceNames:
  - gpu-map
  resources:
  - configmaps
  verbs:
  - get
  - list
  - watch
- apiGroups:
  - ""
  resources:
  - pods
  verbs:
  - get
  - patch
EOF

kubectl create rolebinding testlauncher --role=testlauncher --serviceaccount=$(kubectl get sa default -o jsonpath={.metadata.namespace}):testlauncher

kubectl create sa testlauncher
kubectl create cm gpu-map
kubectl get nodes -o name | sed 's%^node/%%' | while read node; do
    kubectl label node $node nvidia.com/gpu.present=true nvidia.com/gpu.product=NVIDIA-L40S nvidia.com/gpu.count=2 --overwrite=true
    kubectl patch node $node --subresource status -p '{"status": {"capacity": {"nvidia.com/gpu": 2}, "allocatable": {"nvidia.com/gpu": 2} }}'
done

: Load the container images into the kind cluster

make load-test-requester-local
make load-test-launcher-local
make load-controller-local
make load-populator-local

: Populate GPU map for testing

gi=0
kubectl get nodes -o name | sed 's%^node/%%' | while read node; do
    let gi1=gi+1
    kubectl patch cm gpu-map -p "data:${nl} ${node}: '{\"GPU-$gi\": 0, \"GPU-$gi1\": 1 }'"
    let gi=gi1+1
done

: Deploy FMA controllers

FMA_NAMESPACE=default \
FMA_CHART_INSTANCE_NAME=fma \
CONTAINER_IMG_REG=$(make echo-var VAR=CONTAINER_IMG_REG) \
IMAGE_TAG=$(make echo-var VAR=IMAGE_TAG) \
NODE_VIEW_CLUSTER_ROLE=node-viewer \
HELM_EXTRA_ARGS="--set global.local=true" \
./test/e2e/deploy_fma.sh

: Run launcher-based E2E tests

FMA_NAMESPACE=default \
MKOBJS_SCRIPT=./test/e2e/mkobjs.sh \
FMA_CHART_INSTANCE_NAME=fma \
READY_TARGET=1 \
./test/e2e/test-cases.sh
