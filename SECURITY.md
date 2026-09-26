# Security policy

## Reporting a vulnerability

Please report security issues privately through GitHub's **[Report a
vulnerability](https://github.com/jordansmall/spindrift/security/advisories/new)**
(Security, then Advisories) rather than opening a public issue. We aim to
acknowledge a report within a few days and will coordinate a fix and disclosure
timeline with you.

Because spindrift dispatches agents holding a repo-write token in response to
GitHub labels, please flag anything that could let an unprivileged party trigger a
dispatch, widen the token's blast radius, or reach the host from inside a Box.

## Supported versions

spindrift is pre-1.0; security fixes land on `main` and in the next tagged
release. Consumers pin the harness by flake input, so **the operator upgrades by
moving that input to the new tag**. Run the latest release.

## Threat model

spindrift runs a headless agent with `--dangerously-skip-permissions` over
issue text **anyone can write**, so it treats the issue body and every comment
as untrusted, adversarial prompt input. The design doesn't try to filter that
input. It limits what the agent can do with it. The first two ideas below
provide that isolation. The third guards against a separate risk: the Driver
leaking its own secrets, not an adversarial issue. The full rationale is in
[`docs/reference.md`](docs/reference.md#threat-model).

- **The Box is the isolation boundary.** Each issue runs in a disposable
  container with a fresh clone, a scoped token, and no host access. That is what
  makes `--dangerously-skip-permissions` safe: the agent can do anything it
  likes, but only inside the box, and only to what the token allows. Prompt
  injection is therefore inherent to the design, not a bug to patch. Reading
  the issue *is* the job.
- **The label is the launch button.** Applying the dispatch label (`agent-trigger`
  / `ready-for-agent`) is the authorization step, and GitHub gates it behind the
  triage role. The trust boundary is the label, not the issue or comment author.
  Once an issue is labeled, the body and **every comment from any GitHub user**
  feed the agent as prompt input. Treat every label-applier as a trusted operator.
- **Secrets can be sourced from a vault instead of left in plaintext, and
  the Box is hardened against self-inflicted reads.** `<SECRET>_CMD` (e.g.
  `GH_TOKEN_CMD="rbw get spindrift-pat"`) is the preferred way to supply
  every secret. The Launcher runs an operator-controlled command at launch and
  holds the value only in its own memory, so `harness.env` should hold fetch
  recipes rather than live credentials. Inside the Box, two always-on,
  Harness-enforced `PreToolUse` hooks add a second layer that does not depend
  on operator configuration. One rewrites every Bash call to `unset`
  `ANTHROPIC_API_KEY` / `CLAUDE_CODE_OAUTH_TOKEN` before it runs, so a
  spawned subprocess never inherits either credential. The other denies any
  `Read`/`Bash` call targeting a known credential path
  (`~/.claude/.credentials.json`, `**/.env`, `~/.config/gh/hosts.yml`). Both
  apply regardless of `--dangerously-skip-permissions`. See
  [Secret exposure model](docs/reference.md#secret-exposure-model).

The secrets bullet above targets a different threat than the first two:
**self-inflicted context contamination**, not adversarial exfiltration. We have
observed the Driver read secret-bearing files into its own context despite
being told not to. Once a secret is in the transcript it is leaked, because
there is no output redaction. These controls stop the Driver from putting a
secret in its own context. They make no claim about a compromised Box
exfiltrating data over the network. The accepted residual is `GH_TOKEN`, which
stays a live environment variable in the Box because the Box is a first-class
GitHub actor; issue #380 (two-actor separation, below) caps the blast radius of
a leaked one rather than removing it. See [Secret exposure
model](docs/reference.md#secret-exposure-model) for the full story,
including the fully-local, zero-GitHub-token posture.

Deploying safely depends on a few operator-side prerequisites the harness
cannot enforce for you. `spindrift doctor` preflights connectivity, token
validity, and labels, but these are on you:

- **Branch protection is required.** The token needs Contents RW to
  push its `agent/issue-N` branch, and that same scope permits pushing straight to
  the base branch. Without branch protection **the harness is not safe to
  deploy**. Block direct pushes, require CI status checks, and **do not** require
  an external approving review. A bot can't approve its own PR, so that rule
  deadlocks self-merge. Branch protection needs a public repo or a paid plan; do
  not point the harness at a private repo on GitHub Free.
- **Use a fine-grained single-repo PAT.** A broadly-scoped or multi-repo token
  gives an injected agent write access to every repo it reaches. Restrict to the
  one Target repo (Issues RW, Contents RW, Pull requests RW, Metadata R). See the
  [token permission table](docs/reference.md#github-token-permissions).
- **Workflows:RW is off by default and is escalated trust.** Agent PR branches
  live in-repo, so `pull_request` events run with repository secrets; with
  Workflows:RW an injected agent can rewrite CI to auto-pass checks or exfiltrate
  those secrets. Grant it only for an issue that edits `.github/workflows/*`.
- **The launcher owns the merge, and the Box never does, by contract rather
  than by default enforcement.** A Box implements and pushes; the host launcher
  makes the CI-green decision and applies `MERGE_MODE`. Under the single-token
  default the Agent must not run `gh pr merge`, but nothing stops it from
  doing so. The token that opened the PR is the same token that can merge
  it (see the [merge guard bypass
  discussion](docs/reference.md#merge-guard)). **Two-actor separation** is
  the opt-in hard mode that closes this. A second, launcher-held token belongs
  to a user that a repository ruleset bypass-lists, and the ruleset bars the
  Box's user from updating the base branch at all. It is the only
  configuration where a Box cannot merge its own PR. See the [two-actor separation
  recipe](docs/reference.md#two-actor-separation-opt-in-hard-mode).
- **Read-only Box is the second route to that same guarantee.** Two-actor
  separation caps the blast radius of a write-capable token with an
  out-of-band ruleset. `BOX_FORGE_AND_ISSUE_ACCESS=read-only` removes the
  write capability itself. The Box's token can clone and read only
  (Contents R, Issues R, Metadata R), so it cannot push, open a PR, or
  comment regardless of what a repository ruleset does or doesn't enforce.
  The launcher performs those writes host-side instead, with its own token,
  the same host-mediated model `CODE_FORGE=local`/`ISSUE_TRACKER=local`
  already use. See [Read-only
  Box](docs/reference.md#read-only-box-box_forge_and_issue_accessread-only).
- **The macOS build fallback is pinned by digest.** When there's no Linux builder,
  `spindrift build` builds the image in an ephemeral Nix container with the working
  tree bind-mounted read-write. That container image is pinned by SHA-256 digest,
  not a floating tag, as a supply-chain measure. Keep it pinned if you override it.

## Out of scope

- **Prompt injection into the agent is inherent, not a vulnerability.** The
  mitigation is token scope plus container isolation, not content sanitization.
  Reports that the agent "followed instructions in an issue" describe the design;
  reports that it did so *outside* the Box or *beyond* the token's scope are bugs.
- **Operator secrets and host configuration.** `harness.env` should hold fetch
  recipes (`<SECRET>_CMD`) rather than live credentials (see
  [Secret exposure model](docs/reference.md#secret-exposure-model)), but the
  plaintext direct-value and `--<secret>-file` forms remain supported.
  Either way, protecting that file, the host, and the credentials it
  references or contains is the operator's responsibility.
- **Misconfiguration the prerequisites above warn against.** This covers an
  over-scoped token, missing branch protection, or a Workflows:RW grant to an
  untrusted issue. The harness documents these and, where it can, preflights
  them. It cannot override an operator who opts out.
- **The Driver's model behavior.** What the agent CLI or its model chooses to do
  with a well-formed prompt inside a correctly-scoped Box is not a spindrift
  boundary.
