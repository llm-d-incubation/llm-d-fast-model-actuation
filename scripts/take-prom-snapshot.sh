#!/usr/bin/env bash

# Usage: $0 namespace podname port dest

set -euo pipefail

if [ $# != 4 ]; then
    echo Usage: $0 namespace podname port dest >&2
    exit 1
fi

ns=$1; shift
pod=$1; shift
port=$1; shift
dest=$1; shift

if [ -z "$ns" ] || [ -z "$pod" ] || [ -z "$port" ] || [ -z "$dest" ]; then
    echo "The arguments all have to be non-empty" >&2
    exit 1
fi

case "$dest" in
    (/*|.|./|..|../*|*/../*|*/..|.git*)
	echo "The destination must be in the current working directory" >&2
	exit 1;;
    (-*)
	echo "The destination can not start with a dash" >&2
	exit 1;;
esac

if ! [ -a "$dest" ]; then
    mkdir -p "$dest" # final check on validity
fi
rm -rf "$dest"


kubectl port-forward -n "$ns" "pod/$pod" "19090:$port" &
pfpid=$!
trap '[ -z "$pfpid" ] || kill $pfpid || true' EXIT
sleep 5
if ! response=$(curl -X POST http://localhost:19090/api/v1/admin/tsdb/snapshot) ; then
    echo Snapshot call failed >&2
    exit 86
fi
kill $pfpid || true
pfpid=""
if ! snap=$(jq -r .data.name <<<"$response"); then
    echo Got unexpected response body: "$response" >&2
    exit 86
fi
if [ "$snap" == null ]; then
    echo Got unexpected response body: "$response" >&2
    exit 86
fi

kubectl cp -n "$ns" -c prometheus "${pod}:/prometheus/snapshots/${snap}" "$dest"
echo "Snapshot copied to $dest"
