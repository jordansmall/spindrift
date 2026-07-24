# Security Policy

## Reporting a vulnerability

Please report security issues privately through GitHub's **[Report a
vulnerability](https://github.com/jordansmall/spindrift/security/advisories/new)**
(Security → Advisories) rather than opening a public issue. We aim to acknowledge
a report within a few days and will coordinate a fix and disclosure timeline with
you.

Because spindrift dispatches agents holding a repo-write token in response to
GitHub labels, please flag anything that could let an unprivileged party trigger a
dispatch, widen the token's blast radius, or reach the host from inside a Box.

## Supported versions

spindrift is pre-1.0; security fixes land on `main` and in the next tagged
release. Consumers pin the harness by flake input, so **the operator upgrades by
moving that input to the new tag** — run the latest release.

## Threat model

spindrift's job is to run a headless agent with `--dangerously-skip-permissions`
over issue text **anyone can write**, so it treats the issue body and every
comment as untrusted, adversarial prompt input. The design doesn't try to filter
that input — it bounds what the agent can do with it. Two ideas carry the
isolation; the full rationale is in
[`docs/reference.md`](docs/reference.md#threat-model).

- **The Box is the isolation boundary.** Each issue runs in a disposable
  container with a fresh clone, a scoped token, and no host access. That is what
  makes `--dangerously-skip-permissions` safe: the agent can do anything it
  likes, but only inside the box, and only to what the token allows. Prompt
  injection is therefore inherent to the design, not a bug to patch — reading the
  issue *is* the job.
- **The label is the launch button.** Applying the dispatch label (`agent-trigger`
  / `ready-for-agent`) is the authorization step, and GitHub gates it behind the
  triage role. The trust boundary is the label, not the issue or comment author —
  once labeled, the body and **every comment from any GitHub user** feed the agent
  as prompt input. Treat every label-applier as a trusted operator.
- **The Box is hardened against self-inflicted secret reads.** Two always-on,
  Harness-enforced defaults, not operator configuration: `CLAUDE_CODE_SUBPROCESS_ENV_SCRUB=1`
  is baked into the Box so the Driver can't `env`-dump its own model-auth
  credentials via a subprocess, and a `PreToolUse` hook denies any `Read`/`Bash`
  call targeting a known credential path (`~/.claude/.credentials.json`,
  `**/.env`, `~/.config/gh/hosts.yml`), enforced independently of
  `--dangerously-skip-permissions`. See [self-inflicted secret reads are
  structurally blocked](docs/reference.md#self-inflicted-secret-reads-are-structurally-blocked).

Deploying safely rests on a few operator-side prerequisites the harness cannot
enforce for you — `spindrift doctor` preflights connectivity, token validity, and
labels, but these are on you:

- **Branch protection is required, not a nicety.** The token needs Contents RW to
  push its `agent/issue-N` branch, and that same scope permits pushing straight to
  the base branch. Without branch protection **the harness is not safe to
  deploy**: block direct pushes, require CI status checks, and **do not** require
  an external approving review — a bot can't approve its own PR, so that rule
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
- **The launcher owns the merge, the Box never does — by contract, not by
  default enforcement.** A Box implements and pushes; the host launcher makes
  the CI-green decision and applies `MERGE_MODE`. Under the single-token
  default the Agent must not run `gh pr merge`, but nothing stops it from
  doing so — the token that opened the PR is the same token that can merge
  it (see the [merge guard bypass
  discussion](docs/reference.md#merge-guard)). **Two-actor separation** is
  the opt-in hard mode that closes this: a second, launcher-held token whose
  user a repository ruleset bypass-lists, barring the Box's user from
  updating the base branch at all — the only configuration where a Box
  genuinely cannot merge its own PR. See the [two-actor separation
  recipe](docs/reference.md#two-actor-separation-opt-in-hard-mode).
- **The macOS build fallback is pinned by digest.** When there's no Linux builder,
  `spindrift build` builds the image in an ephemeral Nix container with the working
  tree bind-mounted read-write; that container image is pinned by SHA-256 digest,
  not a floating tag, as a supply-chain measure. Keep it pinned if you override it.

## Out of scope

- **Prompt injection into the agent is inherent, not a vulnerability.** The
  mitigation is token scope plus container isolation, not content sanitization.
  Reports that the agent "followed instructions in an issue" describe the design;
  reports that it did so *outside* the Box or *beyond* the token's scope are bugs.
- **Operator secrets and host configuration.** `harness.env` holds live
  credentials on the operator's host; protecting that file, the host, and the PAT
  itself is the operator's responsibility.
- **Misconfiguration the prerequisites above warn against** — an over-scoped
  token, missing branch protection, or a Workflows:RW grant to an untrusted issue.
  The harness documents and (where it can) preflights these; it cannot override an
  operator who opts out.
- **The Driver's model behavior.** What the agent CLI or its model chooses to do
  with a well-formed prompt inside a correctly-scoped Box is not a spindrift
  boundary.
