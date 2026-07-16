# The Target devShell is the default toolchain source; baking is an opt-in speed knob

The agent's project toolchain comes from the Target repo's own devShell,
consumed at runtime. When a cloned Target has `flake.nix` with a usable
`devShells.<name>` (default `default`), the entrypoint launches the whole
post-clone lifecycle — prefetch, the Driver, and the checks the agent runs —
inside `nix develop`, so the agent operates in the Target's exact pinned
environment (tools *and* shellHook/env vars), not just a set of binaries on
PATH. This needs zero spindrift configuration: import the harness and it works
from your devShell.

The baked `packages` list stops being the primary toolchain path and becomes an
opt-in optimization plus a fallback:

- **Fallback** — when no devShell is found (no `flake.nix`, no matching shell,
  probe non-zero, or the probe times out), the box degrades to the baked
  toolchain.
- **Speed opt-in** — a Consumer who wants a *warm* toolchain bakes it via
  `packages`, sharing the same `p: [ … ]` function with their `devShells.default`.
  A baked-and-registered path is already in the box store, so the runtime
  `nix develop` finds it present and substitutes nothing.

## Why not auto-bake the devShell closure

The obvious "single source of truth" — have spindrift derive the bake from
`config.devShells.default` so the toolchain is declared once — does not work,
because of cross-compilation. `mkHarness` maps darwin→linux and re-instantiates
nixpkgs for the image, then applies `packages` as `packages pkgs` against the
*Linux* pkgs (ADR 0002). That is why `packages` is a *function* of pkgs: only a
function can be re-instantiated for the image's system. A built
`devShells.default` is already bound to the *host* system — on a Mac its closure
is `aarch64-darwin` and cannot be copied into an `aarch64-linux` image. Runtime
`nix develop` avoids the problem entirely because it runs *inside* the Linux box.
So baking inherently needs a function-shaped declaration; the devShell path needs
none. The redundancy a Consumer sees when they *do* bake (one `p: [ … ]` shared
between `devShells.default` and `spindrift.packages`) is the minimal necessary
coupling under cross-compilation, not accidental duplication — and it only exists
on the opt-in speed path.

## Ephemerality and the "warm cache" boundary

The box is disposable (`--rm`, no shared `/nix` volume — a persistent store was
rejected to keep every run pristine). So there are exactly two caches, with
different lifetimes:

- **Nix toolchain closure** (rustc, node, …) — persists across runs *only* if
  baked into the image (build time, immutable, ephemeral-safe). Not baked ⇒
  substituted cold from a binary cache on every run.
- **Project dependencies** (crates, npm modules) — warmed by `prefetch`, which
  runs *inside* the box and therefore only helps *within* a single run. prefetch
  is not a substitute for baking the toolchain; they are different caches.

When a box runs cold (no baked toolchain, no prefetch) and a recognized lockfile
is present, the entrypoint logs a one-time nudge naming the ecosystem and the
canonical `prefetch`/`packages` config to warm future runs — friction reduction
for the unknowing, ignorable by everyone else.

## Knobs

- `DEV_SHELL_NAME` (default `default`) — which devShell to enter; lets a Target
  expose a lean headless `ci` shell distinct from a heavy interactive `default`.
- `DEV_SHELL_PROBE_TIMEOUT` (default 300s) — abandons a slow devShell eval and
  falls back to baked.
- Probe is the gate, decided once at startup; if `nix develop` fails to *exec
  the Driver at all*, the entrypoint relaunches once in the baked env, but once
  the agent has started doing work there is no mid-run fallback.

`nixInBox = true` is a prerequisite for this whole path — the devShell can only
be entered inside the box if `nix` is present there (ADR 0008).

## Harness tools stay reachable inside the devShell

The agent's own subprocess calls (`git`, `gh`, `jq`, …) still need to work once
`nix develop` has rewritten PATH to the Target's devShell — the devShell-first
intent above is about the Target's *toolchain*, not about the harness's own
plumbing. `driver-exec`'s `buildCmd` (issue #626) only re-resolves the Driver
binary itself before entering the devShell; it does not re-export or prepend
any other harness tool the way the entrypoint's former `_harness_path` dance
did. This works only because of two facts holding together: `lib/image.nix`
bakes `git`/`gh`/`jq`/`driver-exec` into a `buildEnv` linked at the image's
`/bin`, with the container's `PATH` initialized to exactly that; and `nix
develop` (default, non-`--pure`) *prepends* the devShell's own paths onto the
existing PATH rather than replacing it, so `/bin` survives at the tail
regardless of what the Target's devShell adds. A Target `shellHook` that
overwrites PATH outright (`export PATH=foo` instead of `export
PATH=foo:$PATH`) would break this — unusual devShell hygiene, but possible.
`cmd/launcher/driver-exec`'s `integration`-tagged test builds a real minimal
devShell naming neither tool and asserts both stay resolvable, so this
invariant is checked rather than assumed (issue #798).

## Scope

The image is assumed unique per repo (Consumer == Target). One image serving
many heterogeneous Targets is out of scope: it could not bake any single Target's
toolchain, and would pay cold substitution per run.
