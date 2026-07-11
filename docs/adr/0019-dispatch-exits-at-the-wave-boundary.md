# Dispatch exits at the wave boundary; the launcher invocation is the image-freshness boundary

The agent image reference is evaluated once, at flake-eval time: `nix run`
bakes `IMAGE_TAG="spindrift:<store-hash>"` (and `IMAGE_DRV`) into the
launcher's environment before the Go binary starts. The launcher never
re-evaluates the flake. Any dispatch path that launches more than one wave
inside a single invocation therefore launches every wave after the first from
an image frozen at dispatch start: a dependent issue clones the right tree but
runs a stale image whenever its blocker's merge changed image inputs (prompt,
toolchain, entrypoint, the generated schema surface). That is issue #477, and
it is the exact inconsistency dependency edges exist to prevent.

Only one path loops waves in-process: `dispatchWaves`, reached when
`MAX_JOBS=0` and the batch has dependency edges or a touch-set overlap. Drain
mode (`MAX_JOBS>0`, #475) already has the safe shape — select the currently
unblocked set, dispatch one wave, exit — and the dogfood loop (#476) already
supplies the freshness half: pull + `spindrift build` between invocations,
where build is a no-op unless the merged diff changed the image hash (#474).

**Decision: every dispatch invocation runs at most one wave.** `dispatchWaves`
is deleted and all queue dispatch unifies on drain semantics; `MAX_JOBS=0` now
means an *uncapped* drain batch rather than "loop waves in-process":

- One selection pass gates each issue on blocker readiness and touch-set
  overlap, cascades failure to issues whose in-batch blocker failed, then
  dispatches the selected set as a single wave and exits.
- Held issues are not claimed; they stay on the dispatch label and are picked
  up by the next invocation, which re-evaluates the flake — freshness is
  automatic, never orchestrated.
- Zero selected with issues remaining is exit 3 ("open issues exist but none
  are dispatchable", #475) in place of the old in-process poll. The timed
  dependency-deadlock detector and its knobs (`DEPS_POLL_SECS`,
  `DEPS_WAIT_SECS`) are removed: waiting was only meaningful while one
  invocation was expected to finish the whole graph. Static cycle detection
  stays — a cycle is still a hard error before any Box launches.
- After the wave, if skipped issues remain, the launcher says so and names
  the re-invocation as the way to continue, so a bare `dispatch` caller is
  never left believing the queue drained.

Selective-list dispatch (`dispatch 12 15 18`, ADR 0011) follows the same
shape, but its hand-picked list bypasses the label gate, so re-discovery
cannot carry the remainder. The list is carried by the operator instead: after
the wave, the launcher prints the remaining numbers and the exact re-run
command (`spindrift dispatch --yes <remaining>`). Selective dispatch is an
interactive operator override — it already prompts for confirmation — so a
printed command is the honest continuation mechanism; a state file would turn
an explicit override into hidden machine state.

## Considered Options

- **Mid-run refresh** — between waves, re-evaluate the image derivation
  against the updated tree, rebuild and reload on change, and keep looping in
  one invocation. Rejected: the image reference is baked at wrap time
  precisely so the launcher realizes pre-evaluated derivations instead of
  evaluating flakes (ADRs 0005/0007); mid-run refresh pulls `git fetch` +
  `nix eval` + reload orchestration into the wave loop and duplicates what
  the driving loop already does between invocations. It also leaves two
  freshness mechanisms to keep honest instead of one.
- **Exit-after-wave inside `dispatchWaves`** — keep the function, return
  after the first dispatched wave. Rejected as a resting point: with the loop
  gone, what remains is `drainMaxJobs` minus the cap. Two near-identical
  selection passes with different failure semantics is how the drain/wave
  cascade divergence of #475 happened in the first place; delete the
  duplicate rather than re-align it forever.
- **Persist the selective remainder in a state file** — survives operator
  absence, but selective dispatch is interactive by design (unlabeled-issue
  confirmation), and a state file silently re-arms an explicit override.
  Rejected in favor of the printed re-run command.

## Consequences

- `MAX_JOBS` semantics tighten: it caps the wave size, and `0` means
  uncapped — the "dependency-wave concurrency" framing disappears along with
  the in-process multi-wave loop. A bare `dispatch` no longer drains a
  dependency graph in one invocation; a driving loop (dogfood.sh, CI, or a
  human re-running) drains it wave by fresh wave, terminating on exit 2
  (queue empty) or 3 (none dispatchable).
- `DEPS_POLL_SECS`/`DEPS_WAIT_SECS` leave the settings surface
  (`settings.concurrency.depsPollSecs`/`depsWaitSecs` included). Under the
  pre-1.0 policy (ADR 0010) this ships as a breaking MINOR; a consumer who
  set them gets the loud unknown-key eval error naming the valid keys.
- The dependency-deadlock failure mode moves from "error after
  `DEPS_WAIT_SECS`" to "exit 3 on the invocation that finds nothing
  dispatchable" — faster, and the same signal drain callers already handle.
- The glossary "Wave" entry, README exit-code table context, flake-options
  reference, template settings example, and the schema-generated artifacts
  (`flagtable_gen.go`, `harness.env.example`) are regenerated/updated to
  match.
- Continuous-pipe dispatch (#478) builds on the invariant this ADR
  establishes — an image is never reused across a freshness boundary — by
  moving the boundary from the invocation to the slot refill: a long-running
  dispatch mode re-checks freshness before each launch (fetch + eval of the
  image drvPath at the base ref, compared against the baked `IMAGE_DRV`) and
  exits for rebuild only when the hash actually changed. That work is
  investigated on #478 and does not change this decision; it extends it.
