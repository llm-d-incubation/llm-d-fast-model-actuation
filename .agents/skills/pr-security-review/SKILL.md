name: pr-security-review
description: When reviewing PRs that bump dependencies or GitHub Actions, actively search for known vulnerabilities rather than reasoning abstractly
---

When reviewing dependency bump PRs, verify security by actually checking external sources — don't just reason about it in the abstract.

Checklist:
1. Verify the pinned SHA matches the upstream tag
2. Search github.com/advisories for the dependency
3. Web search for CVEs/vulnerabilities in the specific new version being introduced
4. Check the release notes for any security-relevant changes
