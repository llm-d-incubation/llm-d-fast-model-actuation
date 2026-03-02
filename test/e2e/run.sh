#!/usr/bin/env bash

# Usage: $0
# Current working directory must be the root of the Git repository.
# The reasons for this are the `make` commands, the location of CRDs,
# and the location of the validating admission policy files.

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
    local limit=${LIMIT:-45}
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
clear_img_repo ko.local/test-server
clear_img_repo my-registry/my-namespace/test-server
clear_img_repo ko.local/dual-pods-controller
clear_img_repo my-registry/my-namespace/dual-pods-controller
make build-test-requester-local
make build-test-server-local
make build-controller-local

: Set up the kind cluster

kind delete cluster --name fmatest
kind create cluster --name fmatest --config test/e2e/kind-config.yaml
kubectl wait --for=create sa default
kubectl wait --for condition=Ready node fmatest-control-plane
kubectl wait --for condition=Ready node fmatest-worker
kubectl wait --for condition=Ready node fmatest-worker2

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
kubectl create cm gpu-map
kubectl get nodes -o name | sed 's%^node/%%' | while read node; do
    kubectl label node $node nvidia.com/gpu.present=true nvidia.com/gpu.product=NVIDIA-L40S nvidia.com/gpu.count=2 --overwrite=true
    kubectl patch node $node --subresource status -p '{"status": {"capacity": {"nvidia.com/gpu": 2}, "allocatable": {"nvidia.com/gpu": 2} }}'
done

: Load the container images into the kind cluster

make load-test-requester-local
make load-test-server-local
make load-controller-local

: Detect whether API server supports ValidatingAdmissionPolicy

POLICIES_ENABLED=false
if kubectl api-resources --api-group=admissionregistration.k8s.io -o name | grep -q 'validatingadmissionpolicies'; then
  POLICIES_ENABLED=true
fi

: Deploy the FMA controllers in the cluster

img_reg=$(make echo-var VAR=CONTAINER_IMG_REG)
img_tag=$(make echo-var VAR=IMAGE_TAG)

helm upgrade --install fma charts/fma-controllers \
  --set global.imageRegistry="$img_reg" \
  --set global.imageTag="$img_tag" \
  --set global.nodeViewClusterRole=node-viewer \
  --set dualPodsController.sleeperLimit=2 \
  --set global.local=true \
  --set dualPodsController.debugAcceleratorMemory=false \
  --set global.enableValidationPolicy=${POLICIES_ENABLED} \
  --set launcherPopulator.enabled=false

: Test Pod creation

rs=$(test/e2e/mkrs.sh)
inst=${rs#my-request-}

# Expect only one Pod because controller can not create dual yet
expect "kubectl get pods -o name | grep -c '^pod/$rs' | grep -w 1"

sleep 5
kubectl get pods -o name | grep -c "^pod/$rs" | grep -w 1

gi=0
kubectl get nodes -o name | sed 's%^node/%%' | while read node; do
    let gi1=gi+1
    kubectl patch cm gpu-map -p "data:${nl} ${node}: '{\"GPU-$gi\": 0, \"GPU-$gi1\": 1 }'"
    let gi=gi1+1
done

expect "kubectl get pods -o name | grep -c '^pod/$rs' | grep -w 2"

pods=($(kubectl get pods -o name | grep "^pod/$rs" | sed s%pod/%%))
req=${pods[0]}
prv=${pods[1]}

expect '[ "$(kubectl get pod $req -o jsonpath={.metadata.labels.dual-pods\\.llm-d\\.ai/dual})" == "$prv" ]'

expect '[ "$(kubectl get pod $prv -o jsonpath={.metadata.labels.dual-pods\\.llm-d\\.ai/dual})" == "$req" ]'

date
kubectl wait --for condition=Ready pod/$req --timeout=35s
kubectl wait --for condition=Ready pod/$prv --timeout=1s

cheer Successful upside

: Test node deletion
: expect nothing to remain on the deleted node, replacement pair to appear on other node

doomed=$(kubectl get pods $req -o jsonpath={.spec.nodeName})
keeper=$(kubectl get nodes -o name | sed s%node/%% | grep -vw $doomed | grep -v control-plane)

kubectl delete node $doomed
LIMIT=100 expect '[ $(kubectl get ds -n kube-system kube-proxy -o jsonpath={.status.currentNumberScheduled}) == "2" ]'
expect '! kubectl get pod $req'
expect '! kubectl get pod $prv'

expect "kubectl get pods -o name | grep -c '^pod/$rs' | grep -w 2"

pods=($(kubectl get pods -o name | grep "^pod/$rs" | sed s%pod/%%))
req=${pods[0]}
prv=${pods[1]}

expect '[ "$(kubectl get pod $req -o jsonpath={.metadata.labels.dual-pods\\.llm-d\\.ai/dual})" == "$prv" ]'

expect '[ "$(kubectl get pod $prv -o jsonpath={.metadata.labels.dual-pods\\.llm-d\\.ai/dual})" == "$req" ]'

date
kubectl wait --for condition=Ready pod/$req --timeout=35s
kubectl wait --for condition=Ready pod/$prv --timeout=1s

used=$(kubectl get pod $req -o jsonpath={.spec.nodeName})
[ "$used" == "$keeper" ]

cheer Successful test of node deletion

: Test requester deletion
: expect provider to remain

kubectl scale rs $rs --replicas=0

expect "kubectl get pods -o name | grep -c '^pod/$rs' | grep -w 1"

sleep 10 # does it stay that way?

kubectl get pods -o name | grep -c "^pod/$rs" | grep -w 1

kubectl get pod $prv -L dual-pods.llm-d.ai/dual

cheer Successful requester deletion

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
kubectl wait --for condition=Ready pod/$nrq --timeout=10s
kubectl wait --for condition=Ready pod/$prv --timeout=1s

cheer Successful re-use

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
kubectl wait --for condition=Ready pod/$rq2 --timeout=35s
kubectl wait --for condition=Ready pod/$pv2 --timeout=1s

cheer Successful test of provider deletion

: Test limit on sleeping inference servers

# Leave the existing one occupying one of the cluster's two GPUs;
# cycle through some additional ReplicaSets exercising the other GPU.

# Make ReplicaSet 2, expect its fulfillment, delete the requester, expect the provider to remain

rs2=$(test/e2e/mkrs.sh)

expect "kubectl get pods -o name | grep -c '^pod/$rs2' | grep -w 2"

pods=($(kubectl get pods -o name | grep "^pod/$rs2" | sed s%pod/%%))
req2=${pods[0]}
prv2=${pods[1]}

expect '[ "$(kubectl get pod $req2 -o jsonpath={.metadata.labels.dual-pods\\.llm-d\\.ai/dual})" == "$prv2" ]'

expect '[ "$(kubectl get pod $prv2 -o jsonpath={.metadata.labels.dual-pods\\.llm-d\\.ai/dual})" == "$req2" ]'

date
kubectl wait --for condition=Ready pod/$req2 --timeout=35s
kubectl wait --for condition=Ready pod/$prv2 --timeout=1s

kubectl scale rs $rs2 --replicas=0

expect "kubectl get pods -o name | grep -c '^pod/$rs2' | grep -w 1"
! kubectl get pod $req2
kubectl get pod $prv2


# Make ReplicaSet 3, expect its fulfillment, delete the requester, expect the provider to remain

rs3=$(test/e2e/mkrs.sh)

expect "kubectl get pods -o name | grep -c '^pod/$rs3' | grep -w 2"

pods=($(kubectl get pods -o name | grep "^pod/$rs3" | sed s%pod/%%))
req3=${pods[0]}
prv3=${pods[1]}

expect '[ "$(kubectl get pod $req3 -o jsonpath={.metadata.labels.dual-pods\\.llm-d\\.ai/dual})" == "$prv3" ]'

expect '[ "$(kubectl get pod $prv3 -o jsonpath={.metadata.labels.dual-pods\\.llm-d\\.ai/dual})" == "$req3" ]'

date
kubectl wait --for condition=Ready pod/$req3 --timeout=35s
kubectl wait --for condition=Ready pod/$prv3 --timeout=1s

kubectl scale rs $rs3 --replicas=0

expect "kubectl get pods -o name | grep -c '^pod/$rs3' | grep -w 1"
! kubectl get pod $req3
kubectl get pod $prv3

# Expect provider for ReplicaSet 2 to remain
kubectl get pod $prv2

# Make ReplicaSet 4, expect its fulfillment, delete the requester, expect the provider to remain

rs4=$(test/e2e/mkrs.sh)

expect "kubectl get pods -o name | grep -c '^pod/$rs4' | grep -w 2"

pods=($(kubectl get pods -o name | grep "^pod/$rs4" | sed s%pod/%%))
req4=${pods[0]}
prv4=${pods[1]}

expect '[ "$(kubectl get pod $req4 -o jsonpath={.metadata.labels.dual-pods\\.llm-d\\.ai/dual})" == "$prv4" ]'

expect '[ "$(kubectl get pod $prv4 -o jsonpath={.metadata.labels.dual-pods\\.llm-d\\.ai/dual})" == "$req4" ]'

date
kubectl wait --for condition=Ready pod/$req4 --timeout=35s
kubectl wait --for condition=Ready pod/$prv4 --timeout=1s

kubectl scale rs $rs4 --replicas=0

expect "kubectl get pods -o name | grep -c '^pod/$rs4' | grep -w 1"
! kubectl get pod $req4
kubectl get pod $prv4

# Expect providers for ReplicaSets 2 and 3 to remain
kubectl get pod $prv2
kubectl get pod $prv3

# Make ReplicaSet 5, expect its fulfillment, delete the requester, expect the providers for 3 and 4 to remain, provider for 2 to be gone

rs5=$(test/e2e/mkrs.sh)

expect "kubectl get pods -o name | grep -c '^pod/$rs5' | grep -w 2"

# Provider for RS 2 should be gone or going
if dsts="$(kubectl get pod $prv2 -o jsonpath={.metadata.deletionTimestamp})"
then [ -n "$dsts" ]
fi

pods=($(kubectl get pods -o name | grep "^pod/$rs5" | sed s%pod/%%))
req5=${pods[0]}
prv5=${pods[1]}

expect '[ "$(kubectl get pod $req5 -o jsonpath={.metadata.labels.dual-pods\\.llm-d\\.ai/dual})" == "$prv5" ]'

expect '[ "$(kubectl get pod $prv5 -o jsonpath={.metadata.labels.dual-pods\\.llm-d\\.ai/dual})" == "$req5" ]'

date
kubectl wait --for condition=Ready pod/$req5 --timeout=35s
kubectl wait --for condition=Ready pod/$prv5 --timeout=1s

# Provider for ReplicaSet 2 should actually disappear
expect '! kubectl get pod $prv2'

kubectl scale rs $rs5 --replicas=0

expect "kubectl get pods -o name | grep -c '^pod/$rs5' | grep -w 1"
! kubectl get pod $req5
kubectl get pod $prv5

# Expect providers for ReplicaSets 3 and 4 to remain
kubectl get pod $prv3
kubectl get pod $prv4

cheer Successful test of limit on sleepers
