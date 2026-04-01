#!/usr/bin/env bash

# Runs all launcher-based E2E test scenarios.
#
# Usage: test-cases.sh
# Current working directory must be the root of the Git repository.
#
# Required environment variables:
#   FMA_NAMESPACE           - Kubernetes namespace to run tests in
#   MKOBJS_SCRIPT           - path to the mkobjs script to call
#
# Optional environment variables:
#   FMA_CHART_INSTANCE_NAME - Helm release name prefix (default: fma)
#   READY_TARGET            - minimum ready launchers before proceeding (default: 2)
#   POLICIES_ENABLED        - "true"/"false"; auto-detected if unset
#   E2E_PLATFORM            - "openshift" or "kind" (default: openshift)
#   POLL_LIMIT_SECS         - polling timeout seconds (default: 600)
#   FMA_DEBUG               - "true" to enable shell tracing (set -x)

set -euo pipefail
if [ "${FMA_DEBUG:-false}" = "true" ]; then
    set -x
fi

: "${FMA_NAMESPACE:?FMA_NAMESPACE is required}"
: "${MKOBJS_SCRIPT:?MKOBJS_SCRIPT is required}"

POLL_LIMIT_SECS="${POLL_LIMIT_SECS:-600}"
READY_TARGET="${READY_TARGET:-2}"
FMA_CHART_INSTANCE_NAME="${FMA_CHART_INSTANCE_NAME:-fma}"
E2E_PLATFORM="${E2E_PLATFORM:-openshift}"

NS="$FMA_NAMESPACE"

# ---------------------------------------------------------------------------
# Helpers
# ---------------------------------------------------------------------------

green=$'\033[0;32m'
nocolor=$'\033[0m'
nl=$'\n'

cheer() {
    echo
    echo "${nl}${green}✔${nocolor} $*"
    echo
}

intro_case() {
    echo
    echo "====== Test case: $* ======"
    echo
}

expect() {
    local elapsed=0
    local start=$(date)
    local limit=${POLL_LIMIT_SECS}
    echo "Expecting $1" >&2
    while true; do
        if (( elapsed < 7 || elapsed+7 > POLL_LIMIT_SECS )); then
            kubectl get pods -n "$NS" -L dual-pods.llm-d.ai/dual,dual-pods.llm-d.ai/sleeping
        fi
        if eval "$1"; then return; fi
        if (( elapsed > limit )); then
            echo "Did not become true (from $start to $(date)): $1" >&2
            exit 99
        fi
        sleep 5
        elapsed=$(( elapsed+5 ))
    done
}

# ---------------------------------------------------------------------------
# Create test objects
# ---------------------------------------------------------------------------

intro_case Basic Launcher Pod Creation

objs=$("$MKOBJS_SCRIPT" -n "$NS")
isc=$(echo $objs | awk '{print $1}')
lc=$(echo $objs | awk '{print $2}')
rslb=$(echo $objs | awk '{print $3}')
isc2=$(echo $objs | awk '{print $4}')
# $5 is isc3 (tinyllama) — not used directly but created for completeness
lpp=$(echo $objs | awk '{print $6}')
instlb=${rslb#my-request-}

# LauncherPopulationPolicy specifies launcherCount per node with nvidia.com/gpu.present=true
GPU_NODES=$(kubectl get nodes -l nvidia.com/gpu.present=true --field-selector spec.unschedulable!=true -o name | wc -l | tr -d ' ')
echo "Expecting launcher-populator to create $GPU_NODES launcher(s) (one per schedulable GPU node)"
expect "[ \$(kubectl get pods -n $NS -o name -l dual-pods.llm-d.ai/launcher-config-name=$lc | wc -l | tr -d ' ') -ge $GPU_NODES ]"
echo "Launcher-populator created launchers successfully"
kubectl get pods -n "$NS" -l dual-pods.llm-d.ai/launcher-config-name=$lc

# Wait for READY_TARGET launcher pods to be Ready
echo "Waiting for at least $READY_TARGET launcher pod(s) to be Ready..."
expect "[ \$(kubectl get pods -n $NS -l dual-pods.llm-d.ai/launcher-config-name=$lc -o json | jq '[.items[] | select(.status.conditions[]? | select(.type == \"Ready\" and .status == \"True\"))] | length') -ge $READY_TARGET ]"
echo "At least $READY_TARGET launcher pod(s) are Ready"
kubectl get pods -n "$NS" -l dual-pods.llm-d.ai/launcher-config-name=$lc -o wide

trap 'echo "
reqlb=${reqlb:-}
reqlb2=${reqlb2:-}
reqlb3=${reqlb3:-}
reqlb4=${reqlb4:-}
launcherlb=${launcherlb:-}
launcherlb2=${launcherlb2:-}
launcherlb3=${launcherlb3:-}
launcherlb4=${launcherlb4:-}
testnode=${testnode:-}
"' EXIT

# Expect requester pod to be created
expect "kubectl get pods -n $NS -o name -l app=dp-example,instance=$instlb | wc -l | grep -w 1"

export reqlb=$(kubectl get pods -n "$NS" -o name -l app=dp-example,instance=$instlb | sed s%pod/%%)
echo "Server-requesting Pod is $reqlb"
testnode=$(kubectl get pod $reqlb -n "$NS" -o jsonpath='{.spec.nodeName}')
echo "The test Pods run on Node $testnode"

# Wait for launcher-to-requester binding, then capture the launcher name
expect "kubectl get pods -n $NS -o name -l dual-pods.llm-d.ai/dual=$reqlb | wc -l | grep -w 1"
export launcherlb=$(kubectl get pods -n "$NS" -o name -l dual-pods.llm-d.ai/dual=$reqlb | sed s%pod/%%)
echo "Launcher Pod is $launcherlb"

# Verify requester is bound to launcher (bidirectional check)
expect '[ "$(kubectl get pod -n '"$NS"' $reqlb -o jsonpath={.metadata.labels.dual-pods\\.llm-d\\.ai/dual})" == "$launcherlb" ]'

# Wait for both pods to be ready
date
kubectl wait --for condition=Ready pod/$reqlb -n "$NS" --timeout=180s
[ "$(kubectl get pod $launcherlb -n "$NS" -o jsonpath='{.status.conditions[?(@.type=="Ready")].status}')" = "True" ]

cheer Successful launcher-based pod creation

# ---------------------------------------------------------------------------
# CEL policy verification (if enabled)
# ---------------------------------------------------------------------------

if [ -z "${POLICIES_ENABLED:-}" ]; then
    POLICIES_ENABLED=false
    if kubectl api-resources --api-group=admissionregistration.k8s.io -o name 2>/dev/null \
       | grep -q 'validatingadmissionpolicies'; then
        POLICIES_ENABLED=true
    fi
    echo "Auto-detected POLICIES_ENABLED=$POLICIES_ENABLED"
fi

if [ "$POLICIES_ENABLED" = true ]; then
  intro_case Admission policy enforcement
  if ! test/e2e/validate.sh; then
    echo "ERROR: CEL policy tests failed!" >&2
    exit 1
  fi
  cheer CEL policy checks passed
fi

# TODO: stop skipping once Issues 387 is resolved
if [ "$E2E_PLATFORM" = "openshift" ]; then
    echo "Skipping the remaining test cases on OpenShift because Issue 387 is not resolved there yet" >&2
    cheer All launcher-based tests that are currently expected to pass on OpenShift have done so
    exit 0
fi

# ---------------------------------------------------------------------------
# Same-Node Port Collision
# ---------------------------------------------------------------------------

intro_case Same-Node Port Collision Creates New Launcher

collision_inst="${instlb}-collision"
collision_rs="my-request-collision-$instlb"

kubectl get rs "$rslb" -n "$NS" -o json \
  | jq \
      --arg collision_rs "$collision_rs" \
      --arg collision_inst "$collision_inst" \
      --arg testnode "$testnode" \
      --arg isc "$isc" \
      '
      .metadata.name = $collision_rs |
      del(.metadata.uid, .metadata.resourceVersion, .metadata.creationTimestamp, .metadata.annotations, .metadata.ownerReferences, .status) |
      .spec.replicas = 1 |
      .spec.selector.matchLabels.instance = $collision_inst |
      .spec.template.metadata.labels.instance = $collision_inst |
      .spec.template.spec.nodeSelector = {"kubernetes.io/hostname": $testnode} |
      .spec.template.metadata.annotations["dual-pods.llm-d.ai/inference-server-config"] = $isc
    ' \
  | kubectl apply -n "$NS" -f -

expect "kubectl get pods -n $NS -o name -l app=dp-example,instance=$collision_inst | wc -l | grep -w 1"

collision_req=$(kubectl get pods -n "$NS" -o name -l app=dp-example,instance=$collision_inst | sed s%pod/%%)
echo "Collision requester Pod is $collision_req"

expect '[ "$(kubectl get pod -n '"$NS"' '"$collision_req"' -o jsonpath={.spec.nodeName})" == "'"$testnode"'" ]'
expect "kubectl get pods -n $NS -o name -l dual-pods.llm-d.ai/dual=$collision_req | wc -l | grep -w 1"

collision_launcher=$(kubectl get pods -n "$NS" -o name -l dual-pods.llm-d.ai/dual=$collision_req | sed s%pod/%%)
echo "Collision launcher Pod is $collision_launcher"

[ "$collision_launcher" != "$launcherlb" ]

expect '[ "$(kubectl get pod -n '"$NS"' '"$collision_req"' -o jsonpath={.metadata.labels.dual-pods\\.llm-d\\.ai/dual})" == "'"$collision_launcher"'" ]'

date
kubectl wait --for condition=Ready pod/$collision_req -n "$NS" --timeout=120s
[ "$(kubectl get pod $collision_launcher -n "$NS" -o jsonpath='{.status.conditions[?(@.type=="Ready")].status}')" = "True" ]

req_gpus=$(kubectl get pod "$reqlb" -n "$NS" -o jsonpath='{.metadata.annotations.dual-pods\.llm-d\.ai/accelerators}')
collision_gpus=$(kubectl get pod "$collision_req" -n "$NS" -o jsonpath='{.metadata.annotations.dual-pods\.llm-d\.ai/accelerators}')
[ -n "$req_gpus" ]
[ -n "$collision_gpus" ]
[ "$req_gpus" != "$collision_gpus" ]

kubectl delete rs "$collision_rs" -n "$NS" --wait=true
expect "kubectl get pods -n $NS -o name -l app=dp-example,instance=$collision_inst | wc -l | grep -w 0"
kubectl delete pod "$collision_launcher" -n "$NS" --wait=true
expect '! kubectl get pods -n '"$NS"' -o name | grep -qw pod/'"$collision_launcher"

cheer Successful same-node collision handling

# ---------------------------------------------------------------------------
# Instance Wake-up Fast Path
# ---------------------------------------------------------------------------

intro_case Instance Wake-up Fast Path

# Scale requester to 0 (instance should sleep in launcher)
kubectl scale rs $rslb -n "$NS" --replicas=0

expect "kubectl get pods -n $NS -o name -l app=dp-example,instance=$instlb | wc -l | grep -w 0"

# Patch requester ReplicaSet to stick to testnode
kubectl patch rs $rslb -n "$NS" -p '{"spec": {"template": {"spec": {"nodeSelector": {"kubernetes.io/hostname": "'$testnode'"} }} }}'

# Launcher should remain
kubectl get pod $launcherlb -n "$NS"

# Verify launcher is unbound (no dual label pointing to requester)
expect '[ "$(kubectl get pod -n '"$NS"' $launcherlb -o jsonpath={.metadata.labels.dual-pods\\.llm-d\\.ai/dual})" == "" ]'

# Scale back up (should reuse same launcher and wake sleeping instance)
kubectl scale rs $rslb -n "$NS" --replicas=1

expect "kubectl get pods -n $NS -o name -l app=dp-example,instance=$instlb | wc -l | grep -w 1"

reqlb2=$(kubectl get pods -n "$NS" -o name -l app=dp-example,instance=$instlb | sed s%pod/%%)
echo "Server-requesting Pod2 is $reqlb2"

# Should still be using the same launcher pod
expect "kubectl get pods -n $NS -o name -l dual-pods.llm-d.ai/dual=$reqlb2 | wc -l | grep -w 1"
launcherlb2=$(kubectl get pods -n "$NS" -o name -l dual-pods.llm-d.ai/dual=$reqlb2 | sed s%pod/%%)
[ "$launcherlb2" == "$launcherlb" ]

# Verify requester is bound to launcher (bidirectional check)
expect '[ "$(kubectl get pod -n '"$NS"' $reqlb2 -o jsonpath={.metadata.labels.dual-pods\\.llm-d\\.ai/dual})" == "$launcherlb" ]'

# Wait for requester to be ready (launcher should already be ready)
date
kubectl wait --for condition=Ready pod/$reqlb2 -n "$NS" --timeout=120s
[ "$(kubectl get pod $launcherlb -n "$NS" -o jsonpath='{.status.conditions[?(@.type=="Ready")].status}')" = "True" ]

cheer Successful instance wake-up fast path

# ---------------------------------------------------------------------------
# Multiple Instances Share One Launcher
# ---------------------------------------------------------------------------

intro_case Multiple Instances Share One Launcher

# Scale requester to 0 again
kubectl scale rs $rslb -n "$NS" --replicas=0

expect "kubectl get pods -n $NS -o name -l app=dp-example,instance=$instlb | wc -l | grep -w 0"

# Launcher should remain
kubectl get pod $launcherlb -n "$NS"

# Verify launcher is unbound
expect '[ "$(kubectl get pod -n '"$NS"' $launcherlb -o jsonpath={.metadata.labels.dual-pods\\.llm-d\\.ai/dual})" == "" ]'

# Patch ReplicaSet to use isc2 instead of isc
kubectl patch rs $rslb -n "$NS" -p='{"spec":{"template":{"metadata":{"annotations":{"dual-pods.llm-d.ai/inference-server-config":"'$isc2'"}}}}}'

# Scale back up (should reuse same launcher and create 2nd instance)
kubectl scale rs $rslb -n "$NS" --replicas=1

expect "kubectl get pods -n $NS -o name -l app=dp-example,instance=$instlb | wc -l | grep -w 1"

reqlb3=$(kubectl get pods -n "$NS" -o name -l app=dp-example,instance=$instlb | sed s%pod/%%)
echo "Server-requesting Pod3 is $reqlb3"

# Should still be using the same launcher pod
expect "kubectl get pods -n $NS -o name -l dual-pods.llm-d.ai/dual=$reqlb3 | wc -l | grep -w 1"
launcherlb3=$(kubectl get pods -n "$NS" -o name -l dual-pods.llm-d.ai/dual=$reqlb3 | sed s%pod/%%)
[ "$launcherlb3" == "$launcherlb" ]

# Verify requester is bound to launcher (bidirectional check)
expect '[ "$(kubectl get pod -n '"$NS"' $reqlb3 -o jsonpath={.metadata.labels.dual-pods\\.llm-d\\.ai/dual})" == "$launcherlb" ]'

# Wait for requester to be ready (launcher should already be ready)
date
kubectl wait --for condition=Ready pod/$reqlb3 -n "$NS" --timeout=120s
[ "$(kubectl get pod $launcherlb -n "$NS" -o jsonpath='{.status.conditions[?(@.type=="Ready")].status}')" = "True" ]

cheer Successful multiple instances sharing one launcher

# ---------------------------------------------------------------------------
# Switch Instances In One Launcher
# ---------------------------------------------------------------------------

intro_case Switch Instances In One Launcher

# Scale requester to 0 again
kubectl scale rs $rslb -n "$NS" --replicas=0

expect "kubectl get pods -n $NS -o name -l app=dp-example,instance=$instlb | wc -l | grep -w 0"

# Launcher should remain
kubectl get pod $launcherlb -n "$NS"

# Verify launcher is unbound
expect '[ "$(kubectl get pod -n '"$NS"' $launcherlb -o jsonpath={.metadata.labels.dual-pods\\.llm-d\\.ai/dual})" == "" ]'

# Patch ReplicaSet back to use original isc
kubectl patch rs $rslb -n "$NS" -p='{"spec":{"template":{"metadata":{"annotations":{"dual-pods.llm-d.ai/inference-server-config":"'$isc'"}}}}}'

# Scale back up (should reuse same launcher and wake first instance)
kubectl scale rs $rslb -n "$NS" --replicas=1

expect "kubectl get pods -n $NS -o name -l app=dp-example,instance=$instlb | wc -l | grep -w 1"

reqlb4=$(kubectl get pods -n "$NS" -o name -l app=dp-example,instance=$instlb | sed s%pod/%%)
echo "Server-requesting Pod4 is $reqlb4"

# Should still be using the same launcher pod
expect "kubectl get pods -n $NS -o name -l dual-pods.llm-d.ai/dual=$reqlb4 | wc -l | grep -w 1"
launcherlb4=$(kubectl get pods -n "$NS" -o name -l dual-pods.llm-d.ai/dual=$reqlb4 | sed s%pod/%%)
[ "$launcherlb4" == "$launcherlb" ]

# Verify requester is bound to launcher (bidirectional check)
expect '[ "$(kubectl get pod -n '"$NS"' $reqlb4 -o jsonpath={.metadata.labels.dual-pods\\.llm-d\\.ai/dual})" == "$launcherlb" ]'

# Wait for requester to be ready (launcher should already be ready)
date
kubectl wait --for condition=Ready pod/$reqlb4 -n "$NS" --timeout=120s
[ "$(kubectl get pod $launcherlb -n "$NS" -o jsonpath='{.status.conditions[?(@.type=="Ready")].status}')" = "True" ]

cheer Successful switching instances in one launcher

# ---------------------------------------------------------------------------
# Controller Restart State Recovery
# ---------------------------------------------------------------------------

intro_case Controller Restart State Recovery

# This test verifies that the controller can rebuild its internal state after restart
# by syncing launcher instances from unbound launcher pods

# Scale requester to 0 to create sleeping instances
kubectl scale rs $rslb -n "$NS" --replicas=0

expect "kubectl get pods -n $NS -o name -l app=dp-example,instance=$instlb | wc -l | grep -w 0"

# Verify launcher set is unchanged and target launcher is unbound
launcher_count_pre_restart=$(kubectl get pods -n "$NS" -o name -l dual-pods.llm-d.ai/launcher-config-name=$lc | wc -l)
echo launcher_count_pre_restart = $launcher_count_pre_restart
kubectl get pods -n "$NS" -o name -l dual-pods.llm-d.ai/launcher-config-name=$lc | grep -x "pod/$launcherlb"
expect '[ "$(kubectl get pod -n '"$NS"' $launcherlb -o jsonpath={.metadata.labels.dual-pods\\.llm-d\\.ai/dual})" == "" ]'

# Verify launcher has sleeping instances before restart
launcher_instances_before=$(kubectl exec -n "$NS" $launcherlb -- python3 -c 'import json,urllib.request; print(json.load(urllib.request.urlopen("http://127.0.0.1:8001/v2/vllm/instances"))["total_instances"])')
echo "Launcher has $launcher_instances_before instances before controller restart"
[ "$launcher_instances_before" -gt "0" ]

# Restart the dual-pods controller to test state recovery
echo "Restarting dual-pods controller..."
kubectl rollout restart deployment "${FMA_CHART_INSTANCE_NAME}-dual-pods-controller" -n "$NS"
kubectl rollout status deployment "${FMA_CHART_INSTANCE_NAME}-dual-pods-controller" -n "$NS" --timeout=60s

# Wait for controller to be ready for ongoing checks
# In detail: allow some time for the dual-pods controller to do something unexpected in the case
# that the controller is behaving incorrectly, so that the ongoing checks have some chance to fail
# thus detect the incorrectness, instead of just quickly and coincidentally passing.
sleep 30

# Verify launcher pod set size is unchanged and target launcher is still running
expect "kubectl get pods -n $NS -o name -l dual-pods.llm-d.ai/launcher-config-name=$lc | wc -l | grep -w $launcher_count_pre_restart"
kubectl get pods -n "$NS" -o name -l dual-pods.llm-d.ai/launcher-config-name=$lc | grep -x "pod/$launcherlb"

# Verify launcher still has the same number of instances after controller restart
launcher_instances_after=$(kubectl exec -n "$NS" $launcherlb -- python3 -c 'import json,urllib.request; print(json.load(urllib.request.urlopen("http://127.0.0.1:8001/v2/vllm/instances"))["total_instances"])')
echo "Launcher has $launcher_instances_after instances after controller restart"
[ "$launcher_instances_after" == "$launcher_instances_before" ]

# Now scale up requester - controller should correctly select the launcher with sleeping instance
# Use isc2 which should have a sleeping instance from before
kubectl patch rs $rslb -n "$NS" --type=json -p='[{"op": "replace", "path": "/spec/template/metadata/annotations/dual-pods.llm-d.ai~1inference-server-config", "value": "'$isc2'"}]'
kubectl scale rs $rslb -n "$NS" --replicas=1

expect "kubectl get pods -n $NS -o name -l app=dp-example,instance=$instlb | wc -l | grep -w 1"
reqlb_post_restart=$(kubectl get pods -n "$NS" -o name -l app=dp-example,instance=$instlb | sed s%pod/%%)

# Verify requester is bound to the same launcher (controller recovered state correctly)
expect '[ "$(kubectl get pod -n '"$NS"' $reqlb_post_restart -o jsonpath={.metadata.labels.dual-pods\\.llm-d\\.ai/dual})" == "$launcherlb" ]'
expect '[ "$(kubectl get pod -n '"$NS"' $launcherlb -o jsonpath={.metadata.labels.dual-pods\\.llm-d\\.ai/dual})" == "$reqlb_post_restart" ]'

# Verify requester becomes ready (fast wake-up path should work)
date
kubectl wait --for condition=Ready pod/$reqlb_post_restart -n "$NS" --timeout=30s
[ "$(kubectl get pod $launcherlb -n "$NS" -o jsonpath='{.status.conditions[?(@.type=="Ready")].status}')" = "True" ]

cheer Successful controller restart state recovery

# ---------------------------------------------------------------------------
# Unbound Launcher Deletion Cleanup
# ---------------------------------------------------------------------------

intro_case Unbound Launcher Deletion Cleanup

# This test verifies that deleting an unbound launcher does not leave the controller
# stuck with stale instance state.

kubectl scale rs $rslb -n "$NS" --replicas=0

expect "kubectl get pods -n $NS -o name -l app=dp-example,instance=$instlb | wc -l | grep -w 0"
expect '[ "$(kubectl get pod -n '"$NS"' $launcherlb -o jsonpath={.metadata.labels.dual-pods\\.llm-d\\.ai/dual})" == "" ]'

kubectl delete pod $launcherlb -n "$NS" --wait=true

! kubectl get pods -n "$NS" -o name | grep -qw pod/$launcherlb

kubectl scale rs $rslb -n "$NS" --replicas=1

expect "kubectl get pods -n $NS -o name -l app=dp-example,instance=$instlb | wc -l | grep -w 1"
reqlb_after_delete=$(kubectl get pods -n "$NS" -o name -l app=dp-example,instance=$instlb | sed s%pod/%%)
echo "Server-requesting Pod after delete = $reqlb_after_delete"

expect "kubectl get pods -n $NS -o name -l dual-pods.llm-d.ai/dual=$reqlb_after_delete | wc -l | grep -w 1"
launcherlb_after_delete=$(kubectl get pods -n "$NS" -o name -l dual-pods.llm-d.ai/dual=$reqlb_after_delete | sed s%pod/%%)
echo "Launcher after delete = $launcherlb_after_delete"

[ "$launcherlb_after_delete" != "$launcherlb" ]
expect '[ "$(kubectl get pod -n '"$NS"' $reqlb_after_delete -o jsonpath={.metadata.labels.dual-pods\\.llm-d\\.ai/dual})" == "$launcherlb_after_delete" ]'

date
kubectl wait --for condition=Ready pod/$reqlb_after_delete -n "$NS" --timeout=120s
[ "$(kubectl get pod $launcherlb_after_delete -n "$NS" -o jsonpath='{.status.conditions[?(@.type=="Ready")].status}')" = "True" ]

cheer Successful unbound launcher deletion cleanup

cheer All launcher-based tests passed
