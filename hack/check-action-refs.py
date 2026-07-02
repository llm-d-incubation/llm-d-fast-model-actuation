#!/usr/bin/env python3

# Copyright 2026 The llm-d Authors.
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
# 	http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

"""Check that GitHub Actions references in workflows obey design rule DR-10.

See DESIGN_RULES.md (rule DR-10) for the full statement. In brief, this checks
the *mechanical* clauses:

  * First-party actions (the ``llm-d/llm-d-infra`` repo or this repo) are
    referenced by branch ``main``.
  * Every third-party action is pinned by a full 40-hex commit SHA and carries a
    version-tag comment, e.g.::

        uses: actions/checkout@9c091bb...  # v7.0.0

  * Every reference to a given action across all workflows uses the same SHA and
    tag.
  * With ``--online``: the commented tag actually resolves to the pinned SHA
    (verified via ``gh api``).

It does NOT check clause (b) of DR-10 (no known vulnerabilities / soaked >=7 days /
not egregiously stale); that clause is advisory and checked by a human or AI
reviewer.

Usage (run from the repo root)::

    hack/check-action-refs.py            # offline checks only
    hack/check-action-refs.py --online   # also verify each tag resolves to its SHA

Exits non-zero and prints ``file:line: reason`` for every violation.
"""

import argparse
import glob
import json
import os
import re
import subprocess
import sys
from typing import Iterator, NamedTuple, Optional

# Repositories whose actions/reusable workflows are "first-party" and therefore
# referenced by branch "main" rather than pinned by SHA.
FIRST_PARTY_REPOS = {
    "llm-d/llm-d-infra",
    "llm-d-incubation/llm-d-fast-model-actuation",
}

# A "uses:" line, capturing the reference and any trailing comment. The comment
# is preserved because a YAML parser would discard it, and DR-10 lives there.
USES_RE = re.compile(r"^\s*(?:-\s*)?uses:\s*(?P<ref>\S+)\s*(?:#\s*(?P<comment>.*))?$")

# Commit SHAs are case-insensitive hex; compare them via .lower() elsewhere.
FULL_SHA_RE = re.compile(r"^[0-9a-fA-F]{40}$")

# A reference in DR-10's scope: an external repo ``owner/repo`` (each a GitHub
# name segment), optionally with a ``/subpath`` (reusable workflows), then
# ``@<ref>``. This deliberately excludes local ``./…`` references and other
# ``uses:`` forms that can contain ``@`` (e.g. ``docker://image@sha256:…``).
REMOTE_REF_RE = re.compile(r"^[\w.-]+/[\w.-]+(?:/[\w./-]+)?@[^\s]+$")

# The version tag is the leading run of the comment, optionally followed by a
# single punctuation char, then certainly whitespace or end-of-line. So
# "v7.0.0", "v7.0.0 latest", and "v7.0.0, latest" all yield "v7.0.0".
TAG_RE = re.compile(r"^(?P<tag>\S+?)[.,;:]?(?:\s|$)")


class Violation(NamedTuple):
    wf_path: str
    lineno: int
    ref: str
    reason: str

    def __str__(self) -> str:
        return f"{self.wf_path}:{self.lineno}: {self.reason} (uses: {self.ref})"


class Pin(NamedTuple):
    """A recorded pin of a third-party action, used by the clause-(a) same-version
    check to compare its other references against. Fields: the pinned commit
    ``sha``; its version-tag ``tag``; and the ``wf_path`` and ``lineno`` where this
    pin was seen (cited when another reference disagrees)."""

    sha: str
    tag: str
    wf_path: str
    lineno: int


class UsesRef(NamedTuple):
    """One parsed ``uses:`` line.

    Fields:
      * ``wf_path``     -- path to the workflow file the line appears in.
      * ``lineno``      -- 1-based line number within that file.
      * ``ref_path``    -- the part of the reference before ``@`` (e.g.
                           ``actions/checkout`` or a reusable-workflow path).
      * ``ref_version`` -- what follows ``@``: a commit SHA or a branch/tag.
      * ``comment``     -- the trailing ``# ...`` comment body, or None.
      * ``ref``         -- the full reference as written, for reporting.
    """

    wf_path: str
    lineno: int
    ref_path: str
    ref_version: str
    comment: Optional[str]
    ref: str


def workflow_files() -> list[str]:
    files: list[str] = []
    for pattern in ("*.yml", "*.yaml"):
        files.extend(glob.glob(os.path.join(".github", "workflows", pattern)))
    return sorted(files)


def repo_of(ref_path: str) -> str:
    """Return owner/repo for the part of a ``uses:`` ref before the ``@``.

    Handles both plain actions (``actions/checkout``) and reusable workflows
    with a path (``llm-d/llm-d-infra/.github/workflows/x.yaml``).
    """
    parts = ref_path.split("/")
    if len(parts) < 2:
        return ref_path
    return "/".join(parts[:2])


def _require_object(data: object, api_path: str, key: str) -> dict:
    """Return ``data[key]`` when both are dicts; exit hard otherwise.

    Used to validate the shape of a ``gh api`` JSON payload. An unexpected shape
    (e.g. a JSON array from a ref name that matches several refs, or a missing
    nested object) means the API contract we rely on does not hold, so we surface
    it rather than let it masquerade as an unresolvable tag.
    """
    if not isinstance(data, dict):
        sys.exit(
            f"error: 'gh api {api_path}' returned {type(data).__name__}, "
            f"expected a JSON object"
        )
    value = data.get(key)
    if not isinstance(value, dict):
        sys.exit(
            f"error: 'gh api {api_path}' response has no object at "
            f"'{key}' (got {type(value).__name__})"
        )
    return value


def extract_tag(comment: Optional[str]) -> Optional[str]:
    """Return the version tag from a comment body, or None if absent."""
    if not comment:
        return None
    m = TAG_RE.match(comment.strip())
    return m.group("tag") if m else None


def parse_uses(wf_paths: list[str]) -> Iterator[UsesRef]:
    """Yield a UsesRef for each ``uses:`` line across the given workflow files."""
    for wf_path in wf_paths:
        with open(wf_path, encoding="utf-8") as f:
            for lineno, line in enumerate(f, start=1):
                m = USES_RE.match(line.rstrip("\n"))
                if not m:
                    continue
                ref = m.group("ref")
                if not REMOTE_REF_RE.match(ref):
                    # Out of DR-10's scope: local ``./…`` actions, ``docker://…``
                    # forms, and malformed refs. Only ``owner/repo…@<ref>`` counts.
                    continue
                ref_path, _, ref_version = ref.partition("@")
                yield UsesRef(
                    wf_path=wf_path,
                    lineno=lineno,
                    ref_path=ref_path,
                    ref_version=ref_version,
                    comment=m.group("comment"),
                    ref=ref,
                )


class TagResolver:
    """Resolves (repo, tag) -> commit SHA via ``gh api``, caching each pair."""

    def __init__(self) -> None:
        self._cache: dict[tuple[str, str], Optional[str]] = {}
        self.calls: int = 0

    def resolve(self, repo: str, tag: str) -> Optional[str]:
        key = (repo, tag)
        if key in self._cache:
            return self._cache[key]
        self.calls += 1
        sha = self._resolve_uncached(repo, tag)
        self._cache[key] = sha
        return sha

    def _resolve_uncached(self, repo: str, tag: str) -> Optional[str]:
        # A lightweight tag points straight at a commit; an annotated tag points
        # at a tag object that must be dereferenced. Query the ref, then follow
        # the object if it is a tag.
        api_path = f"repos/{repo}/git/ref/tags/{tag}"
        obj = self._gh_ref(api_path)
        if obj is None:
            # gh reported no such ref: a genuinely unresolvable tag.
            return None
        if obj.get("type") == "tag":
            api_path = f"repos/{repo}/git/tags/{obj['sha']}"
            tag_obj = self._gh_api(api_path)
            tag_target = _require_object(tag_obj, api_path, "object")
            return tag_target.get("sha")
        return obj.get("sha")

    def _gh_ref(self, api_path: str) -> Optional[dict]:
        """Return the ``object`` of a git-ref response, or None if gh found none.

        A ref query can also return a JSON array (when the name matches several
        refs) or an otherwise unexpected shape; those are tooling/contract
        problems, so ``_require_object`` turns them into a hard error rather than
        a silently-unresolvable tag.
        """
        data = self._gh_api(api_path)
        if data is None:
            return None
        return _require_object(data, api_path, "object")

    def _gh_api(self, api_path: str) -> Optional[object]:
        """Run ``gh api <path>``.

        Returns the parsed JSON (dict or list) on success, or None only when gh
        reports HTTP 404 (a genuinely missing ref). Any other failure — rate
        limit, auth, network, or non-JSON output — is a tooling problem, so we
        exit hard with gh's own error rather than mistake it for an unresolvable
        tag (which would be reported as a DR-10 violation).
        """
        try:
            out = subprocess.run(
                ["gh", "api", api_path],
                capture_output=True,
                text=True,
                check=False,
            )
        except FileNotFoundError:
            sys.exit("error: 'gh' not found; --online requires the GitHub CLI")
        if out.returncode != 0:
            # gh writes e.g. "gh: Not Found (HTTP 404)" to stderr.
            if "HTTP 404" in out.stderr:
                return None
            sys.exit(
                f"error: 'gh api {api_path}' failed "
                f"(exit {out.returncode}): {out.stderr.strip()}"
            )
        try:
            return json.loads(out.stdout)
        except json.JSONDecodeError as err:
            snippet = out.stdout.strip()[:200]
            sys.exit(
                f"error: 'gh api {api_path}' returned unparsable output "
                f"({err}); got: {snippet!r}"
            )


def check(online: bool) -> tuple[list[Violation], Optional[TagResolver]]:
    violations: list[Violation] = []
    resolver = TagResolver() if online else None

    # For clause (a)'s "one SHA/tag per action across all workflows": the pin
    # recorded per third-party action, to compare its other references against.
    pins: dict[str, Pin] = {}

    for u in parse_uses(workflow_files()):
        repo = repo_of(u.ref_path)
        if repo in FIRST_PARTY_REPOS:
            if u.ref_version != "main":
                violations.append(
                    Violation(
                        u.wf_path,
                        u.lineno,
                        u.ref,
                        f"first-party {repo} must be referenced by @main",
                    )
                )
            continue

        # Third-party: require SHA pin + tag comment (clause a).
        if not FULL_SHA_RE.match(u.ref_version):
            violations.append(
                Violation(
                    u.wf_path,
                    u.lineno,
                    u.ref,
                    "third-party action must be pinned by full 40-hex commit SHA",
                )
            )
            continue
        tag = extract_tag(u.comment)
        if tag is None:
            violations.append(
                Violation(
                    u.wf_path,
                    u.lineno,
                    u.ref,
                    "third-party SHA pin must carry a version-tag comment",
                )
            )
            continue

        # Clause (a): every reference to this action must use the same SHA and tag.
        # SHAs are case-insensitive; tags are not.
        pin = pins.get(repo)
        if pin is None:
            pins[repo] = Pin(u.ref_version, tag, u.wf_path, u.lineno)
        elif (u.ref_version.lower(), tag) != (pin.sha.lower(), pin.tag):
            violations.append(
                Violation(
                    u.wf_path,
                    u.lineno,
                    u.ref,
                    f"action {repo} is also referenced as {pin.sha} "
                    f"(# {pin.tag}) at {pin.wf_path}:{pin.lineno}; all "
                    f"workflows must use the same SHA and tag",
                )
            )

        if online:
            assert resolver is not None
            resolved = resolver.resolve(repo, tag)
            if resolved is None:
                violations.append(
                    Violation(
                        u.wf_path,
                        u.lineno,
                        u.ref,
                        f"tag {tag} could not be resolved for {repo}",
                    )
                )
            elif resolved.lower() != u.ref_version.lower():
                violations.append(
                    Violation(
                        u.wf_path,
                        u.lineno,
                        u.ref,
                        f"comment tag {tag} resolves to {resolved}, "
                        f"not the pinned SHA",
                    )
                )

    return violations, resolver


def main() -> int:
    # __doc__ is Optional[str] (absent under python -OO), so guard the access.
    description = __doc__.splitlines()[0] if __doc__ else None
    parser = argparse.ArgumentParser(description=description)
    parser.add_argument(
        "--online",
        action="store_true",
        help="also verify each tag comment resolves to its pinned SHA (uses gh)",
    )
    args = parser.parse_args()

    violations, resolver = check(args.online)

    if violations:
        for v in violations:
            print(v, file=sys.stderr)
        print(
            f"\n{len(violations)} GitHub Actions reference violation(s) "
            f"of design rule DR-10; see DESIGN_RULES.md",
            file=sys.stderr,
        )
        return 1

    mode = "online" if args.online else "offline"
    extra = f" ({resolver.calls} tag lookup(s))" if resolver else ""
    print(f"All GitHub Actions references comply with DR-10 ({mode}){extra}.")
    return 0


if __name__ == "__main__":
    sys.exit(main())
