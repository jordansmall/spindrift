# Nix is a first-class default in the box; lean escape hatch via nixInBox = false

Every box ships the nix CLI, a registered store DB, and a single-user
sandbox-off nix.conf by default. A Consumer whose checks are `nix flake check`
can validate inside the disposable container instead of round-tripping CI.

Previously nix was absent from the box entirely — a Consumer had to either
supply it through `packages` (which gets it on PATH but not the store DB or
config, so `nix flake check` would re-substitute the world from scratch) or
wait for CI. An opt-in `nixInBox` knob was prototyped and verified working but
left off by default, keeping the image lean for Consumers who never run nix.
Experience showed the opposite default makes more sense: most Consumers of a
nix-native harness run nix in their checks, and the cost of a cold re-substitute
on every box was paying for the wrong default.

The nix-centric baseline is now:

- **`pkgs.nix` in `harnessPackages`** — the nix CLI is on PATH inside the
  container, built from the same locked nixpkgs pin as everything else.
- **Store DB registered at image-build time** via `nix-store --load-db` from
  the `closureInfo` of the full env+files closure, so `nix` inside the box sees
  the already-present store rather than treating it as empty.
- **`/etc/nix/nix.conf`** baked into the image with
  `experimental-features = nix-command flakes`, `sandbox = false`, and
  `filter-syscalls = false` — no daemon, no nested build sandbox (neither is
  available in an unprivileged throwaway container).

## Lean escape hatch

Consumers who want the smallest possible image pass `nixInBox = false`; this
omits the nix CLI from `harnessPackages`, skips the store-DB registration step,
and skips writing `nix.conf`. The `packages-baked` check continues to cover the
nix-present default. New checks `nix-baked-by-default` and `lean-escape-hatch`
assert both sides of the boundary at eval time (no Linux builder needed).

## Considered Options

- **Always on, no escape hatch** — simplest surface, but forces a larger image
  on Consumers that never run nix inside the box; rejected.
- **Always off, opt-in as before** — keeps the image lean by default, but makes
  the common case (nix Consumer, nix checks) a configuration tax; rejected for
  the default, kept as the escape hatch via `nixInBox = false`.
- **Separate `build-nix` / `run-nix` commands** — the prototype exposed a
  parallel pair of launchers for the nix-enabled variant; this adds surface
  without simplifying the common case and splits the audience unnecessarily;
  superseded by promoting `nixInBox = true` as the default.

## Consequences

The default box is larger than before (adds the nix closure), and the image
build step is heavier (runs `nix-store --load-db` on the full closure). Both
are visible in the nix build log and documented here rather than in ephemeral
PR descriptions. On the other side, `nix flake check` and `nix develop` run
inside the container out of the box, and the prefetch hook can warm flake
inputs (`nix flake archive || true`) so subsequent nix commands in the box
are fast. Consumers who do not need nix use `nixInBox = false` to recover the
lean image; the escape hatch is a one-line opt-out, not a rebuild of anything
else.
