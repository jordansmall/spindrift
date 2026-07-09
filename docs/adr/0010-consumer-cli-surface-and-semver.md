# The Consumer drives one `spindrift` CLI; its interface is a versioned, release-please-managed contract

Until now a Consumer interacted with the Harness through two nix attributes —
`nix run .#build` then `nix run .#run` — with a third verb (`engage <issue>`)
hidden behind `.#run --`. The `.#` attribute-selector is nix-insider jargon, the
two-attribute split fragments what is *already* one multi-subcommand Launcher
binary, and `build`/`run` name neither what they touch nor the GitHub queue they
drain. This ADR makes the Consumer surface a single **`spindrift` CLI** with
plain subcommands, delivered devShell-first, and declares that surface a
**versioned public contract** migrated with the deprecation idioms native to
each layer.

The everyday surface becomes `nix develop` (or a template `.envrc` via direnv) →
`spindrift dispatch`. The Launcher is already one binary with subcommands; the
flake was splitting it back apart. Collapsing to one CLI gives unified `--help`,
promotes the hidden `engage` path (renamed `recover`, ADR 0011) into a
documented form, and hides `.#`. The
verbs are deliberately **plain, not themed** — a snow register (`flurry`/`pack`/
`forecast`) was explored and rejected, because thematic verbs re-impose exactly
the "what does this even do" tax the redesign exists to remove:

- **`dispatch [issue]`** — fan out one Box per `ready-for-agent` issue, or a
  single issue as an optional positional (the existing `ISSUE_NUMBER`
  single-issue path — *not* `engage`, which is a separate merge-gate verb; see
  ADR 0011). It **auto-builds the image on first use** (the Launcher's
  `EnsureReady` already does this), so the happy path is one command, not two; a
  loud progress line covers the slow first build and `--no-build` fails fast for
  callers who want the steps separate.
- **`build`** — realize and load the image explicitly: the "will this build?"
  pre-warm / CI verb, no longer a prerequisite for `dispatch`.
- **`preview`** — read-only dry run: what `dispatch` *would* pick up, on which
  Target repo, without launching. `status` was rejected for this because it
  reads as *live* state; `status` is **reserved** for a future verb reporting
  active Boxes and the in-flight queue.

Delivery is **devShell-first**: `packages.spindrift` (the CLI) is the blessed
path, dropped into `devShells.default.packages` and fronted by a template
`.envrc`; `apps.default` (`nix run .`) is the cold-start fallback. `nix profile
install` is not promoted — a per-repo agent runner pinned to the Consumer's lock
should not drift as a global binary. Naming the CLI `spindrift` collides with
the OCI image output, so the image is renamed **`packages.agent-image`** — a
rarely-typed artifact whose name is optimized for clarity to a stranger over
brevity.

The Consumer surface is a **versioned contract**: the CLI (verbs, flags, exit
codes), the flake option surface (`perSystem.spindrift.*` and the `mkHarness`
signature), the `env-schema.nix` variable names, and the Label lifecycle names.
Everything else — `cmd/launcher/internal/*`, prompt wording, log formatting — is
explicitly internal and free to churn. spindrift is **pre-1.0**: while `0.y`, a
MINOR bump may break that contract and PATCH is fixes only. Versioning is
managed by **release-please in manifest mode with pre-major bumping**
(`bump-minor-pre-major` + `bump-patch-for-minor-pre-major`), so a `feat!:` lands
as `0.1`→`0.2`, not `1.0.0`, driven by the Conventional Commits already in use.
The bot's `.release-please-manifest.json` is the version source of truth; Nix
reads it to stamp the two `buildGoModule` version fields and ldflags-inject
`spindrift --version` (`spindrift 0.2.0 (rev abc1234)`). Consumers upgrade by
moving a tag-ref pin (`github:jordansmall/spindrift/v0.2.0`).

Interface changes migrate with the idiom **native to each layer**, and the
first migration (`run`→`dispatch`, `.#build`/`.#run`→`spindrift build/dispatch`)
is kept as the worked teaching example even though there is no external Consumer
yet:

- **CLI verbs** deprecate app-style: the old `apps.{build,run}` survive one
  release as warn-then-exec aliases that print a one-line stderr notice naming
  the new command and the removal version, then exec through so nothing breaks
  today.
- **Flake options** deprecate module-style: renames use
  `mkRenamedOptionModule` / `mkRemovedOptionModule` (the Home-Manager idiom) so
  the old option keeps evaluating with a deprecation warning that points at the
  replacement — declaratively, at eval time, not a hand-rolled echo.

No `stateVersion`-style compatibility knob is introduced: Boxes are disposable
and stateless, and the Consumer already pins the whole flake by ref, so a
version bump cannot silently change a default under a live setup the way
Home-Manager's persistent state can. The pinned flake ref *is* the compatibility
guarantee.

## Considered Options

- **Keep `nix run .#build`/`.#run`, just rename the attributes** — smaller
  change, but it keeps the `.#` jargon and the two-attribute fragmentation of a
  binary that is already unified. Rejected; the obtuseness is structural, not
  cosmetic.
- **Thematic snow verbs (`flurry`/`pack`/`forecast`)** — a genuine pun on nix
  *flakes*, and `forecast`≈preview is a clean semantic match. Rejected as the
  register: for a surface whose complaint is "obtuse," verbs that need `--help`
  to decode cut against the goal. Keep the product name evocative, the verbs
  boring.
- **Single-issue dispatch as a `dispatch` positional vs. a dedicated verb** — a
  distinct verb is more discoverable in `--help` but doubles the vocabulary for
  "do work." Folded into `dispatch <issue>`; one verb, optional target. (This is
  the `ISSUE_NUMBER` path only; `engage` — the unrelated merge-gate/adopt verb,
  renamed `recover` in ADR 0011 — is *not* folded in and stays distinct.)
- **A hand-maintained `VERSION` file (or fully git-tag-derived version)** — the
  file is auditable but needs a CI guard to stay in sync with the tag; tag-only
  is zero-toil but a flake cannot read its own tag, so `--version` degrades to a
  bare rev. Rejected in favour of release-please, whose manifest is a
  bot-maintained, Nix-readable source of truth — friendly `--version` *and* no
  manual sync, subsuming both.
- **semantic-release instead of release-please** — fully hands-off and strong at
  registry publishing, but it releases on every qualifying merge with no human
  gate and its edge (npm publish) does not apply to a flake. Rejected;
  release-please's reviewable release-PR is the better fit and the more
  teachable artifact, and a human gate matters while MINOR may break.

## Consequences

- The Launcher grows a real subcommand parser and `--help`/`--version`; `dispatch`
  gains an optional issue positional and a `--no-build` flag; `preview` is a new
  read-only path over the existing discovery logic. `engage` remains a distinct
  verb (the merge-gate/adopt path), renamed `recover` per ADR 0011.
- Flake outputs are restructured: `packages.spindrift` (CLI), `apps.default`
  (CLI), `packages.agent-image` (the former `packages.spindrift` image), with
  `apps.{build,run}` retained as deprecated aliases for one release. Consumer
  docs, the template `flake.nix`, and a new template `.envrc` move to the
  devShell-first flow.
- A `.release-please-config.json` + `.release-please-manifest.json` + a
  release-please GitHub workflow are added; the two hardcoded `version = "0.1.0"`
  strings in `mkHarness.nix` are replaced by a read of the manifest, threaded
  with `self.shortRev` for the rev.
- `VERSIONING.md` (the public-contract definition + pre-1.0 policy) and
  `MIGRATING.md` (an old→new mapping table) are added; the latter is what the CLI
  deprecation notice links to.
- This ADR is authoritative on flake-output naming. The Driver work (ADR 0009)
  ships one lean image per Driver; those outputs use the `agent-image-<driver>`
  family (`agent-image-claude`, `agent-image-opencode`, …), with bare
  `agent-image` aliasing the default Driver's image — superseding ADR 0009's
  earlier `spindrift-<driver>` illustration. The `spindrift` output name is
  reserved for the host CLI; no image output is `spindrift`-prefixed. All of
  these outputs are covered by the versioned flake-output contract.
- The flake option surface `perSystem.spindrift.defaults.*` was replaced by
  `perSystem.spindrift.settings.<section>.<knob>` (ADR 0015). This is a MINOR
  bump under the pre-1.0 policy; no external consumers existed at migration time.
