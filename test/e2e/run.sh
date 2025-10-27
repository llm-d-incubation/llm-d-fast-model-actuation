#!/usr/bin/env bash

# Usage: $0
# Current working directory must be the root of the Git repository.
# The only reason for this is the `make` commands.

set -x

function expect() {
    local tries=1
    local start=$(date)
    while true; do
	kubectl get pods -L dual-pods.llm-d.ai/dual
	if eval "$1"; then return; fi
	if (( tries > 15 )); then
	    echo "Did not become true (from $start to $(date)): $1" >&2
            exit 99
	fi
	sleep 5
	tries=$(( tries+1 ))
    done
}

set -euo pipefail

: Build the container images, no push

make build-test-requester-local
make build-test-server-local
make build-controller-local

: Set up the kind cluster

kind delete cluster --name fmatest
kind create cluster --name fmatest
kubectl wait --for=create sa default

kubectl create clusterrole node-viewer --verb=get,list,watch --resource=nodes

# The following is needed because the Helm chart is buggy (creates a RoleBinding)
kubectl create clusterrolebinding dpctlr-node-viewer --clusterrole=node-viewer --serviceaccount=$(kubectl get sa default -o jsonpath={.metadata.namespace}):dual-pods-controller

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
EOF

kubectl create rolebinding testreq --role=testreq --serviceaccount=$(kubectl get sa default -o jsonpath={.metadata.namespace}):testreq
kubectl create clusterrolebinding testreq-view --clusterrole=view --serviceaccount=$(kubectl get sa default -o jsonpath={.metadata.namespace}):testreq


kubectl create sa testreq
kubectl create cm gpu-map
nl=$'\n'
gi=0
kubectl get nodes -o name | sed 's%^node/%%' | while read node; do
    kubectl label node $node nvidia.com/gpu.present=true nvidia.com/gpu.product=NVIDIA-L40S nvidia.com/gpu.count=2 --overwrite=true
    kubectl patch node $node --subresource status -p '{"status": {"capacity": {"nvidia.com/gpu": 2}, "allocatable": {"nvidia.com/gpu": 2} }}'
    let gi1=gi+1
    kubectl patch cm gpu-map -p "data:${nl} ${node}: '{\"GPU-$gi\": 0, \"GPU-$gi1\": 1 }'"
    let gi=gi1+1
done

: Load the container images into the kind cluster

make load-test-requester-local
make load-test-server-local
make load-controller-local

: Deploy the dual-pods controller in the cluster

ctlr_img=$(make echo-var VAR=CONTROLLER_IMG)
helm upgrade --install dpctlr charts/dpctlr --set Image="$ctlr_img" --set SleeperLimit=1 --set Local=true

: Test Pod creation

rs=$(test/e2e/mkrs.sh)
inst=${rs#my-request-}

expect "kubectl get pods -o name | grep -c '^pod/$rs' | grep -w 2"

pods=($(kubectl get pods -o name | grep "^pod/$rs" | sed s%pod/%%))
req=${pods[0]}
prv=${pods[1]}

expect '[ "$(kubectl get pod $req -o jsonpath={.metadata.labels.dual-pods\\.llm-d\\.ai/dual})" == "$prv" ]'

expect '[ "$(kubectl get pod $prv -o jsonpath={.metadata.labels.dual-pods\\.llm-d\\.ai/dual})" == "$req" ]'

date
kubectl wait --for condition=Ready pod/$req --timeout=60s
kubectl wait --for condition=Ready pod/$prv --timeout=1s

echo Successful upside

: Test requester deletion
: expect provider to remain

kubectl scale rs $rs --replicas=0

expect "kubectl get pods -o name | grep -c '^pod/$rs' | grep -w 1"

sleep 10 # does it stay that way?

kubectl get pods -o name | grep -c "^pod/$rs" | grep -w 1

kubectl get pod $prv -L dual-pods.llm-d.ai/dual

echo Successful requester deletion

: Scale back up and check for re-use of existing provider

kubectl scale rs $rs --replicas=1

expect "kubectl get pods -o name | grep -c '^pod/$rs' | grep -w 2"

sleep 10

kubectl get pods -o name | grep -c "^pod/$rs" | grep -w 2
kubectl get pods -o name -l app=dp-example | grep -c "^pod/$rs" | grep -w 1

nrq=$(kubectl get pods -o name -l app=dp-example | grep "^pod/$rs" | sed s%pod/%%)

[ "$nrq" != "$req" ]

expect '[ "$(kubectl get pod $nrq -o jsonpath={.metadata.labels.dual-pods\\.llm-d\\.ai/dual})" == "$prv" ]'

expect '[ "$(kubectl get pod $prv -o jsonpath={.metadata.labels.dual-pods\\.llm-d\\.ai/dual})" == "$nrq" ]'

date
kubectl wait --for condition=Ready pod/$nrq --timeout=60s
kubectl wait --for condition=Ready pod/$prv --timeout=1s

echo Successful re-use

: Test provider deletion
: expect requester to be deleted and a new pair to appear

kubectl delete pod $prv

expect "! kubectl get pods -o name | grep '^pod/$nrq'"

expect "kubectl get pods -o name | grep -c '^pod/$rs' | grep -w 2"
pods=($(kubectl get pods -o name | grep "^pod/$rs" | sed s%pod/%%))
rq2=${pods[0]}
pv2=${pods[1]}

[ "$rq2" != "$req" ]
[ "$rq2" != "$nrq" ]
[ "$pv2" != "$prv" ]

expect '[ "$(kubectl get pod $rq2 -o jsonpath={.metadata.labels.dual-pods\\.llm-d\\.ai/dual})" == "$pv2" ]'

expect '[ "$(kubectl get pod $pv2 -o jsonpath={.metadata.labels.dual-pods\\.llm-d\\.ai/dual})" == "$rq2" ]'

date
kubectl wait --for condition=Ready pod/$rq2 --timeout=60s
kubectl wait --for condition=Ready pod/$pv2 --timeout=1s

echo Successful test of provider deletion
