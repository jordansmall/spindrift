# spindrift

*A nix-based agent automation harness.*

Run headless [Claude Code](https://claude.com/claude-code) agents in
**disposable, nix-built containers** — one per GitHub issue. A small,
dependency-light harness built from two ideas:

1. **The container is the isolation boundary.** Each issue runs in its own
   throwaway podman container with a fresh clone, a scoped token, and no host
   access. That's what makes `claude --dangerously-skip-permissions` safe: the
   agent can do anything it likes, but only inside the box.
2. **The toolchain is a nix image.** The container is built with
   `dockerTools` from the *same* pinned toolchain you'd use locally, so the
   agent's environment and your dev shell can never drift apart. One source of
   truth, no hand-maintained Dockerfile.

```
bin/run
  └─ gh issue list --label ready-for-agent        (find the work)
     └─ for each issue, up to MAX_PARALLEL at once:
        podman run  spindrift:latest               (disposable box)
          └─ agent/entrypoint.sh
             ├─ git clone <repo>  +  git checkout -b agent/issue-N
             └─ claude -p "<prompts/issue-prompt.md>" --dangerously-skip-permissions
                └─ implement → check → commit → push → open PR (Closes #N)
```

The harness never touches the target repo's working tree on your host — it all
happens through fresh clones inside containers — so it can drive **any** GitHub
repo you point `REPO_SLUG` at.

## Prerequisites

- **nix** with flakes enabled.
- **podman** (Docker works too with minor edits to `bin/build`/`bin/run`).
- A **GitHub token** scoped to the target repo (see [GitHub token](#github-token)).
- **Claude Code auth**: run `claude setup-token` on the host, or an API key.

## Quick start

```sh
cp harness.env.example harness.env   # then fill in REPO_SLUG, GH_TOKEN, auth
bin/build                            # nix build + podman load  (slow first time)
bin/run                              # fan out one container per ready-for-agent issue
```

Per-issue logs land in `logs/issue-<n>.log`.

## Configuration

Everything is env vars (set in `harness.env` or your shell):

| var                       | default                | meaning                                   |
| ------------------------- | ---------------------- | ----------------------------------------- |
| `REPO_SLUG`               | — (required)           | target repo, `owner/repo`                 |
| `GH_TOKEN`                | — (required)           | GitHub token for `gh` inside containers   |
| `CLAUDE_CODE_OAUTH_TOKEN` | — (one auth required)  | from `claude setup-token`                 |
| `ANTHROPIC_API_KEY`       | —                      | alternative to the OAuth token            |
| `LABEL`                   | `ready-for-agent`      | issues to pick up                         |
| `BASE_BRANCH`             | `main`                 | branch to cut from and PR into            |
| `MAX_PARALLEL`            | `3`                    | concurrent containers                     |
| `BRANCH_PREFIX`           | `agent/issue-`         | branch name = prefix + issue number       |
| `IMAGE`                   | `spindrift:latest`     | image tag to run                          |
| `GIT_USER_NAME`           | host `git config` (required)  | commit author name (see below)     |
| `GIT_USER_EMAIL`          | host `git config` (required)  | commit author email (see below)    |

The agent's commits are authored by `GIT_USER_NAME`/`GIT_USER_EMAIL` if set;
otherwise `bin/run` inherits your host's `git config user.name`/`user.email`.
These are **required** — if neither an override nor a host value is found,
`bin/run` exits rather than committing under an arbitrary identity.

## GitHub token

Use a **fine-grained personal access token** with access to **only the target
repository**. That scoping is what bounds `--dangerously-skip-permissions`: even
if an agent misbehaves, the token can touch nothing but that one repo. The same
token is used by `gh` inside each container and by `bin/run` to list issues on
the host.

Repository permissions:

| permission        | level          | why                                          |
| ----------------- | -------------- | -------------------------------------------- |
| Contents          | Read and write | clone the repo + push the branch             |
| Pull requests     | Read and write | open the PR (including drafts)               |
| Issues            | Read and write | read the issue; **write only** for the "if blocked" path that comments on the issue — drop that fallback and Read suffices |
| Metadata          | Read           | mandatory baseline, auto-selected            |
| Workflows         | Read and write | **only if** an issue edits `.github/workflows/*` — omit otherwise |

The agent never merges the PR or closes the issue (a human does; `Closes #N`
closes it on merge), so no admin or merge scope is needed.

## Customizing for your project

The harness core (`flake.nix`, `bin/`, `agent/entrypoint.sh`) is generic. Two
files are the knobs:

- **`toolchain/rust-toolchain.toml`** — the pinned compiler/targets. Edit, or
  delete it (and its reference in `flake.nix`) for a non-Rust stack.
- **`toolchain/packages.nix`** — the build/test tools baked into the image
  (here: trunk, binaryen, sqlx-cli, a C toolchain). Swap in node/go/python
  tooling as needed.

The agent's behaviour lives in **`prompts/issue-prompt.md`** — edit it to match
your repo's workflow (test commands, commit conventions, PR etiquette). If the
target repo ships a `commit` skill or `CLAUDE.md`, the agent picks it up from
the clone automatically.

## Building on macOS

OCI images are Linux-only, so `nix build .#spindrift` produces a *Linux* image
even on a Mac. nix can't realise a Linux derivation on darwin without a Linux
builder. Options:

- **nix-darwin**: enable `nix.linux-builder.enable = true;` (spins up a small
  Linux VM nix uses automatically). Simplest if you're on nix-darwin.
- **Remote builder**: point nix at any Linux box via
  `nix.buildMachines` / `--builders`.
- **Just build on Linux / CI** and `podman load` the result on the Mac.

The flake already exposes `aarch64-linux` and `x86_64-linux`, so on a Linux host
`bin/build` works with no extra setup.

## Design notes

The harness reproduces the part that matters for isolation — *containerize the
runner, fan out one box per issue* — and leans on nix for the toolchain instead
of a Dockerfile. The trade-offs:

- **Simpler & fewer deps**: bash + nix + podman + Claude Code. No orchestration
  library, no Node runtime to import.
- **No cross-issue dependency unblocking within a run.** Each container is
  independent and opens its own PR; ordering is left to humans (or a future
  planner phase). Good when issues are largely independent.
- **Reproducible toolchain by construction** via the pinned flake, rather than
  a floating `rustup`/`binstall` image.

## Credits

Heavily inspired by Matt Pocock's
[Sandcastle](https://github.com/mattpocock/sandcastle) project.

## Unattended runs

`bin/run` is just a command, so wrap it however you schedule things — `cron`,
`launchd`, a systemd timer, or a CI job on a Linux runner (where `bin/build`
needs no Linux-builder dance). Each run is idempotent per issue: re-running
re-clones and updates the same `agent/issue-N` branch / PR.
