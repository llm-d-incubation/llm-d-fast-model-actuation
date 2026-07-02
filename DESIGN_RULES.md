# Design Rules

This document records the project's **design rules**: cross-cutting conventions
that any change is expected to respect, but that are not captured by the compiler,
the type system, or ordinary unit tests. They are the kind of expectation that is
easy to state ("every outbound call from a controller logs how long it took") and
easy to erode over time unless it is written down and checked.

## Two tiers of checking

A design rule is placed in one of two tiers, according to **how it is checked**:

1. **Automation tier** — the rule is checked by automation (a script, a linter).
   Such a check is mechanical, deterministic, and may run in more than one place
   (for example, both a pre-commit hook and a CI workflow). The automation tier is
   not new: much of it predates this document. Two places already hold such
   checks — the [pre-commit hooks](.pre-commit-config.yaml) (formatters, linters,
   typo and whitespace/file checks) and the [CI workflows](docs/README.md#ci)
   (which overlap the pre-commit hooks only partly and add checks of their own).
   Those checks are self-documented by their own configuration and are not
   re-catalogued here; only rules that need prose beyond their checker's config get
   a numbered entry below.

2. **Subjective tier** (requires judgement) — the rule cannot be reduced cleanly
   to a mechanical check, so it is checked by **whoever is doing the work — an AI
   agent or a human**. A subjective rule may be **firm** (a deviation should be
   treated as a defect) or **advisory** (a deviation is flagged, and a human
   decides whether to accept it).

For an advisory rule, an accepted deviation is an **exception**: it is recorded as
a note placed *next to the thing the rule applies to*, naming which aspect of the
rule is being waived, so that whoever reviews next — agent or human — recognizes
it as already accepted and does not re-flag it. There is no universal syntax for
that note; each rule that admits exceptions defines its own, because the note lives
wherever that rule applies.

This document is the single source of truth for the rules below. Whoever does the
work is pointed here: AI agents by [`AGENTS.md`](AGENTS.md), human contributors by
[`CONTRIBUTING.md`](CONTRIBUTING.md). Other documents (for example that
`CONTRIBUTING.md` section and the `pr-security-review` skill) link here rather
than restating a rule.

## Adding a rule

Give each rule a stable id (`DR-<n>`) and a short title, then state:

- **Rule** — what must (or should) be true.
- **Rationale** — why, with links to the originating issue(s)/PR(s).
- **Enforcement** — which tier, and for automation-tier rules, the checker that
  enforces it.

Prefer moving a rule (or a clause of one) into the automation tier when a
mechanical check becomes practical; leave in the subjective tier only what
genuinely needs judgement.

---

## DR-10 — GitHub Actions references

### DR-10: Rule

In GitHub Actions workflow files (`.github/workflows/*.y*ml`), every
`uses:` reference that names an external repository (`owner/repo…@<ref>`) obeys
the following, by clause. Local references (`uses: ./…`, which have no `@<ref>`)
are out of scope.

- **(first-party)** References to first-party actions and reusable workflows —
  those in the [`llm-d/llm-d-infra`](https://github.com/llm-d/llm-d-infra) repo or
  in this repo — are by branch `main` (`@main`). *(automation tier)*
- **(a)** Every other (third-party) reference is pinned by a **full 40-hex commit
  SHA** and carries a **comment naming the corresponding version tag**, e.g.
  `uses: actions/checkout@9c091bb… # v7.0.0`. The commented tag must actually
  resolve to the pinned SHA, and **every reference to a given action across all
  workflows uses the same SHA and tag**. *(automation tier)*
- **(b)** The version so chosen for an action **should** have **no reported
  vulnerabilities**, have been published **at least 7 days** ago, and not be
  egregiously out of date — but it need not be the latest release. Because of
  clause (a) there is one such version per action. *(subjective tier, advisory —
  see below)*

### DR-10: Rationale

SHA-pinning third-party actions defends against a moving tag being
repointed at malicious code (supply-chain hardening); the tag comment keeps the
pin human-readable and reviewable. Pinning every workflow to the same SHA/tag for
a given action keeps its version consistent and makes a bump a single, reviewable
change. The 7-day soak lets newly published releases accumulate scrutiny before we
adopt them; we do not otherwise require the newest release. First-party reusable
workflows track `main` by design. Established in
[#633](https://github.com/llm-d-incubation/llm-d-fast-model-actuation/pull/633).

### DR-10: Enforcement

- The **first-party** and **(a)** clauses are checked by
  [`hack/check-action-refs.py`](hack/check-action-refs.py), run in both
  [pre-commit](.pre-commit-config.yaml) (offline: pin format, tag-comment
  presence, and one SHA/tag per action across workflows) and
  [CI](.github/workflows/check-action-refs.yml) (`--online`, which additionally
  verifies each tag resolves to its SHA via `gh`).
- Clause **(b)** is **subjective and advisory**: the script does not check it.
  Whoever is doing the work — an AI agent or a human — checks that the chosen
  version has no reported vulnerabilities, has soaked at least 7 days, and is not
  egregiously stale, and flags a deviation. **The user may allow an exception.**

### DR-10: Exceptions to clause (b)

First, some non-exceptions: clause (b) does not ask for the latest release, so
staying on an older version — including because a newer one has not yet soaked 7
days — is ordinary **compliance**, not an exception. A genuine exception is a
deliberate deviation from what clause (b) asks, e.g. staying on a version that has
a known vulnerability because no fixed release exists yet, or on one that is
egregiously stale.

Record a clause-(b) exception (per the framework convention above) as free-form
English **appended to the version-tag comment on the `uses:` line**, naming which
aspect of clause (b) is being waived, e.g.:

```yaml
uses: some/action@<sha>  # v1.2.0 — staying despite open CVE-2026-1234, no fixed release yet
```

Further justification is welcome but not required. (The automation checker reads
only the version tag at the start of the comment, so an exception note after it
does not affect the mechanical clauses.)

---

## DR-20 — External-process call latency logging

### DR-20: Rule

Every time a controller calls out to an external process — the launcher
API, a vLLM instance over HTTP, or the Kubernetes API — it emits a log line, on
the call's outcome, that carries a **start-timestamp field** for the call (e.g.
`httpCallStartTime` for launcher/vLLM HTTP calls, `k8sCallStartTime` for
Kubernetes API calls). Two shapes are acceptable:

- **start time only** — the recovered start is exact; the end is approximated by
  the log line's own klog timestamp; or
- **start time plus the elapsed duration** — both endpoints are recovered exactly
  from logged values.

Logging the duration *without* the start time is **not** acceptable: the end would
come from the log-emission timestamp (which klog prefixes to the line), whose
uncontrolled lag after the reply then contaminates the start time recovered as
(end − duration). The start timestamp must always be present; the duration is
optional alongside it.

Emit this line at `V(2)` (visible by default) for state-changing calls and for
failures; for read-only successes it may be at `V(4)`.

### DR-20: Rationale

Benchmarking and diagnosis of the actuation path depend on being
able to correlate the duration of each outbound call with other events.
Generalized from
[#495](https://github.com/llm-d-incubation/llm-d-fast-model-actuation/issues/495)
and applied to the dual-pods controller in
[#522](https://github.com/llm-d-incubation/llm-d-fast-model-actuation/pull/522).

### DR-20: Enforcement

**Subjective tier.** There is no mechanical check: whether a
given Go call is an "external process" call, and whether a duration-revealing log
wraps it, needs judgement. Whoever writes or reviews controller code that adds or
changes an outbound call — an AI agent or a human — is expected to confirm the
call is logged per this rule.
