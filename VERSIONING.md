# Versioning

spindrift uses [Semantic Versioning 2.0.0](https://semver.org/). Releases are
managed by [release-please](https://github.com/googleapis/release-please) in
manifest mode, driven by the [Conventional Commits](https://www.conventionalcommits.org/)
already in use on this repository. The authoritative version is the root entry in
`.release-please-manifest.json`; Nix reads that value to stamp the two
`buildGoModule` derivations in `lib/mkHarness.nix`.

## Public contract

The following surfaces are part of the versioned contract. Breaking changes here
require a version bump per the policy below.

| Surface | What is versioned |
|---|---|
| **CLI verbs** | `spindrift dispatch`, `spindrift build`, `spindrift preview` — verb names, flag names, exit codes |
| **Flake options** | `perSystem.spindrift.*` and all named parameters of `mkHarness` |
| **`env-schema.nix` variable names** | Every `SPINDRIFT_*` environment variable name listed in `lib/env-schema.nix` |
| **Label lifecycle names** | `ready-for-agent`, `agent-trigger`, `agent-in-progress`, `agent-complete`, `agent-failed` |

Everything else is internal and may change without a version bump:
`cmd/launcher/internal/*`, prompt wording, log formatting, image layer layout,
and any unexported Nix helpers.

## Pre-1.0 policy

spindrift is pre-1.0. While the major version is `0`:

- A **MINOR** bump (`0.y` → `0.y+1`) **may break** the public contract.
  `feat!:` and `fix!:` commits (breaking-change footer) trigger a minor bump,
  not a major one (`bump-minor-pre-major: true`).
- A **PATCH** bump (`0.y.z` → `0.y.z+1`) is **fixes only** — no contract
  changes. `fix:` commits trigger a patch bump; `feat:` commits also land as
  patch while pre-major (`bump-patch-for-minor-pre-major: true`).

The first `1.0.0` release freezes the contract under full semver guarantees.

## Cutting a release

Release-please opens a release PR whenever qualifying commits land on `main`.
That PR updates `CHANGELOG.md` and `.release-please-manifest.json`. Merging it
is the human gate — on merge, release-please tags `vX.Y.Z` and creates the
GitHub Release automatically. No manual tag or `gh release create` is needed.

Consumers upgrade by moving their flake input to the new tag:

```
github:jordansmall/spindrift/v0.2.0
```
