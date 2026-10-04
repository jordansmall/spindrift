# The Box's main is a Go program above a generated shim; seam tests follow the code

Status: accepted

## Context

[ADR 0007](0007-runtime-logic-is-a-nix-built-go-binary.md) drew the
runtime into three tiers: nix computes everything knowable at evaluation
time, a nix-built Go binary orchestrates live state, and the in-container
entrypoint stays thin generated bash — "linear exec glue (clone, checkout,
`envsubst` the prompt, exec the Agent) with no branching logic." It
rejected porting the entrypoint on two grounds: the Go binary would have
to be cross-compiled and baked into every image, and there was no logic in
the entrypoint for Go's error model to protect.

Both grounds have since dissolved. The image already bakes two Go binaries
from the same module — `driver-exec` ([ADR 0009](0009-the-agent-cli-is-a-pluggable-driver.md)
and later) and `orchestrator` ([ADR 0035](0035-the-in-box-orchestrator-loop-is-a-go-program-above-driver-exec-not-entrypoint-prose.md)) —
so the closure cost is paid. And the entrypoint is no longer glue: at
the time of writing it is ~1300 lines with eight `phase_*` functions,
~66 branches, and a `main()` that forks on the dispatch kind, on the
devshell probe, and twice on marker-gate outcomes to run nudge loops.
ADR 0035 saw this coming and promised the entrypoint would "shrink back"
once the orchestrator owned the pass loop; the pass loop moved, but the
phases around it kept growing. Twelve `driver-exec` verbs now carry
logic the entrypoint used to hold, called back from bash eleven times,
which is a Go program sequenced by shell.

The test suite followed the shell. [ADR 0005](0005-nix-computes-generated-bash-executes.md)
kept bats for runtime behaviour because "nix cannot exercise runtime
behavior." That suite is now ~11.7k lines across 66 files, but only part
of it tests shell for its own sake. The `run-*` files drive the Go
launcher through a nix-generated wrapper that exists only as a test
fixture (issue #613 took it off the flake outputs); the `prompt` and
parity files assert on nix-rendered bytes with the entrypoint as a
conduit; and the fakes on PATH are ~1.4k lines of untested shell driven
by forty-odd `FAKE_*` variables, duplicating a second shell-fake family
the forge contract tests embed. Those tests never run in the devshell:
the fixtures they need are exported by the check derivation, so outside
nix the suite "fails by design."

## Decision

The entrypoint's tier is corrected one last step, in the direction 0007
and 0035 already moved: **the Box's main is a Go program.** Shell remains
only as a nix-generated shim — the baked `export` preamble followed by
`exec` of that program — and the seam tests follow the code into Go.

Concretely:

- **A new `box` binary owns the sequence `main()` runs today**, from the
  env guards to the outcome backstop. Each binary in the image keeps one
  sentence of purpose: `box` starts the Box, `orchestrator` runs passes,
  `driver-exec` serves a pass. `box` calls the internal packages the
  verbs already wrap (bind-registry, read-only guards, prompt assembly,
  marker-gate, outcome backstop) in-process rather than spawning verbs,
  and execs `orchestrator` for the pass loop exactly as bash does now.
  The verbs stay for the callers that still need a process boundary —
  the orchestrator and the Driver's hooks.

- **Config still crosses the seam as env.** Every Box knob already arrives
  that way, the nix-rendered preambles are already `export` lines, and
  Go already reads the Box env through a schema-generated view. The shim
  is what `lib/image.nix` renders today minus the script body, and
  `writeShellApplication` still shellchecks it. Turning the Box config
  into a document the way the launcher input is (ADR 0020) is a real
  improvement with a clean trigger — wanting `box` as the OCI entrypoint
  itself, with no shim — and is deferred to its own record.

- **The orchestrator-off path is demolished first.** ADR 0035's
  `ORCHESTRATOR_ENABLED` switch has been on in dogfood long enough to be
  the sustained signal its amendment asked for, and its schema doc already
  calls the direct path "legacy, slated for demolition." Porting a dead
  path is work on dead code, so flipping the default and deleting the
  off-path is a prerequisite, not a slice.

- **The port is tail-first at a single moving boundary.** `box` is born
  owning the Driver run, the two nudge loops and the outcome backstop,
  which already cross as files (the handoff document and the prompt). The
  bash `main()` ends with `exec box`. Each later slice moves the next
  phase up the sequence into `box`, deleting its bash, its bats file and
  its parity file in the same change, and adding Go unit tests plus a
  seam test. The phases that touch the working tree — clone, branch
  recovery, prework rebase — go last, on a Go harness that has matured on
  simpler moves.

- **Seam tests are Go integration tests fed by nix-rendered fixtures,
  rendered on the fly.** One derivation lays every rendered fixture the
  suite needs — the Driver and agent-paths preambles, the fragment
  registry, the contract files, the launcher input document — into a
  single store path. Tests read it from one environment variable. Under
  the existing `integration` build tag, an unset variable falls back to
  `nix build --print-out-paths` of that derivation and skips only when
  `nix` is absent, so the same tests run in the check derivation (the
  authority) and in the devshell's ordinary `go test` loop (where every
  port slice is iterated). Nothing rendered is committed: a committed copy
  of bytes nix derives on demand is a second source of truth with a drift
  guard to police it. (The committed `_gen.go` files are the other case —
  Go source that must *compile* against nix-known constants — and the
  prompt goldens are expected *outputs* of a behaviour, whose value is
  the frozen diff a reviewer judges. Neither pattern applies to a test
  input.)

- **Seam tests live beside the package they exercise**, with a small
  shared helper for fixture resolution, the subprocess harness and fakes.
  Process fakes are Go: the test binary re-execs itself under the tool's
  name and dispatches on its basename, reading a typed JSON config the
  test wrote. They are built one tool at a time as the Go seam tests need
  them; the shell fakes stay for the bats files that outlive each slice,
  and die with the last consumer.

- **Bats is retired slice by slice, not wholesale.** The harness change
  lands first and retires only the files whose system under test contains
  no shell: the launcher-side `run-*` and `build` files become a handful
  of binary-level smoke tests proving the rendered input document drives
  the real launcher, with the rest folded into the existing in-process
  launcher tests or deleted as duplicates, and the fixture-only `run` and
  `build` wrappers go with them; the `prompt` file moves to Go against the
  fixtures directory. Parity and `entrypoint-*` files are each slice's to
  delete. Whatever still execs real shell at the end — the hooks, the
  shim — keeps bats, which is exactly the tier 0005 reserved it for.

## Considered options

- **Leave the entrypoint as bash and keep extracting verbs** — the status
  quo trajectory. Each extraction has made the entrypoint a thinner
  sequencer of Go verbs, but the sequencing, the dispatch-kind forks and
  the nudge loops stay in the error model 0007 found unfit for exactly
  that kind of code. Rejected: the remaining shell is now the logic, not
  the glue.

- **Grow the orchestrator upward to own the whole Box** — one in-box
  binary. It already sits between the entrypoint and `driver-exec`. But it
  is a 13k-line pass machine with a single responsibility, and absorbing
  the Box start would hard-wire the orchestrator-on path before the
  off-path is gone. Rejected in favour of a fourth binary from the same
  module, which costs nothing in closure terms and keeps one purpose per
  binary.

- **A `run-box` verb on `driver-exec`** — the verbs are thin over internal
  packages, so a sequencing verb is cheap. But `driver-exec` is the
  per-pass tool the orchestrator and the Driver's hooks call; making it
  also the thing that starts the Box puts the top of the stack inside the
  bottom. Rejected.

- **Write `box` whole behind a feature flag, prove parity, flip, delete** —
  the shape that produced `ORCHESTRATOR_ENABLED` and its long-lived
  off-path. Rejected in favour of a single moving boundary that never has
  two paths alive.

- **Head-first port** — `box` owns env setup, advise-only and clone first,
  then execs the remaining bash. Same mechanics, but it starts with the
  phases that mutate the working tree and are hardest to prove
  byte-identical, on an immature harness. Rejected for tail-first.

- **Keep bats as the seam harness** — correct exactly as long as the
  system under test is shell. The deletion test on the launcher-side
  files finds one fact the in-process Go tests lack ("the rendered
  document drives the real binary"), worth about five tests, not 132.
  Rejected as a destination; kept as the transitional harness for shell
  that still exists.

- **Commit rendered fixtures via `regen` with a drift guard** — the repo's
  mechanism for `_gen.go`. Rejected for test inputs: it duplicates bytes
  nix produces on demand and adds a guard whose only job is to police the
  duplicate.

- **`testscript` / txtar for the seam tests** — readable, but a new vendored
  dependency for syntax sugar over standard-library subprocess tests.
  Rejected; no new Go dependency.

- **Rust, or any third runtime language** — 0007's language-count
  reasoning still holds and Go is already in the image. Rejected without
  further analysis.

- **Config as a document, `box` as the OCI entrypoint** — strictly better
  than a shim, but it changes the Box's config carrier for every knob and
  deserves its own ADR. Deferred, with the trigger named above.

## Consequences

The image bakes a fourth Go binary from the launcher module. The
entrypoint derivation shrinks to the rendered preamble plus one `exec`
line, still shellchecked at build time. `ORCHESTRATOR_ENABLED` is removed
along with the direct `driver-exec` path and its amendment to ADR 0035.

Test coverage follows the code, as 0007 said it should: each ported phase
gains Go unit tests on its package and a Go seam test against the
fixtures directory, and loses its bats and parity files. The bats suite
ends at a few hundred lines covering the hooks and the shim — the size
0005 originally meant. The check graph gains one fixtures derivation and
loses the per-fixture env-var fan-out in the bats check; issue #3488's
question about where the bats shards sit in the image-only check set is
adjacent and should be resolved alongside.

The devshell gains something it never had: seam tests that run under a
plain `go test -tags integration`, with nix's own cache making the
fixture build free after the first run.

Deliberately out of scope, each with the trigger that would reopen it:

- **The five in-Box hooks** (~400 lines): Driver-invoked PreToolUse and
  PostToolUse commands doing string matching, shellchecked and bats-tested.
  Trigger: a hook needs state or logic beyond matching, or a second Driver
  needs the same guard in a form it cannot invoke as a shell command.
- **The generated shim**: goes away only under the deferred
  config-as-document option.
- **`ab-orchestrator.sh`** and its bats: dev measurement tooling. Trigger:
  a measurement is needed after `box` lands; it should then drive `box`
  and be judged on that.
- **CI `run:` blocks and the Forgejo REST helpers**: YAML's medium.
  Trigger: a block grows a loop or a parse.
- **The `runCommand` bodies under `nix/checks`** (~17k lines): the largest
  shell surface in the repo, untouched by any ADR, exercised only at build
  time. Named here as the known remainder; a separate question.
- **Bats files whose system under test still execs real shell.**

## Amendment (issue #4416): box as PID 1 reaps orphans through an init parent and a worker child

Since issue #4292 the entrypoint `exec`s `box`, so under podman `box` is PID 1.
The image carries no init, so every orphaned grandchild reparented to `box` and
was never waited on: 364 `git` zombies hit `pids.max` 512 and Go could not
create a thread.

- **`box` reaps for itself**, rather than an init such as tini or catatonit in
  front of it. `box` stays the `exec` target and the image gains nothing.
- **When `os.Getpid() == 1`, `box` re-runs its own binary as its only child**
  and does nothing else itself: it forwards SIGTERM/SIGINT/SIGHUP/SIGQUIT and
  loops `Wait4(-1)` to reap adopted orphans. Reaping in the process that runs
  `os/exec` children would race their `Wait`s and steal their statuses; the
  worker child owns every exec child `box` runs, so its waits are never raced.
- **The exit code passes through**: the child's code, or 128+signal when it is
  killed. If the child cannot be started, `box` falls back to running in
  process.
- **bwrap is unaffected**: `box` is not PID 1 there (bwrap's own init reaps),
  so the split is a no-op.
- **Termination signals change behaviour**: before this, PID 1 `box` had no
  forwarding, so on SIGTERM the Go runtime's default handling exited 2 and the
  namespace's other processes died with it. Now init forwards
  SIGTERM/SIGINT/SIGHUP/SIGQUIT to the worker and exits with the worker's
  status, 128+signal when the signal kills it (143 rather than 2).
