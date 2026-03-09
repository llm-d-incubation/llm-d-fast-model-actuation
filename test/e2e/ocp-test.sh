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
        kubectl get pods -n "$namespace" -L dual-pods.llm-d.ai/dual,dual-pods.llm-d.ai/sleeping
        if eval "$1"; then return; fi
        if (( elapsed > limit )); then
            echo "Did not become true (from $start to $(date)): $1" >&2
            exit 99
        fi
        sleep 5
        elapsed=$(( elapsed+5 ))
    done
}


: Test launcher-based server-providing pods

: Basic Launcher Pod Creation

# Use environment variables from workflow
echo "Using test objects from environment variables:"
echo "  NAMESPACE: $NAMESPACE"
echo "  ISC: $ISC"
echo "  LC: $LC"
echo "  RS: $RS"
echo "  INST: $INST"

isc="$ISC"
lc="$LC"
rslb="$RS"
instlb="$INST"
namespace="$NAMESPACE"

# Verify required environment variables are set
if [ -z "$namespace" ] || [ -z "$isc" ] || [ -z "$lc" ] || [ -z "$rslb" ] || [ -z "$instlb" ]; then
    echo "ERROR: Required environment variables not set!" >&2
    echo "  NAMESPACE=$NAMESPACE" >&2
    echo "  ISC=$ISC" >&2
    echo "  LC=$LC" >&2
    echo "  RS=$RS" >&2
    echo "  INST=$INST" >&2
    exit 1
fi

# Note: isc2 not provided by workflow, set to empty for tests that need it
isc2=""


: Test CEL policy verification if enabled

if [ "${POLICIES_ENABLED}" = true ]; then
  if ! test/e2e/validate.sh; then
    echo "ERROR: CEL policy tests failed!" >&2
    exit 1
  fi
  cheer CEL policy checks passed
fi

: Instance Wake-up Fast Path

# Scale requester to 0 (instance should sleep in launcher)
kubectl scale rs $rslb --replicas=0 -n "$namespace"

expect "kubectl get pods -n '$namespace' -o name -l app=dp-example,instance=$instlb | wc -l | grep -w 0"

# Launcher should remain
kubectl get pod $launcherlb -n "$namespace"

# Verify launcher is unbound (no dual label pointing to requester)
expect '[ "$(kubectl get pod $launcherlb -n '"$namespace"' -o jsonpath={.metadata.labels.dual-pods\\.llm-d\\.ai/dual})" == "" ]'

# Scale back up (should reuse same launcher and wake sleeping instance)
kubectl scale rs $rslb --replicas=1 -n "$namespace"

expect "kubectl get pods -n '$namespace' -o name -l app=dp-example,instance=$instlb | grep -c '^pod/' | grep -w 1"

reqlb2=$(kubectl get pods -n "$namespace" -o name -l app=dp-example,instance=$instlb | sed s%pod/%%)

# Should still be using the same launcher pod
launcherlb2=$(kubectl get pods -n "$namespace" -o name -l dual-pods.llm-d.ai/launcher-config-name=$lc | sed s%pod/%%)
[ "$launcherlb2" == "$launcherlb" ]

# Verify new requester is bound to same launcher
expect '[ "$(kubectl get pod $reqlb2 -n '"$namespace"' -o jsonpath={.metadata.labels.dual-pods\\.llm-d\\.ai/dual})" == "$launcherlb" ]'

# Verify launcher is bound to new requester
expect '[ "$(kubectl get pod $launcherlb -n '"$namespace"' -o jsonpath={.metadata.labels.dual-pods\\.llm-d\\.ai/dual})" == "$reqlb2" ]'

# Wait for requester to be ready (launcher should already be ready)
date
kubectl wait --for condition=Ready pod/$reqlb2 -n "$namespace" --timeout=30s
kubectl wait --for condition=Ready pod/$launcherlb -n "$namespace" --timeout=5s

cheer Successful instance wake-up fast path

: Multiple Instances Share One Launcher

# Scale requester to 0 again
kubectl scale rs $rslb --replicas=0 -n "$namespace"

expect "kubectl get pods -n '$namespace' -o name -l app=dp-example,instance=$instlb | wc -l | grep -w 0"

# Launcher should remain
kubectl get pod $launcherlb -n "$namespace"

# Verify launcher is unbound
expect '[ "$(kubectl get pod $launcherlb -n '"$namespace"' -o jsonpath={.metadata.labels.dual-pods\\.llm-d\\.ai/dual})" == "" ]'

# Patch ReplicaSet to use isc2 instead of isc
kubectl patch rs $rslb -n "$namespace" -p='{"spec":{"template":{"metadata":{"annotations":{"dual-pods.llm-d.ai/inference-server-config":"'$isc2'"}}}}}'

# Scale back up (should reuse same launcher and create 2nd instance)
kubectl scale rs $rslb --replicas=1 -n "$namespace"

expect "kubectl get pods -n '$namespace' -o name -l app=dp-example,instance=$instlb | grep -c '^pod/' | grep -w 1"

reqlb3=$(kubectl get pods -n "$namespace" -o name -l app=dp-example,instance=$instlb | sed s%pod/%%)

# Should still be using the same launcher pod
launcherlb3=$(kubectl get pods -n "$namespace" -o name -l dual-pods.llm-d.ai/launcher-config-name=$lc | sed s%pod/%%)
[ "$launcherlb3" == "$launcherlb" ]

# Verify new requester is bound to same launcher
expect '[ "$(kubectl get pod $reqlb3 -n '"$namespace"' -o jsonpath={.metadata.labels.dual-pods\\.llm-d\\.ai/dual})" == "$launcherlb" ]'

# Verify launcher is bound to new requester
expect '[ "$(kubectl get pod $launcherlb -n '"$namespace"' -o jsonpath={.metadata.labels.dual-pods\\.llm-d\\.ai/dual})" == "$reqlb3" ]'

# Wait for requester to be ready (launcher should already be ready)
date
kubectl wait --for condition=Ready pod/$reqlb3 -n "$namespace" --timeout=120s
kubectl wait --for condition=Ready pod/$launcherlb -n "$namespace" --timeout=5s

cheer Successful multiple instances sharing one launcher

: Switch Instances In One Launcher

# Scale requester to 0 again
kubectl scale rs $rslb --replicas=0 -n "$namespace"

expect "kubectl get pods -n '$namespace' -o name -l app=dp-example,instance=$instlb | wc -l | grep -w 0"

# Launcher should remain
kubectl get pod $launcherlb -n "$namespace"

# Verify launcher is unbound
expect '[ "$(kubectl get pod $launcherlb -n '"$namespace"' -o jsonpath={.metadata.labels.dual-pods\\.llm-d\\.ai/dual})" == "" ]'

# Patch ReplicaSet back to use original isc
kubectl patch rs $rslb -n "$namespace" -p='{"spec":{"template":{"metadata":{"annotations":{"dual-pods.llm-d.ai/inference-server-config":"'$isc'"}}}}}'

# Scale back up (should reuse same launcher and wake first instance)
kubectl scale rs $rslb --replicas=1 -n "$namespace"

expect "kubectl get pods -n '$namespace' -o name -l app=dp-example,instance=$instlb | grep -c '^pod/' | grep -w 1"

reqlb4=$(kubectl get pods -n "$namespace" -o name -l app=dp-example,instance=$instlb | sed s%pod/%%)

# Should still be using the same launcher pod
launcherlb4=$(kubectl get pods -n "$namespace" -o name -l dual-pods.llm-d.ai/launcher-config-name=$lc | sed s%pod/%%)
[ "$launcherlb4" == "$launcherlb" ]

# Verify new requester is bound to same launcher
expect '[ "$(kubectl get pod $reqlb4 -n '"$namespace"' -o jsonpath={.metadata.labels.dual-pods\\.llm-d\\.ai/dual})" == "$launcherlb" ]'

# Verify launcher is bound to new requester
expect '[ "$(kubectl get pod $launcherlb -n '"$namespace"' -o jsonpath={.metadata.labels.dual-pods\\.llm-d\\.ai/dual})" == "$reqlb4" ]'

# Wait for requester to be ready (launcher should already be ready)
date
kubectl wait --for condition=Ready pod/$reqlb4 -n "$namespace" --timeout=120s
kubectl wait --for condition=Ready pod/$launcherlb -n "$namespace" --timeout=5s

cheer Successful switching instances in one launcher
