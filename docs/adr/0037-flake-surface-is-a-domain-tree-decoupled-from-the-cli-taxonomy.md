# The flake option surface is a domain tree, decoupled from the CLI `--help` taxonomy

ADR 0015 replaced the flat `perSystem.spindrift.defaults.<knob>` surface with a
grouped `perSystem.spindrift.settings.<section>.<knob>` one, where each section
was the camelCased heading of a `group` in `lib/env-schema.nix` — the same
taxonomy `spindrift --help --all` renders (`groupOrder` in
`cmd/launcher/flags.go`). That was the right move away from a flat bag, but it
left the surface organized along the wrong axis and still fighting itself in
three ways:

- **A flat-vs-nested split.** Only the string/int/bool run-defaults flowed
  through `settings`. The structural mkHarness inputs — `driver`, `roster`,
  `packages`, `prompt`, `skills`, `runtime`, `nixInBox`, `extraClosures`, and
  the rest — stayed flat at the top of `spindrift.*`. Two conceptual peers at
  two different depths: `roster` sat at the top while the per-agent model knobs
  it supersedes were buried in `settings.models`.
- **A borrowed taxonomy.** The sections were the CLI `--help` groups verbatim.
  That grouping exists to lay out flag help text; it is not how a consumer
  editing a flake reaches for a knob. Its seams show: `Repository & identity`
  is a grab-bag of three unrelated domains (git identity, code forge, jira
  connection), and jira alone is scattered across four groups.
- **A name that carves nothing.** Everything under `spindrift.*` is a setting.
  The word marked a population (`settings` = run-defaults) without naming what
  actually distinguishes it.

The key realization is that the taxonomy is not merely *inherited* badly by
Nix — it is wrong on **both** surfaces. `--help` would read better cut along
domain lines too. So the fix is not to fork a Nix-only grouping on top of the
CLI one; it is to organize by domain, and to let the two surfaces share one
improved taxonomy over time rather than carry two forever.

## Decision

The consumer flake surface is a **domain tree**: every knob — structural
build-time input and run-default alike — lives under a domain that names *what
it configures*, directly under `perSystem.spindrift.*`, with no `settings`
wrapper. The domains are `agents`, `git`, `issues`, `forge`, `dispatch`, and
`infra`.

Placement is expressed by a new **Nix-only `nixPath`** on each `flakeOption`
knob in `lib/env-schema.nix` (structural options, which are not schema knobs,
are placed by a small hand-written map in `lib/flakeModule.nix`). `nixPath` is
a dotted path — `agents.models.filer`, `git.merge.mode`,
`dispatch.retry.holdJitter` — and the flake-parts shim generates the entire
option tree from it, forwarding each leaf back to its underlying knob when it
calls `mkHarness`.

Critically, **`nixPath` intentionally diverges from the schema key** for the
duration of the migration. The schema key is what generates the env var and the
CLI flag; leaving it untouched means the Nix surface can be regrouped and its
leaves shortened (`filerModel` → `agents.models.filer`) without touching a
single `SPINDRIFT_*` var or `--flag`. The divergence is temporary scaffolding,
not a second permanent taxonomy — it collapses in Pass 2 (below), when the
env/flag identities are renamed to match and `nixPath` becomes derivable from
the schema again.

### The tree

```
agents.models.{default,scout,review,filer,worker,roster}
agents.{driver,prompt,skills}
agents.{format,lint}.enable
git.{baseBranch,branchPrefix}
git.merge.{mode,guardPaths,pollInterval,pollTimeout,preflightStaleBase}
git.user.{name,email}
issues.{tracker,localDir,localReference}
issues.jira.{baseURL,projectKey,email,includeComments,statusMapping}
issues.labels.{dispatch,inProgress,failed,complete}
forge.{repoSlug,backend,remoteURL,accumulationRepoDir,ghTokenRefreshFile,boxAccess}
dispatch.{maxParallel,maxJobs,overlapGate}
dispatch.{continuous,orchestrator}.enable
dispatch.budget.{tokens,usd}
dispatch.retry.{maxFix,maxRebase,transientBackoff,transientMax,holdJitter}
infra.runtime
infra.image.{packages,prefetch,extraClosures}
infra.nix.{inBox,storeWritable}
infra.limits.{memory,pids}
infra.network.{podman,bwrapUnshare}
infra.devShell.{name,probeTimeout}
infra.{nixpkgs,overlays,config}
```

Leaf names are chosen for the domain they now sit in: `codeForge` →
`forge.backend`, `boxForgeAndIssueAccess` → `forge.boxAccess`, and the discovery
`label` → `issues.labels.dispatch`, which resolves the `issues.label` (singular
knob) vs `issues.labels` (plural namespace) collision by folding the
launch-button label in with the lifecycle labels. `preflightStaleBase` moves to
`git.merge.preflightStaleBase` — it is a pre-merge rebase-and-recheck, so it
belongs with the merge knobs rather than in the retry group its schema section
happens to name.

The `enable`-under-a-feature idiom (`services.foo.enable = true`) applies to the
capability toggles: `orchestratorEnabled` → `dispatch.orchestrator.enable`,
`continuousDispatch` → `dispatch.continuous.enable`, and `autoFormat` / `autoLint`
→ `agents.format.enable` / `agents.lint.enable`. The other booleans stay flat —
they are sub-options, not features you turn on: `issues.jira.includeComments`,
`issues.localReference`, `infra.network.bwrapUnshare`.

### Two sequenced passes

The change ships in two passes with different blast radii, so each is small and
independently reviewable.

**Pass 1 — the pure-Nix regroup (this ADR's immediate scope).** Adds `nixPath`,
regenerates the option tree, folds the structural options in, and aliases every
old path. It touches **no Go, no `--help`, no manpage** — the schema `group`
field is untouched, so operators' env vars and flags are provably unchanged.
The Nix surface shows domains while `--help` still shows the old groups: an
intentional, temporary divergence.

**Pass 2 — the operator-facing rename (a later change, its own decision).**
Re-cuts the schema `group` field along the same domain lines — propagating to
`--help`, the manpage, and the hand-maintained `groupOrder` mirror in
`flags.go` — and renames the env vars and CLI flags to match the domain names,
each behind its own compat shim (read the old env var when the new one is
unset; `MarkDeprecated` on the old flag). At that point `nixPath` collapses
back to equal the schema key and the scaffolding is retired.

### Deprecation and versioning

Every old path — both `settings.<section>.<knob>` and the moved flat structural
options (`spindrift.driver` → `agents.driver`, `spindrift.packages` →
`infra.image.packages`, `spindrift.nixpkgs` → `infra.nixpkgs`, …) — gets a
`mkRenamedOptionModule` alias that forwards to the new path and emits an eval
warning. The aliases live until the **1.0** boundary, then become
`mkRemovedOptionModule` hard errors carrying a migration hint. This is longer
runway than ADR 0015 gave (it hard-broke `defaults` → `settings` outright,
justified by there being no external consumers); we choose to establish
graceful migration as the standing norm regardless, so a consumer who appears
before 1.0 has the whole pre-1.0 window to move.

The flake option surface is part of the versioned consumer contract (ADR 0010);
under the pre-1.0 policy this Pass-1 change is a **MINOR** bump.

## Considered Options

- **Keep the `settings.<section>` surface, fix only the section names
  (amend 0015 in place).** Rejected: it addresses the borrowed-taxonomy sin but
  neither the flat-vs-nested split nor the `settings`-names-nothing sin. The
  structural options would still sit at a different depth from the run-defaults.
- **Fork a Nix-only domain taxonomy, leave the CLI groups as they are.** A
  per-knob `nixGroup` field that permanently diverges from `group`. Rejected:
  the CLI grouping is *also* wrong, so this bakes in two taxonomies to maintain
  forever instead of moving both toward one. The `nixPath` divergence here is
  deliberately temporary — scaffolding for the migration window, not a second
  permanent home.
- **Do the regroup and the env/flag renames in one pass.** One migration for
  consumers, one changelog entry. Rejected: a single diff spanning schema keys,
  Go flags, env shims, and Nix aliases is hard to review and to bisect. The
  Nix-only regroup is a complete, coherent win on its own; sequencing lets it
  land and settle before the operator-facing churn.
- **Group everything by binding-time (build-time vs run-time) rather than by
  domain.** Names the real technical distinction ADR 0020 draws. Rejected: a
  consumer reaching for "where do I set the merge mode" thinks in domains, not
  in whether a knob is baked at build time or read at run time — that is an
  implementation detail of spindrift's pipeline, not an organizing axis for its
  config.
- **Flatten everything under `spindrift.*` and lean on the module system's
  typo protection.** Rejected: ~40 knobs as siblings is the bag ADR 0015
  already moved away from; the module system rejects typos but gives a consumer
  no grouping to navigate.

## Consequences

- `lib/env-schema.nix` gains a `nixPath` on each `flakeOption` knob;
  `lib/flakeModule.nix` generates the option tree and the structural-option
  placement map from it, and no longer derives sections from `groupToAttr`.
  Adding a new `flakeOption` knob now requires giving it a `nixPath`.
- The old→new alias table is generated from current schema state — the old
  `settings.<section>` path is still derivable because `group` is untouched in
  Pass 1, and the new path is the knob's `nixPath` — so `mkRenamedOptionModule`
  entries need no hand-maintained snapshot.
- `mkRenamedOptionModule` is a NixOS-module primitive and the flake-parts
  `perSystemOption` context is not a plain NixOS module; wiring the aliases may
  need `mkAliasOptionModule` or a manual `apply`/warn fallback. This is a Pass-1
  implementation risk to resolve against a real eval, not a design open
  question.
- The dogfood block in `flake.nix`, `templates/default/flake.nix`, the
  `renderTemplateSettingsBlock` renderer, the `docs/flake-options.md` reference,
  and the fixtures all move to the new tree in Pass 1. Direct-`mkHarness`
  consumers (fixtures, tests) are unaffected — the shim still forwards a flat
  `defaults` map to `mkHarness`.
- `nix/checks/schema-drift.nix` gains a check that every `flakeOption` knob has
  a `nixPath` and that the paths are exhaustive and disjoint, so a knob can
  never silently fall off the surface or land in two places.
- The Nix surface and `--help` intentionally disagree between Pass 1 and Pass 2.
  Documentation for the window notes the mapping; the disagreement is designed,
  not drift, and the drift checks are scoped so they do not flag it.
- This ADR supersedes the surface shape of ADR 0015 (the `settings.<section>`
  layout); 0015's underlying rationale — group, don't flatten; let the module
  system reject typos; keep `mkHarness`'s internal `defaults` interface
  unchanged — carries forward intact.
