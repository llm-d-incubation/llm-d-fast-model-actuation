#!/usr/bin/env bash

# Usage: $0 packagename
# where packagename is something like `llm-d-fast-model-actuation%2Frequester`.

# Purpose: enumerate container image versions that are unwanted.
# A wanted image is one that:
# (a) has a tag that does not start with "ref-" nor end with ".sbom";
# (b) has a tag and was created less than a week ago;
# (c) is referenced by one in category (a) or (b);
# or (d) has a tag that is "${name/:/-}.sbom" where `name`
#        is the "algo:hex" of id of something in (a) or (b) or (c).

# Required environment variables:
# GH_TOKEN or GITHUB_TOKEN --- classic with read:packages permission.

set -euo pipefail
set -x

if [ $# != 1 ]; then
    echo "$0" 'Usage: packagename (e.g., `llm-d-fast-model-actuation%2Frequester`)' >&2
    exit 1
fi

pkg="$1"
dir=$(mktemp -d -p /tmp listing-XXX)

gh api -H "Accept: application/vnd.github+json" -H "X-GitHub-Api-Version: 2026-03-10" --paginate "/orgs/llm-d-incubation/packages/container/${pkg}/versions" | jq '.[]' > "${dir}/versions.json"

jq -cr '.name + " " + (.id | tostring)' "${dir}/versions.json" > "${dir}/all-pairs"

jq -cr 'select( ((.metadata.container.tags//[]) | any((startswith("ref-") or endswith(".sbom")) | not)) or (.created_at | fromdateiso8601) > (now - 604800) and ( (.metadata.container.tags//[]) | length) > 0  ) | .name' "${dir}/versions.json" > "${dir}/keep-tagged-names"

jq -cr '.name as $name | .metadata.container.tags.[] | $name + " " + .' "${dir}/versions.json" > "${dir}/tag-table"

cp "${dir}/keep-tagged-names" "${dir}/keep-names"

pkg_slash=$(sed 's:%2F:/:g' <<<$pkg)
cat "${dir}/keep-tagged-names" | while read name; do
    oras manifest fetch "ghcr.io/llm-d-incubation/${pkg_slash}@${name}" > "${dir}/manifest-${name}"
    jq -cr '(.manifests // []) .[] | .digest' "${dir}/manifest-${name}" >> "${dir}/keep-names"
done

sed 's/^\(.*\):\(.*\)$/\1-\2.sbom/' "${dir}/keep-names" > "${dir}/keep-sbom-tags"

grep -f "${dir}/keep-sbom-tags" "${dir}/tag-table" | awk '{ print $1 }' > "${dir}/keep-sbom-names"

cat "${dir}/keep-sbom-names" >> "${dir}/keep-names"

grep -v -f "${dir}/keep-names" "${dir}/all-pairs" | awk '{ print $2 }'

exit

# trap "rm -rf '$dir'" EXIT

hack/find-unwanted-package-versions.sh $pkg > ids

cat ids | while read id; do
    echo $id
    gh api --method DELETE -H "Accept: application/vnd.github+json" -H "X-GitHub-Api-Version: 2026-03-10"  "/orgs/llm-d-incubation/packages/container/$pkg/versions/$id"
done
