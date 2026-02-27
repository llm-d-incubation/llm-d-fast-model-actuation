# OpenShift E2E Tests (WIP)

This directory contains end-to-end tests for OpenShift environments.

## Running the Tests

There are two ways to run the OpenShift e2e tests:

### GitHub UI

1. Go to https://github.com/llm-d-incubation/llm-d-fast-model-actuation/actions/workflows/ci-e2e-openshift.yaml
2. Click "Run workflow" button (top right)
3. Optionally check "Skip cleanup after tests"
4. Click the green "Run workflow" button

### CLI

Basic run:
```bash
gh workflow run ci-e2e-openshift.yaml --repo llm-d-incubation/llm-d-fast-model-actuation
```

Or with skip cleanup:
```bash
gh workflow run ci-e2e-openshift.yaml --repo llm-d-incubation/llm-d-fast-model-actuation -f skip_cleanup=true
```

## Important Notes

- **Manual triggers**: When triggered manually (vs from a PR), it builds from the main branch HEAD and the namespace uses the run ID instead of a PR number (e.g. `fma-e2e-pr-12345678`).

- **Existing PRs**: Pushing a new commit auto-triggers the workflow.

- **Fork PRs**: A maintainer must comment as following:
  - `/ok-to-test` (first time)
  - `/retest` (to re-run)
