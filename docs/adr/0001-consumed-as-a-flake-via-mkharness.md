# Consumed as a flake via `lib.mkHarness` + a flake-parts shim

spindrift is imported by a Consumer flake, not cloned. The real logic lives in a
pure `lib.mkHarness` function that returns `{ image, packages.{build,run}, apps.*
}`; a thin `flakeModules.default` (flake-parts) is layered on top for consumers
who want declarative options. `mkHarness` is the foundation because it works for
*any* flake; the flake-parts module would otherwise force that framework on
every consumer.

**Note (issue #613):** `packages.build`/`packages.run` and the matching
`apps.build`/`apps.run` were removed in v0.5.0 — deprecated since 0.1.1 in
favor of `packages.spindrift`/`apps.default` (ADR 0010).

## Considered Options

- **flake-parts module only** — idiomatic given our own flake, but forces
  flake-parts on consumers; rejected.
- **templates output** — that's copy-and-fork, the opposite of "import"; rejected.

## Consequences

The built image is **target-agnostic**: `REPO_SLUG`, `LABEL`, `BASE_BRANCH`,
etc. stay runtime env, never Nix options, so one image can be pointed at any
Target repo without a rebuild. This keeps the control-plane pattern free while
optimizing docs/defaults for the self-hosting case. Commands ship as packages so
a consumer can drop them into `devShells.default.packages`, not only `nix run`.

Consumer-tunable knobs are exposed as `perSystem.spindrift.settings.<section>.<knob>`
(grouped by the same headings as `spindrift --help --all`); see ADR 0015 for the
grouped-settings rationale and the section-attr-name mapping.
