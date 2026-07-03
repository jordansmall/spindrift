# Runtime orchestration logic is a nix-built Go binary; bash stays thin exec glue

ADR 0005 kept the whole runtime in nix-generated bash and explicitly rejected a
compiled binary, to hold the language count at one. Experience corrected that.
The launcher accreted real *logic* — a dependency graph, cycle detection, poll
loops, fan-out accounting — and bash's error model made it fragile: a
blocker-less issue body made `grep` exit non-zero, and `set -euo pipefail`
turned that benign no-match into a silent whole-run abort before any container
launched (the fix in the launcher was a bare `|| true`). Bash is the wrong tool
for code with data structures; that is the lesson 0005 could not have from the
start.

So the boundary is refined into three tiers, and pure-nix-first is *strengthened*,
not relaxed:

1. **nix computes** everything knowable at evaluation time — config, defaults,
   pinned tools, and now the `--agents` JSON, which moves out of hand-built
   string interpolation in the entrypoint into `builtins.toJSON`. This tier is
   maximized, exactly as 0005 intended.
2. **A nix-built Go binary orchestrates live state** — query GitHub, build and
   verify the dependency graph, poll for blocker completion, fan out containers,
   roll up outcomes. This is the part 0005 told us to keep in bash; that was
   wrong for logic-bearing code. `buildGoModule` keeps it hermetic and yields a
   single static binary with no interpreter closure.
3. **The in-container entrypoint stays thin generated bash** — linear exec glue
   (clone, checkout, `envsubst` the prompt, exec the Agent) with no branching
   logic, where bash's footguns do not fire and a container-baked binary would
   only add cross-compilation and closure weight for no robustness gain.

0005's "no third language / no compiled binary" clause is therefore superseded:
Go is admitted, but *scoped* to the orchestration tier. Its tiers 1 and 3 —
nix computes, thin generated bash executes — still hold.

## Considered Options

- **Keep everything in bash (0005 unchanged)** — one language, but the logic
  tier is precisely where every bug has landed; bash's error model is a footgun
  for graph and poll code that no amount of discipline fully removes. Rejected.
- **Port all runtime bash to Go, entrypoint included (zero bash)** — uniform,
  but the entrypoint runs *inside* the container: a Go binary there means
  cross-compiling to the container arch and baking it into every image, more
  closure to replace glue that has no logic. Cost with no brittleness payoff;
  rejected in favour of leaving the entrypoint as thin bash.
- **Edit ADR 0005 in place** — rewrites a decided record; rejected in favour of
  a superseding ADR that preserves the original reasoning and the correction.

## Consequences

Go becomes a build-time dependency for the launcher, pinned through
`buildGoModule` so the runtime stays hermetic and ships as one static binary
rather than an interpreter plus script. Test coverage follows the code: the
launcher's bats tests give way to Go tests for the orchestration logic, while
the entrypoint's bash and its bats coverage stay — 0005's "bats stays bash for
runtime behavior" still applies to that tier. Generating `--agents` in nix
removes the JSON quoting hazard from the entrypoint. The language count rises
from one runtime language to two (nix + Go), deliberately, because the
alternative was one language used past the point it was the right tool.
