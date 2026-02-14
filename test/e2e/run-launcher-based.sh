#!/usr/bin/env bash

# Usage: $0
# Current working directory must be the root of the Git repository.
# This script tests launcher-based server-providing pods independently.

set -euo pipefail

set -x

green=$'\033[0;32m'
nocolor=$'\033[0m'
nl=$'\n'

function cheer() {
    echo
    echo "${nl}${green}✔${nocolor} $*"
    echo
}

function expect() {
    local elapsed=0
    local start=$(date)
    local limit=${LIMIT:-600}
    while true; do
	kubectl get pods -L dual-pods.llm-d.ai/dual,dual-pods.llm-d.ai/sleeping
	if eval "$1"; then return; fi
	if (( elapsed > limit )); then
	    echo "Did not become true (from $start to $(date)): $1" >&2
            exit 99
	fi
	sleep 5
	elapsed=$(( elapsed+5 ))
    done
}

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
make build-test-requester-local
make build-test-launcher-local
make build-controller-local

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
EOF

kubectl create rolebinding testreq --role=testreq --serviceaccount=$(kubectl get sa default -o jsonpath={.metadata.namespace}):testreq
kubectl create clusterrolebinding testreq-view --clusterrole=view --serviceaccount=$(kubectl get sa default -o jsonpath={.metadata.namespace}):testreq

kubectl create sa testreq
kubectl create cm gpu-map
kubectl get nodes -o name | sed 's%^node/%%' | while read node; do
    kubectl label node $node nvidia.com/gpu.present=true nvidia.com/gpu.product=NVIDIA-L40S nvidia.com/gpu.count=2 --overwrite=true
    kubectl patch node $node --subresource status -p '{"status": {"capacity": {"nvidia.com/gpu": 2}, "allocatable": {"nvidia.com/gpu": 2} }}'
done

: Load the container images into the kind cluster

make load-test-requester-local
make load-test-launcher-local
make load-controller-local

: Detect whether API server supports ValidatingAdmissionPolicy

POLICIES_ENABLED=false
if kubectl api-resources --api-group=admissionregistration.k8s.io -o name | grep -q 'validatingadmissionpolicies'; then
  POLICIES_ENABLED=true
fi

: Deploy the dual-pods controller in the cluster

ctlr_img=$(make echo-var VAR=CONTROLLER_IMG)

helm upgrade --install dpctlr charts/dual-pods-controller --set Image="$ctlr_img" --set NodeViewClusterRole=node-viewer --set SleeperLimit=2 --set Local=true --set DebugAcceleratorMemory=false --set EnableValidationPolicy=${POLICIES_ENABLED}

: Populate GPU map for testing

gi=0
kubectl get nodes -o name | sed 's%^node/%%' | while read node; do
    let gi1=gi+1
    kubectl patch cm gpu-map -p "data:${nl} ${node}: '{\"GPU-$gi\": 0, \"GPU-$gi1\": 1 }'"
    let gi=gi1+1
done

: Test launcher-based server-providing pods

: Basic Launcher Pod Creation

objs=$(test/e2e/mkobjs.sh)
isc=$(echo $objs | awk '{print $1}')
lc=$(echo $objs | awk '{print $2}')
rslb=$(echo $objs | awk '{print $3}')
instlb=${rslb#my-request-}

# Expect requester pod to be created
expect "kubectl get pods -o name -l app=dp-example,instance=$instlb | grep -c '^pod/' | grep -w 1"

reqlb=$(kubectl get pods -o name -l app=dp-example,instance=$instlb | sed s%pod/%%)

# Expect launcher pod to be created (not a direct provider)
expect "kubectl get pods -o name -l dual-pods.llm-d.ai/launcher-config-name=$lc | grep -c '^pod/' | grep -w 1"

launcherlb=$(kubectl get pods -o name -l dual-pods.llm-d.ai/launcher-config-name=$lc | sed s%pod/%%)

# Verify requester is bound to launcher
expect '[ "$(kubectl get pod $reqlb -o jsonpath={.metadata.labels.dual-pods\\.llm-d\\.ai/dual})" == "$launcherlb" ]'

# Verify launcher is bound to requester
expect '[ "$(kubectl get pod $launcherlb -o jsonpath={.metadata.labels.dual-pods\\.llm-d\\.ai/dual})" == "$reqlb" ]'

# Wait for both pods to be ready
date
kubectl wait --for condition=Ready pod/$reqlb --timeout=60s
kubectl wait --for condition=Ready pod/$launcherlb --timeout=60s

cheer Successful launcher-based pod creation

: Instance Wake-up Fast Path

# Scale requester to 0 (instance should sleep in launcher)
kubectl scale rs $rslb --replicas=0

expect "kubectl get pods -o name -l app=dp-example,instance=$instlb | grep -c '^pod/' || true | grep -w 0"
! kubectl get pod $reqlb

# Launcher should remain
kubectl get pod $launcherlb

# Verify launcher is unbound (no dual label pointing to requester)
expect '[ "$(kubectl get pod $launcherlb -o jsonpath={.metadata.labels.dual-pods\\.llm-d\\.ai/dual})" == "" ]'

sleep 5

# Scale back up (should reuse same launcher and wake sleeping instance)
kubectl scale rs $rslb --replicas=1

expect "kubectl get pods -o name -l app=dp-example,instance=$instlb | grep -c '^pod/' | grep -w 1"

reqlb2=$(kubectl get pods -o name -l app=dp-example,instance=$instlb | sed s%pod/%%)

# Should still be using the same launcher pod
launcherlb2=$(kubectl get pods -o name -l dual-pods.llm-d.ai/launcher-config-name=$lc | sed s%pod/%%)
[ "$launcherlb2" == "$launcherlb" ]

# Verify new requester is bound to same launcher
expect '[ "$(kubectl get pod $reqlb2 -o jsonpath={.metadata.labels.dual-pods\\.llm-d\\.ai/dual})" == "$launcherlb" ]'

# Verify launcher is bound to new requester
expect '[ "$(kubectl get pod $launcherlb -o jsonpath={.metadata.labels.dual-pods\\.llm-d\\.ai/dual})" == "$reqlb2" ]'

# Wait for requester to be ready (launcher should already be ready)
date
kubectl wait --for condition=Ready pod/$reqlb2 --timeout=30s
kubectl wait --for condition=Ready pod/$launcherlb --timeout=5s

cheer Successful instance wake-up fast path

: Clean up launcher-based workloads

kubectl delete rs $rslb --ignore-not-found=true
kubectl delete inferenceserverconfig $isc --ignore-not-found=true
kubectl delete launcherconfig $lc --ignore-not-found=true
expect '[ $(kubectl get pods -o name | grep -c "^pod/my-request-") == "0" ]'

cheer All launcher-based tests passed
