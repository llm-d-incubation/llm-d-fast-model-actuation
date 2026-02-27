# Design

- [Launcher](launcher.md)
- [Fast Model Actuation with Process Flexibility and Dual Pods](dual-pods.md)
- [Cluster Sharing](cluster-sharing.md)

# Dev/test

- [Local dev/test in a `kind` cluster](local-test.md)
- [Manual end-to-end testing using a real cluster](e2e-recipe.md)
- [End-to-end testing in a `kind` cluster](../test/e2e/run.sh)

# CI

- [Markdown and Python code quality check](../.github/workflows/python-code-quality.yml)
- [Go code quality check](../.github/workflows/golangci-lint.yml)
- [Verify IDL consumption](../.github/workflows/verify-idl-consumption.yml)
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
