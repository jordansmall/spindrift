# The turn's configuration crosses the box seam as one handoff document, not a flag fan-out

[ADR 0035](0035-the-in-box-orchestrator-loop-is-a-go-program-above-driver-exec-not-entrypoint-prose.md)
put `orchestrator` between `entrypoint.sh` and `driver-exec`, and had it
forward the same flag surface `entrypoint.sh`'s direct call to `driver-exec`
already used: `--prompt-file`/`--agents-file`/`--session-file`/`--log-path`
plus the devshell pair. That surface grew as the turn grew. `driver-exec`
picked up its own `--model`/`--effort`/`--driver`/`--driver-bin`/
`--driver-flags`/argv-shape flags to become Driver-pluggable (ADR 0009), the
code-owned review pass (ADR 0035's #2037 amendment) added
`--review-prompt-file`/`--review-model`/`--review-effort`, and issue #2694
added `--max-review-rounds`/`--max-slices`/`--max-budget-tokens`/
`--max-budget-usd`. `orchestrator` needed every one of those to reach
`driver-exec` unchanged across N loop passes, so it grew its own
hand-maintained forward list mirroring `driver-exec`'s flag declarations one
for one, and the Driver argv-shape grammar (which flag spells which fact, and
in what order) ended up declared twice — once in `driver-exec`'s own flag
set, once implicitly in whatever `entrypoint.sh` and `orchestrator` had to
pass to reach it. Two declarations of the same grammar drift by
construction: a new argv-shape flag added to one call site and not the other
is a silent gap, not a build error, and issue #2975 (this ADR) is the point
that drift was named directly rather than patched around again.

## Decision

The turn's per-run static configuration — the prompt and review-prompt
paths (the review prompt travels as a path now, never inline text), the
subagent roster, models, efforts, caps, the Driver's argv-shape grammar, and
the session mode — crosses the entrypoint→orchestrator→driver-exec seam as
one JSON document, `promptassembly.Handoff`, instead of a flag fan-out.
`entrypoint.sh` selects the invoker (`orchestrator` or `driver-exec`
directly, per ADR 0035's `ORCHESTRATOR` gate) and passes the document
through unread; it no longer re-extracts fields out of it or re-writes temp
files to shuttle them onward.

Concretely:

- **One producer per Driver pass that precedes a Handoff, one consumer
  grammar.** `driver-exec assemble-prompt` is the main producer: it already
  loads the fragment registry and renders the prompt, so writing the handoff
  alongside `Result.Handoff` costs it nothing new — every field Assemble
  itself computes (`SessionMode`, `Invoker`, `ReviewModel`/`ReviewEffort`
  extracted from the roster's `reviewer` entry) or that arrives as pure
  passthrough from its own flags (`--model`, `--driver`, `--max-slices`,
  the `--argv-*` family, ...) lands in the same struct it writes to disk.
  `driver-exec env-handoff` is the narrower second producer (issue #2975
  slice 2): the one Driver pass that runs before `assemble-prompt` ever
  executes — `phase_conflict_resolve`'s pre-work rebase-fixup pass — has no
  registry to load and no gates to compute, so it builds the same `Handoff`
  shape straight from env-derived flags with no `promptassembly.Assemble`
  dependency at all. Both write the identical JSON shape; `orchestrator` and
  `driver-exec` load it through the same `promptassembly.LoadHandoffFile`
  and don't know or care which producer wrote it.
- **Per-pass flags are only what genuinely varies per pass.** `--handoff-file`
  (required on both binaries), `--prompt-file` (falls back to the handoff's
  own `PromptFile` when empty — only a seeded run-state pass overrides it),
  `--session-file`, `--log-path`, and `--top-level-role`
  (`driver-exec`) / the run-state artifact paths (`orchestrator`) stay real
  per-invocation flags, because a value like "which log this one pass tees
  to" cannot live in a document shared unmodified across every pass of a
  loop. Everything else that used to be a flag on `driver-exec` or a
  forwarded flag on `orchestrator` — `--agents-file`, `--driver`,
  `--driver-bin`, `--model`/`--effort`, `--review-model`/`--review-effort`,
  `--max-review-rounds`/`--max-slices`/`--max-budget-tokens`/
  `--max-budget-usd`, the whole `--argv-*` family, `--devshell`/
  `--devshell-name` — is gone from both binaries' own flag sets entirely and
  read straight off the loaded `Handoff` instead.
- **The `Handoff` struct — and the JSON document built from it — is
  unified.** `Handoff.ArgvShape` (prompt style/flag, model flag, agents
  flag, effort flag, argument order) is populated once per run, by whichever
  producer ran, and `driver-exec` is the only binary that ever reads it back
  to build a Driver's argv — `orchestrator` passes the handoff through
  unread on this point exactly as it does on every other field it forwards
  without consulting. The `--argv-*` flag *grammar* that feeds it is not
  unified, though: `assembleprompt_cmd.go` and `envhandoff_cmd.go` each
  independently declare the same seven `--argv-*` flags
  (`argv-order`/`argv-prompt-style`/`argv-model-flag`/`argv-prompt-flag`/
  `argv-agents-flag`/`argv-effort-flag`/`argv-model-omit-empty`) as their two
  producers' own CLI surface, so that duplication survives this ADR — what's
  unified is the one `Handoff` shape both producers write and both consumers
  read, not the flags that populate it.
- **The orchestrator's own forward list is deleted, not shrunk.**
  `orchestrator` no longer declares `--model`/`--driver`/`--review-*`/cap
  flags at all; there is nothing left to keep in sync with `driver-exec`'s
  own flag set for those facts. The `--argv-*` family is a narrower
  exception, per above: it was never on `orchestrator`'s forward list to
  begin with (both `Handoff` producers, not `orchestrator`, ever needed
  it), so this ADR does not touch it either way.

## Considered Options

- **Keep growing the flag fan-out, add a linter or generated-parity check
  between `driver-exec`'s flags and `orchestrator`'s forward list.** Treats
  the symptom, not the cause: a parity check only catches a drift once it
  has already happened, and the two declarations still exist, still cost a
  matching edit on every new knob, and still have to be kept in the same
  order for `orchestrator`'s exec-args construction to build a correct
  `driver-exec` invocation. Rejected — the grammar duplication is the
  problem, not the absence of a check on it.
- **Fold the handoff facts into the run-state artifact (`RunState`,
  ADR 0035) instead of a separate document.** Rejected: run-state is
  explicitly the mutable, pass-to-pass artifact (done/remaining slices, last
  verdict, dispositions/decisions logs) that each pass reads and rewrites.
  The turn's static configuration never changes mid-loop; conflating it with
  what does would make every pass's run-state write a hazard for the
  read-only facts riding alongside it, for no benefit over a second file.
- **Pass the handoff as inline JSON on argv instead of a file path.**
  Rejected on the same grounds `--review-prompt-file` already settled for
  the review prompt itself: an unbounded text blob does not belong on a
  process's argv (visible in `ps`, subject to `ARG_MAX`), and a file both
  binaries independently `stat`/read is no more expensive than a flag they'd
  have needed anyway (`--handoff-file` replaces what would otherwise still
  need to be at least one flag naming where the facts live). Accepted
  trade-off of the chosen file-based approach: `DriverBin`/`DriverFlags`/
  `ArgvShape` move off immutable process argv onto a file every later
  `driver-exec` invocation re-reads, so — unlike argv, fixed for the life of
  a process — those facts are rewritable between passes of the same loop.

## Consequences

- `driver-exec`'s and `orchestrator`'s own flag sets shrink to the genuinely
  per-pass surface named above; every other configuration fact is invisible
  to `flag.Parse` and only reachable by reading the loaded `Handoff`, which
  is a compile-time win for anything iterating the handoff's fields (a typo
  in a struct field name fails to build; a typo in a forwarded flag name
  fails silently at runtime, the exact class of gap this ADR replaces).
- Two producers now have to agree on one JSON shape instead of one binary
  owning its own flag grammar outright. `driver-exec env-handoff`'s narrower
  field set (it leaves `PromptFile`, `AgentsFile`, `ReviewPromptFile`,
  `ReviewModel`, `ReviewEffort`, and `SessionMode`/`Invoker` unset, since the
  one pass it serves needs none of them) means a `Handoff` loaded from that
  producer is a valid but sparser document than one `assemble-prompt`
  writes — every consumer already treats those fields as legitimately empty
  (a review pass is enabled only when `ReviewPromptFile` is non-empty, so an
  unset one already meant "no review pass" before this ADR), so the sparser
  shape needs no new handling, only correct defaults on the read side.
  Documented, not hidden, since a shared shape with two producers is
  precisely how a *third* future producer could reintroduce the drift this
  ADR closes if this weren't spelled out for it.
- ADR 0035's own "Driver stays pluggable" bullet, which named the flag
  surface this ADR replaces, is now historical — see the superseded note
  added there rather than rewritten, since an ADR records the decision as it
  was made, not as it was later revised.
- The end-to-end shape is now testable as data: a fixture `Handoff` JSON
  file drives `entrypoint` → `orchestrator` → `driver-exec` (with a faked
  Driver) across an implement pass and a review pass to an outcome, without
  needing to reconstruct the equivalent flag invocation by hand for every
  test case.
