# Release notes

Highlights for each released version of **spindrift**, the nix-based harness
that runs headless [Claude Code](https://claude.com/claude-code) agents in
disposable per-issue containers (the "Box").

For a detailed view of every change, see [`CHANGELOG.md`](CHANGELOG.md).
Anything tagged **⚠ Breaking** may need a change on your end when you upgrade,
depending on how you use spindrift; it won't affect everyone.

---

## Unreleased

Boolean knobs are now presence-style flags.

**⚠ Breaking changes**: the space-separated value form (`--flag 1`) is no
longer accepted for the converted boolean flags.

- **⚠ Breaking: boolean flags are presence-style.** `--continuous-dispatch`,
  `--auto-format`, `--auto-lint`, `--local-issue-reference`,
  `--orchestrator-enabled`, `--preflight-stale-base`, `--bwrap-unshare-net`, and
  `--jira-include-comments` now turn on from the bare flag alone (e.g.
  `--auto-format`). The old space-separated value form (`--auto-format 1`) no
  longer feeds the flag its value — the trailing `1` is left as a stray
  positional arg; use the bare flag, or the explicit equals form to set either
  direction (`--auto-format=1` on, `--auto-format=0` off).
- **`--continuous` alias.** `--continuous-dispatch` is now one of these presence
  flags, with `--continuous` as its bare-flag alias — `spindrift dispatch
  --continuous` turns on the slot-refill loop.

---

## 0.8.0 — 2026-07-28

spindrift can now run an agent other than Claude Code. The Box picks its agent
CLI through a new pluggable "Driver," and the first alternative is an
experimental opencode Driver, which brings GitHub Copilot along as a model
provider.

No breaking changes.

- **Experimental opencode Driver.** spindrift is no longer locked to Claude
  Code. A new Driver seam selects which agent CLI runs in the Box by name;
  `claude` stays the default and is unchanged. Set the Driver to `opencode` and
  the Box runs [opencode](https://opencode.ai) instead, with its own image,
  subagents rendered to `agents/*.md`, and run-log usage parsing. It's
  experimental: expect rough edges and give it a non-critical queue for now.
- **GitHub Copilot through opencode.** Because opencode is provider-flexible,
  pointing it at its `github-copilot` provider is how you run spindrift on
  Copilot. Run `opencode auth login -p github-copilot` on the host once, export
  the resulting auth slice into `OPENCODE_AUTH_CONTENT`, and set
  `MODEL=github-copilot/<model>`. The credential is kept off the sandbox's
  command line, and the launcher only requires it when the Driver actually needs
  it. See the opencode Driver docs in `docs/reference.md`.
- **Orchestrator mode moves more work into the harness.** The opt-in,
  experimental orchestrator (still off by default) leans further on the launcher
  instead of the agent's own judgment. The launcher now resolves each pass's
  role itself, including nested and `Agent`-named subagent spawns, so
  coordinator, worker, and reviewer get wired up by the harness rather than
  inferred by the model, and the review pass is attributed to a reviewer role.
  The harness also takes over steps the agent used to run from its prompt: it
  owns the read-only git bundle and derives the merged-outcome verify ref
  host-side. Less of the run rides on the model doing the right thing.
- **Per-model token usage.** Run usage now breaks tokens down by exact model id
  instead of one lump, with cache-creation split out by TTL, so a run that spans
  a coordinator and a cheaper worker shows where the tokens went. The old dollar
  cost estimate is dropped in favor of that per-model table.
- **Roster of subagents.** Define an N-agent roster and spindrift renders each
  one into the Box, replacing the single-subagent knobs (now deprecated). Lets a
  run stand up several named, differently-tuned subagents instead of one.
- **Sturdier run recovery.** When an agent finishes just short of declaring its
  outcome, the recovery nudge now recognizes that near-miss instead of treating
  it as a clean miss, and the host's outcome-backstop push retries with bounded
  backoff rather than giving up on a transient failure.
- **Local-forge improvements.** Host-posted issues get filed on the local
  tracker (closing the local gap next to GitHub's), blocker chains resolve their
  own seed branch instead of silently seeding off a bare base, and issue
  frontmatter is escaped so a hostile title can't inject fields.
- **Operational tidying.** Host logs and the dogfood pidfile moved under
  `.spindrift/`, continuous dispatch halts on an image that won't converge, and
  the dogfood loop halts on exit code 5 instead of spinning.

---

## 0.7.1 — 2026-07-27

An opt-in multi-pass orchestrator that runs the whole work-review-fix loop
inside the Box, plus a spend cap and issue-filing for read-only runs.

No breaking changes.

- **In-box orchestrator loop (`ORCHESTRATOR_ENABLED`).** Opt in and the Box
  drives its own loop across multiple passes instead of a single shot: a
  coordinator plans the work, hands it to a worker, and keeps going until the
  task is done or gives up cleanly. It writes a run-state artifact and emits a
  heartbeat marker per pass so you can see where a run is. Off by default; the
  existing single-pass behavior is unchanged.
- **Coordinator and worker models.** The orchestrator runs the coordinator on
  Opus 4.8 by default and lets you point the worker at a different model with
  `WORKER_MODEL`, so you can spend the big model where the planning happens and a
  cheaper one on the grind.
- **Code-owned review pass.** The orchestrator can run a review pass over the
  agent's own work and feed the findings back into the next fix pass, driven by
  the harness rather than left to the agent to remember.
- **Spend cap per run (`MAX_BUDGET_TOKENS` / `MAX_BUDGET_USD`).** Set a token or
  dollar ceiling and fix passes stop once the run hits it. Usage is summed across
  a run's own passes, and the check is skipped entirely when no cap is set.
- **Read-only Box can file issues now.** Closing the gap from 0.7.0's read-only
  mode: an agent with no write access emits its issue-filing requests as
  tamper-checked log lines and the host files them on GitHub, the same relay
  pattern already used for comments and PRs.
- **Read-only runs get nudged to declare their PR.** If a read-only agent
  finishes without emitting its PR intent, it gets one reminder to do so, and if
  it still doesn't, the run bails visibly instead of looking done.
- **Quieter, cheaper Box.** Bash output is teed and summarized instead of dumped
  in full, Claude Code's output caps are lower, and the agent's async background
  tasks are off, all of which cuts noise and wasted tokens in a run.
- **Fix: failed blockers no longer cascade.** When a blocking issue fails, its
  dependents are held rather than dragged down with it, so one bad blocker
  doesn't sink an otherwise-fine queue.
- **Fix: stale labels cleared on GitHub claim.** Claiming a GitHub issue strips
  leftover terminal labels, so a re-run doesn't start out wearing a stale
  completed/failed state.

---

## 0.7.0 — 2026-07-25

The sandboxed agent can now run against GitHub with no write access at all, plus
a safer way to hand it secrets.

No breaking changes.

- **Read-only Box for GitHub (`BOX_FORGE_AND_ISSUE_ACCESS`).** The agent no
  longer needs push or write access to GitHub. It writes its work as a git
  bundle and emits comments and PR requests as tamper-checked log lines; the
  host relays them to the forge and lands the branch. A compromised or
  misbehaving agent can't push, comment, or open a PR on its own. This brings
  GitHub to parity with the local-forge runs added in 0.6.1.
- **Startup token gate, fail closed.** In read-only mode the launcher inspects
  the GitHub token before it does anything and refuses to start if that token
  can actually push. `spindrift doctor` reports the result. If the write-enable
  signal never reaches the Box, the Box assumes read-only rather than guessing
  it can write.
- **Secrets from an external command (`--secret-cmd`).** Instead of passing
  tokens as flags or environment variables, point spindrift at a command that
  prints the secret (a keychain or password-manager lookup, say). It only runs
  when attached to a terminal, gives an unlock hint when the command fails, and
  has a per-secret templated fallback.
- **Credentials kept out of the agent's reach.** Two new pre-tool hooks: one
  blocks the agent from reading credential files, the other scrubs secrets from
  the environment of any subprocess it spawns. The image bakes that subprocess
  scrub on by default.
- **Fix: no more wasted fix passes.** A fix pass that changes nothing no longer
  burns one of the run's limited fix attempts.

---

## 0.6.1 — 2026-07-24

Run spindrift against a plain local git repo with no GitHub in the loop, plus a
big Console refresh and safer token handling.

No breaking changes.

- **Fully local runs, no GitHub (`CODE_FORGE=local`).** Point spindrift at a
  local repo and local issue files and it dispatches, works, and lands changes
  without ever touching a forge. The Box hands the host a git bundle to land by
  rebase, so nothing inside the sandbox needs push access.
- **New `spindrift reconcile` command** to clean up stuck work. It finds
  PRs and branches, spots abandoned runs, closes local issues once their change
  has actually landed, and finishes landings that got stuck halfway. A liveness
  probe keeps it from resetting work that's still running.
- **Console got a visual and interaction pass.** Bordered panels, ticket and
  detail views that float over the list, a sidebar that live-tails a running
  agent, vim motions in modals, toast notifications on picks, and quicker keys
  (`p` picks, `P` picks all ready, `r`/`R` research or refresh a row). You can
  start research and pick issues right from the detail modal.
- **The Box can carry its own GitHub token (`BOX_GH_TOKEN`).** Opt in to a
  separate scoped token for the sandboxed agent. A few fixes also stop the
  launcher's own token from leaking into the Box, and tokens now stay fresh
  mid-run via a refresh file.
- **Cross-tracker blocker awareness.** GitHub and Jira issues read native
  "blocked by / blocks" links, and a dependent unblocks the moment its blocker
  *lands* rather than when someone gets around to closing it.
- **Looser outcome parsing.** A near-miss or markdown-wrapped `SPINDRIFT_OUTCOME`
  line is now accepted or cleanly rejected instead of quietly mishandled, so one
  slightly-off agent report won't sink the run.

---

## 0.6.0 — 2026-07-20

A from-scratch Console redesign (keybindings changed) plus guardrails around what
agents can do and how PRs merge.

**⚠ Breaking changes** this release: the Console navigation and keybindings
changed.

- **⚠ Breaking: Console navigation and keys changed.** Drilling into a run opens
  a docked sidebar now, only going fullscreen when the terminal is too narrow,
  instead of always taking over. The body shows one section at a time (`H`/`L`,
  or `1`–`5` to jump) rather than two columns. Terminate moved from `k` to `X`,
  `Tab` and the pane-mode key are gone, and `t` cycles Activity, Transcript, raw
  JSONL. If you drive it interactively, retrain your fingers.
- **Live-tailing session view.** The sidebar advances a running agent on its own
  and opens at the newest output. `G`/`End` re-attach follow mode and `z` zooms
  to fullscreen. Vim-style scrolling works across panes.
- **Agents can't background shell processes anymore.** A guard rejects
  backgrounded Bash from inside the Box, including `&`, `setsid`, and `coproc`
  tricks, so nothing can spawn a process that outlives or escapes the run.
- **PRs open as drafts.** The agent opens a draft; the launcher marks it ready
  only once CI is green and it's about to merge. Half-finished PRs no longer look
  mergeable.
- **Git calls can't hang.** Clone, merge, rebase, `ls-remote`, and force-push all
  have timeouts now, so a stuck network call won't freeze a run.
- **Smaller wins:** issue-number tab completion, a "resume once" recovery when
  the agent quits without an outcome, Quickstart locked to GitHub-only with
  validated input and no echoed secrets, and OAuth session-limit messages treated
  as rate limits so runs back off instead of failing.

---

## 0.5.1 — 2026-07-18

A guided first-run wizard, plus a lot of hardening across the Console,
credentials, and dependency handling.

No breaking changes.

- **New `quickstart` wizard.** Takes you from nothing to a working setup: finds
  your container runtime (Docker, Podman, or Rancher/nerdctl), grabs your git
  identity and repo, captures and audits your GitHub token, captures Claude auth,
  then runs doctor and a build and prints a summary. This is the path new users
  should start on.
- **Rancher / nerdctl support.** Set the runtime to `rancher` (alias for
  nerdctl); quickstart auto-detects it.
- **Credentials scrubbed from errors.** URL credentials get redacted in clone,
  probe, merge, and force-push error messages, so a token baked into a git remote
  won't show up in logs.
- **Console feels more solid.** Quit-confirmation and orphaned-run recovery are
  back, a rebuild-output pane surfaces build failures, a chord indicator shows
  when you're mid-gesture, and actions that used to fail silently now say
  something. Plus a pile of sizing and wrapping fixes for narrow windows and
  wide/emoji characters.
- **Better dependency handling in wave dispatch.** A picked issue is held rather
  than failed when a dependency check hits a transient error, blocked issues name
  their blockers in the skip message, and an issue counts as unblocked once its
  blocker's PR merges.
- **Review findings triaged inline.** Non-blocking findings get handled in place,
  and only real judgment calls get filed as their own issue. Less tracker noise.
- **Safer merge preflight.** The stale-base rebase preflight is opt-in and off by
  default now, since it could thrash without a merge queue.

---

## 0.5.0 — 2026-07-16

The interactive Console lands: a full-screen dashboard for watching and steering
runs. Also a new advise-only research mode.

**⚠ Breaking changes** this release: the `run`/`build` app aliases were removed
and the outcome line's `pr=` field became `landing=`.

- **Interactive Console (TUI).** A full terminal dashboard for running spindrift
  live, with a backlog/queue split and keyboard control throughout. Pick and
  unpick issues, launch everything that's ready, rebuild the Box image without
  leaving, terminate a running Box, and quit with a drain-by-default that
  recovers orphans. Tune live parallelism on the fly with `+`/`-`.
- **Drill into a transcript.** Open a running issue to read its agent transcript
  in a docked or floating pane, with a raw-output toggle and escape-code
  sanitizing so a stray control sequence can't wreck your terminal.
- **Smarter picks.** A pick is held while the issue still has open blockers and
  fires once they clear. Blockers come from GitHub's dependency API first, then
  fall back to issue-body references. Picks on finished or terminated issues get
  rejected.
- **New `research` dispatch kind.** A `research` subcommand (and
  `DOGFOOD_KIND=research`) runs an advise-only review that posts one verdict
  comment and never opens a PR or merges. It has its own label family and can use
  a separate least-privilege research GitHub App token.
- **Merge and settle behavior is steadier.** `agent-complete` waits until the
  landing actually settles, a stale-but-green PR gets rebased before merge, a
  force-pushed head that never goes green now fails the issue, and mid-stream 5xx
  errors are retried as transient.
- **⚠ Breaking:** the deprecated `run`/`build` app aliases are gone. Also the
  outcome line's `pr=` field is now `landing=`, and the default sandbox memory
  limit went up to 5g.

---

## 0.4.2 — 2026-07-14

Small one: turned the Filer on for spindrift's own dogfood loop, so non-blocking
review findings get auto-filed as `agent-review-finding` issues (a human promotes
them before an agent picks them up).

No breaking changes.

---

## 0.4.1 — 2026-07-14

Shell completion, sturdier dispatch and retry logic, and a lighter in-Box check
target.

No breaking changes.

- **Tab completion** for bash, zsh, and fish, built in at image time, with
  per-flag descriptions on zsh.
- **Lighter in-Box checks.** The new `checks-inbox` target runs just the
  source-level checks (Go test/vet/fmt, shellcheck, and so on) and skips the
  heavy OCI image builds, so an agent can validate its work without re-baking the
  container.
- **Dispatch and retry are more careful.** A rate-limited box that exits zero is
  held and retried instead of counted as done, a zero-exit hold isn't
  re-dispatched when a PR already exists, and stale per-issue logs rotate instead
  of getting truncated.
- **Cleaner outcomes and guards.** The agent synthesizes a "blocked" outcome when
  the driver prints none, strips all lifecycle labels when it claims an issue,
  and no longer adopts stray `agent-in-progress` issues. A merge blocked by
  checks is now told apart from a real conflict.
- **Memory preflight for dogfood.** The loop bails early when the podman machine
  has less RAM than `MEMORY_LIMIT` needs.
- A bare or unknown subcommand prints help instead of failing on you.

---

## 0.4.0 — 2026-07-12

The continuous dispatch engine: drain a dependency graph wave by wave, refilling
slots as work finishes. Plus an image-freshness check.

**⚠ Breaking changes** this release: the `DEPS_POLL_SECS`/`DEPS_WAIT_SECS`
settings were removed.

- **Continuous (slot-refill) dispatch.** The new `waves` engine with
  `CONTINUOUS_DISPATCH` keeps parallel slots full, topping them up as issues
  finish instead of running in rigid batches. Exit code 4 means there's more to
  do, and a partial drain prints the leftover issues and the exact command to
  pick up where it stopped.
- **Image-freshness probe.** spindrift notices when the Box image is stale and
  says so on the `preview` verb, so you know before you launch that you'd be
  running an old container.
- **⚠ Breaking:** the `depsPollSecs`/`depsWaitSecs`
  (`DEPS_POLL_SECS`/`DEPS_WAIT_SECS`) settings are gone. Draining is wave-based
  now and `MAX_JOBS` caps the wave size (0 means uncapped). Set either old key
  and you get an unknown-key error.
- **Merge guard:** `MERGE_MODE=auto` is now blocked on a push-only forge that
  can't actually merge.

---

## 0.3.0 — 2026-07-11

Faster, safer batch runs: parallel dispatch by default, plus optional
auto-lint/auto-format inside the agent.

**⚠ Breaking changes** this release: macOS Intel (`x86_64-darwin`) is no longer
built.

- **Runs go parallel by default.** Batch dispatch works issues in bounded
  parallel waves instead of one at a time, so a queue clears much faster. A run
  now reports clearly when there are issues but none are dispatchable (exit 3),
  and a failed blocker cascade-fails whatever depended on it instead of hanging.
- **Graceful stop.** Stop a batch cleanly after the current wave rather than
  hard-killing work in flight.
- **Optional auto-lint and auto-format.** Two knobs, `AUTO_LINT` and
  `AUTO_FORMAT`, tell the agent to lint or format its own changes before it
  wraps up.
- **Real checks baked into the Box.** The image can ship with a writable Nix
  store and pre-baked check/dev tooling (`nixStoreWritable` + `extraClosures`),
  plus `bats` and `shellcheck`, so the agent runs genuine checks instead of
  guessing. It warns loudly when the store is left writable.
- **Plain-language narration on by default.** In-box agents narrate their
  progress in a skimmable style out of the box, with an opt-out.
- **⚠ Breaking:** no more macOS Intel (`x86_64-darwin`) flake outputs.

---

## 0.2.1 — 2026-07-10

Pluggable backends and a smarter merge safety net: drive non-GitHub trackers and
push-only remotes, and feed CI failures back into a fix attempt.

No breaking changes.

- **Pick your tracker and forge.** New `ISSUE_TRACKER` (GitHub, **Jira**, or
  **local**) and `CODE_FORGE` knobs let spindrift drive backends other than
  GitHub, including a push-only Git flow that lands work by pushing a branch with
  no PR/CI round-trip.
- **CI failures drive a targeted fix.** When a pipeline goes red, the actual
  failure detail gets handed to a dedicated fix box with its own prompt
  (`CI_FAILURE_SUMMARY`), so the retry works the real error instead of guessing.
  Transient push failures retry on their own.
- **Overlap guard.** `OVERLAP_GATE` / `MERGE_GUARD_PATHS` spot when two in-flight
  issues touch the same files, from a declared `## Touches` section or the files
  in their open PRs, and serialize them so they don't clobber each other.
- **Optional Filer.** An opt-in step that files follow-up issues for things it
  turns up during a run.
- **Security hardening.** The Git forge rejects flag-like refs (an RCE vector)
  and sets git identity local to the repo instead of globally.
- **Fix passes resume instead of restarting.** The Claude driver pins and resumes
  its session across a fix pass so it keeps its context.

---

## 0.2.0 — 2026-07-09

Settling the config surface and CLI names before wider use: grouped settings, a
tougher reviewer, and a newer default model.

**⚠ Breaking changes** this release: the `engage` command was removed (use
`recover`).

- **⚠ Breaking: `engage` is gone.** `spindrift engage <issue>` is removed; use
  `spindrift recover <issue>`.
- **Newer default model.** The implementor agent defaults to `claude-sonnet-5`.
- **Config is grouped and discoverable.** Consumer config moved to a structured
  `settings.<section>` surface, 13 more knobs became operator-tunable, and a
  generated (drift-guarded) reference documents them.
- **Tougher reviewer.** The reviewer subagent was retuned to dig harder for
  problems.
- **`agent-recover` label workflow** so you can kick off recovery by labeling an
  issue.

---

## 0.1.3 — 2026-07-08

Onboarding and self-checks: a `doctor` command, a real man page, and toolchain
that comes straight from the project's dev shell.

No breaking changes.

- **New `spindrift doctor`.** Checks connectivity and the state of the required
  triage labels, and offers to create any that are missing when you run it
  interactively.
- **Man page and layered help.** The CLI got a proper man page and split its help
  into a short summary and a full reference.
- **Toolchain from your dev shell.** The agent runs post-clone setup inside the
  project's Nix `devShell` (set with `DEV_SHELL_NAME`) so it picks up the right
  tools on its own, with a nudge on cold runs.
- **Containers kept on failure.** A finished container gets reaped on success but
  held on failure, so you can poke at what went wrong.
- **Simpler dependency handling.** The old barrier/fanout-blocker fencing was
  dropped for the newer dependency-wave model.

---

## 0.1.2 — 2026-07-07

The CLI takes shape: a real `spindrift` binary with `dispatch`, `preview`, and
`recover`, plus a configurable merge mode.

No breaking changes.

- **A real CLI.** The `spindrift` command arrives with `--version`, a `dispatch`
  verb (`--no-build`, selective issue lists, `--yes`/`--force`), and a `preview`
  verb that shows the queue and flags which issues are blocked before you commit
  to a run.
- **`recover` replaces `engage`.** The `recover` verb landed and `engage` became
  a deprecated warn-then-run alias (removed in 0.2.0).
- **Configurable merge.** The `MERGE_MODE` knob splits marking an issue "complete"
  from merging it, and adds an `auto` mode that enqueues GitHub auto-merge (with a
  preflight) instead of merging on the spot.
- **Better live progress.** The activity view attributes work to the right
  sub-agent and phase (review, plan, search, git) and shows which model each
  agent is on.

---

## 0.1.1 — 2026-07-07

The foundational release. Basically the whole engine shows up here: the
containerized Box, the Go launcher, dependency-aware dispatch, the merge gate,
the subagent pipeline, and the sandbox.

No breaking changes (first release).

- **The dispatch engine.** A Go launcher runs the show: it claims issues, builds
  and runs the Box on demand, dispatches a single issue or a whole queue, and
  orders work into dependency waves (reading `## Blocked by` edges) so dependents
  wait on their blockers.
- **Two ways to run the Box.** A full OCI image (baked, content-hash-tagged, run
  as non-root) or a daemonless bubblewrap sandbox that runs straight from the Nix
  store with no container runtime at all.
- **Merge gate with conflict resolution.** Once work goes green the launcher
  waits for CI, rebases, and merges. When a rebase conflicts it hands the
  conflict to an agent to resolve. Pushes use `--force-with-lease`.
- **Multi-agent pipeline.** A scout plus reviewer pipeline, per-role model
  tiering (cheaper models for cheaper roles), and a reviewer you can't skip.
- **Self-healing red pipelines.** A capped fix-agent takes a shot at repairing a
  failing pipeline before giving up.
- **Live view and cost tracking.** The transcript streams into a readable
  heartbeat of milestones and phases, and a usage comment (tokens and cost, split
  by sub-agent role) gets posted when the issue finishes.
- **Sandboxed from day one.** Container hardening flags, restricted network
  egress, PID and memory limits, secrets kept off the command line, and the Nix
  builder image pinned by digest.
- **Ready to consume.** Ships a `templates.default` starter, an MIT license,
  schema-derived CLI flags (flag beats env beats default), and a
  language-agnostic core with Nix baked in as the default.
