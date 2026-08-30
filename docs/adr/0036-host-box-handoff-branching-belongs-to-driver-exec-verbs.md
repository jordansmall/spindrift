# Branching logic at the host/box hand-off belongs to `driver-exec` verbs

ADR 0007 drew the runtime into three tiers: nix computes what is knowable at
evaluation time, a nix-built Go binary orchestrates live state, and the
in-container `entrypoint.sh` stays thin generated bash — linear exec glue with
no branching logic. ADR 0035 applied that same boundary one level deeper,
moving the box's own review loop out of prose in `issue-prompt.md` and into the
`cmd/launcher/orchestrator` Go binary, so the loop's state machine, caps, and
log-scanning have a code owner under test rather than living as instructions
the model may or may not obey.

The entrypoint had one more pocket of exactly the logic tier ADR 0007 found
bash unfit for, at a seam neither 0007 nor 0035 scoped: the *host/box
hand-off*. When the Driver exits without a parseable `SPINDRIFT_OUTCOME` line,
`emit_outcome_backstop` had to decide — across the Dispatch kind (research
never cuts a branch), the Code Forge (`local`'s read-only mount, `github`'s
read-only relay), the box's write-enable signal, and the branch's commit count
— whether to push the work with bounded retry, relay it via the outbox bundle,
or merely note that nothing could be preserved. That decision was ~90 lines of
hand-rolled bash: a push-retry loop with its own negative-clamp guard, a
dense readonly-relay decision tree, and — because the backoff knobs were not
forwarded into the box — hand-copied `${MAX_REBASE_ATTEMPTS:-3}` schema
defaults that could silently drift from `lib/env-schema.nix`. This is the same
"graph and poll code" shape ADR 0007 pushed to Go and ADR 0035 pushed one tier
deeper, sitting in the tier that was supposed to be branch-free.

`driver-exec` already had the answer to where such logic goes. Its `bundle-out`
verb (issue #1808) owns `CODE_FORGE=local`'s harness-side code-out — real git
plumbing and an empty-range corrective-outcome decision — behind a thin CLI
wrapper, so the entrypoint's contract there shrank to "invoke the verb." The
outcome-backstop is the same category of work at the same seam, and it belongs
in the same place.

Concretely:

- **The decision is a verb, not a shell function.** `driver-exec
  outcome-backstop` (backed by `cmd/launcher/internal/outcomebackstop`) owns
  the whole backstop decision: the research short-circuit, the dirty-tree
  salvage, the commit-count fallback, and the four-way branch across no-work /
  `CODE_FORGE=local` / read-only-github-relay / writable-push-with-retry,
  ending in the single synthetic `status=blocked` `SPINDRIFT_OUTCOME` line.
  The push retry rides the shared `internal/retry` `LinearBackoff` leaf (ADR-
  scoped by issue #2154) behind an injectable Clock, so the schedule, cap, and
  negative-input clamp are decided and tested once, not re-hand-rolled here.
- **The knobs arrive as launcher-delivered Box plumbing.** `MAX_REBASE_ATTEMPTS`,
  `TRANSIENT_BACKOFF_SECS`, and `HOLD_JITTER_SECS` flip to `boxEnv = true`, so
  the launcher forwards them into the Box at their schema-resolved values and
  the entrypoint passes them straight to the verb as flags. The hand-copied
  bash defaults are deleted — the schema stays the one source of truth for the
  backoff shape.
- **The entrypoint reduces to linear exec glue.** `emit_outcome_backstop`
  becomes a single `driver-exec outcome-backstop` invocation that hands the
  verb its inputs and lets it own the decision — exactly what ADR 0007
  intended for the tier. main()'s read-only-github fall-through to the
  harness-owned `bundle-out` step, which relays the branch the backstop
  deliberately does not push, is unchanged: the verb decides and notes, the
  harness relays.
- **The Go decision is tested; the bash is proven by wiring.** The verb's
  decision table gets Go unit tests through a git seam and a fake Clock, so
  each arm is exercised without a real remote. The existing entrypoint
  outcome-backstop bats suite keeps passing against the one-call form, which is
  itself the proof the migration preserved the hand-off protocol.

## Decision

Branching logic at the host/box hand-off belongs to a `driver-exec` verb, not
to `entrypoint.sh`. When the entrypoint reaches a point where it must *decide*
between hand-off strategies — push vs. relay vs. note, retry vs. give up,
which ref survives and how — that decision is a Go verb with a thin CLI
wrapper and its own unit tests, and the entrypoint's role shrinks to invoking
it. `entrypoint.sh` stays linear exec glue: assemble inputs, invoke the verb,
exec. The `bundle-out` verb is the precedent; the outcome-backstop verb is the
second instance; the rule generalizes to the next in-box hand-off feature so it
has a named owner instead of accreting into the entrypoint.

This upholds ADR 0035's intent (and ADR 0007's before it) at a seam those ADRs
did not name: the launcher's orchestration is Go host-side, the box's review
loop is Go in-box, and now the box's host/box hand-off decisions are Go in-box
too — each behind the smallest interface the entrypoint can call.

## Considered Options

- **Keep the decision as bash in `entrypoint.sh` (status quo).** Rejected for
  the reason ADR 0007 already ruled on for this logic tier: a retry state
  machine with a clamp guard, a multi-dimension decision tree, and knob
  defaults hand-copied from the schema is exactly what bash is unfit for, and
  the hand-copied defaults are a standing drift hazard. Leaving it in bash
  repeats one layer deeper the mistake 0007 and 0035 already corrected.
- **Move the decision into the `cmd/launcher/orchestrator` in-box binary.**
  Rejected: the orchestrator (ADR 0035) is scoped narrowly to the
  implement → review → fix loop and only runs under `ORCHESTRATOR_ENABLED`.
  The backstop fires on every dispatch, orchestrator or not, at a seam
  orthogonal to the loop — a hand-off decision, not a loop pass. `driver-exec`,
  which every path already invokes, is the right owner.
- **A new standalone host-side or in-box binary just for the backstop.**
  Rejected as needless surface: `driver-exec` already owns process mechanics
  at this exact seam and already carries the `bundle-out` precedent, so a verb
  reuses its dispatch, its build closure, and its test harness rather than
  minting a third binary the image must bake.
- **Forward the knobs but keep the decision in bash.** Rejected as half a
  fix: deleting the hand-copied defaults removes the drift hazard but leaves
  the retry loop and decision tree in the tier that is supposed to be
  branch-free. The knob plumbing and the verb land together.

## Consequences

- `cmd/launcher/internal/outcomebackstop` joins `internal/bundleout` as a
  `driver-exec` verb package, and both `outcomebackstop` and its `internal/retry`
  dependency enter the driver-exec image fileset — a genuine new dependency of
  the baked binary, so the image drvPath legitimately changes (the fileset's
  loud missing-package failure on an out-of-closure import is working as
  intended).
- `emit_outcome_backstop` shrinks from ~90 lines of branching bash to a single
  verb invocation; the push-retry loop, its negative-clamp, and the
  hand-copied schema defaults are gone from the entrypoint.
- `MAX_REBASE_ATTEMPTS`, `TRANSIENT_BACKOFF_SECS`, and `HOLD_JITTER_SECS` now
  ride into the Box as forwarded `boxEnv` vars; a new boxEnv knob shows up in
  the `tests/box_env_gen.bash` fixture and the image's BOX_ENV_VARS list.
- The bats suite drives a fake `driver-exec` (issue #626), so the fake gains an
  `outcome-backstop` verb reproducing the protocol from its flags — the same
  test-double pattern `bundle-out` already established. The production
  decision lives in Go under unit test; the fake is bats scaffolding, not
  production logic, and carries no schema defaults of its own.
- The rule is now citable: a future in-box hand-off feature that needs to
  branch has a named owner (`driver-exec` verb) instead of a default landing
  spot (the entrypoint).

## Amendment (issue #2348): the verb rule generalizes past the host/box hand-off to in-box decision trees over launcher-delivered plumbing

The Decision above scoped the rule to one seam: the entrypoint's own branching
between hand-off strategies — push vs. relay vs. note — with `bundle-out` and
`outcome-backstop` as its two named instances. That framing undersold the
rule's actual shape. Both verbs share a pattern that has nothing intrinsically
to do with the host/box hand-off: each owns an in-box decision tree computed
over plumbing the launcher delivers into the Box (`bundle-out`'s Code Forge
and branch state, `outcome-backstop`'s Dispatch kind, Code Forge, write-enable
signal, and commit count), rather than leaving that decision tree to bash or
to ad hoc logic wherever it happens to land. The hand-off seam was where the
rule was first named, not the boundary of what it governs.

`assemble-prompt` is the third instance of the rule, and it sits at a
different seam entirely: entrypoint.sh's prompt/`--agents` assembly, not the
host/box hand-off. As a `driver-exec assemble-prompt` verb, it owns gate
computation and assembly over the nix-supplied fragment registry — deciding,
for a given run's inputs, which fragment rows render and how they compose
into the final prompt and `--agents` JSON — the same shape of in-box decision
tree `bundle-out` and `outcome-backstop` already established, now applied to
prompt assembly instead of hand-off branching. The `driver-exec` package doc
(`cmd/launcher/driver-exec/main.go`) already states this charter in exactly
those terms: "the fragment registry stays nix-supplied; the verb owns gate
computation and assembly over it." This ADR does not design
`assemble-prompt`'s own internal implementation — gate representation, fragment
lookup, JSON rendering, and so on are that verb's own concern — it only
records that the verb is governed by the rule this ADR names, generalized past
the seam where the rule was first drawn.

A sibling in-box binary just for prompt assembly was considered for
`assemble-prompt` and rejected for the same reason this ADR's Considered
Options section already gives for the outcome-backstop's sibling-binary
option: `driver-exec` already owns process mechanics at this seam and already
carries two verb precedents, so a third verb reuses its dispatch, build
closure, and test harness rather than the image baking a fourth binary to do
the same job.

This is the same argument ADR 0035 made one seam over, moving the box's
review loop out of prose in `issue-prompt.md` and into a Go binary
(`cmd/launcher/orchestrator`) because a state machine, caps, and log-scanning
are the "graph and poll code" tier ADR 0007 found bash — and prose — unfit
for. The amendment states both as one principle: an in-box decision tree over
launcher-delivered plumbing gets a named Go owner — a `driver-exec` verb, or,
where ADR 0035 already applies, the orchestrator itself — rather than living
as ad hoc logic wherever in the entrypoint or its prose it happens to land.

This is an amendment to the rule's scope, not a reversal of the original
decision. The `bundle-out` and `outcome-backstop` reasoning, and the Decision,
Considered Options, and Consequences sections above, stand as originally
written for the host/box hand-off seam; only the boundary of what seams the
rule governs is broadened to cover `assemble-prompt` and, by the same
reasoning, whatever in-box decision tree over launcher-delivered plumbing
comes after it.

## Amendment (issue #2930): bind-registry continues the rule's pattern

`driver-exec bind-registry` (backed by `cmd/launcher/internal/bindregistry`)
is the sixth verb `main.go`'s `mainRun` dispatch chain checks, in the order it
checks them: `bundle-out` (1st), `outcome-backstop` (2nd), `marker-gate`
(3rd), `assemble-prompt` (4th), `readonly-guards` (5th), `bind-registry`
(6th). That dispatch-chain position is a separate fact from this ADR's own
instance count of the rule it names — the amendment above already counts
`assemble-prompt` as the *third* instance despite it sitting 4th in dispatch
order, so dispatch position and rule-instance count are not the same
number. What this amendment records is narrower: `bind-registry` is a
further instance of the rule — an in-box decision tree over
launcher-delivered plumbing gets a named `driver-exec` verb owner, not ad hoc
bash — continuing the pattern `bundle-out`, `outcome-backstop`, and
`assemble-prompt` already established.

As it lands in issue #2930, the verb owns computing the toolchain-nudge
ecosystem classification from the shared ecosystem table
(`cmd/launcher/internal/registryproxy.Ecosystems()`, the same table the
Registry proxy's own path-allowlist already reads), collapsing the
npm-family rows (npm, yarn, pnpm) into one presentation classification, and
emitting that classification into a sourceable env file. `agent/entrypoint.sh`'s
`phase_toolchain_nudge` sources that file instead of re-deriving its own
lockfile if/elif chain — the same file-output idiom `assemble-prompt` already
established for handing decisions back to a parent process that cannot
inherit a child's exports.

Following the `assemble-prompt` amendment's own precedent, this amendment
does not design `bind-registry`'s eventual fuller scope — the apply/revert
Binding mechanisms and Forwarder lifecycle spec issue #2918 describes are
later sibling issues' concern. It only records that the verb, as it exists
after #2930, is governed by this ADR's rule, and that those sibling issues
extending its responsibilities remain governed by the same rule without
needing an amendment of their own.

This is an amendment to the rule's scope, not a reversal of anything decided
above.
