#!/usr/bin/env bash

set -euo pipefail

release=""
ns=$(kubectl get sa default -o jsonpath='{.metadata.namespace}')
existing_nvcr=""
ensure_nvcr=""
install_crds="false"
install_aps="false"
oci_reg=""
config_dir=""
chart_set=()
chart_instance_name=fma

function usage() {
    (
        (( $# > 0 )) && echo "$*" || true
        cat <<EOF
$0 usage: --release \$semver OPTIONS

where OPTIONS are any of the following.

    --namespace[=]\$ns
    --existing-node-view-cluster-role[=]\$crname
    --ensure-node-view-cluster-role[=]\$crname
    --install-crds[=]\$trueorfalse
    --install-admission-policies[=]\$trueorfalse
    --config-dir[=]\$pathname
    --chart-set[=]\$path=\$val (may be repeated)
    --chart-instance-name[=]\$chart_instance_name
EOF
    ) >& 2
}

while (( $# > 0 )); do
    case "$1" in
        (--release=*)
            release=${1#--release=};;
        (--release) if (( $# > 1 ))
                    then release="$2"; shift
                    else usage missing value for --release; exit 1
                    fi;;

        (--namespace=*)
            ns=${1#--namespace=};;
        (--namespace) if (( $# > 1 ))
                    then ns="$2"; shift
                    else usage missing value for --namespace; exit 1
                    fi;;

        (--existing-node-view-cluster-role=*)
            existing_nvcr=${1#--existing-node-view-cluster-role=};;
        (--existing-node-view-cluster-role) if (( $# > 1 ))
                    then existing_nvcr="$2"; shift
                    else usage missing value for --existing-node-view-cluster-role; exit 1
                    fi;;

        (--ensure-node-view-cluster-role=*)
            ensure_nvcr=${1#--ensure-node-view-cluster-role=};;
        (--ensure-node-view-cluster-role) if (( $# > 1 ))
                    then ensure_nvcr="$2"; shift
                    else usage missing value for --ensure-node-view-cluster-role; exit 1
                    fi;;

        (--install-crds=*)
            install_crds=${1#--install-crds=};;
        (--install-crds) if (( $# > 1 ))
                    then install_crds="$2"; shift
                    else usage missing value for --install-crds; exit 1
                    fi;;

        (--install-admission-policies=*)
            install_aps=${1#--install-admission-policies=};;
        (--install-admission-policies) if (( $# > 1 ))
                    then install_aps="$2"; shift
                    else usage missing value for --install-admission-policies; exit 1
                    fi;;

        (--oci-registry=*)
            oci_reg=${1#--oci-registry=};;
        (--oci-registry) if (( $# > 1 ))
                    then oci_reg="$2"; shift
                    else usage missing value for --oci-registry; exit 1
                    fi;;

        (--config-dir=*)
            config_dir=${1#--config-dir=};;
        (--config-dir) if (( $# > 1 ))
                    then config_dir="$2"; shift
                    else usage missing value for --config-dir; exit 1
                    fi;;

        (--chart-set=*)
            chart_set+=(${1#--chart-set=});;
        (--chart-set) if (( $# > 1 ))
                    then chart_set+=("$2"); shift
                    else usage missing value for --chart-set; exit 1
                    fi;;

        (--chart-instance-name=*)
            chart_instance_name=${1#--chart-instance-name=};;
        (--chart-instance-name) if (( $# > 1 ))
                    then chart_instance_name="$2"; shift
                    else usage missing value for --chart-instance-name; exit 1
                    fi;;

        (*) usage "Unknown argument $1"
            exit 1;;
    esac
    shift
done

if [[ -z "$release" ]]
then usage --release must not be the empty string
     exit 1
fi

case "$install_crds" in
    (true|false) ;;
    (*) usage "--install-crds must be given 'true' or 'false'";
        exit 1;;
esac

case "$install_aps" in
    (true|false) ;;
    (*) usage "--install-aps must be given 'true' or 'false'";
        exit 1;;
esac

if [ -n "$existing_nvcr" ] && [ -n "$ensure_nvcr" ]; then
    usage "You can not specify both --existing-node-view-cluster-role and --ensure-node-view-cluster-role"
    exit 1
fi

oci_reg="${oci_reg:-ghcr.io/llm-d-incubation/llm-d-fast-model-actuation}"

cat >&2 <<EOF
Installing FMA release $release with
    --namespace=$ns
    --existing-node-view-cluster-role=$existing_nvcr
    --ensure-node-view-cluster-role=$ensure_nvcr
    --install-crds=$install_crds
    --install-admission-policies=$install_aps
    --config-dir=$config_dir
    --chart-instance-name=$chart_instance_name
EOF
if [[ -n "${chart_set[*]}" ]]; then
    for setting in "${chart_set[*]}"; do
        echo "    --chart-set $setting" >&2
    done
fi

nvcr="${existing_nvcr}${ensure_nvcr}"

if [ -z "$ensure_nvcr" ]; then true
elif already=$(kubectl get -n "$ns" ClusterRole "$ensure_nvcr" -o json 2>/dev/null); then
    if jq --exit-status '[ .rules[] | select(
            (.apiGroups | index("")) and
            (.resources | index("nodes")) and
            (.verbs | index("get")) and
            (.verbs | index("list")) and
            (.verbs | index("watch")) and
            true) ] | length > 0' <<<$already > /dev/null
    then
        echo "ClusterRole $ensure_nvcr already exists and is adequate"
    else
        extended=$(jq '.rules |= . + [{"apiGroups":[""], "resources":["nodes"], "verbs":["get","list","watch"]}]' <<<$already)
        kubectl apply -n "$ns" -f - <<<$extended
    fi
else
     kubectl apply -n "$ns" -f - <<EOF
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: $ensure_nvcr
rules:
- apiGroups: [ "" ]
  resources: [ nodes ]
  verbs: [ get, list, watch ]
EOF
fi

if [[ "$install_crds" == "true" ]]; then
    if [ -n "$config_dir" ]; then
        ysrc=$(cat "${config_dir}/crds.yaml")
    else
        ysrc=$(curl "https://raw.githubusercontent.com/llm-d-incubation/llm-d-fast-model-actuation/refs/tags/v$release/config/crds.yaml")
    fi
    kubectl apply -n "$ns" --server-side -f - <<<"$ysrc"
    jsrc=$(yq -o json eval . <<<$ysrc)
    for name in $(jq -r .metadata.name <<<$jsrc); do
        if [[ "$name" == "null" ]]; then
            echo "Eek! ysrc=$ysrc" >&2
            exit 1
        fi
        kubectl wait -n "$ns" crd "$name" --for condition=Established
	echo "CRD $name is Established" >&2
    done
fi

if [[ "$install_aps" != "true" ]]; then true
elif [ -n "$config_dir" ]; then
    kubectl apply -n "$ns" -f "${config_dir}/validating-admission-policies.yaml"
else
    kubectl apply -n "$ns" -f "https://raw.githubusercontent.com/llm-d-incubation/llm-d-fast-model-actuation/refs/tags/v$release/config/validating-admission-policies.yaml"
fi

helm_args=()
for arg in "${chart_set[@]}"; do
    helm_args+=(--set "$arg")
done

helm_args+=(
    --set global.imageRegistry="${oci_reg}"
    --set global.imageTag="v${release}"
)

if [ -n "$nvcr" ]; then
    helm_args+=(--set global.nodeViewClusterRole="${nvcr}")
fi

helm upgrade --install "$chart_instance_name" \
    "oci://${oci_reg}/charts/fma-controllers" \
    --version "$release" \
    -n "$ns" \
    "${helm_args[@]}"

kubectl wait -n "$ns" --for=condition=available --timeout=180s \
    deployment "${chart_instance_name}-dual-pods-controller"
kubectl wait -n "$ns" --for=condition=available --timeout=120s \
    deployment "${chart_instance_name}-launcher-populator"

echo "FMA successfully installed" >&2
