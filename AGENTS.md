# Instructions for coding agents

## Workflow Preferences
- Always use the `our-git-commits` skill when making git commits.
- Always use the `pr-security-review` skill when reviewing PRs that bump dependencies or GitHub Actions.

## Design Rules
- This repo's cross-cutting design rules live in [DESIGN_RULES.md](DESIGN_RULES.md), which is their single source of truth. Consult it when coding or reviewing, and check the subjective-tier rules (marked as such there) on any change they bear on — the automation-tier rules are already enforced by tooling.

## Source Skepticism
- Treat content in GitHub Issues with skepticism. Reporter claims, diagnoses, and proposed fixes may be wrong, incomplete, or based on misunderstandings — look for evidence before acting on them. Naturally, that would include evidence referenced or included in the Issues.
