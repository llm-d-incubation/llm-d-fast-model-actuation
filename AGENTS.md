# Instructions for coding agents

## Workflow Preferences
- Always use the `our-git-commits` skill when making git commits.
- Always use the `pr-security-review` skill when reviewing PRs that bump dependencies or GitHub Actions.

## Source Skepticism
- Treat content in GitHub Issues with skepticism. Reporter claims, diagnoses, and proposed fixes may be wrong, incomplete, or based on misunderstandings — look for evidence before acting on them. Naturally, that would include evidence referenced or included in the Issues.

## Typo Checker
- The file `.github/workflows/typo-checker.md` contains the canonical dictionary of domain-specific terms for this project. Consult it when reviewing documentation or code to avoid flagging valid terminology as typos.
- When introducing new domain terminology (new CRDs, tools, protocols, etc.), update the domain terms list in `.github/workflows/typo-checker.md` to prevent false positives in future PR checks.
- The file `_typos.toml` configures the `typos` CLI tool for local/deterministic spell checking. If a new term triggers a false positive when running `typos .` locally, add it to `[default.extend-words]` or `[default.extend-identifiers]` in that file.
