# Grouped `settings.<section>.<knob>` flake option surface

The flat `perSystem.spindrift.defaults.<knob>` surface is replaced with a
grouped `perSystem.spindrift.settings.<section>.<knob>` surface, where each
section corresponds to a `group` heading in `lib/env-schema.nix` (and in
`groupOrder` in `cmd/launcher/flags.go`).

## Motivation

The flat surface was an ergonomic trap: a consumer who wrote
`perSystem.spindrift.mergeMode = "immediate"` received only
`option ... does not exist` with no signpost toward the correct nesting under
`defaults`. The flat attr name gave no hint that a `defaults` wrapper was
required, and no grouping hint to suggest where in `--help --all` the knob
lived.

## Decision

Knobs are grouped by section — the same headings as `spindrift --help --all`
(`groupOrder` in `cmd/launcher/flags.go`) — under `settings.<sectionAttr>`.
Section attr names are camelCase translations of the headings:

| `--help --all` heading              | `settings` attr      |
| ----------------------------------- | -------------------- |
| Issue discovery                     | `issueDiscovery`     |
| Lifecycle labels                    | `lifecycleLabels`    |
| Branches & merge                    | `branches`           |
| Concurrency & dependency waves      | `concurrency`        |
| Models                              | `models`             |
| Sandbox & resources                 | `sandbox`            |

Sections with no consumer-tunable knobs (`selfHealing`, `repository`,
`promptSkillIteration`) are not exposed — they would be empty submodules.

The mapping is kept as a static `groupToAttr` attrset in `lib/flakeModule.nix`
rather than derived algorithmically so it stays legible and can be audited
against `groupOrder` at a glance.

## Typo protection

The previous flat `defaults` had an explicit unknownDefaultKeys guard in
`lib/mkHarness.nix`. The grouped surface gets an equivalent guarantee from the
NixOS module system itself: any undeclared section or knob key is rejected at
eval time with a clear option-not-found error.

## Internal interface unchanged

`lib/mkHarness.nix` still receives a flat `defaults = { knob = value; }` map.
`lib/flakeModule.nix` flattens `cfg.settings.<section>.<knob>` into that shape
before forwarding, keeping the mkHarness call site and all direct-mkHarness
consumers (fixtures, tests) unchanged.

## Versioning impact

This is a **breaking change** to the `perSystem.spindrift.*` flake option
surface, which is part of the versioned consumer contract (ADR 0010). Under the
pre-1.0 policy a MINOR bump is sufficient; this change warrants one. No
external consumers exist at the time of this ADR, so the migration cost is
confined to in-repo dogfood, fixtures, and the template.

## Considered Options

- **Keep flat surface, add a deprecation notice on typos** — unhelpful; the
  problem is discoverability, not just the error message. Rejected.
- **Generate the mapping algorithmically from the heading string** — brittle;
  "Concurrency & dependency waves" → `concurrency` is not a deterministic
  mechanical transform. A static map is explicit and auditable. Rejected.
- **Expose both `defaults` (deprecated) and `settings`** — there are no
  external consumers; a hard rename is cleaner. Rejected.

## Consequences

- `perSystem.spindrift.defaults` is removed; consumers set
  `perSystem.spindrift.settings.<section>.<knob>` instead.
- `flakeModule.nix` derives the option surface from `lib/env-schema.nix`
  automatically; adding a new `flakeOption = true` knob to the schema
  propagates to the correct section without a manual `flakeModule.nix` edit.
- The section headings and the `groupOrder` in `cmd/launcher/flags.go` are the
  single source of order truth; divergence between them would silently lose
  help-text groups (already guarded by `TestGroupOrder_CoversEverySchemaGroup`).
