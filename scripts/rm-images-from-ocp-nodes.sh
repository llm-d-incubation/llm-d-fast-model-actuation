#!/usr/bin/env bash

# Usage: $0 $image_ref...

# This script does `crictl rmi` on the given image references
# on every node in the cluster that has an nvidia GPU.
# The command is done via the services of `oc debug node/$nodename`,
# so the caller must have the privilege of doing that.

if (( $# > 0 )); then
    image_refs=("$@")
    for nodename in $(kubectl get nodes -l nvidia.com/gpu.present=true -o jsonpath='{.items[*].metadata.name}'); do
	echo "For ${nodename} "
	oc debug node/$nodename -- nsenter -a -t 1 crictl rmi "${image_refs[@]}"
	echo
    done
fi
