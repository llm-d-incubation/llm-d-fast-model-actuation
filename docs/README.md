# Design

- [Launcher](launcher.md)
- [Fast Model Actuation with Process Flexibility and Dual Pods](dual-pods.md)
- [Cluster Sharing](cluster-sharing.md)
- [Prometheus Metrics](metrics.md)
- [Design Rules](../DESIGN_RULES.md)

# Well-lit Path

This will ultimately go into
https://github.com/llm-d/llm-d/tree/main/guides --- but maybe not for
some time, because FMA is currently only a sandbox project. In the
meantime, we home a copy here while we work on developing it.

- [Well-lit Path](../guides/fast-model-actuation/README.md)

As explained in https://github.com/llm-d/llm-d/pull/1779, this is
maintained using the structured guide mechanism described in
https://github.com/llm-d/llm-d/pull/1988 . The following shows me
using the three scripts of that mechanism on the copy in my working
tree for FMA on my local machine.

```console
me@mymac llm-d-fast-model-actuation % ../../llm-d/llm-d/scripts/guide-render.py --yaml guides/fast-model-actuation/guide.yaml --readme guides/fast-model-actuation/README.md
updated guides/fast-model-actuation/README.md

me@mymac llm-d-fast-model-actuation % ../../llm-d/llm-d/scripts/guide-check-yaml.py guides/fast-model-actuation/guide.yaml
Installed 1 package in 2ms
guides/fast-model-actuation/guide.yaml: OK

me@mymac llm-d-fast-model-actuation % ../../llm-d/llm-d/scripts/guide-check-readme.py --yaml guides/fast-model-actuation/guide.yaml --readme guides/fast-model-actuation/README.md
Installed 1 package in 2ms
guides/fast-model-actuation/README.md: OK

```

To support that, I have
https://github.com/Vezio/llm-d/tree/structured-guide-implementation
checked out in `../../llm-d/llm-d/`.

# Dev/test

- [Local dev/test in a `kind` cluster](local-test.md)
- [Manual end-to-end testing using a real cluster](e2e-recipe.md)
- [End-to-end testing without launcher in a `kind` cluster](../test/e2e/run.sh)
- [End-to-end testing with launcher in a `kind` cluster](../test/e2e/run-launcher-based.sh)

# CI

- [Code quality: Markdown, Python & Go lint/format/typos plus launcher tests](../.github/workflows/code-quality.yml)
- [Verify IDL consumption](../.github/workflows/verify-idl-consumption.yml)
- [Check GitHub Actions references (DR-10)](../.github/workflows/check-action-refs.yml)
- [Test build of dual-pods controller image](../.github/workflows/build-controller-image.yml)
- [Test build of launcher image](../.github/workflows/build-launcher-image.yml)
- [Test build of requester image](../.github/workflows/build-requester-image.yml)
- [Test build of launcher populator image](../.github/workflows/build-populator-image.yml)
- [End-to-end testing in CI using a `kind` cluster](../.github/workflows/pr-test-in-kind.yml)
- [Launcher-based end-to-end testing in CI](../.github/workflows/launcher-based-e2e-test.yml)
- [End-to-end testing on OpenShift](../.github/workflows/ci-e2e-openshift.yaml)
- [Signed commits check](../.github/workflows/ci-signed-commits.yaml)
- [Release – Build Images & Publish Helm Charts to GHCR](../.github/workflows/publish-release.yaml)

# Release

- [Release process](release-process.md)
