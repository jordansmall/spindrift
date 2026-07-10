# Contributing to spindrift

Thanks for your interest. spindrift is a nix-based harness that fans out headless
Claude Code agents into disposable, nix-built containers — one per GitHub issue.
Before any non-trivial change, read [`CONTEXT.md`](CONTEXT.md) for the vocabulary
(Harness, Consumer flake, Target repo, Box, Issue Tracker, Code Forge, Driver)
and [`docs/reference.md`](docs/reference.md) for how a run actually works. The
words matter here: most review friction comes from mixing up the seams.

## Workflow

`nix flake check` is the canonical task runner — there is no Makefile. It drives
every gate CI enforces: `shellcheck`, the `bats` suite (the bash layers under
fakes — no real container, network, or LLM), the Go launcher's
`fmt`/`vet`/`test`/cross-build, and the nix-level fixture checks that assert the
declarative surface and the built image.

```sh
nix flake check                 # everything CI runs, in one shot
nix flake check -L              # same, streaming build logs (aka --print-build-logs)
nix build .#checks.$(nix eval --raw --impure --expr builtins.currentSystem).launcher-go-test -L
                                # run a single check (Go tests here) in isolation
nix build .#packages.x86_64-linux.agent-image -L
                                # realise the Linux OCI image (the one thing checks assert only at eval time)
nix develop                     # host-side dev shell: git, gh, jq, and the spindrift CLI on PATH
```

CI (`.github/workflows/ci.yml`) runs `nix flake check --print-build-logs` and
then realises the Linux image on every push and PR. **Before opening a PR,
`nix flake check` must be green**, and new behavior needs a test — a bats case
for bash/entrypoint changes, a Go test for launcher changes, or a nix fixture
check for the declarative surface.

An agent working in the dogfood Box (where `nix flake check` may be
unavailable, e.g. no writable nix store) has faster, store-free stand-ins for
two of those gates: `nil diagnostics path/to/file.nix` for changed `*.nix`
files, and `shellcheck path/to/file.sh` for changed shell files. Both tools
are baked into the Box and complement, but do not replace, the full
`nix flake check` run in CI.

The dogfood Box also bakes the upstream [`caveman`
skill](https://github.com/juliusbrussee/caveman) (issue #486), advertised
in-box as `/caveman`. It compresses agent narration ~65% in output tokens
while leaving code, commands, and error messages untouched — worth having
given how much of an agent's output in this Box is narration. The pin lives
in `flake.nix`'s `caveman` input (`flake = false`; the rev is owned by
`flake.lock`, never a floating fetch); `nix/dogfood-skills.nix` renames
upstream's `skills/caveman/SKILL.md` to the `caveman.md` basename the
skill-discovery loop in `agent/entrypoint.sh` keys off of. This is
dogfood-only — the generic harness keeps its empty `skills` default.

After editing `lib/env-schema.nix`, regenerate the artifacts it drives —
`templates/default/harness.env.example`, `cmd/launcher/flagtable_gen.go`, and
`docs/flake-options.md` — instead of hand-editing them until the drift-guard
checks (`nix/checks.nix`) go quiet:

```sh
nix run .#regen
```

The regenerator and the drift-guard checks share one renderer per artifact
(`lib/renderers.nix`), so they can't drift from each other. It's repo-internal
dev tooling, not part of the flake-option/env-schema consumer surface. The man
page rebuilds fresh from the schema on every `nix flake check` (nothing to
regenerate); `templates/default/flake.nix`'s commented-out `settings` example
is hand-curated — `nix flake check`'s `template-settings-example` check still
flags any section or knob you need to add by hand.

To exercise the whole loop end to end against a live repo, use `./dogfood.sh`
(never hand-run `nix run .#run`) — see [`docs/reference.md`](docs/reference.md).

## Where code goes

The engine is nix; the runtime logic is a nix-built Go binary; the only bash left
is the in-box entrypoint. Respect that split — it is the point of the project.

- **`lib/`** — the nix engine. `mkHarness.nix` (the function Consumers import),
  `flakeModule.nix` (the flake-parts option surface), `env-schema.nix` (the
  **source of truth** for every `SPINDRIFT_*` variable; the `launcher-env-coverage`
  check fails if the launcher and the schema drift), and `renderers.nix` (the
  schema → artifact render functions shared by the `nix/checks.nix` drift
  guards and `nix run .#regen`). No language-specific tooling belongs here —
  the core is language-agnostic ([ADR 0003](docs/adr/0003-language-agnostic-core.md)).
- **`cmd/launcher/`** — the Go host-side launcher (its own module). Public
  behavior lives at the top level; `internal/` holds the seams — `forge`,
  `outcome`, `runner`, `heartbeat`, `usage`. The flag table
  (`flagtable_gen.go`) is generated and pinned by a check; don't hand-edit it.
  Go tests use standard `_test` files alongside the code.
- **`agent/`** — the in-box entrypoint (`entrypoint.sh` and friends). Bash here
  is deliberately thin: nix computes the glue, bash only executes it
  ([ADR 0005](docs/adr/0005-nix-computes-generated-bash-executes.md)). Keep the
  ratio that way — reach for nix-generated config over more shell.
- **`nix/`** — `checks.nix` (the flake-check suite), `fixtures.nix` (the
  harness variants the checks build), and `regen.nix` (`nix run .#regen`, the
  schema-artifact regenerator). Add a check when you add a guarantee.
- **`templates/default/`** — the consumer starter (`nix flake init -t`).
  spindrift dogfoods this very template, so changes here are load-bearing.

Two invariants worth calling out:

- **The outcome line is a contract.** A Box's final `SPINDRIFT_OUTCOME issue=…`
  line on stdout is parsed by the launcher (`cmd/launcher/internal/outcome`).
  Keep it well-formed; that package is the authoritative grammar. The
  contract is harness-owned: `lib/mkHarness.nix` appends it to any baked
  `prompt` that omits it, and `agent/entrypoint.sh` appends the same
  canonical contract at run time to a rendered issue prompt that omits it —
  covering a runtime `SPINDRIFT_PROMPT_DIR` override too — so a Consumer
  can't accidentally ship an agent that never emits the line (see
  `docs/reference.md`).
- **Do task work in a dedicated git worktree**, one per task/branch — don't edit
  files on whatever branch happens to be checked out (see [`CLAUDE.md`](CLAUDE.md)).

  ```sh
  git worktree add ../spindrift-<task> -b <branch> origin/main
  ```

## Decisions & the public contract

Architectural decisions that would otherwise live only in a PR description get an
ADR under [`docs/adr/`](docs/adr/), numbered `NNNN-slug.md` (we're at 0014). A
change to a seam — a new Issue Tracker or Code Forge adapter, a new Driver, a new
runner — should reference or add an ADR.

Some surfaces are a **versioned contract**: CLI verbs and flags, the flake option
surface, `SPINDRIFT_*` variable names, and the label lifecycle names. Breaking
any of them needs a `feat!:`/`fix!:` commit and a note in
[`MIGRATING.md`](MIGRATING.md). See [`VERSIONING.md`](VERSIONING.md) for the full
contract and the pre-1.0 policy. Everything under `internal/`, prompt wording,
and log formatting is free to change.

## Commits & releases

Commits follow [Conventional Commits v1.0.0](https://www.conventionalcommits.org/)
with hard-wrapped bodies — the type prefix isn't cosmetic: `CHANGELOG.md` and the
version bump are generated from it by
[release-please](https://github.com/googleapis/release-please), and the
`release-please-changelog` check pins the type→section map. Use `security` for
fixes with a security dimension so they surface in the notes. Cutting a release
is a single human gate: merge the release PR release-please opens; it tags and
publishes. Don't hand-tag or `gh release create`.

## Reporting security issues

See [SECURITY.md](SECURITY.md) — please report privately, not in a public issue.
