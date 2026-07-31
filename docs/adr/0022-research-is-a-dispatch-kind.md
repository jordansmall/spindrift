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

## Amendment (issue #2201): the verdict vocabulary and label mapping are configurable via `RESEARCH_VERDICTS`

The original decision above fixed the verdict set at three tokens
(`recommend` / `reject` / `unclear`) with hard-coded labels
(`agent-research-recommend` / `-reject` / `-unclear`). That set is not
universal — a deployment may want different verdict names, more or fewer
terminals, or different label text — so the vocabulary and its label
mapping move behind a new operator knob, `RESEARCH_VERDICTS` (flake option
`settings.issues.research.verdicts`, schema key `researchVerdicts`; see
[Configuring the research verdict vocabulary
(`RESEARCH_VERDICTS`)](../reference.md#configuring-the-research-verdict-vocabulary-research_verdicts)
for the operator-facing format and validation rules). The default remains
the empty string, which resolves to exactly the original three verdicts and
labels with no behavior change — this amendment adds configurability, it
does not change any default-configuration behavior described above.

`Verdict` becomes string-valued rather than a closed three-value enum
(`cmd/launcher/internal/forge/verdict.go`), and the fixed pair — `recommend`
→ promote / `reject` → close / `unclear` → answer-and-relabel — becomes an
ordered `VerdictLabels` set (`forge.ParseResearchVerdicts`, launcher
startup) validated for a non-empty array, unique whitespace-free verdict
tokens, non-empty labels, and exclusion of the reserved `blocked` token,
which stays the fixed escape hatch for "the Box crashed or produced no
verdict" (`agent-research-failed`) independent of the configured set. On
Settle, the posted outcome line's verdict is parsed against the configured
set; anything outside it — an unmapped token, `blocked`, or a missing
outcome line — still routes to `agent-research-failed` rather than being
mapped to some label anyway, preserving the original crash/no-verdict-vs.
concluded-verdict separation this ADR established. `agent-research`,
`agent-research-in-progress`, and `agent-research-failed` themselves stay
fixed, non-configurable names; only the verdict-terminal labels move.

The configured set also drives the research prompt, not only the launcher:
the prompt's machine-checkable verdict contract — its VERDICT bullet list,
the verdict enumeration, and the `status=<...>` outcome-line alternation —
renders from the same set at build time (`lib/research-verdicts.nix`, wired
through `lib/mkHarness.nix`), so a custom vocabulary is consistently what
both the Box is told to emit and what the launcher accepts. This is a
`flakeOption`, baked at image build time like the label knobs above, not a
zero-rebuild runtime switch. Deliberately out of scope: the prompt's
surrounding semantic *guidance* prose per verdict (for example, the "Open
questions — mandatory when the verdict is `unclear`" step, which names
default verdicts directly) is not rewritten from a custom set — only the
machine-checkable contract and each verdict's label render dynamically.
Rewriting per-verdict guidance itself is out of scope here and applies
equally to the self-contained research mode's own baked prompt — see the
[amendment for issue
#2202](#amendment-issue-2202-a-self-contained-no-repo-research-sub-mode)
below.

## Amendment (issue #2202): a self-contained, no-repo research sub-mode

The original decision above assumed every research run explores a fresh
clone of the Target repo alongside the issue text. Some issues are
self-contained instead — everything needed to judge relevance and enrich
the issue lives in the issue body and comments, with no repository content
to weigh in. Forcing those through the ordinary clone-and-explore path
buys nothing: it burns the clone, the branch-recovery and toolchain-nudge
steps, and a devShell probe on a repo the prompt never asks the Box to
open. `spindrift research --self-contained` (`SelfContained` on
`dispatch.Config`, forwarded into the Box as `SELF_CONTAINED=1`) is a
sub-mode of the research kind that skips all of it: bootstrap stands up an
empty working directory instead of calling `clone_repo`, and skips branch
recovery, prework rebase, the toolchain nudge, the devShell probe, and
prefetch — the entrypoint's `_is_self_contained` gate short-circuits the
whole repo-setup sequence before prompt assembly.

Because there is no repo, launcher startup validation relaxes its
otherwise-unconditional `REPO_SLUG`/`GH_TOKEN` requirement specifically for
the `research` kind with `--self-contained` set (`validate`,
`cmd/launcher/main.go`) — the same shape as the existing fully-local
exemption, but for a different reason: fully-local has no `github` client
to construct, while self-contained research has a `github` client
available but nothing for it to clone. `--self-contained` is rejected
outright, before bootstrap, on the work-dispatch verbs (`dispatch`,
`recover`) — a no-repo work dispatch has no repo to branch from or land a
PR onto, so the flag is refused rather than silently ignored. The natural pairing is a local issue tracker
(`ISSUE_TRACKER=local`) supplying the self-contained content with no forge
repo configured at all, though nothing enforces that pairing; a GitHub
issue tracker with `--self-contained` works too; only the repo clone is
skipped.

The Box is driven by a distinct baked prompt,
`research-self-contained-prompt.md` (`researchSelfContainedPrompt`,
`lib/mkHarness.nix`/`lib/image.nix`), rather than the ordinary
`research-prompt.md` — it drops the EXPLORE-the-repo step entirely and
instructs the Box to judge relevance from the issue content alone. It
composes with both prior amendments rather than sitting outside them: like
`research-prompt.md`, it is overridable at runtime via the
`SPINDRIFT_PROMPT_DIR` prompt-directory override (issue #2200) — a custom
prompt directory ships both prompt files to override each independently —
and its machine-checkable verdict contract renders from the same
configured `RESEARCH_VERDICTS` set as the ordinary prompt (issue #2201,
`researchVerdicts.renderIfCustom`, applied to both prompts), so a custom
vocabulary reaches self-contained research too. Settle is unchanged:
self-contained research still posts exactly one required verdict comment
through the same configurable-verdict path this ADR and its #2201
amendment established, and applies exactly one terminal label — the
absence of a repo changes what the Box can explore, not the verdict
contract or the one-comment rule.
