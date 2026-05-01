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
            return 99
        fi
        sleep 5
        elapsed=$(( elapsed+5 ))
    done
}

# pin_gpu patches the ReplicaSet to bypass OpenShift's GPU assignment.
# Sets nvidia.com/gpu limit/request to 0 and injects NVIDIA_VISIBLE_DEVICES
# so subsequent pods reuse the same GPU UUID without going through the device plugin.
# Uses global $assigned_gpu_uuids and $NS.
# Arguments: <rs-name>
pin_gpu() {
    local rs="$1"
    echo "Pinning GPU for ReplicaSet $rs: NVIDIA_VISIBLE_DEVICES=$assigned_gpu_uuids" >&2
    local patch
    patch=$(printf \
        '{"spec":{"template":{"spec":{"containers":[{"name":"inference-server","resources":{"limits":{"nvidia.com/gpu":"0"},"requests":{"nvidia.com/gpu":"0"}},"env":[{"name":"NVIDIA_VISIBLE_DEVICES","value":"%s"}]}]}}}}' \
        "$assigned_gpu_uuids")
    kubectl patch rs "$rs" -n "$NS" -p "$patch"
}

# check_gpu_pin waits for the pod's accelerators annotation and verifies it
# matches $assigned_gpu_uuids, ensuring the same GPU is reused after scale-up.
# Uses global $assigned_gpu_uuids and $NS.
# Arguments: <pod-name>
check_gpu_pin() {
    local pod="$1"
    expect '[ -n "$(kubectl get pod -n '"$NS"' '"$pod"' -o jsonpath={.metadata.annotations.dual-pods\\.llm-d\\.ai/accelerators})" ]'
    local actual_uuids
    actual_uuids=$(kubectl get pod "$pod" -n "$NS" -o jsonpath='{.metadata.annotations.dual-pods\.llm-d\.ai/accelerators}')
    if [ "$actual_uuids" != "$assigned_gpu_uuids" ]; then
        echo "ERROR: GPU UUID mismatch on pod $pod: expected=$assigned_gpu_uuids actual=$actual_uuids" >&2
        exit 1
    fi
    echo "GPU UUID(s) verified on pod $pod: $actual_uuids"
}

get_launcher_total_instances() {
    local launcher_pod="$1"
    kubectl exec -n "$NS" "$launcher_pod" -- python3 -c 'import json,urllib.request; print(json.load(urllib.request.urlopen("http://127.0.0.1:8001/v2/vllm/instances"))["total_instances"])'
}

# ---------------------------------------------------------------------------
# Probe for a node with 2 free GPUs
# ---------------------------------------------------------------------------
# Create a throwaway Pod that requests 2 GPUs.  The scheduler will place it
# on a node that actually has 2 GPUs available right now.  Once it is running
# we record the node, delete the probe Pod, and pin every subsequent test
# workload to that node.  This avoids spurious failures on shared clusters
# where GPU availability is dynamic (Issue #422).

intro_case GPU Probe

probe_pod="gpu-probe-$(date +%d-%H-%M-%S)"

if [ -n "${RUNTIME_CLASS_NAME:-}" ]; then
    probe_runtime_class="runtimeClassName: ${RUNTIME_CLASS_NAME}"
else
    probe_runtime_class=""
fi

kubectl apply -n "$NS" -f - <<PROBE
apiVersion: v1
kind: Pod
metadata:
  name: ${probe_pod}
  labels:
    app: gpu-probe
spec:
  ${probe_runtime_class}
  containers:
  - name: pause
    image: registry.k8s.io/pause:3.10.2
    resources:
      limits:
        nvidia.com/gpu: "2"
  terminationGracePeriodSeconds: 0
PROBE

if ! expect '[ "$(kubectl get pod '"$probe_pod"' -n '"$NS"' -o jsonpath={.status.phase})" = "Running" ]'; then
    echo "FAIL: GPU probe Pod $probe_pod did not reach Running." >&2
    echo "This probably means no node in the cluster has 2 GPUs available right now." >&2
    kubectl delete pod "$probe_pod" -n "$NS" --wait=false 2>/dev/null || true
    exit 1
fi

testnode=$(kubectl get pod "$probe_pod" -n "$NS" -o jsonpath='{.spec.nodeName}')
echo "GPU probe Pod $probe_pod scheduled on Node $testnode — using it for the rest of the tests"

kubectl delete pod "$probe_pod" -n "$NS" --wait=true

cheer "GPU probe complete — test node is $testnode"

# ---------------------------------------------------------------------------
# Create test objects
# ---------------------------------------------------------------------------

intro_case Basic Launcher Pod Creation

objs=$("$MKOBJS_SCRIPT" -n "$NS" --node "$testnode")
isc=$(echo $objs | awk '{print $1}')
lc=$(echo $objs | awk '{print $2}')
rs=$(echo $objs | awk '{print $3}')
isc2=$(echo $objs | awk '{print $4}')
# $5 is isc3 (tinyllama) — not used directly but created for completeness
lpp=$(echo $objs | awk '{print $6}')
inst=${rs#my-request-}

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
req1=${req1:-}
req2=${req2:-}
req3=${req3:-}
req4=${req4:-}
launcher1=${launcher1:-}
launcher2=${launcher2:-}
launcher3=${launcher3:-}
launcher4=${launcher4:-}
testnode=${testnode:-}
"' EXIT

# Expect requester pod to be created
expect "kubectl get pods -n $NS -o name -l app=dp-example,instance=$inst | wc -l | grep -w 1"

export req1=$(kubectl get pods -n "$NS" -o name -l app=dp-example,instance=$inst | sed s%pod/%%)
echo "Server-requesting Pod is $req1"
[ "$(kubectl get pod $req1 -n "$NS" -o jsonpath='{.spec.nodeName}')" = "$testnode" ]

# Wait for launcher-to-requester binding, then capture the launcher name
expect "kubectl get pods -n $NS -o name -l dual-pods.llm-d.ai/dual=$req1 | wc -l | grep -w 1"
export launcher1=$(kubectl get pods -n "$NS" -o name -l dual-pods.llm-d.ai/dual=$req1 | sed s%pod/%%)
echo "Launcher Pod is $launcher1"

# Verify requester is bound to launcher (bidirectional check)
expect '[ "$(kubectl get pod -n '"$NS"' $req1 -o jsonpath={.metadata.labels.dual-pods\\.llm-d\\.ai/dual})" == "$launcher1" ]'

# Verify ISC labels and annotations were propagated to launcher
[ "$(kubectl get pod -n "$NS" $launcher1 -o jsonpath='{.metadata.labels.e2e-test\.fma\.llm-d\.ai/isc-label}')" == "test-value" ] || { echo "ERROR: ISC label not propagated to launcher pod $launcher1"; exit 1; }
[ "$(kubectl get pod -n "$NS" $launcher1 -o jsonpath='{.metadata.annotations.e2e-test\.fma\.llm-d\.ai/isc-annotation}')" == "test-value" ] || { echo "ERROR: ISC annotation not propagated to launcher pod $launcher1"; exit 1; }

# Verify LauncherConfig podTemplate metadata labels/annotations were propagated to launcher pod (Issue #433)
[ "$(kubectl get pod -n "$NS" $launcher1 -o jsonpath='{.metadata.labels.e2e-test\.fma\.llm-d\.ai/template-label}')" == "from-launcher-config" ] || { echo "ERROR: LauncherConfig podTemplate label is not correctly set on launcher pod $launcher1"; false; }
[ "$(kubectl get pod -n "$NS" $launcher1 -o jsonpath='{.metadata.annotations.e2e-test\.fma\.llm-d\.ai/template-annotation}')" == "from-launcher-config" ] || { echo "ERROR: LauncherConfig podTemplate annotation is not correctly set on launcher pod $launcher1"; false; }

# Wait for both pods to be ready
date
kubectl wait --for condition=Ready pod/$req1 -n "$NS" --timeout=180s
[ "$(kubectl get pod $launcher1 -n "$NS" -o jsonpath='{.status.conditions[?(@.type=="Ready")].status}')" = "True" ]

# On OpenShift, record the GPU UUID assigned by the cluster so we can pin it later.
# The controller writes the UUID(s) to the dual-pods.llm-d.ai/accelerators annotation
# after querying the requester's SPI endpoint; it is guaranteed to be set by the time
# the pod is Ready.
if [ "$E2E_PLATFORM" = "openshift" ]; then
    expect '[ -n "$(kubectl get pod -n '"$NS"' $req1 -o jsonpath={.metadata.annotations.dual-pods\\.llm-d\\.ai/accelerators})" ]'
    assigned_gpu_uuids=$(kubectl get pod "$req1" -n "$NS" -o jsonpath='{.metadata.annotations.dual-pods\.llm-d\.ai/accelerators}')
    echo "Assigned GPU UUID(s) on OpenShift: $assigned_gpu_uuids"
fi

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

# ---------------------------------------------------------------------------
# Same-Node Port Collision
# ---------------------------------------------------------------------------

intro_case Same-Node Port Collision Creates New Launcher

# Check whether the test node has a free GPU for the collision requester.
# req1 already holds 1 GPU; the collision requester needs 1 more on the same node.
allocatable_gpus=$(kubectl get node "$testnode" -o jsonpath='{.status.allocatable.nvidia\.com/gpu}')
allocated_gpus=$(kubectl get pods --all-namespaces --field-selector spec.nodeName="$testnode" -o json \
  | jq '[.items[]
         | select(.status.phase != "Succeeded" and .status.phase != "Failed")
         | select(.metadata.deletionTimestamp == null)
         | .spec.containers[]?.resources.limits["nvidia.com/gpu"] // "0"
         | tonumber] | add // 0')
available_gpus=$(( allocatable_gpus - allocated_gpus ))
echo "Node $testnode: allocatable_gpus=$allocatable_gpus allocated_gpus=$allocated_gpus available_gpus=$available_gpus"

if (( available_gpus < 1 )); then
    echo "FAIL: Node $testnode has no free GPUs ($allocatable_gpus allocatable, $allocated_gpus allocated)." >&2
    echo "The Same-Node Port Collision test needs 1 free GPU for the collision requester." >&2
    echo "This is likely due to GPU saturation on the shared cluster." >&2
    exit 1
else

    collision_inst="${inst}-collision"
    collision_rs="my-request-collision-$inst"

    kubectl get rs "$rs" -n "$NS" -o json \
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

    [ "$collision_launcher" != "$launcher1" ]

    expect '[ "$(kubectl get pod -n '"$NS"' '"$collision_req"' -o jsonpath={.metadata.labels.dual-pods\\.llm-d\\.ai/dual})" == "'"$collision_launcher"'" ]'

    date
    kubectl wait --for condition=Ready pod/$collision_req -n "$NS" --timeout=120s
    [ "$(kubectl get pod $collision_launcher -n "$NS" -o jsonpath='{.status.conditions[?(@.type=="Ready")].status}')" = "True" ]

    req_gpus=$(kubectl get pod "$req1" -n "$NS" -o jsonpath='{.metadata.annotations.dual-pods\.llm-d\.ai/accelerators}')
    collision_gpus=$(kubectl get pod "$collision_req" -n "$NS" -o jsonpath='{.metadata.annotations.dual-pods\.llm-d\.ai/accelerators}')
    [ -n "$req_gpus" ]
    [ -n "$collision_gpus" ]
    [ "$req_gpus" != "$collision_gpus" ]

    kubectl delete rs "$collision_rs" -n "$NS" --wait=true
    expect "kubectl get pods -n $NS -o name -l app=dp-example,instance=$collision_inst | wc -l | grep -w 0"
    kubectl delete pod "$collision_launcher" -n "$NS" --wait=true
    expect '! kubectl get pods -n '"$NS"' -o name | grep -qw pod/'"$collision_launcher"

    cheer Successful same-node collision handling

fi # available_gpus check

# ---------------------------------------------------------------------------
# Instance Wake-up Fast Path
# ---------------------------------------------------------------------------

intro_case Instance Wake-up Fast Path

# Scale requester to 0 (instance should sleep in launcher)
kubectl scale rs $rs -n "$NS" --replicas=0

expect "kubectl get pods -n $NS -o name -l app=dp-example,instance=$inst | wc -l | grep -w 0"

# On OpenShift, pin the GPU so the next scale-up reuses the same GPU.
if [ "$E2E_PLATFORM" = "openshift" ]; then pin_gpu $rs; fi

# Patch requester ReplicaSet to stick to testnode
kubectl patch rs $rs -n "$NS" -p '{"spec": {"template": {"spec": {"nodeSelector": {"kubernetes.io/hostname": "'$testnode'"} }} }}'

# Launcher should remain
kubectl get pod $launcher1 -n "$NS"

# Verify launcher is unbound (no dual label pointing to requester)
expect '[ "$(kubectl get pod -n '"$NS"' $launcher1 -o jsonpath={.metadata.labels.dual-pods\\.llm-d\\.ai/dual})" == "" ]'

# Verify ISC labels and annotations were removed from launcher
[ "$(kubectl get pod -n "$NS" $launcher1 -o jsonpath='{.metadata.labels.e2e-test\.fma\.llm-d\.ai/isc-label}')" == "" ] || { echo "ERROR: ISC label not removed from launcher pod $launcher1 after unbind"; exit 1; }
[ "$(kubectl get pod -n "$NS" $launcher1 -o jsonpath='{.metadata.annotations.e2e-test\.fma\.llm-d\.ai/isc-annotation}')" == "" ] || { echo "ERROR: ISC annotation not removed from launcher pod $launcher1 after unbind"; exit 1; }

# Verify LauncherConfig podTemplate metadata survives unbind (these are intrinsic to the pod, not ISC-specific)
[ "$(kubectl get pod -n "$NS" $launcher1 -o jsonpath='{.metadata.labels.e2e-test\.fma\.llm-d\.ai/template-label}')" == "from-launcher-config" ] || { echo "ERROR: LauncherConfig podTemplate label is not correctly set on launcher pod $launcher1 after unbind"; false; }
[ "$(kubectl get pod -n "$NS" $launcher1 -o jsonpath='{.metadata.annotations.e2e-test\.fma\.llm-d\.ai/template-annotation}')" == "from-launcher-config" ] || { echo "ERROR: LauncherConfig podTemplate annotation is not correctly set on launcher pod $launcher1 after unbind"; false; }

# Scale back up (should reuse same launcher and wake sleeping instance)
kubectl scale rs $rs -n "$NS" --replicas=1

expect "kubectl get pods -n $NS -o name -l app=dp-example,instance=$inst | wc -l | grep -w 1"

req2=$(kubectl get pods -n "$NS" -o name -l app=dp-example,instance=$inst | sed s%pod/%%)
echo "Server-requesting Pod2 is $req2"

# Should still be using the same launcher pod
expect "kubectl get pods -n $NS -o name -l dual-pods.llm-d.ai/dual=$req2 | wc -l | grep -w 1"
launcher2=$(kubectl get pods -n "$NS" -o name -l dual-pods.llm-d.ai/dual=$req2 | sed s%pod/%%)
[ "$launcher2" == "$launcher1" ]

# Verify requester is bound to launcher (bidirectional check)
expect '[ "$(kubectl get pod -n '"$NS"' $req2 -o jsonpath={.metadata.labels.dual-pods\\.llm-d\\.ai/dual})" == "$launcher1" ]'

# Verify ISC labels and annotations re-propagated after re-bind
[ "$(kubectl get pod -n "$NS" $launcher1 -o jsonpath='{.metadata.labels.e2e-test\.fma\.llm-d\.ai/isc-label}')" == "test-value" ] || { echo "ERROR: ISC label not re-propagated to launcher pod $launcher1 after re-bind"; exit 1; }
[ "$(kubectl get pod -n "$NS" $launcher1 -o jsonpath='{.metadata.annotations.e2e-test\.fma\.llm-d\.ai/isc-annotation}')" == "test-value" ] || { echo "ERROR: ISC annotation not re-propagated to launcher pod $launcher1 after re-bind"; exit 1; }

# Wait for requester to be ready (launcher should already be ready)
date
kubectl wait --for condition=Ready pod/$req2 -n "$NS" --timeout=120s
[ "$(kubectl get pod $launcher1 -n "$NS" -o jsonpath='{.status.conditions[?(@.type=="Ready")].status}')" = "True" ]

# On OpenShift, verify the same GPU UUID was assigned after wake-up.
if [ "$E2E_PLATFORM" = "openshift" ]; then check_gpu_pin $req2; fi

cheer Successful instance wake-up fast path

# ---------------------------------------------------------------------------
# Multiple Instances Share One Launcher
# ---------------------------------------------------------------------------

intro_case Multiple Instances Share One Launcher

# Scale requester to 0 again
kubectl scale rs $rs -n "$NS" --replicas=0

expect "kubectl get pods -n $NS -o name -l app=dp-example,instance=$inst | wc -l | grep -w 0"

# Launcher should remain
kubectl get pod $launcher1 -n "$NS"

# Verify launcher is unbound
expect '[ "$(kubectl get pod -n '"$NS"' $launcher1 -o jsonpath={.metadata.labels.dual-pods\\.llm-d\\.ai/dual})" == "" ]'

# Patch ReplicaSet to use isc2 instead of isc
kubectl patch rs $rs -n "$NS" -p='{"spec":{"template":{"metadata":{"annotations":{"dual-pods.llm-d.ai/inference-server-config":"'$isc2'"}}}}}'

# Scale back up (should reuse same launcher and create 2nd instance)
kubectl scale rs $rs -n "$NS" --replicas=1

expect "kubectl get pods -n $NS -o name -l app=dp-example,instance=$inst | wc -l | grep -w 1"

req3=$(kubectl get pods -n "$NS" -o name -l app=dp-example,instance=$inst | sed s%pod/%%)
echo "Server-requesting Pod3 is $req3"

# Should still be using the same launcher pod
expect "kubectl get pods -n $NS -o name -l dual-pods.llm-d.ai/dual=$req3 | wc -l | grep -w 1"
launcher3=$(kubectl get pods -n "$NS" -o name -l dual-pods.llm-d.ai/dual=$req3 | sed s%pod/%%)
[ "$launcher3" == "$launcher1" ]

# Verify requester is bound to launcher (bidirectional check)
expect '[ "$(kubectl get pod -n '"$NS"' $req3 -o jsonpath={.metadata.labels.dual-pods\\.llm-d\\.ai/dual})" == "$launcher1" ]'

# Wait for requester to be ready (launcher should already be ready)
date
kubectl wait --for condition=Ready pod/$req3 -n "$NS" --timeout=120s
[ "$(kubectl get pod $launcher1 -n "$NS" -o jsonpath='{.status.conditions[?(@.type=="Ready")].status}')" = "True" ]

if [ "$E2E_PLATFORM" = "openshift" ]; then check_gpu_pin $req3; fi

cheer Successful multiple instances sharing one launcher

# ---------------------------------------------------------------------------
# Switch Instances In One Launcher
# ---------------------------------------------------------------------------

intro_case Switch Instances In One Launcher

# Scale requester to 0 again
kubectl scale rs $rs -n "$NS" --replicas=0

expect "kubectl get pods -n $NS -o name -l app=dp-example,instance=$inst | wc -l | grep -w 0"

# Launcher should remain
kubectl get pod $launcher1 -n "$NS"

# Verify launcher is unbound
expect '[ "$(kubectl get pod -n '"$NS"' $launcher1 -o jsonpath={.metadata.labels.dual-pods\\.llm-d\\.ai/dual})" == "" ]'

# Patch ReplicaSet back to use original isc
kubectl patch rs $rs -n "$NS" -p='{"spec":{"template":{"metadata":{"annotations":{"dual-pods.llm-d.ai/inference-server-config":"'$isc'"}}}}}'

# Scale back up (should reuse same launcher and wake first instance)
kubectl scale rs $rs -n "$NS" --replicas=1

expect "kubectl get pods -n $NS -o name -l app=dp-example,instance=$inst | wc -l | grep -w 1"

req4=$(kubectl get pods -n "$NS" -o name -l app=dp-example,instance=$inst | sed s%pod/%%)
echo "Server-requesting Pod4 is $req4"

# Should still be using the same launcher pod
expect "kubectl get pods -n $NS -o name -l dual-pods.llm-d.ai/dual=$req4 | wc -l | grep -w 1"
launcher4=$(kubectl get pods -n "$NS" -o name -l dual-pods.llm-d.ai/dual=$req4 | sed s%pod/%%)
[ "$launcher4" == "$launcher1" ]

# Verify requester is bound to launcher (bidirectional check)
expect '[ "$(kubectl get pod -n '"$NS"' $req4 -o jsonpath={.metadata.labels.dual-pods\\.llm-d\\.ai/dual})" == "$launcher1" ]'

# Wait for requester to be ready (launcher should already be ready)
date
kubectl wait --for condition=Ready pod/$req4 -n "$NS" --timeout=120s
[ "$(kubectl get pod $launcher1 -n "$NS" -o jsonpath='{.status.conditions[?(@.type=="Ready")].status}')" = "True" ]

if [ "$E2E_PLATFORM" = "openshift" ]; then check_gpu_pin $req4; fi

cheer Successful switching instances in one launcher

# ---------------------------------------------------------------------------
# Controller Restart State Recovery
# ---------------------------------------------------------------------------

intro_case Controller Restart State Recovery

# This test verifies that the controller can rebuild its internal state after restart
# by syncing launcher instances from unbound launcher pods

# Scale requester to 0 to create sleeping instances
kubectl scale rs $rs -n "$NS" --replicas=0

expect "kubectl get pods -n $NS -o name -l app=dp-example,instance=$inst | wc -l | grep -w 0"

# Verify launcher set is unchanged and target launcher is unbound
launcher_count_pre_restart=$(kubectl get pods -n "$NS" -o name -l dual-pods.llm-d.ai/launcher-config-name=$lc | wc -l)
echo launcher_count_pre_restart = $launcher_count_pre_restart
kubectl get pods -n "$NS" -o name -l dual-pods.llm-d.ai/launcher-config-name=$lc | grep -x "pod/$launcher1"
expect '[ "$(kubectl get pod -n '"$NS"' $launcher1 -o jsonpath={.metadata.labels.dual-pods\\.llm-d\\.ai/dual})" == "" ]'

# Verify launcher has sleeping instances before restart
launcher_instances_before=$(kubectl exec -n "$NS" $launcher1 -- python3 -c 'import json,urllib.request; print(json.load(urllib.request.urlopen("http://127.0.0.1:8001/v2/vllm/instances"))["total_instances"])')
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
kubectl get pods -n "$NS" -o name -l dual-pods.llm-d.ai/launcher-config-name=$lc | grep -x "pod/$launcher1"

# Verify launcher still has the same number of instances after controller restart
launcher_instances_after=$(kubectl exec -n "$NS" $launcher1 -- python3 -c 'import json,urllib.request; print(json.load(urllib.request.urlopen("http://127.0.0.1:8001/v2/vllm/instances"))["total_instances"])')
echo "Launcher has $launcher_instances_after instances after controller restart"
[ "$launcher_instances_after" == "$launcher_instances_before" ]

# Now scale up requester - controller should correctly select the launcher with sleeping instance
# Use isc2 which should have a sleeping instance from before
kubectl patch rs $rs -n "$NS" --type=json -p='[{"op": "replace", "path": "/spec/template/metadata/annotations/dual-pods.llm-d.ai~1inference-server-config", "value": "'$isc2'"}]'
kubectl scale rs $rs -n "$NS" --replicas=1

expect "kubectl get pods -n $NS -o name -l app=dp-example,instance=$inst | wc -l | grep -w 1"
req_post_restart=$(kubectl get pods -n "$NS" -o name -l app=dp-example,instance=$inst | sed s%pod/%%)

# Verify requester is bound to the same launcher (controller recovered state correctly)
expect '[ "$(kubectl get pod -n '"$NS"' $req_post_restart -o jsonpath={.metadata.labels.dual-pods\\.llm-d\\.ai/dual})" == "$launcher1" ]'
expect '[ "$(kubectl get pod -n '"$NS"' $launcher1 -o jsonpath={.metadata.labels.dual-pods\\.llm-d\\.ai/dual})" == "$req_post_restart" ]'

# Verify requester becomes ready (fast wake-up path should work)
date
kubectl wait --for condition=Ready pod/$req_post_restart -n "$NS" --timeout=30s
[ "$(kubectl get pod $launcher1 -n "$NS" -o jsonpath='{.status.conditions[?(@.type=="Ready")].status}')" = "True" ]

if [ "$E2E_PLATFORM" = "openshift" ]; then check_gpu_pin $req_post_restart; fi

cheer Successful controller restart state recovery

# ---------------------------------------------------------------------------
# Delete Obsolete Sleeping Instances After ISC Update
# ---------------------------------------------------------------------------

intro_case Delete Obsolete Sleeping Instances After ISC Update

old_total_instances=$(get_launcher_total_instances "$launcher1")
echo "Launcher had $old_total_instances instance(s) before updating ISC to make a sleeping instance obsolete"

# Mutate isc in a hash-relevant way so its sleeping instance becomes obsolete.
kubectl patch inferenceserverconfig "$isc" -n "$NS" --type=merge -p='{"spec":{"modelServerConfig":{"options":"--model HuggingFaceTB/SmolLM2-360M-Instruct --served-model-name obsolete-after-update"}}}'

expect '[ "$(get_launcher_total_instances "$launcher1")" == "$((old_total_instances - 1))" ]'

cheer Successful deletion of obsolete sleeping instance

# ---------------------------------------------------------------------------
# Delete Obsolete Awake Instance on Unbinding
# ---------------------------------------------------------------------------

intro_case Delete Obsolete Awake Instance on Unbinding

# This test verifies that when an awake instance's ISC is updated (making it
# obsolete) and then the requester scales down, the controller deletes the
# instance during unbinding rather than putting it to sleep.

[ "$(kubectl get pod -n "$NS" $launcher1 -o jsonpath={.metadata.labels.dual-pods\\.llm-d\\.ai/dual})" == "$req_post_restart" ]

old_total_instances=$(get_launcher_total_instances "$launcher1")
echo "Launcher had $old_total_instances instance(s) before making awake instance obsolete"

# Save isc2's original options so we can restore them after the test.
original_isc2_options=$(kubectl get inferenceserverconfig "$isc2" -n "$NS" -o jsonpath='{.spec.modelServerConfig.options}')
echo "Original isc2 options: $original_isc2_options"

# Mutate isc2 in a hash-relevant way while its instance is still awake (bound to req_post_restart).
kubectl patch inferenceserverconfig "$isc2" -n "$NS" --type=merge -p='{"spec":{"modelServerConfig":{"options":"'"$original_isc2_options"' --served-model-name obsolete-awake-unbind"}}}'

# Scale to 0 — triggers unbinding, which should detect obsolescence and delete the instance.
kubectl scale rs $rs -n "$NS" --replicas=0

expect "kubectl get pods -n $NS -o name -l app=dp-example,instance=$inst | wc -l | grep -w 0"

expect '[ "$(get_launcher_total_instances "$launcher1")" == "$((old_total_instances - 1))" ]'

# Restore isc2's original options so subsequent tests can create working instances.
kubectl patch inferenceserverconfig "$isc2" -n "$NS" --type=merge -p='{"spec":{"modelServerConfig":{"options":"'"$original_isc2_options"'"}}}'

cheer Successful deletion of obsolete awake instance on unbinding

# ---------------------------------------------------------------------------
# Unbound Launcher Deletion Cleanup
# ---------------------------------------------------------------------------

intro_case Unbound Launcher Deletion Cleanup

# This test verifies that deleting an unbound launcher does not leave the controller
# stuck with stale instance state.

kubectl scale rs $rs -n "$NS" --replicas=0

expect "kubectl get pods -n $NS -o name -l app=dp-example,instance=$inst | wc -l | grep -w 0"
expect '[ "$(kubectl get pod -n '"$NS"' $launcher1 -o jsonpath={.metadata.labels.dual-pods\\.llm-d\\.ai/dual})" == "" ]'

kubectl delete pod $launcher1 -n "$NS" --wait=true

! kubectl get pods -n "$NS" -o name | grep -qw pod/$launcher1

kubectl scale rs $rs -n "$NS" --replicas=1

expect "kubectl get pods -n $NS -o name -l app=dp-example,instance=$inst | wc -l | grep -w 1"
req_after_delete=$(kubectl get pods -n "$NS" -o name -l app=dp-example,instance=$inst | sed s%pod/%%)
echo "Server-requesting Pod after delete = $req_after_delete"

expect "kubectl get pods -n $NS -o name -l dual-pods.llm-d.ai/dual=$req_after_delete | wc -l | grep -w 1"
launcher_after_delete=$(kubectl get pods -n "$NS" -o name -l dual-pods.llm-d.ai/dual=$req_after_delete | sed s%pod/%%)
echo "Launcher after delete = $launcher_after_delete"

[ "$launcher_after_delete" != "$launcher1" ]
expect '[ "$(kubectl get pod -n '"$NS"' $req_after_delete -o jsonpath={.metadata.labels.dual-pods\\.llm-d\\.ai/dual})" == "$launcher_after_delete" ]'

date
kubectl wait --for condition=Ready pod/$req_after_delete -n "$NS" --timeout=120s
[ "$(kubectl get pod $launcher_after_delete -n "$NS" -o jsonpath='{.status.conditions[?(@.type=="Ready")].status}')" = "True" ]

if [ "$E2E_PLATFORM" = "openshift" ]; then check_gpu_pin $req_after_delete; fi

cheer Successful unbound launcher deletion cleanup

# ---------------------------------------------------------------------------
# Stopped Instance Recovery
# ---------------------------------------------------------------------------

intro_case Stopped Instance Recovery

# This test verifies that the dual-pods controller detects a stopped vLLM
# instance (via the sidecar notifier annotation) and deletes the server-
# requesting Pod so that the ReplicaSet recreates it with a fresh instance.
#
# Starting state: $req_after_delete is bound to $launcher_after_delete, both Ready.

echo "Bound requester: $req_after_delete, launcher: $launcher_after_delete"
req_uid_before=$(kubectl get pod $req_after_delete -n "$NS" -o jsonpath='{.metadata.uid}')

# Get the running instance ID from the launcher
instance_id=$(kubectl exec -n "$NS" $launcher_after_delete -c inference-server -- python3 -c '
import json, urllib.request
resp = json.load(urllib.request.urlopen("http://127.0.0.1:8001/v2/vllm/instances"))
for inst in resp["instances"]:
    if inst["status"] == "running":
        print(inst["instance_id"])
        break
')
echo "Running instance ID: $instance_id"
[ -n "$instance_id" ]

# Delete the running instance from the launcher to simulate a crash.
# The notifier sidecar will detect the change and update the Pod annotation.
# The dual-pods controller will then query the instance, get 404, and delete the requester.
kubectl exec -n "$NS" $launcher_after_delete -c inference-server -- python3 -c '
import urllib.request
req = urllib.request.Request(
    "http://127.0.0.1:8001/v2/vllm/instances/'"$instance_id"'",
    method="DELETE",
)
urllib.request.urlopen(req)
print("Instance deleted from launcher")
'

# Wait for the old requester Pod to be deleted (the dual-pods controller should do this)
expect '[ "$(kubectl get pod -n '"$NS"' $req_after_delete -o jsonpath={.metadata.uid} 2>/dev/null)" != "$req_uid_before" ]'
echo "Old requester $req_after_delete was deleted by the controller"

# Wait for the ReplicaSet to recreate a new requester Pod
expect "kubectl get pods -n $NS -o name -l app=dp-example,instance=$inst | wc -l | grep -w 1"
req_recovered=$(kubectl get pods -n "$NS" -o name -l app=dp-example,instance=$inst | sed s%pod/%%)
echo "Recovered server-requesting Pod: $req_recovered"

# Wait for the new requester to be bound to the same launcher
expect "kubectl get pods -n $NS -o name -l dual-pods.llm-d.ai/dual=$req_recovered | wc -l | grep -w 1"
launcher_recovered=$(kubectl get pods -n "$NS" -o name -l dual-pods.llm-d.ai/dual=$req_recovered | sed s%pod/%%)
echo "Recovered launcher: $launcher_recovered"
[ "$launcher_recovered" == "$launcher_after_delete" ]

# Verify bidirectional binding
expect '[ "$(kubectl get pod -n '"$NS"' $req_recovered -o jsonpath={.metadata.labels.dual-pods\\.llm-d\\.ai/dual})" == "$launcher_after_delete" ]'

# Wait for both to be ready
date
kubectl wait --for condition=Ready pod/$req_recovered -n "$NS" --timeout=120s
[ "$(kubectl get pod $launcher_after_delete -n "$NS" -o jsonpath='{.status.conditions[?(@.type=="Ready")].status}')" = "True" ]

if [ "$E2E_PLATFORM" = "openshift" ]; then check_gpu_pin $req_recovered; fi

cheer Successful stopped instance recovery

cheer All launcher-based tests passed
