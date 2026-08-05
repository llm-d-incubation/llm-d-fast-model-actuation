#!/usr/bin/env bash

set -euo pipefail

release=""
img_tag=""
ns=$(kubectl get sa default -o jsonpath='{.metadata.namespace}')
existing_nvcr=""
ensure_nvcr=""
install_crds="false"
install_aps="false"
enable_lp="true"
oci_reg=""
config_dir=""
chart_set=()
chart_instance_name=fma

function usage() {
    (
        (( $# > 0 )) && echo "$*" || true
        cat <<EOF
$0 usage: OPTIONS

where OPTIONS are any of the following, but you must
specify either --release or both --image-tag and --oci-registry.

    --release[=]\$semver
    --image-tag[=]\$tag
    --namespace[=]\$ns
    --existing-node-view-cluster-role[=]\$crname
    --ensure-node-view-cluster-role[=]\$crname
    --install-crds[=]\$trueorfalse
    --install-admission-policies[=]\$trueorfalse
    --enable-launcher-populator[=]\$trueorfalse
    --oci-registry[=]\$registry
    --config-dir[=]\$pathname
    --chart-instance-name[=]\$chart_instance_name
    --chart-set[=]\$path=\$val (may be repeated)
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

        (--image-tag=*)
            img_tag=${1#--image-tag=};;
        (--image-tag) if (( $# > 1 ))
                    then img_tag="$2"; shift
                    else usage missing value for --image-tag; exit 1
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

        (--enable-launcher-populator=*)
            enable_lp=${1#--enable-launcher-populator=};;
        (--enable-launcher-populator) if (( $# > 1 ))
                    then enable_lp="$2"; shift
                    else usage missing value for --enable-launcher-populator; exit 1
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
            chart_set+=("${1#--chart-set=}");;
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

case "$install_crds" in
    (true|false) ;;
    (*) usage "--install-crds must be given 'true' or 'false'";
        exit 1;;
esac

case "$install_aps" in
    (true|false) ;;
    (*) usage "--install-admission-policies must be given 'true' or 'false'";
        exit 1;;
esac

case "$enable_lp" in
    (true|false) ;;
    (*) usage "--enable-launcher-populator must be given 'true' or 'false'";
        exit 1;;
esac

if [ -n "$existing_nvcr" ] && [ -n "$ensure_nvcr" ]; then
    usage "You can not specify both --existing-node-view-cluster-role and --ensure-node-view-cluster-role"
    exit 1
fi

if [ -n "$release" ] && [ -z "$img_tag" ]; then
   echo "Installing FMA release $release with" >&2
   oci_reg="${oci_reg:-ghcr.io/llm-d-incubation/llm-d-fast-model-actuation}"
elif [ -n "$img_tag" ] && [ -n "$oci_reg" ] && [ -z "$release" ]; then
    config_dir="${config_dir:-config}"
    echo "Installing FMA from local tree and images in $oci_reg tagged $img_tag, with" >&2
else
    usage "You must choose between installing a release or a local tree"
    exit 1
fi

cat >&2 <<EOF
    --namespace=$ns
    --existing-node-view-cluster-role=$existing_nvcr
    --ensure-node-view-cluster-role=$ensure_nvcr
    --install-crds=$install_crds
    --install-admission-policies=$install_aps
    --enable-launcher-populator=$enable_lp
    --config-dir=$config_dir
    --chart-instance-name=$chart_instance_name
EOF
(
    idx=0
    n=${#chart_set[*]}
    while (( idx < n )); do
        echo "    --chart-set ${chart_set[$idx]}" >&2
        let idx=idx+1
    done
)

nvcr="${existing_nvcr}${ensure_nvcr}"

if [ -z "$ensure_nvcr" ]; then true
elif already=$(kubectl get ClusterRole "$ensure_nvcr" -o json 2>/dev/null); then
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
        kubectl apply -f - <<<$extended
    fi
else
     kubectl apply -f - <<EOF
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
        ysrc=$(curl -fsSL "https://raw.githubusercontent.com/llm-d-incubation/llm-d-fast-model-actuation/refs/tags/v$release/config/crds.yaml")
    fi
    yq -o json eval . <<<$ysrc | jq -c . | while read -r obj; do
        crd_name=$(jq -r .metadata.name <<<$obj)
        if ! kubectl get crd "$crd_name" &>/dev/null; then
            kubectl create -f - <<<$obj
        elif kubectl get crd "$crd_name" -o json 2>/dev/null | jq -e --slurpfile desired <(jq .spec <<<$obj) '.spec as $existing | ($existing * $desired[0]) == $existing' &>/dev/null; then
            echo "  CRD $crd_name already exists and is up to date, skipping"
        else
            echo "  CRD $crd_name needs updating"
            kubectl apply --server-side -f - <<<$obj
            kubectl wait crd "$crd_name" --for condition=Established
        fi
    done
fi

if [[ "$install_aps" != "true" ]]; then true
elif [ -n "$config_dir" ]; then
    kubectl apply  -f "${config_dir}/validating-admission-policies.yaml"
else
    kubectl apply  -f "https://raw.githubusercontent.com/llm-d-incubation/llm-d-fast-model-actuation/refs/tags/v$release/config/validating-admission-policies.yaml"
fi

if [ -n "$release" ]; then
    helm_args=("oci://${oci_reg}/charts/fma-controllers" --version "$release")
else
    helm_args=(charts/fma-controllers --set global.local=true)
fi


idx=0
n=${#chart_set[*]}
while (( idx < n )); do
    helm_args+=(--set "${chart_set[$idx]}")
    let idx=idx+1
done

if [ -n "$nvcr" ]; then
    helm_args+=(--set global.nodeViewClusterRole="${nvcr}")
fi

if [ "$enable_lp" == "false" ]; then
    helm_args+=(--set launcherPopulator.enabled=false)
fi


helm upgrade --install "$chart_instance_name" \
    "${helm_args[@]}" \
    --namespace "$ns" \
    --set global.imageRegistry="${oci_reg}" \
    --set global.imageTag="${img_tag:-v${release}}"

kubectl wait -n "$ns" --for=condition=available --timeout=180s \
    deployment "${chart_instance_name}-dual-pods-controller"

if [ "$enable_lp" != "false" ]; then
    kubectl wait -n "$ns" --for=condition=available --timeout=120s \
            deployment "${chart_instance_name}-launcher-populator"
fi

echo "FMA successfully installed" >&2
