# The in-box orchestrator loop is a Go program above `driver-exec`, not entrypoint prose

ADR 0007 drew the runtime into three tiers: nix computes everything knowable
at evaluation time, a nix-built Go binary orchestrates live state, and the
in-container entrypoint stays thin generated bash — linear exec glue with no
branching logic. That third tier held one exception in prose: the box's
implementor was a single long-lived Driver session that both *coordinated*
the scout → implement → review → fix → land loop and *accumulated* every
token of context that loop produced, and the loop's own control flow lived as
prose in `issue-prompt.md` ("repeat until no blocking findings remain")
rather than as anything a program owned. #1627 named the cost on a genuinely
context-heavy issue: monotonic context growth toward the window, mid-task
compaction with lost precision, and a termination condition that depends
entirely on the model obeying prose rather than on caps a program enforces.

The fix is ADR 0007's own tier boundary applied one level deeper than 0007
drew it. 0007 kept the *launcher's* orchestration in Go, host-side, and left
the box's entrypoint as bash because entrypoint.sh had no branching logic to
speak of. That premise stopped holding the moment the box needed a real loop
— invoke `driver-exec` N times, track a state machine over
`BLOCK`/`APPROVE`/no-verdict, enforce numeric caps — which is exactly the
"graph and poll code" 0007 already found bash unfit for. So the same move
0007 made for the launcher now applies inside the box too: `cmd/launcher/orchestrator`
is a Go binary that sits between `entrypoint.sh` and `driver-exec`
(issues #1996, #1997, #1998), and entrypoint.sh's role shrinks back to what
0007 always intended for it — assemble the prompt/`--agents` JSON, mount the
session cache, and exec.

Concretely:

- **Per-slice fresh sessions, not one growing conversation.** Every pass
  after the first invokes `driver-exec` with a fresh Driver session (no
  `--resume`, ever); only the very first pass carries the box's own initial
  session pin verbatim. This generalizes a pattern the box already had rather
  than inventing one: the conflict-resolve pass already runs the Driver
  sessionless with a tmp-file prompt (`run_driver_in_env "$_cr_prompt" "" ""`).
  Continuity across passes is carried by the state file, not the transcript,
  so a long issue never runs one 200k+-token session to exhaustion (#1627
  user stories 2, 7, 11).
- **State handoff through a tmp file, not a resumed transcript.** `RunState`
  (`cmd/launcher/orchestrator/runstate.go`) is a compact JSON artifact — done
  slices, remaining slices, the last reviewer verdict, and the scout-brief
  path — written outside the repo the same way `/tmp/brief.md` already
  survives context compaction today. Each pass reads it before running and
  writes it back after; a missing or corrupt file degrades to a cold start
  rather than an error, since a crashed or evicted prior pass must never
  block the next one from running. Writes go through a temp-file-then-rename,
  so a mid-write kill (OOM, SIGKILL, host preemption) can never leave a
  half-written, unparseable state file behind.
- **Termination is code, not a "repeat until" instruction.** Two numeric caps
  the orchestrator itself enforces replace the prose loop's termination
  condition (the verdict wording itself stays prose — see Consequences):
  `max-review-rounds` (default 3, additional passes a `BLOCK` verdict may
  trigger) and `max-slices` (default 5, the total `driver-exec` invocation
  count regardless of verdict); either set to `0` disables that cap. The loop
  terminates the instant a pass's own log carries a terminal
  `SPINDRIFT_OUTCOME` line — unconditionally, ahead of either cap — or as
  soon as a pass's verdict is anything but `BLOCK`.
- **The Driver stays pluggable (ADR 0009).** The orchestrator only ever talks
  to `driver-exec` via the same flag surface entrypoint.sh's direct call
  already used (`--prompt-file`/`--agents-file`/`--session-file`/`--log-path`,
  plus the devshell pair) — no CLI-specific assumptions cross into the
  orchestrator itself. The one narrow exception is verdict/outcome
  extraction: `scanPassLog` renders each pass's raw stream-json log back into
  readable lines via the claude Driver's own `RenderTranscript` strategy,
  because a bare-line scan of stream-json would never match either marker
  (both live inside JSON string fields). That call is hardcoded to
  `driver.New("claude")` today — the one Driver-specific line in an otherwise
  Driver-agnostic loop — noted here as a gap a future opencode Driver will
  need to close, not a violation of ADR 0009.
- **The entrypoint is a swap, not a rewrite.** `ORCHESTRATOR_ENABLED` (default
  off) is the only seam: off, `entrypoint.sh` calls `driver-exec` directly
  exactly as before; on, the identical flag set goes to `orchestrator`
  instead, which forwards it to `driver-exec` for the loop's own passes.
  `entrypoint.sh`'s prompt/`--agents` assembly and session-cache mounting are
  unchanged either way — turning the knob on only changes the loop shape.

## Considered Options

- **Leave the loop as prose in `issue-prompt.md`, keep the implementor one
  long-lived session (status quo).** Rejected for the reason #1627
  documents: no externally-owned termination, context growth toward the
  window on a genuinely long issue, and no seam to inject a precise
  between-iteration instruction.
- **Move the loop into `entrypoint.sh` bash instead of a Go binary.** This is
  the same tier ADR 0007 already ruled on for the launcher — a state machine
  over `BLOCK`/`APPROVE`/no-verdict, numeric caps, and log-scanning is
  exactly the "logic tier" 0007 found bash unfit for. Keeping the loop as
  bash would repeat 0005's original mistake one layer deeper than 0007
  already corrected it. Rejected.
- **One long Driver session with `--resume` across every pass, orchestrated
  externally only for caps/termination.** Solves the caps/termination half
  but not the context-growth half — the fused coordinator-plus-accumulator
  problem #1627 names would persist even with a Go-owned loop wrapped around
  it. Rejected in favor of a fresh session per slice with state carried
  through the handoff file instead of the transcript.
- **An orchestrator that special-cases every Driver's transcript format
  inline.** Rejected for now in favor of routing transcript rendering
  through each Driver's own ADR 0009 strategy (`RenderTranscript`); the
  orchestrator's one hardcoded `driver.New("claude")` call is a known,
  narrow gap rather than the general shape.

## Consequences

- `cmd/launcher/orchestrator` becomes a second in-box Go binary alongside
  `driver-exec`, baked into the image and built by the same hermetic
  `buildGoModule` pipeline ADR 0007 already established — no new
  cross-compilation or interpreter closure, since it runs inside the box, not
  on the host.
- `entrypoint.sh` keeps exactly one branch point (`ORCHESTRATOR_ENABLED`) and
  otherwise stays the thin exec glue ADR 0007 intended; the loop, caps, and
  log-scanning logic that used to have no code owner now live in Go tests
  against a fake Driver, rather than only being observable in a live run.
- `issue-prompt.md`'s REVIEW section no longer needs to carry the
  *termination* half of its "repeat until no blocking findings remain"
  instruction — the caps are enforced by the orchestrator regardless of what
  the model does — though the verdict wording (`VERDICT: APPROVE`/
  `VERDICT: BLOCK`) stays load-bearing, since `scanPassLog` still greps for
  it verbatim.
- A run under `ORCHESTRATOR_ENABLED` produces up to `max-slices` separate
  Driver sessions and log files instead of one; anything downstream that
  assumed a single session per box needs to account for N passes, not just
  N=1.
- The orchestrator's hardcoded `claude` Driver in `scanPassLog` is a known
  gap against ADR 0009's Driver-agnostic goal, left for whichever issue lands
  the opencode Driver rather than solved speculatively here.
- Default is off (`ORCHESTRATOR_ENABLED=false`); every existing dispatch
  keeps its current single-pass `driver-exec` behavior unchanged until an
  operator opts in.

## Amendment (issue #2047): `ORCHESTRATOR_ENABLED` is a master feature-flag switch, not merely a binary swap

"The entrypoint is a swap" (above) undersold what the flag was already
doing. By the time #2019 landed the filer's read-only write-mechanism split,
`ORCHESTRATOR_ENABLED` gated two independent things — which binary receives
the pass, and (in a compound condition with `BOX_WRITE_ENABLED`) whether the
filer relays issue-filing host-side instead of calling `gh issue create`
in-box — each tested ad hoc at its own call site, with no single place
declaring "this segment differs when the orchestrator is on." That shape
would only have gotten worse: #2037 (next) needs to drop the review-loop
prose and the `reviewer` subagent from the on-path prompt entirely once the
orchestrator's own code-owned review pass replaces it, which means the
prompt itself — not just the invoked binary — has to fork on this flag.

`ORCHESTRATOR_ENABLED` is therefore amended to **master feature-flag switch**
semantics: a single canonical `ORCHESTRATOR` gate local, computed once at the
top of `entrypoint.sh`'s prompt assembly, is the one authority every
orchestrator-conditioned prompt/`--agents` fork reads — expressed through the
repo's existing gate/registry-row idiom (the same shape
`FILER_FILE_DIRECT`/`FILER_FILE_RELAY`, `ISSUE_TRACKER_GITHUB`/`_LOCAL`, and
`BOX_ACCESS_READ_WRITE`/`_READ_ONLY` already use) rather than each fork
testing the raw env var independently. Issue #2047 lands this seam
behavior-preserving — the rendered prompt and `--agents` JSON stay
byte-identical to today for every input combination, and the review-loop
prose/`reviewer` subagent are untouched; only the predicate's plumbing
changes. A fork-well-formedness parity check (`orchestrator-fork-well-formed`,
`nix/checks/prompts.nix`, run in `checks-inbox`) now fails pre-merge if a
future orchestrator-gated segment is added with only an on-row or only an
off-row, or with more than one rendering for a given input — the same
drift-guard shape `fragment-gate-parity` already gives the fragment registry.

**Migration stance.** `ORCHESTRATOR_ENABLED` is a migration flag, not a
permanent configuration axis: orchestrator-on is the destination, and the
orchestrator-off path (direct `driver-exec`, the prose review loop) is
legacy, kept alive only until the on-path fully subsumes it. The prompt fork
this amendment introduces is built to be *collapsed*, not curated forever —
when the off-path is deleted, its registry rows and gate come out together,
not maintained in parallel indefinitely.

**Demolition trigger.** The orchestrator-off path is deleted once both hold:
`ORCHESTRATOR_ENABLED` defaults to *on* in production, and the live A/B
harness (`ab-orchestrator.sh`) shows the on-arm performing at least as well
as the off-arm across a sustained default-on run (the exact dispatch-count/
duration threshold for "sustained" is fixed when the flag is actually
flipped to default-on, not speculatively here). The tracking issue for that
deletion is filed only once the condition is observed to hold, not now.
