# Contributing to spindrift

Thanks for your interest. spindrift is a nix-based harness that fans out headless
coding agents into disposable, nix-built containers — one per issue.
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
                                # core launcher Go tests: no Driver, bundle or main in its source
nix build .#checks.$(nix eval --raw --impure --expr builtins.currentSystem).launcher-binaries-go-test -L
                                # the tests of the mains, which link every Driver through the bundle
nix build .#checks.$(nix eval --raw --impure --expr builtins.currentSystem).drivers-claude-go-test -L
                                # one Driver's own tests (drivers-<name>-go-test)
nix build .#checks.$(nix eval --raw --impure --expr builtins.currentSystem).backends-github-go-test -L
                                # one backend's own tests (backends-<name>-go-test)
nix build .#packages.x86_64-linux.agent-image -L
                                # realise the Linux OCI image (the one thing checks assert only at eval time)
nix develop                     # host-side dev shell: git, gh, jq, and the spindrift CLI on PATH
```

CI (`.github/workflows/ci.yml`) runs `nix flake check --print-build-logs` and
then realises the Linux image on every push and PR, except a PR touching only
`docs/`, root `*.md`, or the release-please manifest, or editing only comments
in `.go`/`.nix` files: that builds just `.#checks-docs`, the checks that read
docs files (and gofmt, nixfmt and the Go tests). A comment edit still runs the
full matrix when it touches a directive (`//go:build`, `//nolint`, a
shellcheck or `GENERATED` marker) or a file listed in
`.github/scripts/ci-comment-readers`, whose text a check outside that set
reads. The `ci-ok` job is the one required status check either way. The
x86_64-linux leg also posts an eval-cost table (function calls, CPU time,
heap, marginal instance cost) to its job summary, compared against the latest
green `main` run; it is for review and never fails the run. For a
self-time breakdown, label the PR `eval-profile` or dispatch
`.github/workflows/eval-profile.yml`. **Before
opening a PR,
`nix flake check` must be green**, and new behavior needs a test — a bats case
for bash changes, a Go test for launcher and `box` changes, or a nix fixture
check for the declarative surface.

spindrift's own dogfood Consumer config opts into `nixStoreWritable` and bakes
the check/dev closure via `extraClosures` (ADR 0018, issue #470), so a Box
working a spindrift issue has a writable `/nix/store` and can run real checks
in-box. The primary in-box gate is the scoped gate, `nix run .#gate`, not the
full flake check (issue #581):

```sh
nix run .#gate   # source-level checks only: go, shellcheck,
                 # nil-clean, marker/parity — no bats shards
                 # (CI runs them), no image build,
                 # no docs-edit-isolation
```

The gate evaluates the `checks-inbox` set in parallel `nix-eval-jobs` workers
and builds each check as it evaluates; `nix build .#checks-inbox -L` builds
the same set as one aggregate, for CI and humans.

That set excludes the checks that build/inspect the OCI image
(`dockerTools.buildLayeredImage`, `lib/image.nix`) or assert facts about
the box's own baked toolchain — the box working the issue is already built
from that image, so re-baking it in-box is redundant and the nested build is
heavy/unreliable in a Box (issue #565 saw one killed with `EXIT:137`). Those
checks still run in CI's full `nix flake check` below, so coverage isn't
lost — just moved out of the box. It likewise skips
`docs-edit-isolation`, too costly to evaluate in a Box; CLAUDE.md's "Nix
edits" section says why (issue #5849).

For faster, store-free per-file iteration (or as a fallback on a Box built
without the self-test knobs), use `nil diagnostics path/to/file.nix` for
changed `*.nix` files and `shellcheck path/to/file.sh` for changed shell
files. Both tools are baked into the Box and complement, but do not replace,
a gate run (or full `nix flake check`) before opening a PR.

### Seam tests

Seam tests are Go tests behind the `integration` build tag that read the
artifacts Nix renders (the launcher input documents, the contracts, the
preambles) instead of hand-written copies. They find those artifacts in the
directory named by `SPINDRIFT_SEAM_FIXTURES_DIR`. Run them locally from the
dev shell:

```sh
cd cmd/launcher
go test -tags integration ./...   # builds .#seam-fixtures itself via nix

SPINDRIFT_SEAM_FIXTURES_DIR=$(nix build --no-link --print-out-paths ../..#seam-fixtures) \
  go test -tags integration ./... # reuse one build across runs
```

With the variable unset, the `seamtest` package falls back to `nix build
.#seam-fixtures`, and skips where `nix` is absent. In the check gate, both
`checks-inbox` and `nix flake check` run them as `launcher-go-seam-test`, with
the variable set to the built store path. The bats checks export the same
variable, and `tests/helper.bash` reads the contracts, preambles and fragment
registry from that directory, so bats and Go test one set of bytes. See [Seam
tests](docs/reference.md#seam-tests) for the fixture list and its drift guard.

The `piseam` package (issue #5478) runs the locked `pi` binary against a stub
OpenAI-compatible Provider. It takes `pi` from `SPINDRIFT_PI_BIN`, else builds
`nixpkgs#pi-coding-agent` from the flake's locked inputs, and skips when
neither is available. `launcher-go-seam-test` sets the variable in the check
gate. To capture raw pi Pass logs from the locked version, point
`SPINDRIFT_PI_CAPTURE_DIR` at a directory; each pi run then writes its JSON
event stream there, named as [Seam tests](docs/reference.md#seam-tests)
describes:

```sh
SPINDRIFT_PI_CAPTURE_DIR=$(mktemp -d) go test -tags integration ./internal/piseam/
```

The hook-bridge tests (issue #5482) run `agent/pi-hook-bridge.js` against the
repo's own `agent/` hooks by default; `SPINDRIFT_PI_BRIDGE` and
`SPINDRIFT_PI_HOOKS_DIR` point them at another bridge file or hooks directory,
which `launcher-go-seam-test` sets to the store-pinned copies. The hooks parse
their input with `jq`, so a local `go test -tags integration` run needs `jq` on
`PATH`.

The dogfood Box also bakes the upstream [`caveman`
skill](https://github.com/juliusbrussee/caveman) (issue #486), advertised
in-box as `/caveman`. It compresses agent narration ~65% in output tokens
while leaving code, commands, and error messages untouched — worth having
given how much of an agent's output in this Box is narration. The pin lives
in `flake.nix`'s `caveman` input (`flake = false`; the rev is owned by
`flake.lock`, never a floating fetch); `nix/dogfood-skills.nix` declares
upstream's `skills/caveman/SKILL.md` unchanged, and `lib/image.nix` bakes
it under its own `caveman/SKILL.md` directory — the layout the
`agents.skills` row in [flake options](docs/flake-options.md) says the
Drivers discover. This is dogfood-only — the generic harness keeps its
empty `skills` default.

After editing `lib/env-schema.nix` (or a registry row's `knobs`), regenerate
the artifacts it drives — `templates/default/harness.env.example`,
`cmd/launcher/flagtable_gen.go`, `docs/flake-options.md`,
`cmd/launcher/internal/doctor/labelmeta_gen.go`,
`cmd/launcher/internal/inputdoc/emptyissetting_gen.go`,
`cmd/launcher/internal/inputdoc/schemadefaults_gen.go`,
`cmd/launcher/internal/inputdoc/daemononlyknobs_gen.go`, each knob-declaring row's
`knobs_gen.go` reader (`lib/row-knob-readers.nix`), and the generated section of
`templates/default/flake.nix`'s commented-out `settings` example — instead of
hand-editing them until the drift-guard checks (`nix/checks/schema-drift.nix`) go quiet:

```sh
nix run .#regen
```

`nix run .#regen` likewise renders `lib/cli-flags.nix` into
`cmd/launcher/cliflags_gen.go` and `lib/subcommands.nix` into
`cmd/launcher/subcommands_gen.go`. A flag's short form (`-h`, `-v`) is a
`short` field on its long flag's row in `lib/cli-flags.nix`, never its own row
and never only doc text; the completions and man page render it from there. An
`intercepted = true` row marks a flag `mainRun` acts on before `parseFlags`
runs, so the row feeds only completions, the man page, and
`spindrift --help --all`.

The regenerator and the drift-guard checks share one renderer per artifact
(`lib/renderers.nix`), so they can't drift from each other. It's repo-internal
dev tooling, not part of the flake-option/env-schema consumer surface. The man
page rebuilds fresh from the schema on every `nix flake check` (nothing to
regenerate).
`lib/env-schema.nix` is the only hand-edit a new knob requires, unless a
registry row owns it (today the `knobs` of `lib/backends/<name>.nix`,
`lib/chores/<name>.nix`, or `lib/runners/<name>.nix`, folded in by
`lib/row-knobs.nix`, ADR 0068 decision 2), in which case edit it on its row;
the launcher's Go wiring (when the binary reads the knob directly) is the only
other hand-edit. A structural knob's doc/type/default metadata lives instead
in `lib/structural-options-doc.nix`, paired with its `mkOption` declaration in
`lib/flakeModule.nix`'s `structuralOptions`. A generated span embedded
between BEGIN/END markers — in a committed doc (like `docs/reference.md`'s
Default models table), a template (`templates/default/flake.nix`'s settings
example), or a baked-in Go source file (the skill-baked probes/flags/Env-assignment/
struct-field/gate spans in `cmd/launcher/...`) — is a documented-fact row; add
one to `lib/documented-facts.nix` rather than hand-writing a new
check/guard/regen call (issues #2948, #2949). A row whose span lives inside Go
source that `gofmt -w` reformats beyond just the spliced span (column-aligning
a struct, say) sets `postSplice = "gofmt";` so the checker `gofmt -w`s a
reconstruction of the whole host file before diffing it, and the regenerator
`gofmt -w`s the real host file after writing the spliced content.
MIGRATING.md's legacy
settings mapping block predates the registry and is a deliberate exception,
out of this migration's scope (see `lib/documented-facts.nix`). The other is
`docs/reference.md`'s LOCKFILE CLASSIFICATION and REGISTRY DISCOVERY SOURCES
spans: their rows live only in Go, so `TestReferenceDocs_GeneratedSpans` in
`cmd/launcher/internal/driver/bundle` renders them instead.

To exercise the whole loop end to end against a live repo, run the daemon
(`nix run .#daemon`, or `nix run .#dogfood-bwrap-daemon` for spindrift's own
bwrap harness) rather than hand-running `spindrift dispatch` — see
[`docs/reference.md`](docs/reference.md).

## Where code goes

The engine is nix; the runtime logic is a nix-built Go binary, in the launcher
and in the in-box `box` program; the only bash left is the generated shim that
`exec`s `box`. Respect that split — it is the point of the project.

- **`lib/`** — the nix engine. `mkHarness.nix` (the function Consumers import),
  `flakeModule.nix` (the flake-parts option surface), `env-schema.nix` (the
  **source of truth** for every operator-facing `SPINDRIFT_*` knob, folding in
  those a registry row owns via `row-knobs.nix`; the
  `launcher-env-coverage` check fails if the launcher and the schema drift,
  and internal non-knob env names sit on that check's own allowlist),
  `dispatch-kinds/default.nix` (the Dispatch kind descriptor rows — one row
  per kind, each in its own `dispatch-kinds/<name>.nix` file, rendered into
  `cmd/launcher/internal/dispatchkind/kinds_gen.go` by `renderers.nix`'s
  `renderDispatchKindsGo`, drift-guarded by the `dispatch-kinds-gen` check;
  `mkHarness` and `image.nix` bake each row's `prompts` files),
  `backends/default.nix` (the backend descriptor registry — one row per
  ISSUE_TRACKER/CODE_FORGE backend, each in its own `backends/<name>.nix`
  file; `env-schema.nix`'s tracker/forge choices derive from it),
  `baked-skills.nix` (the registry of skills `box` probes at
  `DRIVER_SKILLS_DIR/<name>/SKILL.md`, skipping human-only skills — one row
  per skill drives every hand-mirrored consumer copy), `labels.nix` (the label registry — one row
  per label family/role, rendered into
  `cmd/launcher/internal/doctor/labelmeta_gen.go` by `renderers.nix`'s
  `renderLabelRegistryGo`, drift-guarded by `nix/checks/schema-drift.nix`'s
  `label-registry-gen` check), `cli-flags.nix` (the non-schema CLI flag table
  the parser, completions and man page all consume), `renderers.nix` (the schema → artifact
  render functions shared by the `nix/checks/schema-drift.nix` drift guards and
  `nix run .#regen`), and `agent-hooks.nix` (the baked claude/opencode/pi Bash
  hook scripts, shebangs pinned to the store bash; shared by the image and the
  opencode-guards fixture). No language-specific tooling belongs here — the core
  is language-agnostic ([ADR 0003](docs/adr/0003-language-agnostic-core.md)).
- **`cmd/launcher/`** — the Go host-side launcher (its own module). Public
  behavior lives at the top level; `internal/` holds the seams — `forge`,
  `outcome`, `runner` (the Runner interface and the runner family Strategy
  registry; each family's constructors live in a sibling subpackage, e.g.
  `runner/oci`, registered by the Bundle, `driver/bundle`),
  `driver` (the Driver interface and registry; each
  Driver's own behavior lives in a sibling subpackage, e.g. `driver/claude`),
  `dispatchkind` (the Dispatch kind descriptors; `kinds_gen.go` is generated
  from `lib/dispatch-kinds/default.nix`),
  `backend` (the Descriptor registry; `registry_gen.go` is generated from
  `lib/backends/default.nix`), `backendkit` (the Backend Strategy type and
  registry every binary builds and validates adapters through),
  `recordsdigest` (the Records digest Strategy type and registry records-scoped
  Chores render through, [ADR 0070](docs/adr/0070-a-chores-scope-source-is-a-seam-and-records-chores-plug-in-a-digest.md)),
  `hostbundle` (the host-side bundle registering built-in Digests; only the
  butler-sweep binaries import it), `usage`. The
  flag table (`flagtable_gen.go`),
  which also carries each knob's baked-in default (`schemaFlags[].dflt`), is
  generated and pinned by a check; don't hand-edit it — neither are
  `registry_gen.go`, `kinds_gen.go`, nor `cliflags_gen.go` (generated from
  `lib/cli-flags.nix` by `renderers.nix`'s `renderCliFlagsGo`, pinned by the
  `cli-flags-gen` check).
  Go tests use standard `_test` files alongside the code.
- **`dashboard/`** — the read-only Dashboard web view of the Daemon's status
  file and Events file ([ADR 0060](docs/adr/0060-the-dashboard-is-a-read-only-process-over-the-daemons-published-files.md)).
  Its own Go module (own `go.mod`, stdlib only), kept outside the
  Daemon/launcher filesets so a Dashboard commit never moves the Daemon's
  store path; `nix/dashboard.nix` builds it.
- **`agent/`** — `entrypoint.sh`, the generated shim, and the Driver's hook
  scripts. The shim has no logic: `lib/image.nix` renders the preambles ahead
  of one `exec box` line, and `box` (`cmd/launcher/box`) does the rest
  ([ADR 0058](docs/adr/0058-the-box-main-is-a-go-program-above-a-generated-shim.md),
  building on [ADR 0005](docs/adr/0005-nix-computes-generated-bash-executes.md)).
  Keep it that way — reach for nix-generated config or Go over more shell.
- **`nix/`** — `checks/` (the flake-check suite; `checks/default.nix`
  defines `checks`, `checks-inbox`, and `checks-docs`). A Driver author puts
  their Driver's checks in `checks/drivers/<name>.nix`, named after its row
  in `lib/drivers/default.nix`, and `checks/drivers.nix` unions it in and
  runs the parity checks over every row — a row with no file fails
  evaluation (issue #5502). Its Go tests run in `drivers-<name>-go-test`,
  which `checks/go.nix` generates from the same row; each backend row in
  `lib/backends/default.nix` likewise yields `backends-<name>-go-test` over
  its `internal/forge/<name>` package. Also here:
  `fixtures.nix` (the
  harness variants the checks build), `regen.nix` (`nix run .#regen`, the
  schema-artifact regenerator), `regen-opencode-schema.nix` (`nix run
  .#regen-opencode-schema`, which rewrites the opencode session-store fixture
  from the baked opencode), and `regen-goldens.nix` (`nix run
  .#regen-goldens`, which reruns the Go golden test
  `cmd/launcher/internal/promptassembly/golden_integration_test.go` with
  `UPDATE_GOLDENS=1` to overwrite `tests/testdata/prompt-assembly-golden/`,
  and also reruns the golden tests in `cmd/launcher/stats_test.go`,
  `cmd/launcher/stats_by_test.go`, `cmd/launcher/stats_reverts_test.go`, and
  `cmd/launcher/stats_churn_test.go`
  to overwrite
  `cmd/launcher/testdata/golden/stats*.{txt,jsonl}`, the ecosystem doc test
  `TestReferenceDocs_GeneratedSpans` (in
  `cmd/launcher/internal/driver/bundle`) to rewrite its two spans in
  `docs/reference.md`, and the Driver
  Pass-log corpus test (each Driver's `TestCorpus`) to overwrite the goldens
  under `cmd/launcher/internal/driver/<driver>/testdata/corpus/`, and the
  Digest corpus tests (each records Chore's `TestDigestCorpus` and the
  digestkit's `TestRunReplaysCorpusIntoGoldens`) to overwrite the goldens under
  `cmd/launcher/internal/*/testdata/digest/` and
  `cmd/launcher/internal/digestkit/testdata/corpus/`, and the Ecosystem corpus
  tests (`TestCorpus` in each ecosystem package and
  `cmd/launcher/internal/ecosystemtest`) to overwrite the goldens
  under `cmd/launcher/internal/ecosystem/<ecosystem>/testdata/corpus/` and
  `cmd/launcher/internal/ecosystemtest/testdata/corpus/` —
  the update-mode counterpart to the `launcher-go-seam-test` check). Add a
  check when you add a guarantee.
- **`templates/default/`** — the consumer starter (`nix flake init -t`).
  spindrift dogfoods this very template, so changes here are load-bearing.

A few invariants worth calling out:

- **The outcome line is a contract.** A Box's final `SPINDRIFT_OUTCOME issue=…`
  line on stdout is parsed by the launcher (`cmd/launcher/internal/outcome`).
  Keep it well-formed; that package is the authoritative grammar. The
  contract is harness-owned: `lib/mkHarness.nix` appends it to any baked
  `prompt` that omits it, and `box` appends the same
  canonical contract at run time to a rendered issue prompt that omits it —
  covering a runtime `SPINDRIFT_PROMPT_DIR` override too — so a Consumer
  can't accidentally ship an agent that never emits the line (see
  `docs/reference.md`).
- **Do task work in a dedicated git worktree**, one per task/branch — don't edit
  files on whatever branch happens to be checked out (see [`CLAUDE.md`](CLAUDE.md)).

  ```sh
  git worktree add ../spindrift-<task> -b <branch> origin/main
  ```

- **A baked fragment's anchor line is pinned verbatim across languages.**
  [`templates/default/prompts/fragments/tdd-baked.md`](templates/default/prompts/fragments/tdd-baked.md)
  — currently ``Work test-first: run `/tdd` for each slice.`` — is the source
  of truth; the same bytes recur as the Go literal `tddAnchorClause`
  (`cmd/launcher/internal/promptassembly/assemble_test.go`), as `want` and
  `wantNot` strings in
  `cmd/launcher/internal/promptassembly/fragment_rendering_test.go` (a
  half-done reword leaves the `wantNot` arm passing), in the goldens under
  `tests/testdata/prompt-assembly-golden/`, and in this bullet's own quote.
  The pair's other arm, `tdd-unbaked.md`, pins its opening line separately: a
  full-sentence grep, a `Work test-first, one slice` prefix grep that resolves
  a line number, a negated `commits\.Work test-first` regex that goes vacuous
  the moment that prefix changes, goldens, and
  `nix/checks/tdd-fragment-parity.nix`'s `one slice at a time` clause, which
  that opening is the fragment's only occurrence of. `commit-baked.md` and
  `code-review-baked.md` follow the same idiom with their own copies, and a
  pin may cover only a leading clause — the Go and bats pins on
  `code-review-baked.md` stop before the `${REVIEW_FANOUT_AGENT}`
  interpolation, while its goldens hold the rendered sentence whole. So grep
  both the whole sentence and its first few words before rewording any
  anchor: it is a coordinated edit, not a one-line change.
  `lib/fragments.nix` and [`MIGRATING.md`](MIGRATING.md) are near-misses —
  they name the gate and the fragment **file**, never the phrase.

  The Go literals and the bats greps fail loudly on a reword; the goldens do
  not — the Linux-only `nix run .#regen-goldens` rewrites them (never
  hand-edit), so they catch an unregenerated edit rather than a deliberate
  one. The parity checks under `nix/checks/` mostly constrain an anchor's
  shape, but each also requires it to still name its own skill, and
  `nix/checks/code-review-fragment-parity.nix` pins the
  `spawning ... as agent type` clause and the exact placeholder spelling
  (issue #3447). `nix/checks/prompts.nix`'s grep-pin idiom
  (`commit-unbaked-fragment-two-tier-subject-limit`, issue #3478) is the
  model if that gap, or the prose copies no test reads at all, ever needs
  closing.

## Decisions & the public contract

Architectural decisions that would otherwise live only in a PR description get an
ADR under [`docs/adr/`](docs/adr/), numbered `NNNN-slug.md`, one above the highest
number already there. A change to a seam — a new Issue Tracker or Code Forge
adapter, a new Driver, a new runner — should reference or add an ADR.

An ADR is a decided record, so how you amend one depends on what changed:

- **Prose that only catches up to shipped detail** — a path, a name, a
  mechanism that landed as decided — is edited in place with no marker. It
  does not change the decision, so it does not rewrite the record.
- **Shipped behaviour that diverges from the decision** gets an inline marker
  at the point of divergence: an italic *Amended by issue #NNNN:* lead-in
  paragraph, a parenthetical (Amended by issue #NNNN: …), or a mid-sentence
  "amended by issue #NNNN". Any of these spellings is fine; naming the
  landing PR (`, landed in PR #NNNN`) is optional.
- **A substantive new decision layered on top** gets an appended
  `## Amendment (issue #NNNN): <title>` section.
- **A later ADR that changes one aspect** — a default, say — while the
  earlier ADR's mechanism stands gets a `> **Amended by [ADR NNNN](…):** …`
  header note on the earlier ADR.
- **A reversal** gets a new superseding ADR, never an in-place rewrite (ADR
  0007), and the old ADR gains a `> **Superseded by [ADR NNNN](…).**` header
  note — `Superseded in part by` when the new ADR replaces the mechanism of
  only part of the old one. Older notes (ADRs 0005, 0018) vary in spelling.
  A single superseded consequence can instead be marked inline, as a
  parenthetical (Superseded by ADR NNNN: …).

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
