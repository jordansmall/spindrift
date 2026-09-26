# Contributing to spindrift

Thanks for your interest. spindrift is a nix-based harness that runs headless
coding agents in disposable, nix-built containers, one per issue.
Before any non-trivial change, read [`CONTEXT.md`](CONTEXT.md) for the vocabulary
(Harness, Consumer flake, Target repo, Box, Issue Tracker, Code Forge, Driver)
and [`docs/reference.md`](docs/reference.md) for how a run works. Use these
terms precisely: most review friction comes from mixing up the seams.

## Workflow

`nix flake check` is the canonical task runner. There is no Makefile. It drives
every gate CI enforces: `shellcheck`, the `bats` suite (the bash layers under
fakes, with no real container, network, or LLM), the Go launcher's
`fmt`/`vet`/`test`/cross-build, and the nix-level fixture checks that assert
facts about the declarative options and the built image.

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
`nix flake check` must be green**, and new behavior needs a test. That means a
bats case for bash/entrypoint changes, a Go test for launcher changes, or a nix
fixture check for the declarative options.

spindrift's own dogfood Consumer config opts into `nixStoreWritable` and bakes
the check/dev closure via `extraClosures` (ADR 0018, issue #470), so a Box
working a spindrift issue has a writable `/nix/store` and can run real checks
in-box. The primary in-box gate is the scoped `checks-inbox` target, not the
full flake check (issue #581):

```sh
nix build .#checks-inbox -L   # source-level checks only: go, shellcheck,
                               # nil-clean, marker/parity — no image build
```

`checks-inbox` excludes the checks that build/inspect the OCI image
(`dockerTools.buildLayeredImage`, `lib/image.nix`) or assert facts about
the box's own baked toolchain. The box working the issue is already built
from that image, so re-baking it in-box is redundant, and the nested build is
heavy and unreliable in a Box (issue #565 saw one killed with `EXIT:137`).
CI's full `nix flake check` below still runs those checks, so the move out of
the box loses no coverage.

For faster, store-free per-file iteration (or as a fallback on a Box built
without the self-test knobs), use `nil diagnostics path/to/file.nix` for
changed `*.nix` files and `shellcheck path/to/file.sh` for changed shell
files. Both tools are baked into the Box. They complement, but do not replace,
a `checks-inbox` (or full `nix flake check`) run before opening a PR.

The dogfood Box also bakes the upstream [`caveman`
skill](https://github.com/juliusbrussee/caveman) (issue #486), advertised
in-box as `/caveman`. It cuts agent narration by ~65% in output tokens and
leaves code, commands, and error messages untouched. Most of an agent's output
in this Box is narration, so the saving is large. The pin lives in
`flake.nix`'s `caveman` input (`flake = false`; `flake.lock` owns the rev,
never a floating fetch). `nix/dogfood-skills.nix` renames upstream's
`skills/caveman/SKILL.md` to the `caveman.md` basename that the
skill-discovery loop in `agent/entrypoint.sh` looks for. This is dogfood-only.
The generic harness keeps its empty `skills` default.

After editing `lib/env-schema.nix`, regenerate the artifacts it drives:
`templates/default/harness.env.example`, `cmd/launcher/flagtable_gen.go`,
`docs/flake-options.md`, `tests/box_env_gen.bash`,
`cmd/launcher/internal/doctor/labelmeta_gen.go`, and the generated section of
`templates/default/flake.nix`'s commented-out `settings` example. Don't
hand-edit them until the drift-guard checks (`nix/checks/schema-drift.nix`) go quiet:

```sh
nix run .#regen
```

The regenerator and the drift-guard checks share one renderer per artifact
(`lib/renderers.nix`), so they can't drift from each other. It's repo-internal
dev tooling, not part of the flake-option/env-schema consumer contract. The man
page rebuilds fresh from the schema on every `nix flake check` (nothing to
regenerate).
`lib/env-schema.nix` is the only hand-edit a new knob requires;
the launcher's Go wiring (when the binary reads the knob directly) is the only
other hand-edit. A structural knob's doc/type/default metadata lives instead
in `lib/structural-options-doc.nix`, paired with its `mkOption` declaration in
`lib/flakeModule.nix`'s `structuralOptions`. Some generated spans sit between
BEGIN/END markers in a committed doc (like `docs/reference.md`'s Default
models table), a template (`templates/default/flake.nix`'s settings example),
or a baked-in bash/Go source file (`agent/entrypoint.sh`'s outcome status
words and skill-baked probes, or the skill-baked flags/Env-assignment/
struct-field/gate spans in `cmd/launcher/...`). Each such span is a
documented-fact row. Add one to `lib/documented-facts.nix` rather than
hand-writing a new check/guard/regen call (issues #2948, #2949). Some rows
have a span inside Go source that `gofmt -w` reformats beyond the spliced span
(column-aligning a struct, say). Such a row sets `postSplice = "gofmt";`. The
checker then `gofmt -w`s a reconstruction of the whole host file before
diffing it, and the regenerator `gofmt -w`s the real host file after writing
the spliced content.
MIGRATING.md's legacy
settings mapping block predates the registry and is the one deliberate
exception, out of this migration's scope (see `lib/documented-facts.nix`).

To exercise the whole loop end to end against a live repo, run the daemon
(`nix run .#daemon`, or `nix run .#dogfood-bwrap-daemon` for spindrift's own
bwrap harness) rather than hand-running `spindrift dispatch`. See
[`docs/reference.md`](docs/reference.md).

## Where code goes

The engine is nix; the runtime logic is a nix-built Go binary; the only bash left
is the in-box entrypoint. Respect that split. It is the point of the project.

- **`lib/`** is the nix engine. It holds `mkHarness.nix` (the function
  Consumers import), `flakeModule.nix` (the flake-parts options),
  `env-schema.nix` (the **source of truth** for every `SPINDRIFT_*` variable;
  the `launcher-env-coverage` check fails if the launcher and the schema
  drift), and `backends/default.nix` (the backend descriptor registry, one row
  per ISSUE_TRACKER/CODE_FORGE backend; `env-schema.nix`'s tracker/forge
  choices derive from it). It also holds `baked-skills.nix` (the registry of
  skills `entrypoint.sh` probes at `DRIVER_SKILLS_DIR/<name>/SKILL.md`; one
  row per skill drives every hand-mirrored consumer copy) and `labels.nix`
  (the label registry, one row per label family/role). `renderers.nix`'s
  `renderLabelRegistryGo` renders the label registry into
  `cmd/launcher/internal/doctor/labelmeta_gen.go`, and
  `nix/checks/schema-drift.nix`'s `label-registry-gen` check guards it against
  drift. Last, `renderers.nix` holds the schema-to-artifact render functions
  that the `nix/checks/schema-drift.nix` drift guards and `nix run .#regen`
  share. No language-specific tooling belongs here. The core is
  language-agnostic ([ADR 0003](docs/adr/0003-language-agnostic-core.md)).
- **`cmd/launcher/`** is the Go host-side launcher (its own module). Public
  behavior lives at the top level. `internal/` holds the seams: `forge`,
  `outcome`, `runner`, `driver` (the Driver interface and registry; each
  Driver's own behavior lives in a sibling subpackage, e.g. `driver/claude`),
  `backend` (the Descriptor registry; `registry_gen.go` is generated from
  `lib/backends/default.nix`), and `usage`. The flag table (`flagtable_gen.go`)
  also carries each knob's baked-in default (`schemaFlags[].dflt`). It is
  generated and pinned by a check, so don't hand-edit it. The same goes for
  `registry_gen.go`.
  Go tests use standard `_test` files alongside the code.
- **`agent/`** is the in-box entrypoint (`entrypoint.sh` and friends). Bash
  here is deliberately thin: nix computes the glue, and bash only executes it
  ([ADR 0005](docs/adr/0005-nix-computes-generated-bash-executes.md)). Keep the
  ratio that way. Prefer nix-generated config over more shell.
- **`nix/`** holds `checks.nix` (the flake-check suite), `fixtures.nix` (the
  harness variants the checks build), `regen.nix` (`nix run .#regen`, the
  schema-artifact regenerator), and `regen-goldens.nix` (`nix run
  .#regen-goldens`). That last one reruns `tests/prompt-assembly-parity.bats`
  with `UPDATE_GOLDENS=1` to overwrite `tests/testdata/prompt-assembly-golden/`,
  and is the update-mode counterpart to the `promptassembly-parity` check.
  `parity-env.nix` is the env wiring both share. Add a check when you
  add a guarantee.
- **`templates/default/`** is the consumer starter (`nix flake init -t`).
  spindrift dogfoods this template, so changes here affect every dogfood run.

A few invariants worth calling out:

- **The outcome line is a contract.** The launcher
  (`cmd/launcher/internal/outcome`) parses a Box's final
  `SPINDRIFT_OUTCOME issue=…` line on stdout. Keep it well-formed; that
  package is the authoritative grammar. The harness owns the contract:
  `lib/mkHarness.nix` appends it to any baked `prompt` that omits it, and
  `agent/entrypoint.sh` appends the same canonical contract at run time to a
  rendered issue prompt that omits it, including under a runtime
  `SPINDRIFT_PROMPT_DIR` override. So a Consumer can't accidentally ship an
  agent that never emits the line (see `docs/reference.md`).
- **Do task work in a dedicated git worktree**, one per task/branch. Don't edit
  files on whatever branch happens to be checked out (see [`CLAUDE.md`](CLAUDE.md)).

  ```sh
  git worktree add ../spindrift-<task> -b <branch> origin/main
  ```

- **A baked fragment's anchor line is pinned verbatim across languages.**
  [`templates/default/prompts/fragments/tdd-baked.md`](templates/default/prompts/fragments/tdd-baked.md)
  is the source of truth. Its anchor line is currently
  ``Work test-first: run `/tdd` for each slice.`` The same bytes recur as the
  Go literal `tddAnchorClause`
  (`cmd/launcher/internal/promptassembly/assemble_test.go`), as `grep -qF`
  assertions in `tests/entrypoint-skills.bats` and
  `tests/entrypoint-prompt-fragments.bats` (some negated, so a half-done
  reword leaves the `!` arms passing), in the goldens under
  `tests/testdata/prompt-assembly-golden/`, and in this bullet's own quote.
  The pair's other arm, `tdd-unbaked.md`, pins its opening line separately: a
  full-sentence grep, a `Work test-first, one slice` prefix grep that resolves
  a line number, a negated `commits\.Work test-first` regex that goes vacuous
  the moment that prefix changes, goldens, and
  `nix/checks/tdd-fragment-parity.nix`'s `one slice at a time` clause. That
  opening is the clause's only occurrence in the fragment. `commit-baked.md` and
  `code-review-baked.md` follow the same idiom with their own copies, and a
  pin may cover only a leading clause. The Go and bats pins on
  `code-review-baked.md` stop before the `${REVIEW_FANOUT_AGENT}`
  interpolation, while its goldens hold the rendered sentence whole. So grep
  both the whole sentence and its first few words before rewording any
  anchor. It is a coordinated edit, not a one-line change.
  `lib/fragments.nix` and [`MIGRATING.md`](MIGRATING.md) are near-misses.
  They name the gate and the fragment **file**, never the phrase.

  The Go literals and the bats greps fail on a reword; the goldens do not.
  The Linux-only `nix run .#regen-goldens` rewrites the goldens (never
  hand-edit them), so they catch an unregenerated edit rather than a
  deliberate one. The parity checks under `nix/checks/` mostly constrain an
  anchor's shape, but each also requires it to still name its own skill, and
  `nix/checks/code-review-fragment-parity.nix` pins the
  `spawning ... as agent type` clause and the exact placeholder spelling
  (issue #3447). If that gap, or the prose copies no test reads at all, ever
  needs closing, follow `nix/checks/prompts.nix`'s grep-pin idiom
  (`commit-unbaked-fragment-two-tier-subject-limit`, issue #3478).

## Decisions & the public contract

Architectural decisions that would otherwise live only in a PR description get an
ADR under [`docs/adr/`](docs/adr/), numbered `NNNN-slug.md`, one above the highest
number already there. A change to a seam, such as a new Issue Tracker or Code
Forge adapter, a new Driver, or a new runner, should reference or add an ADR.

Some interfaces are a **versioned contract**: CLI verbs and flags, the flake
options, `SPINDRIFT_*` variable names, and the label lifecycle names. Breaking
any of them needs a `feat!:`/`fix!:` commit and a note in
[`MIGRATING.md`](MIGRATING.md). See [`VERSIONING.md`](VERSIONING.md) for the full
contract and the pre-1.0 policy. Everything under `internal/`, prompt wording,
and log formatting is free to change.

## Commits & releases

Commits follow [Conventional Commits v1.0.0](https://www.conventionalcommits.org/)
with hard-wrapped bodies. The type prefix matters:
[release-please](https://github.com/googleapis/release-please) generates
`CHANGELOG.md` and the version bump from it, and the
`release-please-changelog` check pins the type-to-section map. Use `security`
for fixes with a security dimension so they appear in the notes. Cutting a
release is a single human gate: merge the release PR release-please opens,
and it tags and publishes. Don't hand-tag or `gh release create`.

## Reporting security issues

See [SECURITY.md](SECURITY.md). Please report privately, not in a public issue.
