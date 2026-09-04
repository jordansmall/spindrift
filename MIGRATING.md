# Migration Guide

## The repo-backed research prompt's EXPLORE section now anchors `/check-hygiene` (issue #3227)

`research-prompt.md`'s `# EXPLORE` section asks a researcher to attempt a
repro — run the repo's own suite, or write and run a throwaway script — when
the issue claims a bug and the repro is cheap. That is a check gate inside a
dispatch that still has to print a `SPINDRIFT_OUTCOME` line, exactly the
concern the harness-owned `/check-hygiene` skill covers, so the section now
carries `${CHECK_HYGIENE_STEP}`, prefixing the repro paragraph — ahead of the
instruction it governs, matching where the same variable sits in
`issue-prompt.md`'s CHECK section.

No new gate, row, or fragment: this reuses the existing `CHECK_HYGIENE_BAKED`
row and its `fragments/check-hygiene-default.md` anchor. When the skill is
not baked the variable renders empty, so adding it to an override is safe in
either bakedness state.

`research-self-contained-prompt.md` deliberately does not get the anchor —
it has no repo and nothing to run, so the line would be dead text. Do not
"fix" that asymmetry by adding it there.

### If you override the prompt directory

A `SPINDRIFT_PROMPT_DIR` (or `perSystem.spindrift.agents.prompt`) override
that ships its own `research-prompt.md` keeps working — the harness does not
rewrite it — but silently loses the `/check-hygiene` anchor on baked runs,
the same silent-loss failure mode the #3226 entry documents for the review
prompt's hunt dimensions: no error, no double rendering, just a repro
paragraph with no skill backing it. Restore it by adding
`${CHECK_HYGIENE_STEP}` immediately before the repro paragraph in your
EXPLORE section; diff `templates/default/prompts/research-prompt.md` against
your copy for the exact placement.

Separately, the TASK section's advise-only enumeration now appears once, in
the `# POST THE VERDICT` trailer both prompt kinds already shared
byte-for-byte and which is unchanged, and
`fragments/research-file-issues-relay.md` dropped two design-history
clauses that restated rules already stated outright elsewhere in the same
fragment. Neither needs override action — they are noted here so an
override author diffing the bundled templates recognises them as
intentional trims, not drift.

## The fix, worker, scout, and conflict-resolve prompts were re-derived against the layered intent; wording only (issue #3225)

This one is editorial, and there is nothing for a Consumer to do. Nothing
here changes what any of the four prompts tell a Box to do.

`fix-prompt.md`, `worker-prompt.md`, and `scout-prompt.md` each lost a
sentence or two of behavioural coaching explaining *why* a rule holds: the
warm-fix override bullets' argument against COMMIT's "several small
commits" guidance, the batching paragraph's coordinator-side economics,
and the `## Map` section's case for the citation obligation. Every
operative rule those sentences sat beside stays in full.
`conflict-resolve-prompt.md` classified as pure contract and sequencing
end to end — its generated-file examples are detection patterns, not
argument — so it is untouched.

Nothing structural moved either: no section, heading, `envsubst` variable,
or fragment-pair (`inverseOf`) row changed, no anchor line was added, the
`# COMMS` / `# CHECK` / `# LAND THE CHANGE` injection into `fix-prompt.md`
is as it was, and `worker-prompt.md`'s quarantine from the marker grammar
holds. Every operative rule across the four templates is now pinned by a
content-invariant Go test
(`cmd/launcher/orchestrator/{fix,worker,scout,conflict_resolve}_prompt_content_test.go`),
so a later tightening pass can't silently drop one.

### If you override the prompt directory

Nothing to fix. Nothing moved between a gated fragment and a template, so
— unlike the `#3226` entry below — there is no double-render hazard from a
stale fragment and no silent-loss hazard from a stale template. An
override that ships its own copy of any of these four prompts simply keeps
its own, slightly longer, wording.

## The review prompt hunts deeper: phased ordering, trace obligations, a failure-scenario rule, and an APPROVE receipt (issue #3228)

`review-prompt.md` gained four unconditional depth mechanisms, all inline
between the hunt dimensions and the output contract, none gated by
bakedness — they apply whether or not `/code-review` is baked, same as the
dimensions issue #3226 moved to this file:

- **Phased hunt ordering.** CORRECTNESS and SECURITY must be hunted to
  completion before a single STANDARDS & SMELLS finding gets recorded, so an
  early-noticed smell can't crowd the reviewer's attention (or the output
  cap) ahead of the load-bearing defects those two dimensions exist to
  catch.
- **Trace obligations.** Four diff shapes now carry a named obligation to
  execute with tools beyond the diff hunk, not a hunch to weigh from the
  hunk alone: a rename or mass replacement gets a tree-wide grep of both
  the old and new forms for collisions; a changed signature gets every
  caller read; a concurrency-adjacent change gets its shared state named
  and one interleaving walked by hand; a new error path gets its
  propagation traced.
- **Failure-scenario rule for Blocking.** A Blocking finding must state a
  one-line concrete failure scenario — the triggering input or state, and
  the wrong outcome it produces. A finding that cannot state one is
  Non-blocking by definition, whatever category it otherwise resembles.
- **APPROVE Probed section.** The output contract gained a `## Probed
  (APPROVE only)` section, strictly below the untouched `VERDICT: APPROVE`
  line and inside the existing ~40-line cap, naming which hunt dimensions
  and trace obligations ran clean — a receipt that APPROVE is work done,
  not an assertion taken on faith.

The `VERDICT: APPROVE | BLOCK` first-line grammar (ADR 0035) and the review
caveman fragment's `## Blocking`/`## Non-blocking` exemption tier are both
unchanged.

### If you override the prompt directory

A `SPINDRIFT_PROMPT_DIR` (or `perSystem.spindrift.agents.prompt`) override
that ships its own `review-prompt.md` predating this issue keeps rendering
its old text — nothing fails, the assembled prompt is still valid, so
there's no error to notice. It silently loses all four mechanisms above:
no phased ordering, no trace obligations, no failure-scenario requirement,
no Probed receipt. Reviews on that override just go shallow again, the same
silent failure mode the #3226 entry below describes. Diff your
`review-prompt.md` against the bundled
`templates/default/prompts/review-prompt.md` and pull the new prose in.

## The review prompt's hunt dimensions now always render; the code-review pair carries execution mode only (issue #3226)

`${CODE_REVIEW_BAKED_STEP}` and `${CODE_REVIEW_UNBAKED_STEP}` still both
exist in `review-prompt.md` and still render exactly-one-on — unlike the
`${COMMIT_STEP}`/`${CODE_REVIEW_STEP}` entry further down, no variable was
renamed or retired here.

What moved: the four hunt dimensions (SPEC / CORRECTNESS / SECURITY /
STANDARDS & SMELLS) and the obligation to reconcile every finding into
Blocking or Non-blocking are now unconditional inline text in
`review-prompt.md`, between the diff-reading instruction and the Severity
bullets. They used to render
only from the gated arms — the dimensions from
`fragments/code-review-unbaked.md`, the reconcile obligation from
`fragments/code-review-baked.md` — so each was invisible on exactly the runs
the other arm covered. The baked arm hands review depth to a pinned upstream
`/code-review` skill spindrift does not author and cannot edit, so a depth
obligation that lived only in the unbaked arm vanished on precisely the runs
with the least in-repo control over how deep the review goes. Depth is
contract now; only the execution mode stays gated.

What the pair carries instead: how the dimensions get hunted, never what
they are. Both arms are one line. Baked: run `/code-review`, fold its
two-axis (Standards + Spec) findings back into the inline hunt below, each
axis holding its own full-diff read. Unbaked: run every dimension yourself
in this one context, no reviewer subagents to fan out to.

Separately, `review-prompt.md`'s role posture, prior-round paragraph, and
Non-blocking severity bullet, plus `filer-prompt.md`'s dedup step, were
tightened editorially — every rule survives in meaning, nothing relocated to
a skill (neither role has a further skill to anchor on), and no anchor was
added. The `VERDICT:` first-line grammar, the review caveman fragment's
`## Blocking`/`## Non-blocking` exemption tier, and the filer's
`SPINDRIFT_ISSUE_INTENT` relay contract and output grammar are all
unchanged. An override that diffs against the bundled templates will see
wording churn in those spots and no behaviour change.

### If you override the prompt directory

A `SPINDRIFT_PROMPT_DIR` (or `perSystem.spindrift.agents.prompt`) override
that ships its own `fragments/code-review-unbaked.md` carrying the old
four-dimension prose, but takes the bundled `review-prompt.md`, now renders
the dimensions twice on unbaked runs — once inline, once from the stale
fragment. Fix it by replacing that fragment's body with the one-line
execution-mode statement (point at the bundled
`templates/default/prompts/fragments/code-review-unbaked.md` for the exact
text), or by dropping the fragment override entirely so the bundled one
takes over.

The inverse is worse and silent: an override that takes the bundled
fragments but ships its own `review-prompt.md` without the inline
dimensions loses them outright on every run, baked or unbaked — no error,
no double rendering, just a shallower review across the board, since the
bundled `code-review-unbaked.md` no longer carries a fallback copy either.
Unbaked runs come off worst: that fragment's "run every dimension below
yourself" now dangles with nothing below it. Diff your `review-prompt.md`
against the bundled copy and pull the hunt-dimensions block in.

## The CODE COMMENTS section is gone from the prompt, replaced by a harness-owned `/code-comments` skill (issue #3221)

The bundled issue-pass prompt template no longer carries a `# CODE COMMENTS`
heading. It used to sit inline at the end of the IMPLEMENT phase, unconditionally,
stating the comment-discipline rule in full: a comment earns its place only by
carrying something the code cannot state itself, never restating what the code
already says, and keeping comment volume proportional to the size of the
change. That prose is not gone — it now lives in the body of a new
harness-owned `/code-comments` skill, and the prompt carries a single anchor
line, `${CODE_COMMENTS_STEP}`, at the end of the IMPLEMENT phase immediately
before `# CHECK`, directing the agent to invoke the skill.

`code-comments` is a **harness-owned** skill, the fourth alongside
`auto-format`, `auto-lint`, and `check-hygiene`: it bakes into every image
unconditionally, independent of the Consumer's `agents.skills` list, and
there is no knob to turn it off. Nothing to configure, and nothing to do if
you use the bundled prompts — rebuild with `spindrift build` and the new
skill and the anchor line arrive together.

### If you override the prompt directory

A Consumer that points `perSystem.spindrift.agents.prompt` (or a
`SPINDRIFT_PROMPT_DIR` override) at its own issue-pass prompt keeps whatever
CODE COMMENTS section that prompt already has — the harness does not rewrite
it. The skill still bakes into the image, so `/code-comments` is available to
the agent, but nothing will invoke it unless your prompt says so. Two
options:

- Take the reduction: delete your inline comment-discipline prose and put
  `${CODE_COMMENTS_STEP}` in its place. That variable renders the
  `fragments/code-comments-default.md` anchor whenever the skill is baked —
  the same bakedness gating `/check-hygiene` and `/caveman` already use, so a
  prompt never names a skill its box does not carry. Diff
  `templates/default/prompts/issue-prompt.md` against your copy to see the
  exact placement.
- Change nothing: your prompt keeps its inline prose and simply never anchors
  the skill. This is fully supported — the skill is inert if unmentioned —
  but you pay the prompt-context cost the reduction was meant to recover.

### Custom fix-pass prompts: this one needs action

Unlike the CHECK reduction, this change is not automatic for a custom
`fix-prompt.md`. The old `CODE COMMENTS` block was small enough that the
harness sliced it out of `issue-prompt.md` at build time and injected the
same canonical text into `fix-prompt.md` via a prompt-contract block, backed
by a baked `/agent/code-comments-contract.md` file — so a custom fix prompt
picked up the rule with no action on your part. With the section gone from
`issue-prompt.md` there is nothing left to slice: the `code-comments`
prompt-contract inject block and the `/agent/code-comments-contract.md` file
it fed have both been removed. The bundled `fix-prompt.md` now carries
`${CODE_COMMENTS_STEP}` directly in its own FIX section, the same anchor
`worker-prompt.md` and `conflict-resolve-prompt.md` already carried.

If you ship your own `fix-prompt.md`, you no longer gain the comment rule
automatically — add `${CODE_COMMENTS_STEP}` to it yourself, in the FIX
section, at the point in your prompt where an agent is about to start
editing code. Diff `templates/default/prompts/fix-prompt.md` against your
copy to see the bundled placement.

### The fragment file was renamed

`fragments/code-comments.md` is now `fragments/code-comments-default.md`,
matching the `<name>-default.md` naming `check-hygiene-default.md` and
`caveman-default.md` already use. If your `SPINDRIFT_PROMPT_DIR` override
ships this fragment, rename it too. The read is now guarded on the skill
being baked (`agent/entrypoint.sh`'s `phase_conflict_resolve` checks
`[ -f "$DRIVER_SKILLS_DIR/code-comments/SKILL.md" ]` before reading the
fragment, the same pattern its `CAVEMAN_STEP` neighbor already used), so an
override directory that ships neither the fragment nor the skill no longer
aborts `phase_conflict_resolve` under `set -euo pipefail` the way the old
unguarded read did — the old behavior required every override to ship the
fragment unconditionally or risk a mid-run abort on the first rebase
conflict; the new behavior only requires the fragment when the skill is
also present.

## The COMMIT and CODE-REVIEW sections are baked/unbaked fragment pairs; `${COMMIT_STEP}` and `${CODE_REVIEW_STEP}` are gone (issue #3222)

This only affects consumers who ship their own prompt directory overriding
this repo's `templates/default/prompts/`. If you use the shipped prompts,
there is nothing to do.

Baking the `commit` or `code-review` skill used to *add* prose: the
`COMMIT_STEP` fragment rendered a "the `/commit` skill is authoritative and
supersedes the inline format rules below" deferral, stacked on top of the
Conventional Commits format rules that sat inline in `issue-prompt.md` and
rendered unconditionally; `CODE_REVIEW_STEP` did the same thing in
`review-prompt.md`, deferring to the skill's two-axis verdict on top of the
SPEC/CORRECTNESS/SECURITY/STANDARDS & SMELLS dimensions, which the prompt
said rendered "regardless of whether the `/code-review` skill is baked." In
both cases the driver had to read past prose it was just told a skill
already superseded.

Baking either skill now **subtracts** that prose instead, the same
exactly-one-on fragment-pair mechanic issue #3219 established for the
IMPLEMENT section's test-first step (see the `TDD_STEP` entry below for how
the eval-time declaration-shape assert and the render-time Go sweep divide
the guarantee — that split is not repeated here):

| Gate | Fragment | Variable | Renders |
|---|---|---|---|
| `COMMIT_BAKED` | `fragments/commit-baked.md` | `${COMMIT_BAKED_STEP}` | a one-line anchor pointing at `/commit`, and nothing else |
| `COMMIT_UNBAKED` | `fragments/commit-unbaked.md` | `${COMMIT_UNBAKED_STEP}` | the Conventional Commits format rules |
| `CODE_REVIEW_BAKED` | `fragments/code-review-baked.md` | `${CODE_REVIEW_BAKED_STEP}` | a one-line anchor pointing at `/code-review`, and nothing else |
| `CODE_REVIEW_UNBAKED` | `fragments/code-review-unbaked.md` | `${CODE_REVIEW_UNBAKED_STEP}` | the four SPEC/CORRECTNESS/SECURITY/STANDARDS & SMELLS dimension paragraphs |

`COMMIT_UNBAKED` and `CODE_REVIEW_UNBAKED` are each declared in the fragment
registry as `inverseOf` their baked partner, with the same eval-time
declaration-shape assert and render-time `TestRegistryInverseOfPairsAreExactlyOneOn`
Go sweep backing the guarantee that TDD_BAKED/TDD_UNBAKED gets.

### What breaks in an override prompt directory

- **`${COMMIT_STEP}` and `${CODE_REVIEW_STEP}` no longer exist.** An
  override prompt still writing either one renders a literal,
  unsubstituted `${COMMIT_STEP}` / `${CODE_REVIEW_STEP}` into the assembled
  prompt — the assembler substitutes only variables the registry declares,
  and nothing raises. Replace each with its adjacent pair, no separator
  between them: `${COMMIT_BAKED_STEP}${COMMIT_UNBAKED_STEP}` in
  `issue-prompt.md`'s COMMIT section, `${CODE_REVIEW_BAKED_STEP}${CODE_REVIEW_UNBAKED_STEP}`
  in `review-prompt.md`.

- **`fragments/commit-default.md` and `fragments/code-review-default.md` are
  deleted.** An override prompt directory carrying its own copy of either
  file is now an orphan the assembler never reads; an override that
  references either by path breaks. Split them into
  `fragments/commit-baked.md` / `fragments/commit-unbaked.md` and
  `fragments/code-review-baked.md` / `fragments/code-review-unbaked.md`
  respectively.

- **The COMMIT section's granularity preference stayed put — do not move
  it.** Unlike the format rules, the sentence "Prefer several small focused
  commits over one big one — commit each logical unit (domain change, then
  wiring, then tests) so each stands alone" stays inline and unconditional
  in `issue-prompt.md`, outside both arms of the pair.
  `fragments/commit-rework-orchestrator.md` back-references it twice by name
  as "the preference above"; moving it into either arm breaks that
  reference for whichever bakedness state doesn't render the arm it moved
  to.

- **`${CODE_REVIEW_BAKED_STEP}${CODE_REVIEW_UNBAKED_STEP}` renders at a
  different position than `${CODE_REVIEW_STEP}` did.** The old variable sat
  at the top of `review-prompt.md`, ahead of the "Read ONLY the issue and
  the diff" instruction. The new pair renders lower down, immediately
  before the Severity bullets, at the position the four dimension
  paragraphs used to occupy. An override that renames the variable in
  place without moving it will anchor the skill (or render its fallback) in
  the wrong phase of the review.

- **The "renders either way" paragraph is gone, and it is no longer true.**
  `review-prompt.md` used to state that the SPEC/CORRECTNESS/SECURITY/
  STANDARDS & SMELLS dimensions "render regardless of whether the
  `/code-review` skill is baked." That is now false — they render only in
  `CODE_REVIEW_UNBAKED_STEP`, when the skill is absent. An override that
  kept a paraphrase of that claim should delete it along with the
  dimensions themselves.

- **The Severity bullets, fenced output shape, and `VERDICT: APPROVE |
  BLOCK` first-line grammar are unaffected.** They stayed inline and
  unconditional in `review-prompt.md` on both arms — they are machine
  contract (ADR 0035 `scanPassLog` parses the first line), not coaching, so
  neither arm owns them. Same for `issue-prompt.md`'s
  `REVIEW_LOOP_INLINE`/`REVIEW_LOOP_ORCHESTRATOR` pair, which gates on
  `$ORCHESTRATOR` rather than on skill bakedness and is untouched by this
  change — do not conflate it with the `CODE_REVIEW_BAKED`/`CODE_REVIEW_UNBAKED`
  pair above.

## CHECK-gate hygiene moved out of the prompt into a harness-owned `/check-hygiene` skill (issue #3220)

The CHECK section of the bundled issue-pass and fix-pass prompt templates has
been cut back to what an agent must read before it can act: the obligation to
run the repo's own checks green before each commit, the Nix check-target lore
(`git add` before the first `nix build`, prefer a `flake.nix` devShell's
pinned toolchain, use a scoped check target and treat "no full `nix flake
check` in-box" as a firm rule, fall back to the baked toolchain and log it),
a tightened one-paragraph statement of the foreground-gate rule (including
the mandate not to stop the run before a terminal `SPINDRIFT_OUTCOME` line
has been printed, which stays inline because the launcher parses that line
on every run, skill or no skill), and a single anchor line directing the
agent to invoke the `/check-hygiene` skill before running its first gate.

The guidance that used to sit inline now lives in that skill's body: the
bounded-tail and log-grepping discipline (never `cat` a whole build/test log
into context — grep or tail the file on disk), the elaborated version of the
foreground-gate guidance, and the killed-build fallback advice (if you ever
background-and-poll a gate anyway, bound the wait and treat a vanished
process as a failure, emitting `status=blocked` rather than looping forever
on an exit marker an OOM-killed build will never write). None of it is gone —
it is loaded on demand, at the moment the agent is about to run a gate,
instead of occupying prompt context for the whole run.

`check-hygiene` is a **harness-owned** skill, the third alongside
`auto-format` and `auto-lint`: it bakes into every image unconditionally,
independent of the Consumer's `agents.skills` list, and there is no knob to
turn it off. Nothing to configure, and nothing to do if you use the bundled
prompts — rebuild with `spindrift build` and the new skill and the reduced
CHECK section arrive together.

### If you override the prompt directory

A Consumer that points `perSystem.spindrift.agents.prompt` (or a
`SPINDRIFT_PROMPT_DIR` override) at its own issue-pass prompt keeps whatever
CHECK section that prompt already has — the harness does not rewrite it. The
skill still bakes into the image, so `/check-hygiene` is available to the
agent, but nothing will invoke it unless your prompt says so. Two options:

- Take the reduction: delete the bounded-tail, elaborated foreground-gate,
  and killed-build paragraphs from your CHECK section and put
  `${CHECK_HYGIENE_STEP}` in their place. That variable renders the
  `fragments/check-hygiene-default.md` anchor whenever the skill is baked —
  the same bakedness gating `/caveman` and `/commit` already use, so a
  prompt never names a skill its box does not carry. Diff
  `templates/default/prompts/issue-prompt.md` against your copy to see the
  exact placement.
- Change nothing: your prompt keeps its inline guidance and simply never
  anchors the skill. This is fully supported — the skill is inert if
  unmentioned — but you pay the prompt-context cost the reduction was meant
  to recover.

Custom **fix-pass** prompts need no action either way: the CHECK section is
injected into `fix-prompt.md` from the same contract, so the reduction
reaches it automatically.

## The IMPLEMENT section's test-first step is a baked/unbaked fragment pair; `${TDD_STEP}` is gone (issue #3219)

This only affects consumers who ship their own prompt directory overriding
this repo's `templates/default/prompts/`. If you use the shipped prompts,
there is nothing to do.

Baking the `tdd` skill used to *add* prose: the `TDD_STEP` fragment rendered
a "its red-green-refactor discipline is authoritative and supersedes the
inline steps" deferral note, stacked on top of the full RED/GREEN/REFACTOR
hard rule that sat inline in `issue-prompt.md` and rendered unconditionally.
The driver had to read past the steps it was just told to ignore.

Baking the skill now **subtracts** that prose instead. The step is a paired
exactly-one-on fork — the same shape as the existing
`${SCOUT_DELEGATE_STEP}${SCOUT_ABSENT_STEP}` pair — where exactly one member
renders and the other is empty:

| Gate | Fragment | Variable | Renders |
|---|---|---|---|
| `TDD_BAKED` | `fragments/tdd-baked.md` | `${TDD_BAKED_STEP}` | a one-line anchor pointing at `/tdd`, and nothing else |
| `TDD_UNBAKED` | `fragments/tdd-unbaked.md` | `${TDD_UNBAKED_STEP}` | the full RED/GREEN/REFACTOR fallback |

`TDD_UNBAKED` is declared in the fragment registry as `inverseOf =
"TDD_BAKED"`. An eval-time assert in `lib/fragments.nix` checks only the
*declaration shape* — the on-gate exists, isn't itself, isn't chained, and
isn't claimed by another pair — not that the two gates disagree when
rendered. That render-time guarantee is a Go test,
`TestRegistryInverseOfPairsAreExactlyOneOn`, which sweeps the gate matrix and
fails the build if the two ever agree.

### What breaks in an override prompt directory

- **`${TDD_STEP}` no longer exists.** An override `issue-prompt.md` still
  writing it renders a literal, unsubstituted `${TDD_STEP}` into the
  assembled prompt — the assembler substitutes only variables the registry
  declares, and nothing raises. Replace it with the adjacent pair, no
  separator between them:

  ```
  ${COORDINATOR_STEP}${COORDINATOR_SCOUT_BRIEF_STEP}${SKILL_PREAMBLE}${TDD_BAKED_STEP}${TDD_UNBAKED_STEP}# CODE COMMENTS
  ```

- **`fragments/tdd-default.md` is deleted.** An override prompt directory
  carrying its own copy of that file is now an orphan the assembler never
  reads; an override that references it by path breaks. Split it into
  `fragments/tdd-baked.md` and `fragments/tdd-unbaked.md`.

- **The IMPLEMENT section is restructured.** The RED/GREEN/REFACTOR prose
  that used to live in the outer `issue-prompt.md` body, right after
  `${TDD_STEP}`, now lives verbatim in `fragments/tdd-unbaked.md`. If you
  edited that prose in your own `issue-prompt.md`, move your edit into your
  `tdd-unbaked.md` — left in the outer template it renders unconditionally
  again, which is exactly the behavior this change retires.

- **The semantics inverted.** Do not assume the baked arm is the old
  fragment plus the old inline steps. With the skill baked, the assembled
  prompt now contains the anchor line *only* — no red-green-refactor prose
  at all, because the skill carries it. Any override that leaned on that
  prose always being present must move it under the unbaked arm or into its
  own fragment.

## Scalar `REGISTRY_PROXY_*` knobs retired; the routes file is the only declaration surface (issue #3145)

The launcher now refuses to start when any of the five scalar
`REGISTRY_PROXY_*` knobs (ADR 0044) is set: `REGISTRY_PROXY_UPSTREAM_URL`,
`REGISTRY_PROXY_CREDENTIAL_FILE`, `REGISTRY_PROXY_CREDENTIAL_ENV`,
`REGISTRY_PROXY_CREDENTIAL_FILE_FORMAT`,
`REGISTRY_PROXY_CREDENTIAL_CARGO_REGISTRY_NAME`.
`REGISTRY_PROXY_ROUTES_FILE` (ADR 0045) is now the only way to configure the
registry proxy; leaving it unset still disables the proxy entirely, same as
before.

Set as environment variables, the retired knobs get a soft landing: the
launch-gate error names every one you still have set and prints a
ready-to-paste `[[routes]]` stanza built from the values you had, so
migrating is "paste this stanza into your routes file," not "read ADR 0045
from scratch":

| Retired scalar knob | Routes-file key |
|---|---|
| `REGISTRY_PROXY_UPSTREAM_URL` | `match-host` (the URL's host) and `upstream-base-url` (the URL itself) |
| `REGISTRY_PROXY_UPSTREAM_URL` alone (no credential knob set) | omit the `credential` key entirely — the route is unauthenticated |
| `REGISTRY_PROXY_CREDENTIAL_ENV` | `credential = { env = "..." }` |
| `REGISTRY_PROXY_CREDENTIAL_FILE` alone (no format, or any format other than `netrc`/`cargo-credentials`) | `credential = { file = "..." }` |
| `REGISTRY_PROXY_CREDENTIAL_FILE_FORMAT=netrc` (+ `REGISTRY_PROXY_CREDENTIAL_FILE`) | `credential = { netrc = "..." }` |
| `REGISTRY_PROXY_CREDENTIAL_FILE_FORMAT=cargo-credentials` (+ `REGISTRY_PROXY_CREDENTIAL_FILE`, `REGISTRY_PROXY_CREDENTIAL_CARGO_REGISTRY_NAME`) | `credential = { cargo-credentials = "...", registry-name = "..." }` |
| `REGISTRY_PROXY_CREDENTIAL_CARGO_REGISTRY_NAME` alone (no `REGISTRY_PROXY_CREDENTIAL_FILE_FORMAT`) | `credential = { cargo-credentials = "<REGISTRY_PROXY_CREDENTIAL_FILE>", registry-name = "..." }` — naming a cargo registry is itself the declaration of intent |
| `REGISTRY_PROXY_CREDENTIAL_FILE_FORMAT` set to anything other than `raw`/`netrc`/`cargo-credentials` | `credential = { file = "..." }` plus a trailing `#` comment naming the unrecognized value and the three the retired knob ever accepted |

Every synthesized route also gets `auth-scheme = "bearer"`, the only scheme
the scalar knobs ever supported; the routes file additionally supports
`basic` and `header:<Name>` if your upstream needs one of those instead.

### The flake options and CLI flags went with them

The knobs are gone from the env schema, so the surfaces generated from it
went too. These flake options no longer exist:

- `perSystem.spindrift.infra.registryProxyCredentialFile`
- `perSystem.spindrift.infra.registryProxyCredentialEnv`
- `perSystem.spindrift.infra.registryProxyCredentialFileFormat`
- `perSystem.spindrift.infra.registryProxyCredentialCargoRegistryName`

and neither do the five matching flags: `--registry-proxy-upstream-url`,
`--registry-proxy-credential-file`, `--registry-proxy-credential-env`,
`--registry-proxy-credential-file-format`, and
`--registry-proxy-credential-cargo-registry-name`.
`REGISTRY_PROXY_UPSTREAM_URL` never had a flake option of its own — it is a
runtime input, so a private registry hostname never lands in a world-readable
store path.

A flake still setting one of those options never reaches the launch gate:
`nix` fails during evaluation with "The option ... does not exist", before
the launcher binary runs, so the ready-to-paste stanza above never gets a
chance to print. Delete the option, set
`perSystem.spindrift.infra.registryProxyRoutesFile`, and write the route by
hand from the mapping table above — or generate the whole file with
`spindrift registry discover`, below. A script still passing one of the
retired flags gets an unknown-flag error, same story.

Rather than hand-transcribe the stanza, `spindrift registry discover
<repo-dir> <routes-file>` (ADR 0045) can write the whole file straight from
the Target repo's own committed registry config, matching each declared host
to a credential across your existing credential stores — see [Registry route
discovery](docs/reference.md#registry-route-discovery).

## `spindrift doctor` exit codes are no longer a flat 0/1 (issue #2569)

`spindrift doctor` used to exit `0` on success and `1` on any failure — the
only two codes it ever produced. It now exits a distinct code per failure
class: `0` healthy (advisory findings — missing research/priority/
ambiguous-spec labels, container runtime not ready, etc. — are allowed and
reported informationally), `1` reserved for internal/unclassified errors,
`2` configuration invalid, `3` auth or connectivity, `4` required checks
failed or declined. See [`spindrift doctor` exit
codes](docs/reference.md#spindrift-doctor-exit-codes-issue-2569) for the full
table.

A script that only distinguishes `$? -eq 0` (success) from anything else is
unaffected. A script that specifically checked `$? -eq 1` to mean "any
failure" must switch to `$? -ne 0` — a real failure can now also exit `2`,
`3`, or `4`.

Two more doctor behaviors changed alongside the exit codes: every
issue-tracker/code-forge connectivity error doctor reports now carries an
`issue tracker or code forge connectivity failure: ` prefix (it names the
failure class being classified as exit `3`), and an advisory-tier label
(research/priority/ambiguous-spec) that fails to create at the interactive
prompt is reported but no longer fails the check — it exits `0`, where it
previously exited `1`. Only a work-tier (triage) label create failure is
still fatal.

## Roster entries fail eval on an unknown key, a missing model, or an unresolvable prompt file (issue #2571)

`roster` entries — both `defaultRoster`'s own built-in four and
hand-authored `perSystem.spindrift.roster` / direct `mkHarness roster`
entries — now go through strict validation in `normalizeRoster`
(`lib/roster.nix`) instead of being accepted as-is. Three previously-silent
mistakes now fail eval:

- An entry carrying a key outside the documented shape (`name`, `model`,
  `effort`, `mode`, `description`, `tools`, `promptFile`, `prompt`) — a
  typo like `dexcription` — now throws instead of silently passing the
  stray key through unused.
- An entry that omits `model` entirely now throws. An explicit `model =
  ""` is unaffected — it's still the supported #392 opt-out, dropped by a
  separate `rosterLib.dropOptedOut` step `lib/mkHarness.nix` applies after
  validation succeeds.
- A `promptFile` that isn't a non-empty string (e.g. a number, `null`, or
  `""`) now throws.
- An entry whose `promptFile` (explicit, or the auto-derived
  `<name>-prompt.md` default) doesn't resolve to a real file under
  `templates/default/prompts/`, and which carries no inline `prompt`, now
  throws instead of silently baking an agent with no usable prompt. The
  auto-derived default is `<name>-prompt.md` for every roster entry except
  `reviewer`, which defaults to `review-prompt.md` (matching that agent's
  on-disk template name, `lib/roster.nix`'s `defaultPromptFileOverrides`).

This promptFile check runs at eval time against the checked-in
`templates/default/prompts/` tree only. A Consumer relying on a *runtime*
prompt-dir override (`SPINDRIFT_PROMPT_DIR` / `--prompt-dir` /
`perSystem.spindrift.agents.promptDir`) to supply a custom agent's prompt
file must add an inline `prompt` to that roster entry (or ship the file in
the checked-in tree) to keep evaluating — the override directory doesn't
exist yet at build time, so the eval-time check can't see into it.

This is a breaking change to the versioned `roster` flake option surface
(see [`VERSIONING.md`](VERSIONING.md)). Most existing rosters are
unaffected: only entries of the exact invalid shapes above — the kind that
used to silently misbehave rather than do anything useful — now fail eval.

A plain default `mkHarness` call (no explicit `byName` overrides) now bakes
3 agents instead of 4: `defaultRoster`'s built-in `filer` entry resolves to
the `filerModel` schema default, `""`, which is the existing #392 opt-out
sentinel, and `rosterLib.dropOptedOut` — which `lib/mkHarness.nix` now runs
unconditionally ahead of every downstream consumer of the resolved roster —
drops it before the image is built. This is not new behavior: `filer` was
always opted out by default, only previously masked by Driver-level
empty-model filters (removed as redundant by this same issue) rather than
by roster-entry count.

## `MAX_BUDGET_TOKENS`/`MAX_BUDGET_USD` now also cap the orchestrator's review loop (issue #2694)

These two knobs previously gated only `selfHealGate`'s host-side decision to
dispatch another fix-pass Box (issue #2001) — the value never left the
launcher process. They are now `boxEnv` (`lib/env-schema.nix`), so
`entrypoint.sh` forwards them into the Box unconditionally as the
orchestrator's own `--max-budget-tokens`/`--max-budget-usd` flags, and the
orchestrator's review-pass loop (`--review-prompt-file` set, the default
under `ORCHESTRATOR_ENABLED`) now applies the same threshold to its own
cumulative spend to commit to a terminal land pass instead of a further
`BLOCK`-triggered review round. This is the same threshold, not the same
running total: the in-Box sum starts fresh at zero each Box invocation
(implement/fix/review passes plus dispatched workers in that Box only),
distinct from `selfHealGate`'s own cross-Box figure that sums every
attempt across the whole issue — a run spanning several fix-pass Boxes
gets a fresh budget in each one.

If you already set `MAX_BUDGET_TOKENS`/`MAX_BUDGET_USD` purely to bound
host-side fix-pass retries, this is a behavior widening, not a breaking
change: the cap now also applies to the in-Box review loop, which can land
a run earlier than before at the same threshold. A malformed or negative
value degrades to `0` (disabled) rather than erroring, matching this pair's
existing host-side tolerance (`atoiNonneg`/`floatNonneg`) — under the
legacy single-loop path (`ORCHESTRATOR_ENABLED` off, or a custom
`SPINDRIFT_PROMPT_DIR` with no review-pass prompt) the value is accepted
but never consulted.

## `agent-trigger` dropped from the versioned label lifecycle contract (issue #2557)

[`VERSIONING.md`](VERSIONING.md)'s "Label lifecycle names" row no longer lists
`agent-trigger` as part of the versioned contract. It's an internal
CI-dispatch-workflow label `agent-dispatch.yml` swaps away the moment it
claims an issue, not a durable state like the four triage names
(`ready-for-agent`, `agent-in-progress`, `agent-complete`, `agent-failed`) —
so it never should have carried a semver guarantee. The label itself is
unaffected and still in active use by `.github/workflows/agent-dispatch.yml`;
only the documented *contract* — the promise that its name won't change
without a version bump — is withdrawn. If you depended on `agent-trigger`'s
name as a stable integration point, it may still change in a future
non-major release.

The same table also now enumerates the previously-undocumented research
(ADR 0022) and priority (ADR 0040) label families as part of the contract.

## Choices-bearing knobs now enforce their valid values (issue #2519)

The seven knobs that declare a `choices` list in `lib/env-schema.nix`
(`mergeMode`, `codeForge`, `issueTracker`, `overlapGate`, `mergeMethod`,
`syncMethod`, `boxForgeAndIssueAccess`) now reject an out-of-list value at
build/eval time instead of accepting it silently, on both entry paths:

- **Flake module.** Each knob's generated Consumer option
  (`perSystem.spindrift.<path>`) now has type `types.enum choices` instead
  of `types.str`. Setting one to a value outside its choices — e.g.
  `git.merge.policy = "Auto"` (valid: `immediate`, `auto`, `manual`) — now
  fails `nix eval`/`nix build` at the option, naming the option path and
  the valid values, instead of evaluating.
- **Direct `mkHarness` callers.** A Consumer or fixture calling `mkHarness
  { defaults = { ... }; }` directly, bypassing the flake module, now gets
  the same rejection: an out-of-choices value, or explicitly passing `null`
  for one of these seven knobs, now throws naming the knob, the bad value,
  and the valid choices, instead of silently rendering into the Launcher
  input document (a `null` used to render as an empty string, e.g.
  `MERGE_METHOD=""`).

If you set one of these seven knobs, either through `perSystem.spindrift.*`
or a direct `mkHarness` call, confirm the value is one of its documented
choices (see `docs/reference.md` or `spindrift --help --all`).

## `mkHarness`'s check-only return keys moved under `internals` (issue #2529)

A direct `mkHarness { ... }` call's return attrset no longer carries its
21 check-only keys (`build`, `run`, `manpage`, `bashCompletion`,
`fishCompletion`, `zshCompletion`, `imagePath`, `promptDir`, `skillsDir`,
`driverExecBin`, `roster`, …) at the top level. They now live under one
named `internals` attrset instead, e.g. `result.driverExecBin` becomes
`result.internals.driverExecBin`. This surface was never part of the
versioned Consumer contract (ADR 0010 scopes that to `image`/`spindrift`/
`packages`/`apps`; see `docs/reference.md`'s "Calling `mkHarness` directly"),
so it carries no semver bump, but any fixture or fork reaching into these
keys directly needs the `internals.` prefix added.

## Knob env overrides deprecated; use `--flag` or `settings.*` (ADR 0020)

`spindrift` now hands every nix-computed value to the launcher through one
**Launcher input document** (the resolved `settings.*` values plus build/run
artifacts), passed via a single `--input` flag. Precedence is **CLI flag >
flake `settings.<section>.<knob>` > baked default**. An ambient knob env var
(a forgotten shell export, a sourced `harness.env`, CI env) still wins over
the flake setting **this release** — the launcher warns, naming the variable
and both migration targets — but a future MINOR release makes it an error.
Secrets (`GH_TOKEN`, `CLAUDE_CODE_OAUTH_TOKEN`, `ANTHROPIC_API_KEY`,
`JIRA_TOKEN`) and internal launcher→Box plumbing are unaffected — env keeps
those two jobs unchanged. `harness.env.example` now lists secrets only; move
any other knob you kept there to `settings.*` in your `flake.nix`, or pass it
as a flag per invocation.

`dogfood.sh` forwards its `MAX_JOBS`/`CONTINUOUS_DISPATCH` shell variables as
`--max-jobs`/`--continuous-dispatch` flags instead of exporting them —
`KNOB=x ./dogfood.sh` for any other knob is no longer a supported override
idiom; set it in `flake.nix` `settings` instead.

| Env var (deprecated as a knob channel) | Flag | Flake setting |
|---|---|---|
| `AUTO_FORMAT` | `--auto-format` | `settings.promptSkillIteration.autoFormat` |
| `AUTO_LINT` | `--auto-lint` | `settings.promptSkillIteration.autoLint` |
| `BASE_BRANCH` | `--base-branch` | `settings.branches.baseBranch` |
| `BRANCH_PREFIX` | `--branch-prefix` | `settings.branches.branchPrefix` |
| `BWRAP_UNSHARE_NET` | `--bwrap-unshare-net` | `settings.sandbox.bwrapUnshareNet` |
| `CODE_FORGE` | `--forge-backend` | `settings.repository.codeForge` |
| `CODE_FORGE_REMOTE_URL` | `--remote-url` | `settings.repository.codeForgeRemoteURL` |
| `COMPLETE_LABEL` | `--complete-label` | `settings.lifecycleLabels.completeLabel` |
| `CONTINUOUS_DISPATCH` | `--continuous-dispatch` | `settings.concurrency.continuousDispatch` |
| `DEV_SHELL_NAME` | `--dev-shell-name` | `settings.sandbox.devShellName` |
| `DEV_SHELL_PROBE_TIMEOUT` | `--dev-shell-probe-timeout` | `settings.sandbox.devShellProbeTimeout` |
| `FAILED_LABEL` | `--failed-label` | `settings.lifecycleLabels.failedLabel` |
| `FILER_MODEL` | `--filer-model` | `settings.models.filerModel` |
| `GIT_USER_EMAIL` | `--user-email` | `settings.repository.gitUserEmail` |
| `GIT_USER_NAME` | `--user-name` | `settings.repository.gitUserName` |
| `HOLD_JITTER_SECS` | `--hold-jitter-secs` | `settings.selfHealing.holdJitterSecs` |
| `IN_PROGRESS_LABEL` | `--in-progress-label` | `settings.lifecycleLabels.inProgressLabel` |
| `ISSUE_NUMBER` | `--issue-number` | — |
| `ISSUE_TRACKER` | `--tracker` | `settings.issueDiscovery.issueTracker` |
| `JIRA_BASE_URL` | `--jira-base-url` | `settings.repository.jiraBaseURL` |
| `JIRA_EMAIL` | `--jira-email` | `settings.repository.jiraEmail` |
| `JIRA_INCLUDE_COMMENTS` | `--jira-include-comments` | `settings.issueDiscovery.jiraIncludeComments` |
| `JIRA_PROJECT_KEY` | `--jira-project-key` | `settings.repository.jiraProjectKey` |
| `JIRA_STATUS_MAPPING` | `--jira-status-mapping` | `settings.lifecycleLabels.jiraStatusMapping` |
| `LABEL` | `--dispatch-label` | `settings.issueDiscovery.label` |
| `LOCAL_ISSUES_DIR` | `--local-dir` | `settings.issueDiscovery.localIssuesDir` |
| `LOCAL_ISSUE_REFERENCE` | `--local-reference` | `settings.issueDiscovery.localIssueReference` |
| `MAX_FIX_ATTEMPTS` | `--max-fix-attempts` | `settings.selfHealing.maxFixAttempts` |
| `MAX_JOBS` | `--max-jobs` | `settings.concurrency.maxJobs` |
| `MAX_PARALLEL` | `--max-parallel` | `settings.concurrency.maxParallel` |
| `MAX_REBASE_ATTEMPTS` | `--max-rebase-attempts` | `settings.selfHealing.maxRebaseAttempts` |
| `MEMORY_LIMIT` | `--memory-limit` | `settings.sandbox.memoryLimit` |
| `MERGE_GUARD_PATHS` | `--merge-guard-paths` | `settings.branches.mergeGuardPaths` |
| `MERGE_MODE` | `--merge-policy` | `settings.branches.mergeMode` |
| `MERGE_POLL_INTERVAL` | `--merge-poll-interval` | `settings.branches.mergePollInterval` |
| `MERGE_POLL_TIMEOUT` | `--merge-poll-timeout` | `settings.branches.mergePollTimeout` |
| `MODEL` | `--model` | `settings.models.model` |
| `OVERLAP_GATE` | `--overlap-gate` | `settings.concurrency.overlapGate` |
| `PIDS_LIMIT` | `--pids-limit` | `settings.sandbox.pidsLimit` |
| `PODMAN_NETWORK` | `--podman-network` | `settings.sandbox.podmanNetwork` |
| `REPO_SLUG` | `--repo-slug` | `settings.repository.repoSlug` |
| `REVIEW_MODEL` | `--review-model` | `settings.models.reviewModel` |
| `SCOUT_MODEL` | `--scout-model` | `settings.models.scoutModel` |
| `SPINDRIFT_PROMPT_DIR` | `--prompt-dir` | — |
| `SPINDRIFT_SKILLS_DIR` | `--skills-dir` | — |
| `TRANSIENT_BACKOFF_SECS` | `--transient-backoff-secs` | `settings.selfHealing.transientBackoffSecs` |
| `TRANSIENT_RETRY_MAX` | `--transient-retry-max` | `settings.selfHealing.transientRetryMax` |

### Flag names re-cut to domains (ADR 0037 Pass 2)

The `Flag` column above shows each knob's **canonical** flag. Several were
renamed to read from their domain leaf, dropping a now-redundant prefix —
`--issue-tracker` → `--tracker`, `--code-forge` → `--forge-backend`,
`--merge-mode` → `--merge-policy`, `--git-user-name` → `--user-name`, and so on.
Every previous flag name **keeps working as a deprecated alias** (it resolves to
the same value; `spindrift --help --all` marks it `(deprecated)`), so no
dispatch script breaks — migrate at your leisure before the aliases are removed
at 1.0. The env-var names are unchanged. The primary flake surface is the domain
tree under `perSystem.spindrift.*` (see `docs/flake-options.md`); the
`settings.<section>.*` paths above remain as deprecated aliases until 1.0.

Full alias-to-domain mapping:

<!-- BEGIN GENERATED LEGACY SETTINGS MAPPING -- nix run .#regen -- DO NOT EDIT -->
| Legacy alias | Canonical replacement |
| --- | --- |
| `perSystem.spindrift.settings.branches.baseBranch` | `perSystem.spindrift.git.baseBranch` |
| `perSystem.spindrift.settings.branches.branchPrefix` | `perSystem.spindrift.git.branchPrefix` |
| `perSystem.spindrift.settings.branches.mergeGuardPaths` | `perSystem.spindrift.git.merge.guardPaths` |
| `perSystem.spindrift.settings.branches.mergeMethod` | `perSystem.spindrift.git.merge.method` |
| `perSystem.spindrift.settings.branches.mergeMode` | `perSystem.spindrift.git.merge.policy` |
| `perSystem.spindrift.settings.branches.mergePollInterval` | `perSystem.spindrift.git.merge.pollInterval` |
| `perSystem.spindrift.settings.branches.mergePollTimeout` | `perSystem.spindrift.git.merge.pollTimeout` |
| `perSystem.spindrift.settings.concurrency.continuousDispatch` | `perSystem.spindrift.dispatch.continuous.enable` |
| `perSystem.spindrift.settings.concurrency.maxJobs` | `perSystem.spindrift.dispatch.maxJobs` |
| `perSystem.spindrift.settings.concurrency.maxParallel` | `perSystem.spindrift.dispatch.maxParallel` |
| `perSystem.spindrift.settings.concurrency.overlapGate` | `perSystem.spindrift.dispatch.overlapGate` |
| `perSystem.spindrift.settings.issueDiscovery.issueTracker` | `perSystem.spindrift.issues.tracker` |
| `perSystem.spindrift.settings.issueDiscovery.jiraIncludeComments` | `perSystem.spindrift.issues.jira.includeComments` |
| `perSystem.spindrift.settings.issueDiscovery.label` | `perSystem.spindrift.issues.labels.dispatch` |
| `perSystem.spindrift.settings.issueDiscovery.localIssueReference` | `perSystem.spindrift.issues.localReference` |
| `perSystem.spindrift.settings.issueDiscovery.localIssuesDir` | `perSystem.spindrift.issues.localDir` |
| `perSystem.spindrift.settings.lifecycleLabels.completeLabel` | `perSystem.spindrift.issues.labels.complete` |
| `perSystem.spindrift.settings.lifecycleLabels.failedLabel` | `perSystem.spindrift.issues.labels.failed` |
| `perSystem.spindrift.settings.lifecycleLabels.inProgressLabel` | `perSystem.spindrift.issues.labels.inProgress` |
| `perSystem.spindrift.settings.lifecycleLabels.jiraStatusMapping` | `perSystem.spindrift.issues.jira.statusMapping` |
| `perSystem.spindrift.settings.models.filerModel` | `perSystem.spindrift.agents.models.filer` |
| `perSystem.spindrift.settings.models.model` | `perSystem.spindrift.agents.models.default` |
| `perSystem.spindrift.settings.models.reviewModel` | `perSystem.spindrift.agents.models.review` |
| `perSystem.spindrift.settings.models.scoutModel` | `perSystem.spindrift.agents.models.scout` |
| `perSystem.spindrift.settings.models.workerModel` | `perSystem.spindrift.agents.models.worker` |
| `perSystem.spindrift.settings.promptSkillIteration.autoFormat` | `perSystem.spindrift.agents.format.enable` |
| `perSystem.spindrift.settings.promptSkillIteration.autoLint` | `perSystem.spindrift.agents.lint.enable` |
| `perSystem.spindrift.settings.promptSkillIteration.orchestratorEnabled` | `perSystem.spindrift.dispatch.orchestrator.enable` |
| `perSystem.spindrift.settings.repository.boxForgeAndIssueAccess` | `perSystem.spindrift.forge.boxAccess` |
| `perSystem.spindrift.settings.repository.codeForge` | `perSystem.spindrift.forge.backend` |
| `perSystem.spindrift.settings.repository.codeForgeAccumulationRepoDir` | `perSystem.spindrift.forge.accumulationRepoDir` |
| `perSystem.spindrift.settings.repository.codeForgeRemoteURL` | `perSystem.spindrift.forge.remoteURL` |
| `perSystem.spindrift.settings.repository.ghTokenRefreshFile` | `perSystem.spindrift.forge.ghTokenRefreshFile` |
| `perSystem.spindrift.settings.repository.gitUserEmail` | `perSystem.spindrift.git.user.email` |
| `perSystem.spindrift.settings.repository.gitUserName` | `perSystem.spindrift.git.user.name` |
| `perSystem.spindrift.settings.repository.jiraBaseURL` | `perSystem.spindrift.issues.jira.baseURL` |
| `perSystem.spindrift.settings.repository.jiraEmail` | `perSystem.spindrift.issues.jira.email` |
| `perSystem.spindrift.settings.repository.jiraProjectKey` | `perSystem.spindrift.issues.jira.projectKey` |
| `perSystem.spindrift.settings.repository.repoSlug` | `perSystem.spindrift.forge.repoSlug` |
| `perSystem.spindrift.settings.sandbox.bwrapUnshareNet` | `perSystem.spindrift.infra.network.bwrapUnshare` |
| `perSystem.spindrift.settings.sandbox.devShellName` | `perSystem.spindrift.infra.devShell.name` |
| `perSystem.spindrift.settings.sandbox.devShellProbeTimeout` | `perSystem.spindrift.infra.devShell.probeTimeout` |
| `perSystem.spindrift.settings.sandbox.memoryLimit` | `perSystem.spindrift.infra.limits.memory` |
| `perSystem.spindrift.settings.sandbox.pidsLimit` | `perSystem.spindrift.infra.limits.pids` |
| `perSystem.spindrift.settings.sandbox.podmanNetwork` | `perSystem.spindrift.infra.network.podman` |
| `perSystem.spindrift.settings.selfHealing.holdJitterSecs` | `perSystem.spindrift.dispatch.retry.holdJitter` |
| `perSystem.spindrift.settings.selfHealing.maxBudgetTokens` | `perSystem.spindrift.dispatch.budget.tokens` |
| `perSystem.spindrift.settings.selfHealing.maxBudgetUSD` | `perSystem.spindrift.dispatch.budget.usd` |
| `perSystem.spindrift.settings.selfHealing.maxFixAttempts` | `perSystem.spindrift.dispatch.retry.maxFix` |
| `perSystem.spindrift.settings.selfHealing.maxRebaseAttempts` | `perSystem.spindrift.dispatch.retry.maxRebase` |
| `perSystem.spindrift.settings.selfHealing.preflightStaleBase` | `perSystem.spindrift.git.merge.preflightStaleBase` |
| `perSystem.spindrift.settings.selfHealing.transientBackoffSecs` | `perSystem.spindrift.dispatch.retry.transientBackoff` |
| `perSystem.spindrift.settings.selfHealing.transientRetryMax` | `perSystem.spindrift.dispatch.retry.transientMax` |
<!-- END GENERATED LEGACY SETTINGS MAPPING -->

## `REVIEW_EFFORT` dispatch-time override removed (issue #2512)

> **Restored (issue #3171):** a later release brought the dispatch-time
> override back, for `REVIEW_MODEL` too, with cleaner semantics than the
> pre-#2512 behavior this section removed: an explicit
> `REVIEW_MODEL=...`/`REVIEW_EFFORT=...` (or `--review-model`/
> `--review-effort`) at dispatch now overrides the baked roster reviewer
> entry on an already-built image, no rebuild needed — precedence is
> dispatch-time env > baked roster reviewer entry > coordinator fallback,
> and unset/empty at dispatch leaves the baked chain unchanged. The rest of
> this section describes the interim releases between #2512 and #3171; the
> default-behavior change below (unset `REVIEW_EFFORT` follows the roster
> reviewer's effort, not the coordinator's) still stands.

`REVIEW_EFFORT` (`--review-effort` / `perSystem.spindrift.agents.models.reviewEffort`)
no longer takes effect as a live, per-dispatch Box-runtime override —
`REVIEW_EFFORT=xhigh spindrift dispatch ...` (or `--review-effort xhigh`) is
now a silent no-op against an already-built image. The knob now has the same
shape `REVIEW_MODEL` already has: it only sets the roster `reviewer` entry's
effort at nix build time, baked into the image before dispatch, which the
orchestrator's code-owned review pass then reads via the prompt-assembly
Handoff. Set `perSystem.spindrift.agents.models.reviewEffort` in your
Consumer flake (or the roster's `reviewer` entry directly) and rebuild
(`spindrift build`) instead.

Under `DRIVER=opencode` specifically, the review pass's effort now always
falls back to the coordinator's own `--effort`: opencode's `agentsJsonTemplate`
never renders a `reviewer` key, so there is no roster value for the Handoff
to extract — the same fallback `REVIEW_MODEL` already has on that Driver.
`perSystem.spindrift.agents.models.reviewEffort` has no effect under
`DRIVER=opencode` for this reason.

This is also a default-behavior change, not just a removed override: with
`REVIEW_EFFORT` unset, the review pass previously fell back to the
coordinator's own `--effort`; under `DRIVER=claude` it now runs at the
roster reviewer's effort (`rosterDefaults.reviewer.effort`, `"high"` by
default) instead. Every default-roster orchestrator Consumer whose
coordinator runs below `high` sees its review pass cost rise accordingly
unless it pins `perSystem.spindrift.agents.models.reviewEffort` (or the
roster's `reviewer` entry) explicitly.

| Removed capability | Replacement |
|---|---|
| Dispatch-time `REVIEW_EFFORT=...` / `--review-effort ...` override | `perSystem.spindrift.agents.models.reviewEffort` in your Consumer flake under `DRIVER=claude` (image rebuild required); no effect under `DRIVER=opencode` |

## `nix run .#run` / `nix run .#build` (removed in v0.5.0)

spindrift 0.1.1 introduced a unified CLI. The `nix run .#run` and `nix run
.#build` app idioms were deprecated at that point and were removed in
**v0.5.0** (issue #613) — a Consumer invoking either now gets an
unknown-flake-output error, not a forwarding alias.

### Quick-start with the new CLI

```sh
# Enter the dev shell (spindrift goes on PATH automatically)
nix develop      # or: direnv allow  (if your repo has .envrc)

# Dispatch all ready-for-agent issues
spindrift dispatch

# Dispatch a single issue
spindrift dispatch 123

# Build / realize the agent image without running any agent
spindrift build

# Dispatch without auto-building (fail fast if image absent)
spindrift dispatch --no-build

# Show all flags and subcommands
spindrift --help

# Print the installed version
spindrift --version
```

### Old → new mapping

| Old command              | New command              | Notes                               |
|--------------------------|--------------------------|-------------------------------------|
| `nix run .#run`          | `spindrift dispatch`     | Removed alias (v0.5.0)              |
| `nix run .`              | `spindrift dispatch`     | `apps.default` now points to CLI    |
| `nix run .#build`        | `spindrift build`        | Removed alias (v0.5.0)              |
| `ISSUE_NUMBER=42 nix run .#run` | `spindrift dispatch 42` | Positional arg replaces env var |

### Template quick-start

Consumer flakes generated from `nix flake init -t github:jordansmall/spindrift`
now include a `.envrc` (`use flake`) and a `devShells.default` that puts
`spindrift` on PATH via `nix develop` or direnv. See `flake.nix` in the
template for the full pattern.

### Why the change?

ADR-0010 established a single `spindrift` CLI as the primary surface for the
harness. The old `nix run .#run` / `.#build` split was a build artefact, not a
user-facing design. The unified CLI is easier to discover, script, and extend.

## `DEPS_POLL_SECS` / `DEPS_WAIT_SECS` (removed)

`DEPS_POLL_SECS`/`DEPS_WAIT_SECS` (`settings.concurrency.depsPollSecs`/
`depsWaitSecs`) configured the in-process dependency-wave poll loop. That
loop was deleted (ADR 0019): every dispatch invocation now runs at most one
wave and exits, so the poll/wait knobs configure nothing. Setting either in
`settings.concurrency` now fails at flake-eval time with an unknown-key
error naming the valid keys.

`MAX_JOBS` still caps the wave size (`0` means uncapped); re-invoking
`dispatch` (directly, via a driving loop, or via `dogfood.sh`) is how a
dependency graph drains wave by wave.

| Removed knob     | Replacement                             |
|------------------|------------------------------------------|
| `DEPS_POLL_SECS` | none — remove it from your settings/env |
| `DEPS_WAIT_SECS` | none — remove it from your settings/env |

## `spindrift engage` (removed in v0.2.0)

`spindrift engage <issue>` was removed in **v0.2.0**. Use
`spindrift recover <issue>` instead — it performs the same merge-gate/adopt
operation.

| Removed command              | Replacement command           |
|------------------------------|-------------------------------|
| `spindrift engage <issue>`   | `spindrift recover <issue>`   |
