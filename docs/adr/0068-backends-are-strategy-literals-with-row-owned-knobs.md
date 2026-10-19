# Backends are Strategy literals with row-owned knobs

Status: proposed

## Context

The Code Forge and Issue Tracker seam already has two parts of the kit ADR
0066 gave the Drivers: a generated Descriptor rendered from
`lib/backends/default.nix` (issue #2521), and a conformance suite in
`forge/forgetest` that every adapter and the fake run. Adding a backend
should therefore be a row and a package. In practice it is about twenty
files.

The cause is where the Go half lives. The per-backend closures (`validate`,
`newIssueTracker`, `newCodeForge`, `newLedger`, `boxTokenEnvVar`) live in
`backendRows`, in the launcher's `package main`. The daemon, quickstart and
the Box are separate binaries that cannot import it. Each therefore re-derives
backend facts by switching on the name: 23 sites in seven core Go files, and
5 more in `mkHarness.nix`. `trackerbuild` (issue #4592) was a first patch over
the same gap for the daemon. The switches fall into four groups:

- **Remote identity.** The clone URL, the web URL, and whether a checkout is
  the Target repo. `boxclone.CloneURL`'s `default:` arm clones from
  github.com, so an unlisted forge misroutes silently.
- **Credential.** Which knob holds the token, and its `_CMD` form.
- **Construction.** Quickstart builds the adapters a second time.
- **Wording.** The landed banner and the auto-merge remedy.

Four other per-backend facts follow the same pattern:

- **Knobs.** Backend knobs are hand-written in `env-schema.nix` and copied
  field by field into `trackerbuild.Settings` by each binary.
- **Prompt steps.** A prompt step that differs by backend is a fragment file,
  a gate row and a `${…_<BACKEND>_STEP}` variable per backend. Every top-level
  prompt concatenates those variables (35 references across 6 prompts), so a
  Consumer's override prompt never shows a new backend's step.
- **Read-only token gates.** These are hand-written once per backend.
- **Tests.** Every backend's tests rerun in the one core Go test derivation.

## Decision

A backend's Go half is a **Backend Strategy literal** exported by its own
package, and its Nix row owns its knobs.

1. **Strategy plus Bundle.** Each backend package exports one Strategy. It
   holds:
   - the backend's Descriptor;
   - its build and validate closures;
   - remote identity: clone URL, web URL and remote match;
   - in-box CLI setup (`ConfigureCLI`) and the in-box open-PR query;
   - optional hooks: `IntrospectToken`, `AcquireToken` (quickstart token
     setup, taking a `TokenPrompt`), `KnobsFromRemote` and the auto-merge
     remedy. A nil hook is an absence, not a stub.

   The Bundle registers the Strategies, and the launcher, daemon, quickstart
   and Box look a backend up through it. `backendRows` and `trackerbuild` are
   deleted.
2. **Row-owned knobs, for every registry.** A registry row may carry
   `knobs`: entries in the env-schema shape. `env-schema.nix` folds in the
   knobs from every registry's rows (backends now, runners and Drivers as
   they adopt it), and two rows declaring the same env name fail at eval
   time. Consumer `nixSubPath`s do not change. `lib/backends/default.nix`
   splits into one file per backend.
3. **A typed reader per package.** Regen writes a `Knobs` struct and a
   `ReadKnobs(lookup)` into each adapter package, applying the row's
   defaults and kinds. Strategy closures take the cross-backend `Shared`
   values and a `Lookup`, and each binary supplies its own `Lookup`. The
   launcher `config` drops its backend-specific fields.
4. **One prompt slot per step.** A top-level prompt carries one
   `${ISSUE_READ_STEP}`. The active backend fills it from its own fragment
   folder, and a missing file means an empty step. Fragments that differ
   only in the forge's display name collapse into one, with the name taken
   from the row. For one deprecation window, assembly also fills the old
   `*_<BACKEND>_STEP` variables for their own backend and warns on an
   override that uses them.
5. **The in-box half.** The CLI package a backend needs is declared on its
   row, and `image.nix` bakes the union for the selected tracker and forge.
   CLI setup is a Strategy function, not row data, because forgejo's is
   version-coupled argv and URL normalisation.
6. **One read-only token gate.** The row names `TokenEnvVar` and
   `BoxTokenEnvVar`. One gate runs over every distinct active backend with a
   Box token. It warns when the Strategy has no `IntrospectToken`, so a new
   backend gets a gate by default rather than none.
7. **Per-adapter test derivations, for every registry.**
   `driverGoTestsFor` generalises to one helper mapped over any registry's
   names. It lands in two stages, as ADR 0066 did:
   - first additive, so each backend has its own derivation;
   - then inverted, so core tests use the forge fake and only the Bundle
     imports adapters.

   `local` stays a core dependency, because the local loop is built on it,
   and keeps its own derivation as well.
8. **Quickstart reads the rows.** A row's optional `quickstart` block names
   the remote hosts that identify the backend and which axes it sets. A row
   without the block is hand-edit only, which is what jira and local are
   today.

## Considered Options

- **Keep the Go half in `main` and grow only the Nix Descriptor.** Rejected.
  Remote identity is logic (forgejo's clone URL embeds its token), so the
  other binaries would keep their switches.
- **Remote identity as a capability interface on a constructed adapter.**
  Rejected. Quickstart, the demand probe and the Box need the URL before, or
  without, an adapter.
- **An untyped knob lookup inside Strategies.** Rejected. It saves a
  generator but loses compile-time checks, and it leaves default handling to
  each call site.
- **CLI command vocabulary as row data**, to collapse more fragments.
  Rejected. `gh` and `fj` differ in what they print, not just in how they
  are spelled.
- **Row-owned knobs for backends only.** Rejected. Runners and Drivers have
  knobs that belong to a single adapter, and doing it once avoids a second
  fold.

## Consequences

- A new backend is a row file, a package with its Strategy, a fragment
  folder and its corpus. Adding it does not touch core files.
- The campaign ships incrementally, with no release gate. Each change must
  leave `main` releasable: the Consumer surface intact and deprecations
  announced in the change itself.
- Removing the old `*_<BACKEND>_STEP` variables after the window is a
  versioned break (VERSIONING.md lists template variable names).
- The runner and Driver registries inherit row-owned knobs and the
  per-adapter derivation helper from this decision.
