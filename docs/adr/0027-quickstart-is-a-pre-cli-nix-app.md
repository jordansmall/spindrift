# Quickstart is a pre-CLI nix app, not a spindrift subcommand

Getting from zero to a dispatching Consumer today is a manual dance: `nix flake
init -t`, hand-edit `flake.nix` for `repoSlug`/`runtime`/forge, `cp
harness.env.example harness.env` and fill in secrets, then `nix develop → build
→ dispatch`. We want a one-command interactive wizard that detects what it can
(host git identity, an available container runtime, an ambient token) and asks
only for the irreducible rest, targeting under a minute of questions before the
build.

The instinct is a `spindrift quickstart` subcommand, and it is wrong. The
fields the wizard most needs to set — `runtime` (podman/docker/rancher/bwrap), `driver`,
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
`--force` (backing each up first, under a unique name per backup file so
repeated forced runs never clobber an earlier backup — issue #2563), and is
interactive-only: a non-TTY exits with a "write the files directly for
scripted setup" message.

The wizard is GitHub-only (issue #1559) (Amended by issue #5044: it now picks
`github` or `forgejo` from the git remote; see the amendment below; amended
again by issue #7416, which prompts for the tracker and the forge separately):
it never prompts for the Issue Tracker, always writing `issueTracker = "github"` and
letting the Code Forge follow as github; `driver` is fixed at `claude` (the
only one). The Jira and local Issue Tracker adapters, and the runtime
`ISSUE_TRACKER` validation, stay available for an operator who hand-edits the
generated flake — the wizard just never offers them. (Amended by issue #7416:
it now offers local, and Jira is still to come. Amended by issue #7417: it
now offers Jira as a tracker.) Detected values
(runtime by `podman → docker → nerdctl(⇒ rancher) → bwrap` precedence, git
identity from host `git config`, repoSlug from `git remote`) appear as inline
pre-filled defaults. nerdctl is probed after docker since Rancher Desktop's
dockerd mode already satisfies the `docker` branch. Git identity is baked
into `git.user.name`/`git.user.email` rather than left to the runtime
host-config fallback, so the committed flake reproduces in CI where no host
git config exists. It finishes by running `spindrift doctor` (probe the forge,
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
  (Amended by issue #7416: that concern is now met differently; see the
  amendment below.)
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
  (any scope beyond `public_repo` — amended by issue #6016) is detected and
  gated behind a second literal `ACCEPT`; fine-grained tokens (`github_pat_`)
  have no introspection endpoint, so the wizard only probes that they have
  *enough*, prints the four permissions they should carry, and trusts the
  creation-time scoping. The
  `gh auth token` fallback is therefore the case the gate most often bites. A
  Forgejo token gets a live Probe against the instance instead: Forgejo tokens
  carry no sniffable prefix, so there is no least-privilege audit on that path.
- `apps.quickstart` becomes a public entry point advertised in the README's
  Quick start, alongside (not replacing) `nix flake init -t` for users who
  prefer to hand-edit.
- Glossary term **Quickstart** lands in CONTEXT.md with this decision.
- `infra.runtime = "rancher"` (issue #1274) is in the detection chain above
  (issue #1275): nerdctl is probed after docker, since Rancher Desktop's
  dockerd mode already satisfies the `docker` branch and only containerd
  mode needs the nerdctl/rancher branch.

## Amendment (issue #5044): the wizard picks github or forgejo from the git remote

The Forgejo on-ramp (issue #1966), part of [ADR
0038](0038-the-forgejo-backend-decision-set.md)'s decision set, ended
"GitHub-only". The wizard now picks the backend from the git remote:
`github` for a github.com remote or no remote, with no prompt (Amended by
issue #7416: the wizard now always asks); `forgejo`,
auto-detected and announced, for a codeberg.org remote; and for any other host
a `Backend` prompt (Amended by issue #7416: now separate tracker and forge prompts) over the quickstart-eligible backends in the shared backend
registry (the rows with a `quickstart` block, today `github` and `forgejo`),
defaulting to `github`. The answer is written as `issues.tracker`.

A `forgejo` choice sets both the Issue Tracker and the Code Forge to
`forgejo`, so the Code Forge still follows the tracker (Amended by issue
#7416: the forge is now its own prompt). The generated flake
carries a Forgejo base URL only for a non-Codeberg instance, since Codeberg is
the default. Unchanged: `driver` stays fixed at `claude`. (Amended by issue #7416: local is
now offered, and Jira is still available by hand-editing the generated flake.
Amended by issue #7417: Jira is offered as a tracker.)

## Amendment (issue #7416): the wizard picks the tracker and the forge separately, composed from Consumer stanzas

The wizard now asks for the Issue Tracker and the Code Forge as two prompts,
since the two are independent axes. A remote whose host a backend row's
`quickstart` block lists (github.com, codeberg.org) pre-selects both and is
announced, and the operator can override either. With no remote, or one no row
claims, each axis defaults to its first offered row, `github`.

A row is offered on an axis when it is valid for that axis and the wizard can
collect its credential, meaning the row reads no token or its Strategy has an
`AcquireToken` hook. So `local` is offered on both axes, `git` as a forge only,
and `jira` as a tracker only. (Amended by issue #7417: jira was offered on
neither until then.) The `quickstart`
block now only opts a row into remote-host detection. A `git` forge takes its
`CODE_FORGE_REMOTE_URL` from the checkout's remote.

The generated `flake.nix` and `harness.env` are composed from the chosen
tracker's and forge's [Consumer stanza](../../CONTEXT.md) (`consumerStanza`
on the row), through a generated table under `cmd/launcher/quickstart/`.
A row chosen only as the tracker leaves out its `forge.*` settings.

The checkout's remote belongs to the forge. So a tracker takes knobs from it
only when the tracker is the forge's backend or is the row that claimed the
remote's host. Any other tracker that derives knobs from a remote, such as a
forgejo tracker beside a github forge, gets a prompt for a repo URL on its
own instance. A blank answer leaves it on the default instance.

That also settles the rejected "Regenerate/patch the bundled template in
place" option. Its objection was a second flake-authoring codepath bound to
the template's shape. Now the template's composed pieces (the env example and
the settings block) and quickstart's output are both rendered from one
row-owned source, the Consumer stanzas, so neither hand-copies the other's
shape. The wizard still writes a minimal flake rather than patching the
template.

## Amendment (issue #7417): jira is offered as a tracker

The wizard now offers `jira` as a tracker, never as a forge. Its `AcquireToken`
hook prompts for the Connector API token, which lands in both `JIRA_TOKEN` and
`SPINDRIFT_JIRA_CONNECTOR_TOKEN`; the Connector email stays blank.

The batteries flake carries only the github and forgejo Connectors. A Consumer
stanza whose backend it does not carry names a flake input and a module
import, which the wizard splices into the generated `flake.nix`. The input
points at the Connector's subflake with `core` following `spindrift`, so the
lock graph holds one core. The Connector module carries a dedup `key`, so a
second import of it dedups instead of failing.

## Amendment (2026-11-24, issue #7501): jira's credentials are core knobs

The jira `AcquireToken` hook's token now lands in `JIRA_TOKEN` alone, and the
wizard writes `JIRA_EMAIL` beside it (blank until answered). The
`SPINDRIFT_JIRA_CONNECTOR_TOKEN` and `SPINDRIFT_JIRA_CONNECTOR_EMAIL` knobs are
removed, so the wizard no longer writes them.
