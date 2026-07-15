# spindrift

[![CI](https://github.com/jordansmall/spindrift/actions/workflows/ci.yml/badge.svg)](https://github.com/jordansmall/spindrift/actions/workflows/ci.yml)
[![Release](https://img.shields.io/github/v/release/jordansmall/spindrift)](https://github.com/jordansmall/spindrift/releases/latest)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](LICENSE)

*A nix-based agent automation harness, consumed as a flake.*

Run headless [Claude Code](https://claude.com/claude-code) agents in
**disposable, nix-built containers** — one per GitHub issue. spindrift is
**imported by your flake**, not cloned. Two ideas carry it (see
[`CONTEXT.md`](CONTEXT.md) for the full vocabulary):

1. **The container is the isolation boundary.** Each issue runs in its own
   throwaway container with a fresh clone, a scoped token, and no host access.
   That is what makes `claude --dangerously-skip-permissions` safe: the agent
   can do anything it likes, but only inside the box.
2. **The toolchain is a nix image.** The image is built with `dockerTools` from
   the *same* pinned nixpkgs your dev shell uses, so the agent's environment and
   yours can never drift. One source of truth, no hand-maintained Dockerfile.

## Prerequisites

- **nix** with flakes enabled.
- **podman** (or set `runtime = "docker"`; or `runtime = "bwrap"` for the
  daemonless bubblewrap sandbox on Linux, which needs no container runtime).
  On macOS/Windows, podman runs containers inside a VM ("podman machine")
  with its own fixed RAM — give that machine at least as much memory as
  `MEMORY_LIMIT` (default `4g`), e.g. `podman machine set --memory 4096`. A
  smaller machine lets the VM's own Linux OOM-killer fire before the
  per-container `--memory` cgroup cap ever bites, silently killing whatever
  is running (`dogfood.sh` checks for this mismatch and aborts before
  dispatching — see [Dogfood loop](#dogfood-loop)).
- A **fine-grained single-repo GitHub PAT** — scoped to the Target repo only
  (see [Before you deploy](#before-you-deploy)).
- **Claude Code auth**: run `claude setup-token` on the host, or an API key.

## Quick start

Scaffold a Consumer flake from the bundled template:

```sh
mkdir my-agents && cd my-agents
nix flake init -t github:jordansmall/spindrift
```

That drops a ready-to-edit starter: a `flake.nix` importing the harness, a
`prompts/` directory, a `harness.env.example`, and a `.gitignore` for
`harness.env`. Then:

```sh
$EDITOR flake.nix                        # tune the toolchain/packages for your stack
$EDITOR prompts/issue-prompt.md          # tune the agent's workflow
cp harness.env.example harness.env       # fill in REPO_SLUG, GH_TOKEN, Claude auth
nix develop                              # enter the dev shell — puts spindrift on PATH
spindrift build                          # realize the image, then load it  (slow first time)
spindrift dispatch                       # launch one container per ready-for-agent issue
spindrift research                       # advise-only: launch one container per agent-research issue
```

Run commands **from your Consumer flake's directory**: `spindrift build` reads the
flake from `$PWD` for its container fallback, and `spindrift dispatch` reads `harness.env`
from `$PWD` (the same convention). Per-issue logs land in `logs/issue-<n>.log`.

`spindrift` ships bash tab-completion, generated from the same schema as
`--help` and the man page: subcommands (`dispatch`, `research`, `preview`,
`build`, `recover`, `doctor`) complete as the first word, every flag (including the
`--issue` alias and the secret `--*-file` flags) completes anywhere after
it, and a `--*-file` flag's argument completes as a filesystem path. `nix
develop` puts the completion script on `share/bash-completion/completions`
under `spindrift`'s store path; source it directly to enable it in your
shell:

```sh
source "$(dirname "$(command -v spindrift)")/../share/bash-completion/completions/spindrift"
```

`spindrift` also ships fish tab-completion, with the same coverage as the
bash slice plus a one-line description on every flag. `nix develop` puts the
completion script on `share/fish/vendor_completions.d` under `spindrift`'s
store path; fish's `vendor_completions.d` convention loads it automatically
once that directory is on `$fish_complete_path`, or copy/symlink it into
`~/.config/fish/completions/spindrift.fish`.

It also ships zsh tab-completion with the same coverage, plus a per-flag
description drawn from the same `doc` string, so `spindrift --<TAB>` shows
each flag's one-line purpose alongside its name. `nix develop` puts the
completion function on `share/zsh/site-functions/_spindrift` under
`spindrift`'s store path; add that directory to `fpath` before `compinit`
runs to enable it:

```sh
fpath=("$(dirname "$(command -v spindrift)")/../share/zsh/site-functions" $fpath)
autoload -Uz compinit && compinit
```

## Adding spindrift to your flake

If you prefer to wire it by hand rather than `nix flake init`, add spindrift to
your inputs and import the flake-parts module:

```nix
{
  inputs = {
    nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";
    flake-parts.url = "github:hercules-ci/flake-parts";
    spindrift.url = "github:jordansmall/spindrift";
  };

  outputs = inputs@{ flake-parts, spindrift, ... }:
    flake-parts.lib.mkFlake { inherit inputs; } {
      systems = [ "aarch64-darwin" "aarch64-linux" "x86_64-linux" ];
      imports = [ spindrift.flakeModules.default ];
      perSystem = { config, pkgs, ... }: {
        spindrift = {
          # The Target repo's devShell is used by default — no packages
          # needed for a pure devShell setup. Add packages here only as
          # a speed optimization to pre-bake toolchain closures into the
          # image (see docs/reference.md for the full rationale).
          packages = p: [ p.go p.gnumake ];
          prompt = builtins.readFile ./prompts/issue-prompt.md;
        };

        # Put the spindrift CLI on PATH: `nix develop` → `spindrift dispatch`.
        devShells.default = pkgs.mkShell {
          packages = [ config.packages.spindrift ];
        };
      };
    };
}
```

This yields the **`spindrift` CLI** as `packages.<system>.spindrift` and as
`apps.<system>.default`, plus the Linux-only `agent-image`. The bare form
(`nix run .`) prints help and exits; drain the queue with
`nix run . -- dispatch`. See [`docs/reference.md`](docs/reference.md) for the
`mkHarness`-direct variant and the devShell-targeting pattern (one image,
many differently-toolchained Target repos).

**Lean CI shell.** If the Target repo exposes a `devShells.ci` with only the
tools the agent needs (no LSP, no GUI tooling), set `DEV_SHELL_NAME=ci` — or
bake it as the Consumer default:

```nix
spindrift.settings.sandbox.devShellName = "ci";
```

The Box enters that shell instead of `devShells.default`, giving the agent a
smaller closure and a faster probe. See
[devShell targeting](docs/reference.md#how-a-run-works) for the probe flow
and baked-toolchain fallback.

## Before you deploy

Three non-negotiables before pointing the harness at a live repo:

1. **Branch protection is required.** The token has Contents RW to push
   `agent/issue-N` branches — that same scope allows pushing directly to the base
   branch. Without branch protection **the harness is not safe to deploy**. Block
   direct pushes; require CI status checks; do not require an external approving
   review (a bot cannot approve its own PR). See the full rationale in
   [Security → Threat model](docs/reference.md#threat-model).
2. **Use a fine-grained single-repo PAT.** A broadly-scoped token gives an
   injected agent write access to every repo it reaches. Restrict to one Target
   repo (Issues RW, Contents RW, Pull requests RW, Metadata R). See the
   [token permission table](docs/reference.md#github-token-permissions).
3. **Issue body and every comment are attacker-writable prompt input.** The
   trust boundary is the label, not the issue or comment author. What bounds
   the blast radius is what the token allows and nothing more.

Run `spindrift doctor` as a preflight: it checks forge connectivity, token
validity, and that all four triage labels exist on the Target repo. When run
interactively (TTY attached) and labels are missing, it offers to create them;
in CI (no TTY) it reports missing labels and exits non-zero.

## Basic flow

```
spindrift dispatch  ─▶  find ready-for-agent issues
                          └─ one container per issue (up to MAX_PARALLEL)
                               clone repo → run claude → commit → push → open PR
                               └─ SPINDRIFT_OUTCOME issue=N landing=<url> status=ready

host launcher  ─▶  merge gate per issue
                    poll CI → green → agent-complete → merge guard → apply MERGE_MODE
                           → red   → fix boxes (up to MAX_FIX_ATTEMPTS) → re-gate
                           → exhausted → agent-failed (human triage, re-label to retry)
```

The Box implements; the launcher owns the CI-green decision and the merge. A Box
cannot approve or merge its own PR — that is what makes branch protection
meaningful. See [How a run works](docs/reference.md#how-a-run-works) for the full
diagram and label lifecycle.

A green PR whose diff touches a guarded path (`.github/**`, `**/CLAUDE.md`,
`**/AGENTS.md`, `.claude/**`, `.opencode/**` by default) is downgraded to
manual regardless of `MERGE_MODE` — see
[Merge guard](docs/reference.md#merge-guard).

Set `FILER_MODEL` to opt in an optional Filer subagent that turns a review's
non-blocking findings into `agent-review-finding`-labelled issues instead of
leaving them in the PR body — never the dispatch label, so a human still
promotes each one before an agent can pick it up. Off (empty) by default. See
[Filer](docs/reference.md#filer).

Set `AUTO_FORMAT=1` (or `settings.promptSkillIteration.autoFormat = true` in
your Consumer flake) to have the implementor auto-format changed files before
each commit. The formatter is detected automatically: a `format`/`fmt` script
in `package.json`, `Makefile`, or `justfile`, otherwise the language's standard
formatter. Never `nix fmt` — evaluating the flake in-box would copy the dirty
work tree into `/nix/store`, which the agent user cannot write to. Runs only
on changed files; skips silently when none is found. Off by default.

Set `AUTO_LINT=1` (or `settings.promptSkillIteration.autoLint = true` in your
Consumer flake) to have the implementor lint changed files before each commit,
applying the linter's auto-fix mode and then manually resolving any remaining
findings. The linter is detected automatically: a `lint` target in
`package.json`, `Makefile`, or `justfile`, or the language's standard linter
(e.g. `eslint`, `ruff`, `golangci-lint`, `clippy`, `statix`). Runs only on
changed files; skips silently when none is found. Off by default.

An issue's blockers gate its dispatch until each one reaches
`agent-complete`. For the `github` and `jira` trackers, blockers resolve from
the tracker's **native dependency relationships first** (GitHub's
issue-dependencies API, Jira's "is blocked by" issue links) — native wins
whenever it's non-empty. Body-text refs (`depends on #N` / `blocked by #N`,
inline or a `## Blocked by` list) are a fallback used only when the native
lookup is empty or unavailable, and only `github` and `local` support that
fallback — see [Issue Tracker backends](docs/reference.md#issue-tracker-backends).

Every place a blocker ref is shown to an operator — the `preview` command's
blocker annotations, a selective dispatch's blocked-skip notices, and the
blocked-claim marker (and the release comment the workflow posts from it) —
tags the ref with the source it was resolved from: `(native)` for a tracker's
native dependency relationship, `(body)` for a body-text ref. This makes
drift visible, e.g. a stale `## Blocked by` section on an issue whose native
links have since changed shows up as a `(body)`-tagged ref instead of being
silently indistinguishable from a native one.

An issue may also declare a `## Touches` section listing the paths it expects
to change; dispatch defers it while its touch-set overlaps an already
in-progress issue's, retrying once the collider completes — see [Declared
touch-set overlap](docs/reference.md#declared-touch-set-overlap).

## Research dispatch

`spindrift research` (and the selective `research <nums>` form, mirroring
`dispatch <nums>`) is a second, advise-only Dispatch kind (ADR 0022): each
container reviews one posted issue from inside a fresh clone of the Target
repo, then posts a single structured comment carrying a verdict — it never
edits the issue body, never closes it, and never promotes it to
`ready-for-agent`. A human always acts on the verdict.

```
spindrift research  ─▶  find agent-research issues
                          └─ one container per issue
                               clone repo → review issue → post verdict comment
                               └─ SPINDRIFT_OUTCOME issue=N landing=<comment-url> status=recommend|reject|unclear|blocked
```

Research shares the launcher's four canonical Dispatch states with `dispatch`,
but maps them to its own disjoint `github` label family — claiming an issue
never touches `ready-for-agent`/`agent-in-progress`/`agent-complete`, so an
issue can legitimately wear both a work label and a research label at once:

| label | meaning |
|-------|---------|
| `agent-research` | dual-role: standing state and trigger — apply it to fire a research dispatch |
| `agent-research-in-progress` | a Box is reviewing the issue |
| `agent-research-recommend` | relevant and enriched — promote it |
| `agent-research-reject` | false positive, not worth doing, or a duplicate (named in the comment) — close it |
| `agent-research-unclear` | relevance needs an answer only a human has — answer, then re-apply `agent-research` |
| `agent-research-failed` | the Box crashed or produced no verdict — a human triage queue, distinct from `agent-research-reject` (a *successful* "this is a false positive" conclusion is `Complete`, never `Failed`) |

Settle is strictly one-shot: parse the Outcome line, apply exactly one
terminal label, done — no CI watch, no self-heal fix passes, no merge, since
research never lands code. Retry is the same gesture as `dispatch`:
re-applying `agent-research`. Research dispatches also ignore blocker edges
entirely (enriching an issue is useful *especially* while it waits on a
blocker) and are homogeneous in kind — `research` and `dispatch` never mix
issues within one invocation. See the **Dispatch kind** / **Research
dispatch** glossary entries in [`CONTEXT.md`](CONTEXT.md) for the full
vocabulary.

## Dogfood loop

`dogfood.sh` refuses to start against a podman machine whose RAM is smaller
than `MEMORY_LIMIT` (default `4g`): that mismatch lets the VM's own
OOM-killer kill an in-box build before the per-container `--memory` cap ever
bites (#580). The check reads `podman machine inspect`, compares its
`Resources.Memory` (MiB) against `MEMORY_LIMIT`, and on shortfall prints both
numbers plus `podman machine set --memory <N>` before exiting 1 — no box is
dispatched. It's a no-op with adequate machine RAM, and skips cleanly when
there's no active podman machine (native Linux, or a non-podman runtime).

`dogfood.sh` drives spindrift building itself, with `CONTINUOUS_DISPATCH=1`
on by default (#528): instead of draining one bounded batch and returning,
the launcher runs a long-lived slot-refill loop — as each Box finishes, it
re-discovers the queue and refills the freed slot immediately, re-applying
blocker readiness, the Touches overlap gate, and blocker-failed cascade —
gated by the image-freshness probe (#526) before every launch. An operator
can still set `CONTINUOUS_DISPATCH=` (empty) in `harness.env` to fall back to
the older one-wave-and-exit shape (#527).

The freshness boundary is no longer every iteration: a refill launches
straight onto the already-loaded image so long as it's still fresh, and
`dogfood.sh` only pulls and rebuilds when the launcher reports the image has
actually gone stale (build is a no-op unless the merged diff changed the
image hash).

**Parallel by default.** `MAX_JOBS` defaults to `MAX_PARALLEL` (default 3),
so the slot pool holds that many Boxes at once. Set `MAX_JOBS` explicitly to
run a larger or unbounded pool.

**Termination.** The loop is driven entirely by the launcher's exit code:

| exit | meaning | loop action |
|------|---------|-------------|
| 0    | dispatched work | pull + rebuild, then continue |
| 2    | queue empty (no open issues with the dispatch label) | exit cleanly |
| 3    | open issues exist but none are dispatchable | stop and print a triage message — typically a failed blocker needs re-labeling before the queue can drain |
| 4    | `CONTINUOUS_DISPATCH` mode: the image-freshness probe found the loaded image would be rebuilt against the current base-branch tip; in-flight Boxes finished, no new ones launched | pull + rebuild, then re-invoke — the same boundary exit 0 runs |

Set `CONTINUOUS_DISPATCH=1` to opt into the slot-refill dispatch mode (#527)
in a driving loop other than `dogfood.sh`; see `lib/env-schema.nix`'s
`continuousDispatch` entry for the full behavior.

**Baked skills.** The dogfood Box bakes four pinned upstream skills into
`/home/agent/.claude/skills`, each as a `<name>/SKILL.md` directory — the
only layout Claude Code discovers, so a flat `<name>.md` file is silently
ignored — so the in-box agent can invoke them as slash commands:

- [`caveman`](https://github.com/juliusbrussee/caveman) — `/caveman`. The
  rendered issue-pass and fix-pass prompts direct the agent to default to it
  for narration and prose, compressing narration ~65% in output tokens
  without touching code, commands, error messages, or commit messages.
- [`tdd`](https://github.com/mattpocock/skills) and
  [`to-tickets`](https://github.com/mattpocock/skills) — `/tdd`,
  `/to-tickets` (pinned at tag `v1.1.0`). The IMPLEMENT section defers its
  test-first workflow to `/tdd` when baked.
- [`commit`](https://github.com/jordansmall/skills) — `/commit`. The COMMIT
  section defers commit-message formatting to `/commit` when baked.

Beyond the generic "skills available, prefer them" preamble, each of these
skills gets a deferral placed at the exact prompt section its inline guidance
would otherwise duplicate, gated on that skill being baked.

The pins are non-flake `caveman` / `matt-skills` / `jordan-skills` inputs in
`flake.nix` (`flake.lock` owns the revs); the baked set lives in
`nix/dogfood-skills.nix`. See [Contributing](CONTRIBUTING.md) for how it's
wired.

To opt out of a skill, drop it from the consumer's `skills` list (see
`nix/dogfood-skills.nix`). Each per-skill deferral is rendered only when that
skill's `SKILL.md` is actually present at the baked skills path, so a
consumer that skips a skill gets prompts with zero residue for it.

## Console

`spindrift console` opens the interactive Console (ADR 0023): an
in-terminal loop that lists every open issue from the Issue Tracker —
number, title, labels — oldest-first per dispatch order, and lets you Pick
issues to launch as Dispatches.

```sh
spindrift console
```

Type a command and press enter:

| command | effect |
|---------|--------|
| `r` / `refresh` | re-query the Issue Tracker and re-render the backlog |
| `f <text>` / `filter <text>` | narrow the list to issues with a label containing `<text>` |
| `f` / `filter` (no text) | clear the filter, restoring the full list |
| `p <num>` / `pick <num>` | Pick issue `<num>` — the launch button |
| `pa` / `pick-all-ready` | Pick every issue currently `Dispatchable` — the bulk launch button |
| `u <num>` / `unpick <num>` | Unpick a queued-but-unlaunched pick |
| `d <num>` / `drill <num>` | Drill in: open `<num>`'s rendered transcript |
| `t` / `toggle` | toggle the open transcript between rendered and raw |
| `x` / `close` | close the transcript view, back to the backlog/queue |
| `q` / `quit` | exit cleanly |

If a `.dogfood.pid` file is present at startup — a headless loop
(`dogfood.sh`) already draining the same queue — the Console prints an
informational notice and keeps going; it never blocks or refuses to start,
and the two are safe to run side by side (claims are atomic label swaps).

**Pick** is the launch button. An unlabeled issue is promoted through the
normal `Dispatchable` transition first — recorded durably on the tracker —
then queued; an already-`Dispatchable` issue queues directly. The pick
launches through the same continuous engine the headless loops use, up to
`MAX_PARALLEL` slots at once (the same knob `run`'s wave dispatch honors):
its queue row tracks `queued` → `claiming` → `running` → `settled`, and as
each running pick settles, the next queued pick fills the slot it freed —
the session's queue drains continuously without re-invocation. Queued-but-
unlaunched picks hold at `Dispatchable` on the tracker, never `InProgress` —
the claim to `InProgress` only happens when the pick's turn to launch
actually arrives. If that claim races (another loop, the issue closed, a
relabel), the pick dissolves and its row shows why, instead of launching a
Box for a stale listing.

**Pick all ready** (`pa`) picks exactly the issues currently `Dispatchable`
on the tracker, in one snapshot query — an explicit action, never standing
discovery: an issue that becomes `Dispatchable` after `pa` returns is not
picked until the operator asks again. Each issue queues through the same
Pick path a single `p <num>` uses.

**Unpick** removes a queued-but-unlaunched pick from the session with zero
Issue Tracker calls — it only ever un-does the in-session queue entry, never
the durable promotion a pick already recorded.

Every pick defaults to a `work` Dispatch; the record carries a kind field so
research picks can arrive later as a UI gesture rather than a remodel — only
`work` is exposed today.

A running pick's queue row also shows its latest heartbeat — phase, turn
count, last tool — reusing the same heartbeat parser the live dispatch's own
terminal output already uses, replayed against the pick's on-disk log rather
than a second parser. It updates on every render, since it is a local log
read with no Issue Tracker call behind it.

**Backlog freshness** without spending the shared rate-limit window: `r`
still re-queries on demand, plus the backlog auto-refreshes whenever the
session itself writes to the tracker — a claim, a settle, or a promotion —
and a slow background poll re-queries on a fixed cadence (60-120s) even on an
otherwise idle session. Nothing refreshes faster than that poll; only the
session's own writes and the operator's own `r` trigger a refresh in between.

**Drill-in** (`d <num>`) opens `<num>`'s rendered transcript: assistant turns
and tool calls, readable, spanning the whole Dispatch — initial run, every fix
pass, and conflict-resolve — concatenated in order with a `=== pass: ... ===`
boundary between them, since the Dispatch (claim to verdict) is the domain
object and per-pass logs are storage detail. Re-running `r`/`refresh` while a
transcript is open reloads it too, so a running Dispatch's view grows as you
refresh. `t`/`toggle` switches to the raw byte-exact log for debugging the
harness itself, and back; `x`/`close` returns to the backlog/queue. Rendering
is a per-Driver strategy (beside heartbeat parsing and usage extraction) — a
Driver with no configured strategy, or an issue with no Dispatch logs on disk
yet, surfaces an error in place of the transcript rather than a blank pane.

## Documentation

| document | what's in it |
| -------- | ------------ |
| [`docs/reference.md`](docs/reference.md) | Full CLI table, all configuration options, runtime env vars, how a run works, label lifecycle, security model, macOS build notes, design notes |
| [`docs/flake-options.md`](docs/flake-options.md) | Schema-generated reference for every `settings.<section>.<knob>` — attr path, env var, default, description; regenerated by `nix flake check` |
| [`CONTEXT.md`](CONTEXT.md) | Vocabulary — Harness, Consumer flake, Target repo, Box, Forge, and the three roles |
| [`MIGRATING.md`](MIGRATING.md) | Deprecated commands and breaking changes by version |
| [`VERSIONING.md`](VERSIONING.md) | Semver policy and the stability guarantees for each surface |
| [`CONTRIBUTING.md`](CONTRIBUTING.md) | Dev workflow (`nix flake check`, the scoped in-box `checks-inbox` target, `nix run .#regen`), where code goes, ADRs, commit/release conventions |
| [`SECURITY.md`](SECURITY.md) | How to report a vulnerability privately, plus the deployment threat model |

## Credits

Heavily inspired by Matt Pocock's
[Sandcastle](https://github.com/mattpocock/sandcastle) project.

## License

MIT — see [LICENSE](LICENSE).
