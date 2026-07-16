# Quickstart is a pre-CLI nix app, not a spindrift subcommand

Getting from zero to a dispatching Consumer today is a manual dance: `nix flake
init -t`, hand-edit `flake.nix` for `repoSlug`/`runtime`/forge, `cp
harness.env.example harness.env` and fill in secrets, then `nix develop → build
→ dispatch`. We want a one-command interactive wizard that detects what it can
(host git identity, an available container runtime, an ambient token) and asks
only for the irreducible rest, targeting under a minute of questions before the
build.

The instinct is a `spindrift quickstart` subcommand, and it is wrong. The
fields the wizard most needs to set — `runtime` (podman/docker/bwrap), `driver`,
the forge/tracker seams — are `flake.nix` options resolved at nix-eval/image-
build time, not env knobs the launcher reads. And the `spindrift` binary only
exists *after* `nix develop` inside a Consumer flake, by which point `runtime`
and `driver` are already baked into the very wrapper that produced it. A
subcommand would sit downstream of exactly the files it is meant to author.

**Decision: Quickstart is a pre-CLI nix app (`nix run
github:jordansmall/spindrift#quickstart`, `apps.quickstart`), a Go program under
the launcher module so it reuses `internal/forge` for the token probe and label
creation.** It runs before the binary exists and writes the scaffold: a
minimal, generated-from-scratch `flake.nix` (only the chosen options, a comment
linking `docs/flake-options.md` for the rest), a secrets-only `harness.env`, a
`.gitignore` that protects it, and `.envrc`. No `prompts/` directory — the
harness defaults every prompt (`lib/mkHarness.nix`), so a working Consumer needs
none. It refuses to clobber an existing `flake.nix`/`harness.env` unless
`--force` (backing each up to `*.bak`), and is interactive-only: a non-TTY exits
with a "write the files directly for scripted setup" message.

The wizard prompts the Issue Tracker (github/jira/local) and lets the Code Forge
follow as github; `driver` is fixed at `claude` (the only one). Detected values
(runtime by `podman → docker → bwrap` precedence, git identity from host `git
config`, repoSlug from `git remote`) appear as inline pre-filled defaults. Git
identity is baked into `settings.repository` rather than left to the runtime
host-config fallback, so the committed flake reproduces in CI where no host git
config exists. It finishes by running `spindrift doctor` (probe the forge,
create the four triage labels) and then `spindrift build` — warning that the
first image realization is slow — leaving `spindrift dispatch` as the only
remaining step.

## Considered Options

- **A `spindrift quickstart` subcommand.** Rejected: the binary is built by the
  flake it would author and doesn't exist until `nix develop`; `runtime`/`driver`
  are already baked by then, so it could not set them without a rebuild. It
  could only fill `harness.env` and text-patch `flake.nix` after the manual
  scaffold — the smallest slice of the problem.
- **Regenerate/patch the bundled template in place.** Rejected: the wizard emits
  a minimal flake carrying only the user's answers, with a doc link for the
  rest, rather than the template's fully-commented reference block — cleaner
  output, and no second flake-authoring codepath bound to the template's shape.
- **Bash `writeShellApplication`.** Fits the harness's shell tooling, but would
  reimplement the forge probe, label creation, and `X-OAuth-Scopes` parsing that
  already exist in Go, with weaker tests. Rejected for the Go path's direct
  `internal/forge` reuse and unit-testability.
- **A fully non-interactive flag/env mode for CI.** Deferred: v1 is the wizard;
  scripted setup writes the two files directly. A flag-driven mode can follow if
  demand appears.

## Consequences

- Quickstart validates the GitHub token before writing it, and the least-
  privilege audit is asymmetric by token type: classic/OAuth tokens
  (`ghp_`/`gho_`) expose `X-OAuth-Scopes`, so a wider-than-needed grant
  (`repo`/org/admin) is detected and gated behind a second literal `ACCEPT`;
  fine-grained tokens (`github_pat_`) have no introspection endpoint, so the
  wizard only probes that they have *enough*, prints the four permissions they
  should carry, and trusts the creation-time scoping. The `gh auth token`
  fallback is therefore the case the gate most often bites.
- `apps.quickstart` becomes a public entry point advertised in the README's
  Quick start, alongside (not replacing) `nix flake init -t` for users who
  prefer to hand-edit.
- Glossary term **Quickstart** lands in CONTEXT.md with this decision.
