# Design

- [Launcher](launcher.md)
- [Fast Model Actuation with Process Flexibility and Dual Pods](dual-pods.md)
- [Cluster Sharing](cluster-sharing.md)
- [Prometheus Metrics](metrics.md)
- [Design Rules](../DESIGN_RULES.md)

# Well-Lit Path

There is an llm-d "well-lit path" document [in the llm-d repo](https://github.com/llm-d/llm-d/tree/main/guides/fast-model-actuation).

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
