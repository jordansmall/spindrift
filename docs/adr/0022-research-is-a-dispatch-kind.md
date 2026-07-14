# Research is a dispatch kind, advise-only, through the full Box

Issues arrive under-specified: a worker Agent dispatched onto a thin issue
burns its run rediscovering context a reviewer could have written down, and
some issues (Filer findings included) are false positives that should never
consume a work dispatch at all. We want an agent that reviews a posted issue,
enriches it with real context in comments, and renders a relevance verdict —
before any worker picks it up.

**Decision: research is a second Dispatch kind (`work` | `research`), run
through the full Box machinery, and strictly advise-only.** The researcher
explores the Target repo while chewing on untrusted issue text — exactly the
combination the Box exists to contain — so it gets no lighter path. It posts
one appended, marker-carrying, structured comment (verdict, context for a
worker, open questions) and never edits the issue body, never promotes an
issue to dispatchable, never closes one: a human acts on the verdict,
preserving the rule that a human is the launch button.

Kinds share the four canonical lifecycle states; research maps them to its
own disjoint label family on `github`: `agent-research` (dual-role standing
state and trigger) → `agent-research-in-progress` → verdict terminals
`agent-research-recommend` / `-reject` / `-unclear`, with
`agent-research-failed` strictly meaning the Box crashed or produced no
verdict — a concluded false positive is `Complete` with verdict `reject`,
never `Failed`, so crash-retry and verdict-review stay separate human queues.
The three verdicts are the closed set that routes to three distinct human
actions: `recommend` → promote, `reject` (false positive / not worth it /
duplicate-of-#N, reason in the comment) → close, `unclear` → answer the
researcher's questions and re-apply the label. Settle is one-shot: parse the
outcome line, apply the verdict label, done — no fix passes, no session
resume; every retry is the universal re-label gesture. Blocker edges do not
gate research (it lands no code), and label families never interact at claim
time — an issue legitimately wears `agent-research-recommend` and
`ready-for-agent` at once.

The Box reports its verdict through the existing Outcome line, reinterpreted
per kind: `status` carries the verdict and `landing` carries the
verdict-comment URL — the landing reference onto the Issue Tracker, parallel
to the push-only forge's branch ref. This lands with a rename: the wire token
`pr=` becomes `landing=` (Go field `PR` → `Landing`), since PR-vs-issue is a
GitHub-ism that confuses on split backends. The rename is atomic — the image
and launcher are built from the same flake revision and parse only their own
run's logs, so no compat alias is needed. The line still carries only what
the launcher cannot know without the Box; backend identity and dispatch kind
stay launcher-side run config.

Kind is per-run operator intent, surfaced as a `research` subcommand (the
verb-based house idiom — `dispatch`, `build`, `doctor` — not a `--kind` flag
on dispatch), with the same selective `research <nums>` form. Waves are
homogeneous in kind; both prompts bake into the one image so kind is a
run-time selection, not a build-time seam. The Issue Tracker seam is fully
kind-aware from the start — `jira` rides its existing unmapped-state label
fallback and `local` a frontmatter field, keeping every backend-matrix cell
working — with jira-native status mapping for research states deferred until
a Jira user exists.

Because advise-only is a prompt-level rule, the enforcement boundary is the
token: the research pipeline takes an optional second fine-grained PAT scoped
Issues RW + Contents R + Metadata R (falling back to the main token when
unset), so a fully injection-steered researcher cannot push code, open a PR,
or merge — the blast radius collapses to a bad comment a human reads anyway.

## Considered Options

- **A lightweight CI pipeline** — a bare headless Driver run in a GitHub
  workflow, no Box, no image build. Rejected: it would be the first agent
  run outside the Box while consuming untrusted input with repo access, and
  a second code path outside the launcher, lifecycle, and seams; jira/local
  would inherit nothing.
- **Acting on the verdict** — auto-promoting `recommend` issues to
  `ready-for-agent` and/or auto-closing rejects. Rejected: researcher output
  would become dispatch authorization, letting a crafted issue body graduate
  prompt injection into a dispatched work Box; auto-close silently destroys
  real bug reports on a wrong verdict. Advise-only → acting is a one-line
  change later; the reverse is a policy retreat after an incident.
- **New lifecycle states** (`Researchable → Researched → Dispatchable`) —
  rejected: forces four more states on every tracker adapter for an optional
  activity and hard-wires research as a phase of the worker pipeline, when
  issues are legitimately researched-but-never-dispatched and vice versa.
- **A second outcome line kind** (`SPINDRIFT_RESEARCH verdict=…`) or an
  optional `verdict=` field — rejected: doubles the scanning/parsing/prompt
  contract surface, or creates a field whose validity depends on another
  field's value; kind-contextual `status` costs nothing since settle
  branches on kind before interpreting it.

## Consequences

- The previously unnamed default kind gains the name `work`; glossary
  entries for Dispatch kind, Research dispatch, and the Outcome line rename
  land in CONTEXT.md with this decision.
- A new `agent-research.yml` labeled-event workflow reuses the `agent-setup`
  composite with research claim labels; concurrency groups are
  per-kind-per-issue, so cross-kind concurrent runs on one issue are
  permitted — wasteful at worst, operator's responsibility, matching the
  existing rate-limit stance.
- Applying `agent-research` always fires CI immediately; there is no quiet
  "research later" queue state. If batch research becomes real, a trigger
  label is added beside the standing label exactly as `agent-trigger` was.
- `research` is an additive subcommand (MINOR under ADR 0010); `dispatch`
  is untouched.
- The `pr=` → `landing=` rename sweeps the outcome package, prompt
  templates, and prompt-contract checks in one change.
