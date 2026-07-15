#!/usr/bin/env bash

# No command-line parameters.
# Current working directory does not matter.
# Current kubectl config: accesses the cluster in question.

# Purpose: delete namespaces that are marked as "expired". These would
# be namespaces in an OpenShift dev/test cluster that have been
# retained so that OpenShift allows old Pod logs to be viewed.  They
# are created by the `ci-e2e-openshift.yaml` GitHub workflow.

set -euo pipefail
today=$(date -u +%Y-%m-%d)
echo "Today (UTC): $today"
kubectl get namespace \
  -l fma-e2e.llm-d.ai/sweep=true \
  -o json \
| jq -r --arg today "$today" '
    .items[]
    | .metadata.annotations["fma-e2e.llm-d.ai/expires-at"] as $expires
    | select($expires != null and $expires < $today)
    | .metadata.name
  ' \
| while read -r name; do
    [ -n "$name" ] || continue
    echo "Deleting expired namespace: $name"
    kubectl delete namespace "$name" --ignore-not-found --timeout=180s || true
  done
