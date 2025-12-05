#!/usr/bin/env bash

# Usage: $0
# Current working directory needs to be the repo root.
# The git status is assumed to be clean at the start.
# This script checks that the IDL processing is correct.

rm -rf config/crd $(find api -name "zz_generated*") pkg/generated
make manifests || { echo "Failed to generate CRDs"; exit 1; }
make generate || { echo "Failed to generate auxiliary API code"; exit 1; }
make generate_client || { echo "Failed to generate client code"; exit 1; }
if ! git status --porcelain=v2 | wc -l | grep -qw 0; then
    git status
    exit 1
fi

