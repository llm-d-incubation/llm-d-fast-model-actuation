# Release Process

This document describes how to create and publish a release of Fast Model Actuation (FMA).

## Release Types

### Regular Release
For stable, production-ready versions:
- Tag format: `v0.3.1`, `v1.0.0`, etc.
- Mark as "Latest release" in GitHub

### Pre-Release (Test Release)
For testing before official release:
- Tag format: `v0.3.0-alpha.1`, `v0.3.0-rc.1`, `v0.3.0-beta.2`, etc.
- Mark as "Pre-release" in GitHub
- Useful for integration testing in llm-d-benchmark or other environments

## Step-by-Step Release Process

### 1. Create and Push the Git Tag

Ensure all changes for the release are merged to `main`. Then, create and push the Git tag:

```bash
git tag v0.3.1 # regular release (recommended format)

git tag v0.3.0-alpha.1 # pre-release

git push origin v0.3.1  # or your tag name
```

### 2. Create the GitHub Release

1. Go to https://github.com/llm-d-incubation/llm-d-fast-model-actuation/releases/new

2. **Choose a tag**: Select the tag you just pushed (e.g., `v0.3.1`)

3. **Release title**: Use the tag name (e.g., `v0.3.1`)

4. **Description**: Document what's new in this release:
   ```markdown
   ## What's Changed
   - Feature: Added support for X
   - Fix: Resolved issue with Y
   - Improvement: Enhanced Z performance

   ## Breaking Changes
   - Changed API for ...

   ## Upgrade Notes
   - Users should ...
   ```

5. **Pre-release checkbox**:
   - ✅ Check for pre-releases (alpha, beta, rc)
   - ⬜ Leave unchecked for regular releases

6. **Click "Publish release"**

### 3. Automated Workflow Execution

Once you publish the release, the `publish-release` workflow runs automatically and performs the following steps:

1. **Builds 4 container images**:
   - `ghcr.io/llm-d-incubation/llm-d-fast-model-actuation/dual-pods-controller:v0.3.1`
   - `ghcr.io/llm-d-incubation/llm-d-fast-model-actuation/launcher-populator:v0.3.1`
   - `ghcr.io/llm-d-incubation/llm-d-fast-model-actuation/launcher:v0.3.1`
   - `ghcr.io/llm-d-incubation/llm-d-fast-model-actuation/requester:v0.3.1`

2. **Updates Helm chart values** with the release-specific image references

3. **Packages Helm charts** using `helm package --version 0.3.1 --app-version v0.3.1`

4. **Publishes chart to GHCR**:
   - `oci://ghcr.io/llm-d-incubation/llm-d-fast-model-actuation/charts/fma-controllers`

### 4. Manual Testing

For testing purposes, the workflow can be manually triggered:

Use the GitHub Actions UI to manually trigger the workflow with a specific tag
  1. Go to Actions → publish release → Run workflow
  2. Enter the tag (e.g., `v0.3.1`)
  3. Click "Run workflow"

## Related Documentation

- [Workflow Source](../.github/workflows/publish-release.yaml) - The actual workflow implementation
