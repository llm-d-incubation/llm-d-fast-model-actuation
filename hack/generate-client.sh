#!/usr/bin/env bash
set -o errexit
set -o nounset
set -o pipefail
set -o xtrace

SCRIPT_ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)

source "${CODE_GEN_DIR}/kube_codegen.sh"

PKG_ROOT="github.com/llm-d-incubation/llm-d-fast-model-actuation"

rm -rf "${SCRIPT_ROOT}/pkg/generated"

true || kube::codegen::gen_helpers \
    --boilerplate "${SCRIPT_ROOT}/hack/boilerplate.go.txt" \
    "${SCRIPT_ROOT}/api"

kube::codegen::gen_client \
    --with-applyconfig \
    --with-watch \
    --output-dir "${SCRIPT_ROOT}/pkg/generated" \
    --output-pkg    "${PKG_ROOT}/pkg/generated" \
    --boilerplate "${SCRIPT_ROOT}/hack/boilerplate.go.txt" \
    "${SCRIPT_ROOT}/api"
