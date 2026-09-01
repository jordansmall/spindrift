# Shared render functions for the artifacts generated from lib/env-schema.nix,
# and the owner of the flag-group section taxonomy (groupOrder).
# nix/checks/schema-drift.nix (drift guards) and nix/regen.nix (`nix run .#regen`)
# both call these — one renderer per artifact — so the guard and the regenerator
# can never drift from each other. lib/mkHarness.nix and lib/flakeModule.nix
# import it for the taxonomy and the man-page renderer, for the same reason.
#
# Pure builtins only (no `pkgs.lib`): keeps this file evaluable and unit-testable
# with a bare `nix eval`, without needing a locked nixpkgs.
let
  builtinsCompat = import ./builtins-compat.nix;
  inherit (builtinsCompat) concatStrings mapAttrsToList;
  filterAttrs =
    pred: attrs:
    builtins.listToAttrs (
      map (n: {
        name = n;
        value = attrs.${n};
      }) (builtins.filter (n: pred n attrs.${n}) (builtins.attrNames attrs))
    );
  # ASCII-only; every caller here feeds it a SCREAMING_SNAKE_CASE env var name.
  chars = s: builtins.genList (i: builtins.substring i 1 s) (builtins.stringLength s);
  toLower = builtins.replaceStrings (chars "ABCDEFGHIJKLMNOPQRSTUVWXYZ") (
    chars "abcdefghijklmnopqrstuvwxyz"
  );
  toUpper = builtins.replaceStrings (chars "abcdefghijklmnopqrstuvwxyz") (
    chars "ABCDEFGHIJKLMNOPQRSTUVWXYZ"
  );
  # A resolved flake path (e.g. "git.merge.policy") -> its dot-separated
  # segments. Shared by renderTemplateSettingsBlock and renderFlakeOptionsDocFull
  # (ADR 0037).
  splitNixPath = path: builtins.filter builtins.isString (builtins.split "\\." path);
  resolveNixPath = import ./nixpath.nix;
  # A Go `[]string{"a", "b"}` literal's inner comma-joined, quoted contents.
  # Shared by every renderer emitting a flat string-slice var (groupOrder,
  # nixDriverNames, runner's ValidValues) so the quote/join shape lives once.
  renderGoStringSlice = items: builtins.concatStringsSep ", " (map (s: "\"${s}\"") items);
  # Collapses any run of whitespace down to a single space, trimming the ends --
  # a naive "\n" -> " " replaceStrings leaves doubled spaces at a line-wrap or
  # trailing-newline boundary. Used where a `doc` string must become one line.
  oneLine =
    s:
    builtins.concatStringsSep " " (
      builtins.filter (p: p != "") (builtins.filter builtins.isString (builtins.split "[ \t\n]+" s))
    );
  # A markdown table cell can't carry a literal, unescaped "|" (it reads as a
  # column separator). Shared by renderFlakeOptionsDocFull and
  # renderStructuralOptionsDoc's markdown table rows.
  escapeCell = builtins.replaceStrings [ "|" ] [ "\\|" ];
  # Uppercase a string's first character, leaving the rest untouched.
  upperFirst =
    s: toUpper (builtins.substring 0 1 s) + builtins.substring 1 (builtins.stringLength s - 1) s;
  # Right-pads a string with spaces to the given width (a no-op if already that
  # wide or wider).
  padRight =
    width: s:
    let
      pad = width - builtins.stringLength s;
    in
    s + concatStrings (builtins.genList (_: " ") (if pad > 0 then pad else 0));
  # Renders `path = value;\n` lines with every `=` aligned to the widest `path`,
  # computed per block rather than hand-typed.
  renderAlignedLines =
    items:
    let
      maxWidth = builtins.foldl' (
        acc: item: if builtins.stringLength item.path > acc then builtins.stringLength item.path else acc
      ) 0 items;
    in
    concatStrings (map (item: "${padRight maxWidth item.path} = ${item.value};\n") items);
in
rec {
  # Env var name -> flag name (e.g. MAX_PARALLEL -> max-parallel). Shared by
  # every renderer and check that prints or greps for a flag name.
  toKebab = env: toLower (builtins.replaceStrings [ "_" ] [ "-" ] env);

  # ADR 0037 Pass 2: a knob's canonical CLI flag is its `flag` override when
  # set, else its env-derived kebab name. When `flag` is set, the env-derived
  # name (toKebab env) is retained as a *deprecated* alias so operator scripts
  # using the old flag keep working until 1.0. `alias` stays the knob's
  # optional live short-form alias (e.g. --issue for --issue-number).
  flagName = e: e.flag or (toKebab e.env);
  deprecatedFlagAliases = e: if e ? flag then [ (toKebab e.env) ] else [ ];
  liveFlagAliases = e: if e ? alias then [ e.alias ] else [ ];
  # Non-canonical forms (live alias then deprecated old name) for renderers
  # that list secondary forms; all forms (canonical first) for case patterns.
  secondaryFlagNames = e: liveFlagAliases e ++ deprecatedFlagAliases e;
  allFlagNames = e: [ (flagName e) ] ++ secondaryFlagNames e;

  # Schema entry -> the case-arm flag patterns for a choices knob: its
  # canonical --<name> flag, plus every secondary form (live --<alias> and/or
  # deprecated --<old-name>). Shared by renderBashCompletion/
  # renderZshCompletion's choicesFlagBranch, since bash/zsh `case` patterns
  # are the same `|`-joined syntax.
  choicesFlagPatterns = e: map (n: "--${n}") (allFlagNames e);

  # Schema entry -> the type token the flag table and man page print. A knob opts
  # into the presence-style bool kind explicitly, with `kind = "bool";` — the CLI
  # then parses it by presence, not a following value. Deliberately NOT inferred
  # from a boolean `default`: several knobs carry `default = false` purely to
  # render as a `types.bool` flake option while their CLI flag stays a
  # space-separated value form with in-repo callers, so inferring would silently
  # flip all of them. Each converts on its own ticket, migrating callers
  # atomically.
  flagKind =
    e:
    if e ? kind then
      e.kind
    else if builtins.isInt (e.default or null) then
      "int"
    else
      "string";

  # Schema entry -> its default rendered as a string, or "" if it has none.
  flagDflt = e: if e ? default then builtins.toString e.default else "";

  # Display order for the full flag reference (man page OPTIONS groups,
  # flake-options.md sections): the six domains (ADR 0037). Rendered into
  # cmd/launcher/flagtable_gen.go below, so the Go copy can never drift.
  groupOrder = [
    "agents"
    "git"
    "issues"
    "forge"
    "dispatch"
    "infra"
  ];

  # Subcommands whose positional issue-number argument spindrift can dynamically
  # complete: those whose lib/subcommands.nix entry sets
  # dynamicIssueCompletion = true. `research` also takes an issue list but is
  # deliberately excluded — the gate lives in the registry rather than a field
  # like `acceptsIssueArg`, which would conflate "takes issue args" with "gets
  # dynamic completion" and invite research back in by mistake. Shared by all
  # three renderers and by schema-drift.nix's coverage guard, so the set can't
  # drift between renderer and check.
  issueCompletionSubcommands =
    subcommandRegistry:
    map (s: s.name) (builtins.filter (s: s.dynamicIssueCompletion or false) subcommandRegistry);

  # tests/box_env_gen.bash content: a set_box_env bash function exporting
  # every boxEnv = true schema knob at its schema default, or its placeholder
  # when it has no default, so the entrypoint-*.bats suites exercise the same
  # defaults the nix preamble bakes into the image at build time.
  renderSetBoxEnvFixture =
    schema:
    let
      boxEnvSchema = filterAttrs (_: e: e.boxEnv or false) schema;
      renderExport =
        _key: e:
        let
          hasDefault = e ? default;
          value = if hasDefault then flagDflt e else (e.placeholder or "");
          rendered = if hasDefault && flagKind e == "int" then value else "\"${value}\"";
        in
        "  export ${e.env}=${rendered}\n";
    in
    "# Code generated by lib/renderers.nix renderSetBoxEnvFixture from\n"
    + "# lib/env-schema.nix. DO NOT EDIT.\n"
    + "# Regenerate with `nix run .#regen` after editing lib/env-schema.nix.\n"
    + "\n"
    + "# Every lib/env-schema.nix knob with boxEnv = true, exported at its schema\n"
    + "# default (or placeholder, for a knob with no default), so the\n"
    + "# entrypoint-*.bats suites exercise the same defaults the nix preamble\n"
    + "# bakes into the image at build time. Individual tests override any of\n"
    + "# these before invoking $ENTRYPOINT.\n"
    + "set_box_env() {\n"
    + concatStrings (mapAttrsToList renderExport boxEnvSchema)
    + "}\n";

  # The generated portion of templates/default/flake.nix's commented settings
  # example: every flakeOption knob as a nested domain tree keyed by its derived
  # flake path (ADR 0037), with its doc string, so a new knob is discoverable in
  # the template without a hand-edit.
  # structuralExamples is a list of { path; doc; lines; } spliced into the same
  # tree at the same nesting/sorting step, for knobs like roster/byName that have
  # no representable schema-default literal (their real default is a Nix function
  # call). Every caller passes it explicitly rather than the parameter defaulting
  # to `[ ]`, since a curried `schema: extra:` has no Nix syntax for a default on
  # a non-attrset argument.
  renderTemplateSettingsBlock =
    schema: structuralExamples:
    let
      ind = "            # ";
      flakeOptionEntries = filterAttrs (_: e: e.flakeOption or false) schema;
      # Mirrors renderHarnessEnvExample's value rule (placeholder only for a
      # required knob) so a knob whose placeholder exists solely for the bats
      # fixture renders as "" here, not as a fake identity in consumer-facing
      # documentation.
      nixLiteral =
        e:
        if e ? default then
          if builtins.isBool e.default then
            (if e.default then "true" else "false")
          else if builtins.isInt e.default then
            toString e.default
          else
            "\"${toString e.default}\""
        else if e.required or false then
          "\"${e.placeholder or ""}\""
        else
          "\"\"";
      # Insert one schema entry into the nested domain tree at its derived flake
      # path. Each leaf is tagged (__leaf) so renderNode below can tell a schema
      # entry apart from a plain namespace node, since both are attrsets.
      insertLeaf =
        tree: segs: entry:
        let
          seg = builtins.head segs;
          rest = builtins.tail segs;
        in
        if rest == [ ] then
          if tree ? ${seg} && (tree.${seg}.__leaf or false) then
            throw "renderTemplateSettingsBlock: path collides with an existing leaf at \"${seg}\""
          else
            tree
            // {
              ${seg} = {
                __leaf = true;
                inherit entry;
              };
            }
        else
          tree // { ${seg} = insertLeaf (tree.${seg} or { }) rest entry; };
      schemaDomainTree = builtins.foldl' (
        acc: key:
        insertLeaf acc (splitNixPath (
          resolveNixPath key flakeOptionEntries.${key}
        )) flakeOptionEntries.${key}
      ) { } (builtins.attrNames flakeOptionEntries);
      # Structural examples splice in at their own hand-given path — there is no
      # schema entry/resolveNixPath call for these.
      domainTree = builtins.foldl' (
        acc: ex:
        insertLeaf acc ex.path {
          doc = ex.doc;
          lines = ex.lines;
        }
      ) schemaDomainTree structuralExamples;
      # 2 spaces per depth level; children are ordered by attribute name
      # (mapAttrsToList walks builtins.attrNames, which sorts).
      indentAt = depth: builtins.concatStringsSep "" (builtins.genList (_: "  ") depth);
      renderNode =
        depth: node:
        let
          pad = indentAt depth;
        in
        concatStrings (
          mapAttrsToList (
            name: child:
            if child.__leaf or false then
              let
                entry = child.entry;
              in
              # A structural example entry carries `lines` (a multi-line
              # commented Nix assignment) instead of a schema-default-
              # derived single-line `nixLiteral` value.
              "${ind}${pad}# ${entry.doc}\n"
              + (
                if entry ? lines then
                  concatStrings (map (l: "${ind}${pad}${l}\n") entry.lines)
                else
                  "${ind}${pad}${name} = ${nixLiteral entry};\n"
              )
            else
              "${ind}${pad}${name} = {\n" + renderNode (depth + 1) child + "${ind}${pad}};\n"
          ) node
        );
    in
    renderNode 0 domainTree;

  # templates/default/harness.env.example content: secrets only (ADR 0020).
  # Every other knob flows through the Launcher input document, seeded by flake
  # `settings` and overridable per-run by a CLI flag. env configures nothing but
  # secrets, so an example file listing non-secret knobs would advertise a
  # channel that's deprecated the moment an operator uses it.
  renderHarnessEnvExample =
    schema:
    let
      secretSchema = filterAttrs (_: e: e.secret or false) schema;
      renderEntry =
        _key: entry:
        "# ${entry.doc}\n"
        + "# Preferred: fetch this from a vault instead of the plaintext value below —\n"
        + "# ${entry.env}_CMD=\"rbw get spindrift-${toKebab entry.env}\" (or an op/pass/vault\n"
        + "# read); the command's stdout wins over ${entry.env} and is never baked,\n"
        + "# logged, or written to disk.\n"
        + "${entry.env}=\n\n";
    in
    "# Copy to harness.env (gitignored) and fill in — or export these in your shell.\n"
    + "# Secrets only: every other knob is set via the Consumer flake's `settings`\n"
    + "# or an explicit CLI flag (see docs/reference.md and docs/flake-options.md).\n"
    + "#\n"
    + "# Sourcing every secret below from an external vault via its <NAME>_CMD form\n"
    + "# (rbw, op, pass, vault, ...) is the preferred, highly encouraged way to\n"
    + "# supply secrets — see each entry's comment below and docs/reference.md's\n"
    + "# Runtime configuration section. harness.env then holds fetch recipes, not\n"
    + "# live credentials.\n"
    + "#\n"
    + "# One vault under a uniform naming scheme? SECRET_CMD (or --secret-cmd) sets a\n"
    + "# single templated fetch command for every secret below that has no <NAME>_CMD\n"
    + "# of its own — {name} substitutes the secret's kebab-case env name, e.g.\n"
    + "# SECRET_CMD=\"rbw get spindrift-{name}\" reproduces every per-secret example\n"
    + "# below in one line. A per-secret <NAME>_CMD still wins over this fallback.\n\n"
    + concatStrings (mapAttrsToList renderEntry secretSchema);

  # tests/default_models_gen.bash content: a flat bash fixture exporting one
  # variable per lib/default-model-fixture.nix schemaDefaults leaf. Unlike
  # renderSetBoxEnvFixture's set_box_env, this is a static fixture of expected
  # values rather than environment to inject, so it is not wrapped in a function.
  # dogfoodPins.filer is deliberately NOT rendered (same as
  # renderDefaultModelFixtureGo below): the dogfood roster pin is a Nix-only
  # concept, and the checks asserting against it import
  # lib/default-model-fixture.nix directly, so the export would have no consumer.
  renderDefaultModelFixtureBash =
    fixture:
    let
      inherit (fixture) schemaDefaults;
    in
    "# Code generated by lib/renderers.nix renderDefaultModelFixtureBash from\n"
    + "# lib/default-model-fixture.nix. DO NOT EDIT.\n"
    + "# Regenerate with `nix run .#regen` after editing lib/default-model-fixture.nix.\n"
    + "\n"
    + "# The regen-rendered bats fixture form of lib/default-model-fixture.nix's\n"
    + "# schemaDefaults, so a bats test asserts against this fixture's variables\n"
    + "# instead of hand-typing the expected default model literal at each\n"
    + "# assertion site (issue #2514).\n"
    + "export DEFAULT_MODEL=\"${schemaDefaults.model}\"\n"
    + "export DEFAULT_SCOUT_MODEL=\"${schemaDefaults.scoutModel}\"\n"
    + "export DEFAULT_REVIEW_MODEL=\"${schemaDefaults.reviewModel}\"\n"
    + "export DEFAULT_FILER_MODEL=\"${schemaDefaults.filerModel}\"\n"
    + "export DEFAULT_WORKER_MODEL=\"${schemaDefaults.workerModel}\"\n";

  # cmd/launcher/defaultmodels_gen_test.go content: the Go form of
  # lib/default-model-fixture.nix, keyed by the schema's own env-var names, so a
  # launcher test asserts against this fixture instead of hand-typing the
  # expected default model literal.
  renderDefaultModelFixtureGo =
    fixture:
    let
      inherit (fixture) schemaDefaults;
    in
    "// Code generated by lib/renderers.nix renderDefaultModelFixtureGo from\n"
    + "// lib/default-model-fixture.nix. DO NOT EDIT.\n"
    + "package main\n"
    + "\n"
    + "// Regenerate with `nix run .#regen` after editing lib/default-model-fixture.nix.\n"
    + "\n"
    + "// expectedDefaultModels mirrors lib/default-model-fixture.nix's\n"
    + "// schemaDefaults -- the hand-typed anti-vacuity root for every\n"
    + "// bump-sensitive default-model assertion (issue #2514). dogfoodPins.filer\n"
    + "// is deliberately NOT rendered here: it is a Nix-only concept (nix/\n"
    + "// dogfood-defaults.nix's roster) with no Go-side consumer.\n"
    + "var expectedDefaultModels = map[string]string{\n"
    + "\t\"MODEL\":        \"${schemaDefaults.model}\",\n"
    + "\t\"SCOUT_MODEL\":  \"${schemaDefaults.scoutModel}\",\n"
    + "\t\"REVIEW_MODEL\": \"${schemaDefaults.reviewModel}\",\n"
    + "\t\"FILER_MODEL\":  \"${schemaDefaults.filerModel}\",\n"
    + "\t\"WORKER_MODEL\": \"${schemaDefaults.workerModel}\",\n"
    + "}\n";

  # docs/reference.md's generated "Default models" table body: one row per
  # lib/default-model-fixture.nix schemaDefaults leaf, so the doc regenerates
  # from the same fixture the bats/Go forms above do instead of drifting as
  # hand-typed prose. filerModel renders specially, since its schema default is
  # the empty string -- the parenthetical states dogfoodPins.filer instead. Only
  # the table body is generated; the surrounding heading stays hand-written.
  renderDefaultModelsDoc =
    fixture:
    let
      inherit (fixture) schemaDefaults dogfoodPins;
      filerCell = "*(empty; dogfood pins `${dogfoodPins.filer}`)*";
    in
    "| Agent | Default model |\n"
    + "| --- | --- |\n"
    + "| `MODEL` (coordinator) | `${schemaDefaults.model}` |\n"
    + "| `scout` | `${schemaDefaults.scoutModel}` |\n"
    + "| `reviewer` | `${schemaDefaults.reviewModel}` |\n"
    + "| `filer` | ${filerCell} |\n"
    + "| `worker` | `${schemaDefaults.workerModel}` |\n";

  # docs/reference.md's Subagent roster section restates roster's flake path as
  # literal prose; this pins that string to lib/structural-paths.nix's actual
  # `roster` entry.
  renderRosterFlakePathDoc =
    rosterPath: "`perSystem.spindrift.${builtins.concatStringsSep "." rosterPath}`\n";

  # Same, for lib/roster-schema-defaults.nix's rosterDefaults effort values
  # (name=effort per agent, slash-separated).
  renderRosterEffortsDoc =
    rosterDefaults:
    "`"
    + (builtins.concatStringsSep "/" (
      map (n: "${n}=${rosterDefaults.${n}.effort}") [
        "scout"
        "reviewer"
        "filer"
        "worker"
      ]
    ))
    + "`\n";

  # Same, for spindrift's own dogfood Consumer config's Filer pin, against
  # lib/default-model-fixture.nix's dogfoodPins.filer.
  renderDogfoodFilerPinDoc =
    fixture:
    "`roster = rosterLib.defaultRoster { models = { filer = \"${fixture.dogfoodPins.filer}\"; }; };`\n";

  # Same, for the schemaDefaults scout/reviewer/worker model literals (Filer is
  # the separate local pin renderDogfoodFilerPinDoc handles).
  renderDogfoodModelsDoc =
    fixture:
    "`${fixture.schemaDefaults.scoutModel}`, `${fixture.schemaDefaults.reviewModel}` (issue #2433), and `${fixture.schemaDefaults.workerModel}` respectively.\n";

  # docs/reference.md's "### Option surface" table: the whole table, header
  # through last row, as ONE generated block. A markdown table can't carry a
  # marker line between rows (an HTML-comment line between GFM table rows
  # terminates the table), so owning the entire table as a single unit is the
  # only way to make its domain-path column checkable. Most rows have a real
  # perSystem.spindrift.<domain-path> spelling and are computed here; the few
  # with no domain path (mkHarness-only/auto-supplied/flake-module-only) are
  # literal editorial text copied verbatim, `—` and all -- except
  # nixBuilderImage's default cell, which is computed from lib/build-constants.nix
  # instead of a second hand-typed digest literal that could drift silently.
  renderOptionSurfaceTableDoc =
    {
      structuralPaths,
      byNamePaths,
      nixBuilderImage,
    }:
    let
      path = key: registry: "perSystem.spindrift.${builtins.concatStringsSep "." registry.${key}}";
      # The row names this function hand-renders below. Deliberately an explicit
      # list rather than derived from the table text: a name added to
      # structuralPaths/byNamePaths upstream without a corresponding row here
      # must throw instead of silently vanishing from the generated table.
      knownRowNames = [
        "nixpkgs"
        "overlays"
        "config"
        "packages"
        "prefetch"
        "prompt"
        "skills"
        "runtime"
        "driver"
        "nixInBox"
        "nixStoreWritable"
        "extraClosures"
        "roster"
        "byName"
      ];
      unknownKeys = builtins.filter (k: !(builtins.elem k knownRowNames)) (
        builtins.attrNames (structuralPaths // byNamePaths)
      );
    in
    if unknownKeys != [ ] then
      throw "renderOptionSurfaceTableDoc: structuralPaths/byNamePaths carries key(s) not among the known option-surface table rows: ${builtins.concatStringsSep ", " unknownKeys} -- add a matching row to this function (lib/renderers.nix) and its name to knownRowNames"
    else
      ''
        | option      | domain path | scope          | type                        | default            | meaning                                                              |
        | ----------- | ----------- | -------------- | --------------------------- | ------------------ | -------------------------------------------------------------------- |
        | `nixpkgs`   | `${path "nixpkgs" structuralPaths}` | shared         | flake input                 | your `nixpkgs`     | locked nixpkgs the image and host commands build from                |
        | `system`    | — | **auto-supplied** | string                   | perSystem's system | your host system; mapped to its Linux twin for the image; see the note above (`lib/flakeModule.nix`) |
        | `overlays`  | `${path "overlays" structuralPaths}` | shared         | list                        | `[]`               | overlays applied to the instantiated nixpkgs                         |
        | `config`    | `${path "config" structuralPaths}` | shared         | attrs                       | `{ allowUnfree = true; }` | nixpkgs config attrs                                          |
        | `packages`  | `${path "packages" structuralPaths}` | shared         | `pkgs -> [pkg]`             | `[]`               | project build/test tools baked into the image (the toolchain surface)|
        | `prefetch`  | `${path "prefetch" structuralPaths}` | shared         | shell snippet               | `""`               | runs in the work tree after the clone, to warm dependency caches     |
        | `prompt`    | `${path "prompt" structuralPaths}` | shared         | string                      | bundled starter    | agent prompt template baked into the image; changing it requires a rebuild (`spindrift build`). The SPINDRIFT_OUTCOME contract is harness-owned: `spindrift build` appends it automatically if a custom `prompt` omits it (idempotent — a prompt that already has it is untouched) |
        | `scoutPrompt` / `reviewPrompt` / `filerPrompt` | — | **`mkHarness` only** | string | bundled starters | system prompts for the read-only scout and reviewer subagents and the opt-in filer subagent (see [Filer](#filer)); not settable on `perSystem.spindrift.*` — override at runtime via `SPINDRIFT_PROMPT_DIR` regardless of which caller baked the image |
        | `skills`    | `${path "skills" structuralPaths}` | shared         | list of path/derivation/`{ name; src; }` | `[]`  | skills baked into the image at the fixed `/agent/skills` path alongside the harness-owned skills (e.g. `auto-format`, `auto-lint`, baked regardless of this list), each as a `<name>/SKILL.md` directory (the only layout Claude Code discovers — a flat `<name>.md` is ignored) so the headless agent can `/invoke` them; a `{ name; src; }` content entry (name + SKILL.md body) is realized with the image's own Linux `pkgs` rather than copied from a pre-built host derivation, keeping the agent-image drvPath host-independent (issue #597); `agent/entrypoint.sh` copies both into the Driver's actual runtime skills dir at box startup, then copies `SPINDRIFT_SKILLS_DIR` (staged at `/operator-skills`) over the top so runtime overrides win without erasing the baked set (issue #2489) |
        | `settings`  | — | **flake-module only** | submodule, grouped by section (see below) | `{}` | **deprecated** compat shim (ADR 0037): every schema-generated knob is now a first-class option in the domain tree below; this submodule still works pre-1.0 (forwards with an eval warning) but new config should use the domain paths directly |
        | `runtime`   | `${path "runtime" structuralPaths}` | shared         | `"podman"` \| `"docker"` \| `"rancher"` \| `"bwrap"` | `"podman"` | runner the `spindrift build`/`dispatch` commands drive: an OCI runtime (`"rancher"` is an alias for Rancher Desktop's containerd mode, driven via `nerdctl`), or the daemonless bubblewrap sandbox (`bwrap`, Linux-only, no image build/load) |
        | `driver`    | `${path "driver" structuralPaths}` | shared         | string                      | `"claude"`         | the agent CLI Driver baked into the image and threaded to the launcher (ADR 0009); `"claude"` (default) and `"opencode"` are the Drivers today. A non-`claude` Driver realises its own `spindrift-<driver>` image (e.g. `spindrift-opencode`) so per-Driver artifacts never collide |
        | `nixInBox`  | `${path "nixInBox" structuralPaths}` | shared         | bool                        | `true`             | bake a usable nix (binary + registered store DB + sandbox-off, `cores = 4`-bounded config) into the box so `nix flake check` / `nix develop` work inside it; set `false` for a lean, nix-free image (ADR 0008) |
        | `nixStoreWritable` | `${path "nixStoreWritable" structuralPaths}` | shared  | bool                 | `false`            | self-test mode (ADR 0018): make `/nix/store` itself (not its existing contents) agent-writable so in-box `nix flake check` can substitute/build new paths instead of hitting EACCES; new paths live only in the container's ephemeral copy-on-write layer. Not hermetic — the entrypoint prints a loud `==> WARNING`; both runners support it (ADR 0042) — bwrap overlays an ephemeral tmpfs upper on the store instead |
        | `extraClosures` | `${path "extraClosures" structuralPaths}` | shared     | `pkgs -> [pkg]`         | `[]`               | extra derivations, as a function of the (Linux) `pkgs` (like `packages`), whose closures are baked into the image and registered in the store DB alongside the runtime closure, so in-box nix sees them as already present (ADR 0018) |
        | `nixBuilderImage` | — | **`mkHarness` only** | string        | `"${nixBuilderImage}"` (pinned reference — the real default lives in `lib/build-constants.nix`) | Nix image `spindrift build` uses as a fallback Linux builder when the host can't realize the image; pinned by digest for supply-chain safety (see [Building on macOS](#building-on-macos)) |
        | `roster`    | `${path "roster" structuralPaths}` | shared         | list of subagent-entry attrs | `lib/roster.nix`'s `defaultRoster` | supersedes the four legacy model knobs; see [Subagent roster](#subagent-roster) |
        | `byName`    | `${path "byName" byNamePaths}` | shared         | attrset of `{ model?; effort?; }` keyed by roster entry name | `{}` (this row is the `mkHarness` parameter; the flake option, `agents.models.byName`, defaults to `null`) | name-keyed model/effort shorthand (issue #2560), forwarded into `defaultRoster`; only takes effect when `roster` is unset; no flat `perSystem.spindrift.byName` alias — see [Subagent roster](#subagent-roster) |
      '';

  # MIGRATING.md's generated "Flag names re-cut to domains" table: one row per
  # lib/legacy-settings-section.nix entry, mapping the frozen
  # `perSystem.spindrift.settings.<section>.<knob>` alias to its current
  # `perSystem.spindrift.<path>` home. Both columns carry the full
  # `perSystem.spindrift.` prefix, matching flakeModule.nix's own deprecation
  # warning -- this table stands in for hand-diffing docs/flake-options.md, so
  # its paths must read exactly as they do there. Sorted by "<section>.<knob>" so
  # rows group by section, matching a migrating Consumer's own `settings` block.
  renderLegacySettingsMappingDoc =
    legacySettingsSection: schema:
    let
      sortKey = knob: "${legacySettingsSection.${knob}}.${knob}";
      knobs = builtins.sort (a: b: builtins.lessThan (sortKey a) (sortKey b)) (
        builtins.attrNames legacySettingsSection
      );
      row =
        knob:
        "| `perSystem.spindrift.settings.${legacySettingsSection.${knob}}.${knob}` | `perSystem.spindrift.${
          resolveNixPath knob schema.${knob}
        }` |\n";
    in
    "| Legacy alias | Canonical replacement |\n" + "| --- | --- |\n" + concatStrings (map row knobs);

  # The three renderSettingsExample*Doc renderers below each generate one section
  # of docs/reference.md's flat domain-tree example, as `<path> = <literal>;`
  # assignments. Shared rules for all three:
  #  - every path is derived via resolveNixPath from the knob's own
  #    lib/env-schema.nix entry, never hand-typed, so a `group`/`nixSubPath`
  #    rename can't leave the example stale with the drift check still green;
  #  - values render through builtins.toJSON, not a hand-wrapped "${value}", so a
  #    default containing `"` or `\` stays a syntactically valid quoted literal
  #    (int knobs render unquoted via toString, matching the doc);
  #  - no indentation: the example is a flat top-level literal, not nested inside
  #    a `settings = { ... }` wrapper.
  #
  # Models: the same four schemaDefaults leaves renderDefaultModelsDoc's table
  # draws from, so this second default-model literal site regenerates from the
  # same fixture. workerModel is deliberately absent -- the example never carried
  # it. Takes both `fixture` (the default *values*) and `schema` (the entries
  # paths resolve from), since schemaDefaults carries no `group`/`nixSubPath`.
  renderSettingsExampleModelsDoc =
    fixture: schema:
    let
      inherit (fixture) schemaDefaults;
      inherit (builtins) toJSON;
      item = key: value: {
        path = resolveNixPath key schema.${key};
        inherit value;
      };
    in
    renderAlignedLines [
      (item "model" (toJSON schemaDefaults.model))
      (item "scoutModel" (toJSON schemaDefaults.scoutModel))
      (item "reviewModel" (toJSON schemaDefaults.reviewModel))
      (item "filerModel" (toJSON schemaDefaults.filerModel))
    ];

  # Labels: the four leaves driving an issue's dispatch label and the three
  # lifecycle labels the launcher swaps it through. Takes the whole schema
  # attrset, since these are plain env-schema.nix knobs with no fixture.
  renderSettingsExampleLabelsDoc =
    schema:
    let
      inherit (builtins) toJSON;
      item = key: {
        path = resolveNixPath key schema.${key};
        value = toJSON schema.${key}.default;
      };
    in
    renderAlignedLines [
      (item "label")
      (item "inProgressLabel")
      (item "failedLabel")
      (item "completeLabel")
    ];

  # Config: the eight leaves driving branch naming and merge/dispatch behavior.
  # Also takes the whole schema attrset, for the same reason.
  renderSettingsExampleConfigDoc =
    schema:
    let
      inherit (builtins) toJSON;
      item = key: render: {
        path = resolveNixPath key schema.${key};
        value = render schema.${key}.default;
      };
    in
    renderAlignedLines [
      (item "baseBranch" toJSON)
      (item "branchPrefix" toJSON)
      (item "mergeMode" toJSON)
      (item "mergeGuardPaths" toJSON)
      (item "mergePollInterval" toString)
      (item "mergePollTimeout" toString)
      (item "maxParallel" toString)
      (item "maxJobs" toString)
    ];

  # cmd/launcher/internal/driver/drivernames_gen.go content. driverEntries is the
  # registry's `entries` attrset, not the whole registry -- which also exports
  # shape-assertion and rendering functions that are not Driver names.
  renderDriverNamesGo =
    driverEntries:
    let
      names = builtins.sort builtins.lessThan (builtins.attrNames driverEntries);
    in
    "// Code generated by nix/checks.nix from lib/drivers/default.nix. DO NOT EDIT.\n"
    + "package driver\n"
    + "\n"
    + "// nixDriverNames is the key list of the Nix Driver registry (lib/drivers/default.nix).\n"
    + "// Regenerate with `nix run .#regen` after editing lib/drivers/default.nix.\n"
    + "var nixDriverNames = []string{"
    + renderGoStringSlice names
    + "}\n";

  # agent/entrypoint.sh's generated skill-baked probe block: one
  # `[ -f ... ] && _ap_args+=(...)` line per lib/baked-skills.nix row.
  renderBakedSkillProbesShell =
    bakedSkills:
    concatStrings (
      map (
        s: "  [ -f \"$DRIVER_SKILLS_DIR/${s.name}/SKILL.md\" ] && _ap_args+=(--${s.name}-skill-baked)\n"
      ) bakedSkills
    );

  # cmd/launcher/driver-exec/assembleprompt_cmd.go's generated skill-baked flag
  # declarations.
  renderBakedSkillFlagsGo =
    bakedSkills:
    concatStrings (
      map (
        s:
        "\t${s.goVar} := fs.Bool(\"${s.name}-skill-baked\", false, \"true when DRIVER_SKILLS_DIR/${s.name}/SKILL.md was baked\")\n"
      ) bakedSkills
    );

  # ...and its generated skill-baked env.Field assignment statements. env is
  # built from promptassembly.EnvFromEnviron()'s returned value, not a struct
  # literal, so each row is a plain statement (1-tab indent, no trailing comma)
  # rather than a struct-literal field.
  renderBakedSkillEnvAssignGo =
    bakedSkills: concatStrings (map (s: "\tenv.${s.field} = *${s.goVar}\n") bakedSkills);

  # cmd/launcher/internal/promptassembly/env.go's generated skill-baked struct
  # fields.
  renderBakedSkillFieldsGo =
    bakedSkills:
    concatStrings (
      map (
        s: "\t${s.field} bool // entrypoint.sh: -f \"$DRIVER_SKILLS_DIR/${s.name}/SKILL.md\" (${s.gate})\n"
      ) bakedSkills
    );

  # cmd/launcher/internal/promptassembly/gates.go's generated skill-baked Gates()
  # map assignments.
  renderBakedSkillGatesGo =
    bakedSkills: concatStrings (map (s: "\tg[\"${s.gate}\"] = e.${s.field}\n") bakedSkills);

  # cmd/launcher/internal/backend/registry_gen.go content: one Go `Descriptor`
  # var per lib/backends/default.nix row, plus a Registry slice listing them in
  # the nix list's declaration order (load-bearing -- see that file's header).
  # Emits unaligned Go; gofmt owns struct-literal column alignment. Only emits a
  # field line for a Go-truthy value, mirroring the hand-written struct literals
  # it replaces, which never wrote e.g. `ValidAsTracker: false,`.
  renderBackendRegistryGo =
    backends:
    let
      # Every field lib/backends/default.nix's header documents. A row attribute
      # outside this set is a typo, not a new fact, and must fail the build
      # rather than render silently as if the field were never set.
      knownFields = [
        "name"
        "goVar"
        "validAsTracker"
        "validAsCodeForge"
        "tokenEnvVar"
        "doctorTokenHint"
        "doctorSlugHint"
        "hostMediatedRemote"
        "inBoxUnreachableTracker"
        "outboxRelayCapable"
        "relayCapable"
        "hostPostingCapable"
        "trackerAxisRead"
        "trackerAxisWrite"
        "trackerAxisFiler"
        "forgeBackend"
      ];
      checkRow =
        row:
        let
          unknown = builtins.filter (attr: !(builtins.elem attr knownFields)) (builtins.attrNames row);
        in
        if unknown == [ ] then
          row
        else
          throw "lib/backends/default.nix: row '${row.name or "?"}' has unknown field(s): ${builtins.concatStringsSep ", " unknown}";
      fieldLine =
        goName: value:
        if builtins.isBool value then
          (if value then "\t${goName}: true,\n" else "")
        else if value == "" then
          ""
        else
          "\t${goName}: ${builtins.toJSON value},\n";
      renderRow =
        row:
        "// ${row.goVar} is the descriptor for the \"${row.name}\" backend.\n"
        + "var ${row.goVar} = Descriptor{\n"
        + fieldLine "Name" row.name
        + fieldLine "ValidAsTracker" (row.validAsTracker or false)
        + fieldLine "ValidAsCodeForge" (row.validAsCodeForge or false)
        + fieldLine "TokenEnvVar" (row.tokenEnvVar or "")
        + fieldLine "DoctorTokenHint" (row.doctorTokenHint or "")
        + fieldLine "DoctorSlugHint" (row.doctorSlugHint or "")
        + fieldLine "HostMediatedRemote" (row.hostMediatedRemote or false)
        + fieldLine "InBoxUnreachableTracker" (row.inBoxUnreachableTracker or false)
        + fieldLine "OutboxRelayCapable" (row.outboxRelayCapable or false)
        + fieldLine "RelayCapable" (row.relayCapable or false)
        + fieldLine "HostPostingCapable" (row.hostPostingCapable or false)
        + fieldLine "TrackerAxisRead" (row.trackerAxisRead or "")
        + fieldLine "TrackerAxisWrite" (row.trackerAxisWrite or "")
        + fieldLine "TrackerAxisFiler" (row.trackerAxisFiler or "")
        + fieldLine "ForgeBackend" (row.forgeBackend or "")
        + "}\n";
      checkedBackends = map checkRow backends;
      rows = concatStrings (map renderRow checkedBackends);
      registryVars = builtins.concatStringsSep ", " (map (row: row.goVar) checkedBackends);
    in
    "// Code generated by nix/regen.nix from lib/backends/default.nix. DO NOT EDIT.\n"
    + "package backend\n"
    + "\n"
    + rows
    + "\n"
    + "var Registry = []Descriptor{${registryVars}}\n";

  # cmd/launcher/internal/doctor/labelmeta_gen.go content: one
  # `var Meta<Role> = LabelMeta{...}` per lib/labels.nix work-tier row (the only
  # rows a Go caller resolves by role rather than by possibly-renamed name), plus
  # the `TriageLabelMeta` map every tier feeds into except `recoverable` (never a
  # real created label), `findingType`, and `triggerOnly` (workflow-only
  # vocabulary doctor never creates).
  # findingType gets its own `FindingTypeLabels` map (ADR 0041), deliberately out
  # of TriageLabelMeta, so settle/issue_intent.go's ensureTypeLabel resolves a
  # filed intent's `type` token against a vocabulary that can never collide with a
  # real dispatch/provenance label name. Emits unaligned Go; gofmt owns alignment.
  renderLabelRegistryGo =
    labels:
    let
      metaLit =
        row:
        "LabelMeta{Description: ${builtins.toJSON row.description}, Color: ${builtins.toJSON row.color}}";
      workVar = row: "Meta${row.role}";
      workVarDecl = row: "var ${workVar row} = ${metaLit row}\n";
      workVarDecls = concatStrings (map workVarDecl labels.work);
      # A work-tier row's map entry reuses the Meta<Role> var declared above
      # (so the map and the per-role var can never disagree); every other
      # tier's map entry is a literal, there being no per-role var for it.
      mapEntry = row: "\t${builtins.toJSON row.name}: ${workVar row},\n";
      mapEntryLit = row: "\t${builtins.toJSON row.name}: ${metaLit row},\n";
      mapEntries =
        concatStrings (map mapEntry labels.work)
        + "\n"
        + concatStrings (map mapEntryLit labels.research)
        + "\n"
        + concatStrings (map mapEntryLit labels.researchVerdicts)
        + "\n"
        + concatStrings (map mapEntryLit labels.priority)
        + "\n"
        + concatStrings (map mapEntryLit labels.ambiguous)
        + "\n"
        + concatStrings (map mapEntryLit labels.researchFinding);
      findingTypeEntries = concatStrings (map mapEntryLit labels.findingType);
    in
    "// Code generated by nix/regen.nix from lib/labels.nix. DO NOT EDIT.\n"
    + "package doctor\n"
    + "\n"
    + workVarDecls
    + "\n"
    + "// TriageLabelMeta is the single source of truth for default triage/\n"
    + "// research/priority label colors and descriptions, keyed by the canonical\n"
    + "// label name (lib/labels.nix, issue #2528).\n"
    + "var TriageLabelMeta = map[string]LabelMeta{\n"
    + mapEntries
    + "}\n"
    + "\n"
    + "// FindingTypeLabels is the closed bug/enhancement/chore issue-intent\n"
    + "// type->label vocabulary (lib/labels.nix's findingType family, issue\n"
    + "// #2594 / ADR 0041), kept separate from TriageLabelMeta on purpose -- see\n"
    + "// lib/labels.nix's findingType doc comment.\n"
    + "var FindingTypeLabels = map[string]LabelMeta{\n"
    + findingTypeEntries
    + "}\n";

  # cmd/launcher/internal/runner/runtimevalues_gen.go content: the runner module
  # is the single home of all runtime vocabulary.
  renderRuntimeValuesGo =
    runtimeValues:
    "// Code generated by nix/regen.nix from lib/runtime-values.nix. DO NOT EDIT.\n"
    + "package runner\n"
    + "\n"
    + "// ValidValues are the operator-facing runtime values (lib/runtime-values.nix),\n"
    + "// the same list lib/flakeModule.nix's `runtime` option enum uses, consumed\n"
    + "// by the quickstart wizard and anywhere else that needs the enum.\n"
    + "// Regenerate with `nix run .#regen` after editing lib/runtime-values.nix.\n"
    + "var ValidValues = []string{"
    + renderGoStringSlice runtimeValues
    + "}\n";

  # cmd/launcher/quickstart/quickstart_paths_gen.go content: one Go const per
  # lib/quickstart-path-table.nix key, each a knob's canonical nix option path,
  # so the quickstart wizard's rendered flake.nix literals read the same strings
  # the schema resolves to instead of a hand-typed copy a group/nixSubPath rename
  # would leave stale. Rendered as unexported `path<Key>` identifiers: it's the
  # same package quickstart.go consumes it from, unlike renderAgentPathsGo's
  # cross-package consts below.
  renderQuickstartPathsGo =
    quickstartPaths:
    let
      # builtins.toJSON, not a hand-wrapped "${value}", so a path round-trips
      # into a valid Go string literal.
      renderConst =
        key: value:
        "// path${upperFirst key} is the nix option path for the quickstart wizard's ${key} knob.\n"
        + "const path${upperFirst key} = ${builtins.toJSON value}\n";
      constBlocks = mapAttrsToList renderConst quickstartPaths;
    in
    "// Code generated by nix/regen.nix from lib/quickstart-path-table.nix. DO NOT EDIT.\n"
    + "package main\n"
    + "\n"
    + "// Regenerate with `nix run .#regen` after editing lib/env-schema.nix or\n"
    + "// lib/quickstart-path-table.nix.\n"
    + "//\n"
    + "// The nix option path for each quickstart wizard knob (lib/nixpath.nix's\n"
    + "// domain-tree resolution over lib/env-schema.nix's group/nixSubPath),\n"
    + "// single-sourced here so the wizard's rendered flake.nix literals can't\n"
    + "// drift from the schema's own group/nixSubPath taxonomy (issue #2556).\n"
    + "\n"
    + builtins.concatStringsSep "\n" constBlocks;

  # cmd/launcher/internal/agentpaths/agentpaths_gen.go content: one Go const per
  # lib/agent-paths.nix key, so the launcher's host-side mount/path logic reads
  # the same baked /agent/* literals the image and its preamble are built from,
  # instead of a hardcoded string a rename would leave stale. Each
  # SCREAMING_SNAKE_CASE key becomes a PascalCase Go identifier.
  renderAgentPathsGo =
    agentPaths:
    let
      splitWords = key: builtins.filter builtins.isString (builtins.split "_" key);
      capitalizeWord = w: upperFirst (toLower w);
      pascalCase = key: concatStrings (map capitalizeWord (splitWords key));
      # builtins.toJSON, not a hand-wrapped "${value}", so a path containing `"`
      # or `\` round-trips into a valid Go string literal.
      renderConst =
        key: value:
        "// ${pascalCase key} is the baked in-box path for ${key}.\n"
        + "const ${pascalCase key} = ${builtins.toJSON value}\n";
      constBlocks = mapAttrsToList renderConst agentPaths;
    in
    "// Code generated by nix/regen.nix from lib/agent-paths.nix. DO NOT EDIT.\n"
    + "package agentpaths\n"
    + "\n"
    + "// Regenerate with `nix run .#regen` after editing lib/agent-paths.nix.\n"
    + "//\n"
    + "// The 9 baked /agent/* path literals (lib/agent-paths.nix), single-sourced\n"
    + "// here so the Go launcher can never drift from the Nix image/preamble\n"
    + "// source of truth (issue #2531) -- a rename in lib/agent-paths.nix now fails\n"
    + "// nix/checks/schema-drift.nix's agent-paths-gen check instead of silently\n"
    + "// mounting onto a dead in-box path.\n"
    + "\n"
    + builtins.concatStringsSep "\n" constBlocks;

  # cmd/launcher/subcommands_gen.go content.
  renderSubcommandsGo =
    subcommands:
    let
      rows = concatStrings (
        map (e: "\t{name: \"${e.name}\", usage: \"${e.usage}\", doc: \"${e.doc}\"},\n") subcommands
      );
    in
    "// Code generated by nix/regen.nix from lib/subcommands.nix. DO NOT EDIT.\n"
    + "package main\n"
    + "\n"
    + "// subcommandRegistry is the subcommand table derived from lib/subcommands.nix.\n"
    + "// Regenerate with `nix run .#regen` after editing lib/subcommands.nix.\n"
    + "var subcommandRegistry = []subcommandEntry{\n"
    + rows
    + "}\n";

  # cmd/launcher/internal/outcome/status_gen.go content: typed Go constants plus
  # ordered var slices for every lib/prompt-contract.nix outcomeStatusSets row.
  # One `const` per unique status word across all kinds (a word shared between
  # kinds gets exactly one identifier), plus one []string var per kind.
  renderOutcomeStatusGo =
    outcomeStatusSets:
    let
      capitalize =
        s: toUpper (builtins.substring 0 1 s) + builtins.substring 1 (builtins.stringLength s - 1) s;
      constName = word: "Status" + capitalize word;
      allWords = builtins.concatLists (map (row: row.statuses) outcomeStatusSets);
      uniqueWords = builtins.foldl' (
        acc: w: if builtins.elem w acc then acc else acc ++ [ w ]
      ) [ ] allWords;
      constLines = concatStrings (map (w: "\t${constName w} = \"${w}\"\n") uniqueWords);
      varFor =
        row:
        let
          varName = capitalize row.kind + "Statuses";
          items = builtins.concatStringsSep ", " (map constName row.statuses);
        in
        "// ${varName} is the ordered ${row.kind}-kind agent-emittable status set.\n"
        + "var ${varName} = []string{${items}}\n";
      varBlocks = builtins.concatStringsSep "\n" (map varFor outcomeStatusSets);
    in
    "// Code generated by nix/regen.nix from lib/prompt-contract.nix. DO NOT EDIT.\n"
    + "package outcome\n"
    + "\n"
    + "// Regenerate with `nix run .#regen` after editing lib/prompt-contract.nix's\n"
    + "// outcomeStatusSets.\n"
    + "\n"
    + "// Agent-emittable SPINDRIFT_OUTCOME status words (issue #2504). Host-side\n"
    + "// dispositions (failed, merge verification) are a separate typed family\n"
    + "// (ADR 0039) and are not represented here; `merged` is a tolerated\n"
    + "// off-script arm (cmd/launcher/internal/settle/gate.go) and is\n"
    + "// deliberately absent too. ResearchStatuses is the compiled-default\n"
    + "// research verdict vocabulary (see forge.ResearchVerdictLabels); the\n"
    + "// research vocabulary is operator-configurable via RESEARCH_VERDICTS.\n"
    + "const (\n"
    + constLines
    + ")\n"
    + "\n"
    + varBlocks;

  # cmd/launcher/internal/outcome/markerchannels_gen.go content: one unexported
  # Go const per lib/prompt-contract.nix markerChannels row, plus
  # MarkerChannelTokens, the ordered []string the caveman marker-exemption parity
  # test iterates instead of a hand-listed subset. Deliberately unexported and
  # non-colliding with outcome.go's hand-written consts -- outcome.go aliases its
  # exported consts to these values rather than redeclaring the literals.
  #
  # id -> Go identifier suffix is an explicit lookup table rather than a
  # mechanical camel-case derivation, because "pr-intent" needs the acronym
  # capitalization "PRIntent" that a generic heuristic would get wrong.
  renderMarkerChannelsGo =
    markerChannels:
    let
      idSuffix = {
        outcome = "Outcome";
        comment = "Comment";
        "pr-intent" = "PRIntent";
        "issue-intent" = "IssueIntent";
        "review-verdict" = "ReviewVerdict";
      };
      constName =
        row:
        "markerChannel"
        + (idSuffix.${row.id}
          or (throw "renderMarkerChannelsGo: markerChannels row id \"${row.id}\" has no idSuffix entry in lib/renderers.nix -- add one alongside the row")
        )
        + "Token";
      # builtins.toJSON, not a hand-wrapped "${row.token}", so a token containing
      # a quote or backslash still emits a valid Go string literal.
      constLines = concatStrings (
        map (row: "\t${constName row} = ${builtins.toJSON row.token}\n") markerChannels
      );
      items = builtins.concatStringsSep ",\n\t" (map constName markerChannels);
    in
    "// Code generated by nix/regen.nix from lib/prompt-contract.nix. DO NOT EDIT.\n"
    + "package outcome\n"
    + "\n"
    + "// Regenerate with `nix run .#regen` after editing lib/prompt-contract.nix's\n"
    + "// markerChannels.\n"
    + "//\n"
    + "// One row per marker channel (issue #2974, parent #2972): outcome, comment,\n"
    + "// pr-intent, issue-intent, review-verdict. Each channel's defense\n"
    + "// (structural / nonce / fold) and carrier are recorded in that Nix\n"
    + "// registry, the citable home for the trust model; only the token itself is\n"
    + "// rendered into Go.\n"
    + "const (\n"
    + constLines
    + ")\n"
    + "\n"
    + "// MarkerChannelTokens is the ordered list of every lib/prompt-contract.nix\n"
    + "// markerChannels registry token, iterated by the caveman marker-exemption\n"
    + "// parity test instead of a hand-listed subset.\n"
    + "var MarkerChannelTokens = []string{\n\t"
    + items
    + ",\n}\n";

  # Oxford-joined "a, b, or c" prose rendering of an outcomeStatusSets row's
  # statuses -- e.g. for agent/entrypoint.sh's nudge prompt.
  renderOutcomeStatusProse =
    statuses:
    let
      n = builtins.length statuses;
      allButLast = builtins.genList (i: builtins.elemAt statuses i) (n - 1);
      lastWord = builtins.elemAt statuses (n - 1);
    in
    if n <= 1 then
      concatStrings statuses
    else if n == 2 then
      "${builtins.elemAt statuses 0} or ${lastWord}"
    else
      builtins.concatStringsSep ", " allButLast + ", or " + lastWord;

  # Pipe-joined "a|b|c" grammar-placeholder rendering of the same, e.g. for a
  # `status=<...>` grammar example.
  renderOutcomeStatusPipe = statuses: builtins.concatStringsSep "|" statuses;

  # cmd/launcher/flagtable_gen.go content.
  renderFlagTableGo =
    schema:
    let
      nonSecretSchema = filterAttrs (_: e: !(e.secret or false)) schema;
      secretSchema = filterAttrs (_: e: (e.secret or false)) schema;
      flagAlias = e: if e ? alias then ", alias: \"${e.alias}\"" else "";
      flagDeprecatedAlias = e: if e ? flag then ", deprecatedAlias: \"${toKebab e.env}\"" else "";
      # The knob's valid-value enum, when the schema declares one — carried onto
      # the generated flag row so a Go guard can source it instead of a
      # hand-typed value list.
      flagChoices =
        e:
        if e ? choices && e.choices != [ ] then
          ", choices: []string{${builtins.concatStringsSep ", " (map (c: "\"${c}\"") e.choices)}}"
        else
          "";
      # The knob's derived domain-tree flake path — the flake surface IS the
      # domain tree (ADR 0037 Pass 1), so the settings path is the derived flake
      # path. Empty for a knob with no flake-settings surface (e.g. ISSUE_NUMBER).
      flagSettingsPath = key: e: if e.flakeOption or false then resolveNixPath key e else "";
      # Every non-secret knob must declare a group so the full reference groups
      # it under a heading; a missing group is a schema error, not a silent "".
      ungrouped = mapAttrsToList (k: _: k) (filterAttrs (_: e: !(e ? group)) nonSecretSchema);
      rows =
        if ungrouped != [ ] then
          throw "env-schema.nix: non-secret knob(s) missing `group`: ${builtins.concatStringsSep ", " ungrouped}"
        else
          concatStrings (
            mapAttrsToList (
              key: e:
              "\t{env: \"${e.env}\", flag: \"${flagName e}\", group: \"${e.group}\"${flagAlias e}${flagDeprecatedAlias e}, kind: \"${flagKind e}\", doc: \"${e.doc}\", dflt: \"${flagDflt e}\", settingsPath: \"${flagSettingsPath key e}\"${flagChoices e}},\n"
            ) nonSecretSchema
          );
      secretRows = concatStrings (
        mapAttrsToList (
          _: e:
          "\t{env: \"${e.env}\", doc: \"${e.doc}\", fileFlag: \"${toKebab e.env}-file\", cmdFlag: \"${toKebab e.env}-cmd\"},\n"
        ) secretSchema
      );
    in
    "// Code generated by mkHarness.nix from lib/env-schema.nix (schemaFlags,\n"
    + "// secretKnobs) and lib/renderers.nix (groupOrder). DO NOT EDIT.\n"
    + "package main\n"
    + "\n"
    + "// schemaFlags is the flag table derived from lib/env-schema.nix.\n"
    + "// Secret knobs are excluded from schemaFlags; see secretKnobs below.\n"
    + "// Run `nix flake check` after editing lib/env-schema.nix to regenerate.\n"
    + "//\n"
    + "// schemaFlags[].dflt is also the schema-level runtime defaults source,\n"
    + "// consumed as a fallback by schemaDefault() in cmd/launcher/main.go\n"
    + "// (ADR 0020 lets a loaded input document override it); the separate\n"
    + "// defaults table was consolidated away in issue #670.\n"
    + "var schemaFlags = []flagEntry{\n"
    + rows
    + "}\n"
    + "\n"
    + "// secretKnobs lists secret knobs that have no value flag.\n"
    + "// Callers must supply these via the environment or via --<fileFlag> path flag.\n"
    + "var secretKnobs = []secretKnob{\n"
    + secretRows
    + "}\n"
    + "\n"
    + "// groupOrder is the display order of flag-group headings in the full\n"
    + "// reference (printHelpFull and the man page): the six domains (ADR 0037).\n"
    + "// Every group used in env-schema.nix must appear here, or its flags would\n"
    + "// silently drop out of the full listing (guarded by\n"
    + "// TestGroupOrder_CoversEverySchemaGroup and launcher-flag-table).\n"
    + "var groupOrder = []string{${renderGoStringSlice groupOrder}}\n";

  # Which schema members belong to the launcher's host-config surface: not secret
  # and not boxEnvOnly, or explicitly hostConfig-overridden — mirrors
  # lib/env-schema.nix's hostConfig header doc. Feeds renderSchemaConfigGo below,
  # which the drift check calls too, so struct membership can't drift from it.
  isHostConfigMember =
    e: ((!(e.secret or false)) && !(e.boxEnvOnly or false)) || (e.hostConfig or false);

  # cmd/launcher/schemaconfig_gen.go content: an unexported schemaConfig struct
  # (embedded by value in config) plus its loader, one field/loader line per
  # host-config member. Emits unaligned Go; gofmt owns column alignment.
  renderSchemaConfigGo =
    schema:
    let
      members = filterAttrs (_: isHostConfigMember) schema;
      isFloatTyped = e: builtins.isFloat (e.default or null);
      # Single source of truth for the flag's Go-side type shape; goType and
      # loaderLine both dispatch on it rather than repeating the cascade.
      typeClass =
        e:
        if flagKind e == "bool" then
          "bool"
        else if flagKind e == "int" then
          "int"
        else if isFloatTyped e then
          "float"
        else
          "string";
      # Secrets are always string-typed in schemaConfig regardless of typeClass —
      # a secret ever becoming int/bool/float-typed must not silently mismatch
      # its os.Getenv loader.
      goType =
        e:
        if e.secret or false then
          "string"
        else if typeClass e == "float" then
          "float64"
        else
          typeClass e;
      fieldLine = key: e: "\t${key} ${goType e}\n";
      fields = concatStrings (mapAttrsToList fieldLine members);
      loaderLine =
        key: e:
        if e.hostDerived or false then
          ""
        else if e.secret or false then
          "\t\t${key}: os.Getenv(\"${e.env}\"),\n"
        else if typeClass e == "bool" then
          "\t\t${key}: getenvSchema(\"${e.env}\") != \"\",\n"
        else if typeClass e == "int" then
          "\t\t${key}: ${
            if e.intKind == "positive" then "atoiSchema" else "atoiNonnegSchema"
          }(\"${e.env}\"),\n"
        else if typeClass e == "float" then
          "\t\t${key}: floatNonnegSchema(\"${e.env}\"),\n"
        else if e.emptyDisables or false then
          "\t\t${key}: getenvSchemaPreserveEmpty(\"${e.env}\"),\n"
        else
          "\t\t${key}: getenvSchema(\"${e.env}\"),\n";
      loaderLines = concatStrings (mapAttrsToList loaderLine members);
      hasSecretMember = builtins.any (e: e.secret or false) (builtins.attrValues members);
      importBlock = if hasSecretMember then "\nimport \"os\"\n" else "";
    in
    "// Code generated by nix/regen.nix from lib/env-schema.nix. DO NOT EDIT.\n"
    + "package main\n"
    + importBlock
    + "\n"
    + "// schemaConfig is the generated counterpart to config's schema-derived\n"
    + "// members (issue #2364, #2365): one field per host-config schema member\n"
    + "// (lib/env-schema.nix — not secret and not boxEnvOnly, or hostConfig\n"
    + "// overridden). Embedded by value in config so a copy-and-mutate helper\n"
    + "// like applyDispatchKind can never alias the caller's config through\n"
    + "// this struct.\n"
    + "type schemaConfig struct {\n"
    + fields
    + "}\n"
    + "\n"
    + "// loadSchemaConfig reads schemaConfig's fields from the environment.\n"
    + "// hostDerived members get a struct field above but no loader line here\n"
    + "// — their loader is hand-written elsewhere (e.g. gitIdentityField).\n"
    + "// Secret members read the environment directly, never through the\n"
    + "// schema-default helper (a document-first path would open a\n"
    + "// Launcher-input secret-injection channel that does not exist today).\n"
    + "// Regenerate with `nix run .#regen` after editing lib/env-schema.nix.\n"
    + "func loadSchemaConfig() schemaConfig {\n"
    + "\treturn schemaConfig{\n"
    + loaderLines
    + "\t}\n"
    + "}\n";

  # cmd/launcher/internal/promptassembly/boxenv_gen.go content: a whole generated
  # file (the whole-file-diff pattern, not a splice span), one `Env{}` field
  # assignment per lib/promptassembly-boxenv.nix row, read straight from the
  # Box's OS-process environment. Emits unaligned Go; gofmt owns alignment.
  # Every other Env field is left at its zero value -- assembleprompt_cmd.go
  # layers those on from flags, since they were never env reads to begin with.
  renderPromptAssemblyBoxEnvGo =
    rows:
    let
      loaderLine =
        row:
        if row.kind == "presence" then
          "\t\t${row.field}: os.Getenv(\"${row.env}\") != \"\",\n"
        else if row.kind == "string" then
          "\t\t${row.field}: os.Getenv(\"${row.env}\"),\n"
        else if row.kind == "int" then
          "\t\t${row.field}: boxenvAtoi(os.Getenv(\"${row.env}\")),\n"
        else if row.kind == "equals1" then
          "\t\t${row.field}: os.Getenv(\"${row.env}\") == \"1\",\n"
        else
          throw "renderPromptAssemblyBoxEnvGo: row '${row.field}' has unknown kind '${row.kind}'";
      loaderLines = concatStrings (map loaderLine rows);
    in
    "// Code generated by nix/regen.nix from lib/promptassembly-boxenv.nix. DO NOT EDIT.\n"
    + "package promptassembly\n"
    + "\n"
    + "import (\n"
    + "\t\"os\"\n"
    + "\t\"strconv\"\n"
    + ")\n"
    + "\n"
    + "// EnvFromEnviron reads Env's 29 Box-env-sourced fields directly from the\n"
    + "// process environment (lib/promptassembly-boxenv.nix, issue #2979): fields\n"
    + "// driver-exec/assembleprompt_cmd.go previously populated from a\n"
    + "// hand-declared CLI flag that agent/entrypoint.sh forwarded 1:1 from the\n"
    + "// same env var, until this issue wired this function in and dropped\n"
    + "// those flags. Every other Env field -- the skill-baked/SkillsFound\n"
    + "// filesystem probes and the path-shaped CLI-flag inputs (prompt/contract\n"
    + "// file locations) -- is left at its zero value; assembleprompt_cmd.go\n"
    + "// still layers those on from flags, since they were never env reads to\n"
    + "// begin with.\n"
    + "// Regenerate with `nix run .#regen` after editing\n"
    + "// lib/promptassembly-boxenv.nix.\n"
    + "func EnvFromEnviron() Env {\n"
    + "\treturn Env{\n"
    + loaderLines
    + "\t}\n"
    + "}\n"
    + "\n"
    + "// boxenvAtoi parses an int-kind Box env var, degrading to 0 on empty or\n"
    + "// malformed input. Unlike cmd/launcher/main.go's atoiSchema, which falls\n"
    + "// back to a per-key schema default (intSchemaDefault), these 29 rows are\n"
    + "// deliberately outside lib/env-schema.nix (see\n"
    + "// lib/promptassembly-boxenv.nix's header) and so have no schema default\n"
    + "// to degrade to.\n"
    + "func boxenvAtoi(s string) int {\n"
    + "\tn, err := strconv.Atoi(s)\n"
    + "\tif err != nil {\n"
    + "\t\treturn 0\n"
    + "\t}\n"
    + "\treturn n\n"
    + "}\n";

  # Domain section order for docs/flake-options.md and
  # renderTemplateSettingsBlock's nested domain tree (ADR 0037): the first
  # segment of each flakeOption knob's derived flake path (its `group`).
  domainOrder = [
    "agents"
    "git"
    "issues"
    "forge"
    "dispatch"
    "infra"
  ];

  # docs/flake-options.md's structural-options section: the hand-declared
  # structural knobs plus byNameOption, documented from
  # lib/structural-options-doc.nix at their lib/structural-paths.nix domain-tree
  # paths. Same table style as renderFlakeOptionsDocFull's schema-generated
  # sections, but a "type" column instead of "env var" — structural options have
  # no env var, only a docType string. `doc` strings may be multi-line prose,
  # collapsed to one line here since an embedded newline would break the
  # markdown table's one-row-per-line shape.
  renderStructuralOptionsDoc =
    structuralOptionsDoc: structuralPaths:
    let
      # byName is nested under agents.models rather than being a top-level
      # structuralOptions knob, so it has no structuralPaths entry and its
      # doc-table path is hand-given here.
      byNameStructuralPath = "agents.models.byName";
      pathFor =
        name:
        if name == "byName" then
          byNameStructuralPath
        else
          builtins.concatStringsSep "." structuralPaths.${name};
      # docType (e.g. runtime's `"podman"` | `"docker"` | ...) is the field where
      # escapeCell matters most -- it reliably contains a literal "|".
      names = builtins.attrNames structuralPaths ++ [ "byName" ];
      sortedNames = builtins.sort (a: b: pathFor a < pathFor b) names;
      renderRow =
        name:
        let
          entry = structuralOptionsDoc.${name};
        in
        "| `perSystem.spindrift.${pathFor name}` | ${escapeCell entry.docType} | ${escapeCell entry.docDefault} | ${escapeCell (oneLine entry.doc)} |\n";
    in
    "## Structural options (`perSystem.spindrift`)\n\n"
    + "Hand-declared structural knobs (ADR 0037; issue #2572) — build-time\n"
    + "or otherwise non-schema-derived surfaces such as the Driver, roster,\n"
    + "and image contents. Same table shape as the sections above, but a\n"
    + "type column in place of env var: attr path, type, default, and\n"
    + "description.\n"
    + "\n"
    + "| attr path | type | default | description |\n"
    + "|---|---|---|---|\n"
    + concatStrings (map renderRow sortedNames)
    + "\n";

  # docs/flake-options.md's full content: the banner, then the schema-generated
  # sections (grouped by domain, ADR 0037), then the structural-options section.
  renderFlakeOptionsDocFull =
    schema: structuralOptionsDoc: structuralPaths:
    let
      flakeOptionEntries = filterAttrs (_: e: e.flakeOption or false) schema;
      flakeOptionNames = builtins.attrNames flakeOptionEntries;
      domainKnobs = domain: builtins.filter (n: flakeOptionEntries.${n}.group == domain) flakeOptionNames;
      renderDefault = entry: if entry ? default then "`${toString entry.default}`" else "—";
      renderRow =
        name:
        let
          entry = flakeOptionEntries.${name};
        in
        "| `perSystem.spindrift.${resolveNixPath name entry}` | `${entry.env}` | ${renderDefault entry} | ${escapeCell (oneLine entry.doc)} |\n";
      renderSection =
        domain:
        let
          knobs = builtins.sort (
            a: b: resolveNixPath a flakeOptionEntries.${a} < resolveNixPath b flakeOptionEntries.${b}
          ) (domainKnobs domain);
        in
        if knobs == [ ] then
          ""
        else
          "## ${upperFirst domain} (`perSystem.spindrift.${domain}`)\n\n"
          + "| attr path | env var | default | description |\n"
          + "|---|---|---|---|\n"
          + concatStrings (map renderRow knobs)
          + "\n";
    in
    "<!-- Code generated by nix/checks.nix from lib/env-schema.nix and lib/structural-options-doc.nix. DO NOT EDIT. -->\n"
    + "<!-- Regenerate: nix flake check -->\n"
    + "\n"
    + "# Flake options reference\n"
    + "\n"
    + "Consumer-tunable knobs live under `perSystem.spindrift.*`, grouped by\n"
    + "domain (ADR 0037); domains with no consumer-tunable knobs are omitted.\n"
    + "\n"
    + "Precedence at runtime: CLI flag > flake setting (via the Launcher input\n"
    + "document, ADR 0020) > baked default. A knob env var still wins over the\n"
    + "flake setting this release, but is deprecated and warns; env configures\n"
    + "only secrets and internal plumbing going forward.\n"
    + "See [`docs/reference.md`](reference.md) for the full option surface and runtime vars.\n"
    + "\n"
    + concatStrings (map renderSection domainOrder)
    + renderStructuralOptionsDoc structuralOptionsDoc structuralPaths;

  # share/bash-completion/completions/spindrift content: subcommand completion
  # for the first word, flag completion (incl. the --issue alias and secret
  # --*-file/--*-cmd flags) anywhere after it, and filename completion for a
  # --*-file flag's argument. zsh/fish crib this structure. Rendered fresh at
  # build time, no committed copy — same as renderManpageRoff below.
  renderBashCompletion =
    schema: subcommandRegistry:
    let
      nonSecret = builtins.filter (e: !(e.secret or false)) (builtins.attrValues schema);
      secretEntries = builtins.filter (e: e.secret or false) (builtins.attrValues schema);
      subcommands = map (s: s.name) subcommandRegistry;
      issuePositionalSubcommands = issueCompletionSubcommands subcommandRegistry;
      # Hardcoded like renderManpageRoff's DISPATCH FLAGS / SYNOPSIS sections:
      # dispatch's boolean flags and the top-level flags aren't schema entries.
      extraFlags = [
        "--no-build"
        "--yes"
        "--force"
        "--continuous"
        "--help"
        "--version"
        "--secret-cmd"
      ];
      knobFlags = map (e: "--" + flagName e) nonSecret;
      aliasFlags = builtins.concatMap (e: map (n: "--" + n) (secondaryFlagNames e)) nonSecret;
      fileFlags = map (e: "--" + toKebab e.env + "-file") secretEntries;
      cmdFlags = map (e: "--" + toKebab e.env + "-cmd") secretEntries;
      allFlags = builtins.concatStringsSep " " (
        knobFlags ++ aliasFlags ++ fileFlags ++ cmdFlags ++ extraFlags
      );
      allSubcommands = builtins.concatStringsSep " " subcommands;
      # A `case "$prev" in ) ... esac` (empty pattern) is a syntax error, so the
      # file-flag branch is omitted entirely if the schema has no secret knobs.
      fileFlagBranch =
        if fileFlags == [ ] then
          ""
        else
          ''
            case "$prev" in
              ${builtins.concatStringsSep "|" fileFlags})
                # shellcheck disable=SC2207 # COMPREPLY split-on-space is the standard completion idiom; mapfile needs bash 4+
                COMPREPLY=($(compgen -f -- "$cur"))
                return 0
                ;;
            esac

          '';
      # A flag carrying `choices` completes to that value list as its argument
      # instead of falling through to the flag-name/file branches below; one case
      # arm per flag since each has its own list. An `alias` matches in the same
      # arm — bash `case` supports `|`-separated patterns.
      choicesKnobs = builtins.filter (e: e ? choices) nonSecret;
      choicesFlagBranch =
        if choicesKnobs == [ ] then
          ""
        else
          ''
            case "$prev" in
            ${
              concatStrings (
                map (e: ''
                  ${builtins.concatStringsSep "|" (choicesFlagPatterns e)})
                    # shellcheck disable=SC2207 # COMPREPLY split-on-space is the standard completion idiom; mapfile needs bash 4+
                    COMPREPLY=($(compgen -W "${builtins.concatStringsSep " " e.choices}" -- "$cur"))
                    return 0
                    ;;
                '') choicesKnobs
              )
            }esac

          '';
      # Dynamic positional issue-number completion: shell out to the hidden
      # `__complete-issues` subcommand (the same discovery seam dispatch uses)
      # and offer its candidate numbers, dropping each line's title since bash's
      # compgen -W carries no per-candidate description (zsh/fish keep it).
      # Silenced stderr plus the subcommand's own bounded timeout mean a slow,
      # offline, or erroring query degrades to zero candidates rather than
      # blocking the completion.
      issueCompletionBranch = ''
        case "''${COMP_WORDS[1]}" in
          ${builtins.concatStringsSep "|" issuePositionalSubcommands})
            # shellcheck disable=SC2207 # COMPREPLY split-on-space is the standard completion idiom; mapfile needs bash 4+
            COMPREPLY=($(compgen -W "$(spindrift __complete-issues 2>/dev/null | cut -f1)" -- "$cur"))
            ;;
        esac
      '';
    in
    ''
      # Code generated by lib/renderers.nix renderBashCompletion from
      # lib/env-schema.nix. DO NOT EDIT.
      # Rendered fresh at build time (lib/mkHarness.nix); no committed copy —
      # regenerate by rebuilding, not `nix run .#regen`.
      _spindrift() {
        local cur prev
        COMPREPLY=()
        cur="''${COMP_WORDS[COMP_CWORD]}"
        prev="''${COMP_WORDS[COMP_CWORD - 1]}"

        ${fileFlagBranch}${choicesFlagBranch}if [[ "$cur" == -* ]]; then
          # shellcheck disable=SC2207 # COMPREPLY split-on-space is the standard completion idiom; mapfile needs bash 4+
          COMPREPLY=($(compgen -W "${allFlags}" -- "$cur"))
          return 0
        fi

        if [[ $COMP_CWORD -eq 1 ]]; then
          # shellcheck disable=SC2207 # COMPREPLY split-on-space is the standard completion idiom; mapfile needs bash 4+
          COMPREPLY=($(compgen -W "${allSubcommands}" -- "$cur"))
          return 0
        fi

        ${issueCompletionBranch}
      }
      complete -F _spindrift spindrift
    '';

  # share/fish/vendor_completions.d/spindrift.fish content: same coverage as
  # renderBashCompletion above, in fish's `complete -c` syntax, with each flag's
  # schema doc string as its `-d` description.
  renderFishCompletion =
    schema: subcommandRegistry:
    let
      nonSecret = builtins.filter (e: !(e.secret or false)) (builtins.attrValues schema);
      secretEntries = builtins.filter (e: e.secret or false) (builtins.attrValues schema);
      subcommands = map (s: s.name) subcommandRegistry;
      issuePositionalSubcommands = issueCompletionSubcommands subcommandRegistry;
      extraFlags = [
        {
          flag = "no-build";
          doc = "fail fast if the image is absent instead of building; pair with 'spindrift build' for split build/run flows";
        }
        {
          flag = "yes";
          doc = "skip confirmation prompt when dispatching unlabeled issues (alias: --force)";
        }
        {
          flag = "force";
          doc = "skip confirmation prompt when dispatching unlabeled issues (alias: --yes)";
        }
        {
          flag = "continuous";
          doc = "bare-flag alias for --continuous-dispatch 1 (which stays available, deprecated)";
        }
        {
          flag = "help";
          doc = "show usage and exit";
        }
        {
          flag = "version";
          doc = "show version and exit";
        }
        {
          flag = "secret-cmd";
          doc = "templated fetch command for any secret with none of its own set; {name} substitutes the secret's kebab-case env name (sibling SECRET_CMD env var; lowest precedence)";
        }
      ];
      subcommandCompletions = builtins.concatStringsSep "\n" (
        map (s: "complete -c spindrift -n '__fish_use_subcommand' -f -a '${s}'") subcommands
      );
      # A flag carrying `choices` restricts its argument to that value list
      # (-x: require a value, no file completion). No schema entry pairs `alias`
      # with `choices`, so only knobCompletions uses it.
      choicesArgs = e: " -x -a '${builtins.concatStringsSep " " e.choices}'";
      flagArgs = e: if e ? choices then choicesArgs e else "";
      knobCompletions = builtins.concatStringsSep "\n" (
        map (e: "complete -c spindrift -l ${flagName e} -d \"${e.doc}\"${flagArgs e}") nonSecret
      );
      aliasCompletions = builtins.concatStringsSep "\n" (
        builtins.concatMap (
          e: map (n: "complete -c spindrift -l ${n} -d \"${e.doc}\"${flagArgs e}") (secondaryFlagNames e)
        ) nonSecret
      );
      fileCompletions = builtins.concatStringsSep "\n" (
        map (e: "complete -c spindrift -l ${toKebab e.env}-file -r -F -d \"${e.doc}\"") secretEntries
      );
      cmdCompletions = builtins.concatStringsSep "\n" (
        map (e: "complete -c spindrift -l ${toKebab e.env}-cmd -d \"${e.doc}\"") secretEntries
      );
      extraCompletions = builtins.concatStringsSep "\n" (
        map (e: "complete -c spindrift -l ${e.flag} -d \"${e.doc}\"") extraFlags
      );
      # Same as bash's, but fish's `complete -a` auto-splits a tab-separated
      # candidate into value and description, so `__complete-issues`'s
      # `<number>\t<title>` lines need no reformatting here.
      issueCompletion = "complete -c spindrift -n '__fish_seen_subcommand_from ${builtins.concatStringsSep " " issuePositionalSubcommands}' -f -a '(spindrift __complete-issues 2>/dev/null)'";
    in
    ''
      # Code generated by lib/renderers.nix renderFishCompletion from
      # lib/env-schema.nix. DO NOT EDIT.
      # Rendered fresh at build time (lib/mkHarness.nix); no committed copy —
      # regenerate by rebuilding, not `nix run .#regen`.
      ${subcommandCompletions}
      ${knobCompletions}
      ${aliasCompletions}
      ${fileCompletions}
      ${cmdCompletions}
      ${extraCompletions}
      ${issueCompletion}
    '';

  # share/zsh/site-functions/_spindrift content: same coverage as
  # renderBashCompletion, plus a per-candidate description — zsh carries them,
  # bash's compgen -W doesn't — sourced from each flag's schema `doc` string.
  renderZshCompletion =
    schema: subcommandRegistry:
    let
      nonSecret = builtins.filter (e: !(e.secret or false)) (builtins.attrValues schema);
      secretEntries = builtins.filter (e: e.secret or false) (builtins.attrValues schema);
      subcommands = subcommandRegistry;
      issuePositionalSubcommands = issueCompletionSubcommands subcommandRegistry;
      # A `_describe` array entry is 'completion:description', colon-split on the
      # first colon. Inside the surrounding single-quoted zsh string only an
      # embedded "'" needs escaping — and a literal '\', so the quote-escape this
      # function inserts is never itself re-escaped. Hence backslash first.
      zshEsc = s: builtins.replaceStrings [ "\\" "'" ] [ "\\\\" "'\\''" ] s;
      subcommandSpecs = map (s: "    '${s.name}:${zshEsc s.doc}'\n") subcommands;
      knobSpec = e: "    '--${flagName e}:${zshEsc e.doc}'\n";
      secondarySpec = e: concatStrings (map (n: "    '--${n}:${zshEsc e.doc}'\n") (secondaryFlagNames e));
      fileSpec = e: "    '--${toKebab e.env}-file:${zshEsc e.doc}'\n";
      cmdSpec = e: "    '--${toKebab e.env}-cmd:${zshEsc e.doc}'\n";
      fileFlags = map (e: "--" + toKebab e.env + "-file") secretEntries;
      extraFlagSpecs = [
        "    '--no-build:fail fast if the image is absent instead of building it'\n"
        "    '--yes:skip the confirmation prompt when dispatching unlabeled issues'\n"
        "    '--force:alias for --yes'\n"
        "    '--continuous:bare-flag alias for --continuous-dispatch 1 (which stays available, deprecated)'\n"
        "    '--help:show usage'\n"
        "    '--version:show version'\n"
        "    '--secret-cmd:templated secret-fetch command; {name} substitutes the kebab-case env name (lowest precedence)'\n"
      ];
      allFlagSpecs = concatStrings (
        map knobSpec nonSecret
        ++ map secondarySpec nonSecret
        ++ map fileSpec secretEntries
        ++ map cmdSpec secretEntries
        ++ extraFlagSpecs
      );
      allSubcommandSpecs = concatStrings subcommandSpecs;
      # A `case "$prev" in ) ... esac` (empty pattern) is a syntax error, so the
      # file-flag branch is omitted entirely if the schema has no secret knobs.
      fileFlagBranch =
        if fileFlags == [ ] then
          ""
        else
          ''
            case "$prev" in
              ${builtins.concatStringsSep "|" fileFlags})
                _files
                return
                ;;
            esac

          '';
      # Mirrors renderBashCompletion's choicesFlagBranch, including its `alias`
      # handling.
      choicesKnobs = builtins.filter (e: e ? choices) nonSecret;
      choicesFlagBranch =
        if choicesKnobs == [ ] then
          ""
        else
          ''
            case "$prev" in
            ${
              concatStrings (
                map (e: ''
                  ${builtins.concatStringsSep "|" (choicesFlagPatterns e)})
                    compadd -- ${builtins.concatStringsSep " " e.choices}
                    return
                    ;;
                '') choicesKnobs
              )
            }esac

          '';
      # Same as bash's, but offering `number:title` candidates via _describe so
      # zsh keeps the per-candidate description compgen -W can't carry.
      issueCompletionBranch = ''
        if (( CURRENT >= 3 )); then
          case "''${words[2]}" in
            ${builtins.concatStringsSep "|" issuePositionalSubcommands})
              local -a issue_candidates
              local num title
              while IFS=$'\t' read -r num title; do
                [[ -n "$num" ]] && issue_candidates+=("$num:$title")
              done < <(spindrift __complete-issues 2>/dev/null)
              _describe -t issues 'spindrift issue' issue_candidates
              return
              ;;
          esac
        fi
      '';
    in
    ''
      #compdef spindrift
      # Code generated by lib/renderers.nix renderZshCompletion from
      # lib/env-schema.nix. DO NOT EDIT.
      # Rendered fresh at build time (lib/mkHarness.nix); no committed copy —
      # regenerate by rebuilding, not `nix run .#regen`.
      _spindrift() {
        local -a subcommands
        subcommands=(
      ${allSubcommandSpecs}  )

        local -a flags
        flags=(
      ${allFlagSpecs}  )

        local cur="''${words[CURRENT]}" prev="''${words[CURRENT-1]}"

        ${fileFlagBranch}${choicesFlagBranch}if [[ "$cur" == -* ]]; then
          _describe -t options 'spindrift flag' flags
          return
        fi

        if (( CURRENT == 2 )); then
          _describe -t subcommands 'spindrift subcommand' subcommands
          return
        fi

        ${issueCompletionBranch}
      }
    '';

  # `man spindrift` roff content: the full flag reference that keeps
  # `spindrift --help` (cmd/launcher/flags.go printHelp) concise.
  renderManpageRoff =
    schema: spindriftVersion: subcommands:
    let
      nonSecret = builtins.filter (e: !(e.secret or false)) (builtins.attrValues schema);
      secretEntries = builtins.filter (e: e.secret or false) (builtins.attrValues schema);
      esc = builtins.replaceStrings [ "\\" ] [ "\\\\" ]; # neutralise stray backslashes
      escFlag = f: builtins.replaceStrings [ "-" ] [ "\\-" ] f; # roff renders \- as a minus
      unknownGroups = builtins.filter (g: !(builtins.elem g groupOrder)) (
        map (e: e.group or "") nonSecret
      );
      subcommandBlock =
        s:
        let
          usage = if s.usage == "" then "" else " " + escFlag s.usage;
        in
        ".TP\n.B ${s.name}${usage}\n\\&${esc s.doc}\n";
      optionBlock =
        e:
        let
          # Canonical first, then any live alias, then the deprecated old name
          # tagged "(deprecated)" — parity with `--help --all`'s flag column, so
          # a reader can tell the retired spelling from the supported forms.
          renderName = n: "\\-\\-" + escFlag n;
          names = builtins.concatStringsSep ", " (
            [ (renderName (flagName e)) ]
            ++ map renderName (liveFlagAliases e)
            ++ map (n: renderName n + " (deprecated)") (deprecatedFlagAliases e)
          );
          dflt = flagDflt e;
          dfltSentence = if dflt == "" then "No default." else "Default: " + esc dflt + ".";
          # A presence-style bool flag takes no value, so render its name with no
          # italic type placeholder.
          typeToken = if flagKind e == "bool" then "" else " \\fI${flagKind e}\\fR";
        in
        ".TP\n.B ${names}${typeToken}\n\\&${esc e.doc}. ${dfltSentence}\n";
      groupSection =
        g:
        let
          entries = builtins.sort (a: b: a.env < b.env) (builtins.filter (e: (e.group or "") == g) nonSecret);
        in
        if entries == [ ] then "" else ".SS ${g}\n" + concatStrings (map optionBlock entries);
      secretBlock =
        e:
        ".TP\n.B ${e.env}\n\\&${esc e.doc}. Supply via the environment, via \\-\\-${toKebab e.env}\\-file (reads the value from a file path), or via \\-\\-${toKebab e.env}\\-cmd / ${e.env}_CMD (fetches the value from a command's stdout \\(em the preferred, external\\-vault form). Precedence, first non\\-empty wins: \\-\\-${toKebab e.env}\\-cmd flag, then ${e.env}_CMD env, then \\-\\-${toKebab e.env}\\-file flag, then the direct ${e.env} environment variable. When the launcher's own stdin and stderr are both TTYs, a \\-\\-${toKebab e.env}\\-cmd / ${e.env}_CMD command inherits them as raw file descriptors so a vault tool's own unlock prompt reaches the terminal; non\\-interactively (CI, a pipe), stderr stays discarded and stdin stays unattached. A command that still fails aborts with a message naming ${e.env}, the exit code on a non\\-zero exit, and a generic unlock hint (e.g. \\(lqyour vault may be locked; unlock it (e.g. \\(oqrbw unlock\\(cq) and re\\-run\\(rq) \\(em never the command string, its stdout, or its stderr.\n";
    in
    if unknownGroups != [ ] then
      throw "renderManpageRoff: knob group(s) absent from groupOrder: ${builtins.concatStringsSep ", " unknownGroups}"
    else
      ''
        .TH SPINDRIFT 1 "${spindriftVersion}" "spindrift ${spindriftVersion}" "Spindrift Manual"
        .SH NAME
        spindrift \- launch waves of headless coding agents, one container per issue
        .SH SYNOPSIS
        .B spindrift
        [\fIflags\fR] \fIsubcommand\fR [\fIargs\fR]
        .SH DESCRIPTION
        .B spindrift
        dispatches one disposable, nix-built container per issue, runs a headless
        coding agent inside it, and drives each resulting pull request through a
        merge gate. Issues come from GitHub, Jira, Forgejo, or a purely local
        tracker; pull requests go to GitHub, Forgejo, a plain git remote, or a
        local bundle. The agent CLI is a swappable Driver \(em Claude Code or
        opencode. Every runtime knob is set by flag, by the Consumer
        flake's settings, or by baked default, in that precedence order; a knob
        env var still wins over the flake setting this release, deprecated and
        warned on (ADR 0020). Secrets are read from the environment or from a
        gitignored
        .I harness.env
        in the working directory.
        .SH SUBCOMMANDS
        ${concatStrings (map subcommandBlock subcommands)}.SH "DISPATCH FLAGS"
        .TP
        .B \-\-no-build
        Fail fast if the image is absent instead of building it; pair with
        .B spindrift build
        for split build/run flows.
        .TP
        .B \-\-yes
        Skip the confirmation prompt when dispatching unlabeled issues. Alias:
        .BR \-\-force .
        .TP
        .B \-\-continuous
        Bare-flag alias for
        .B \-\-continuous-dispatch 1
        (which stays available, deprecated).
        .SH OPTIONS
        Flags take precedence over the Consumer flake's settings (carried by the
        Launcher input document, ADR 0020), which take precedence over baked
        defaults. A knob env var still wins over the flake setting this release,
        deprecated and warned on; the next release makes it an error.
        ${concatStrings (map groupSection groupOrder)}.SH ENVIRONMENT
        Secret knobs are never exposed as value flags; they are read from the
        environment, from a file via their
        .B \-\-<name>-file
        flag, or fetched from an external command via their
        .B \-\-<name>-cmd
        flag or
        .B <NAME>_CMD
        environment variable \(em the preferred way to supply secrets, since
        it keeps plaintext credentials off disk. A single
        .B \-\-secret\-cmd
        flag or
        .B SECRET_CMD
        environment variable sets a templated fetch command tried as a
        lowest\-precedence fallback for any secret above with none of its
        own forms set;
        .I {name}
        substitutes that secret's kebab\-case env name (e.g.
        .B GH_TOKEN
        becomes
        .BR gh\-token ),
        so
        .B SECRET_CMD=\(dqrbw get spindrift\-{name}\(dq
        covers every secret whose vault item follows that one uniform
        naming scheme; a per\-secret form still wins over it, and it is
        tried only for a secret the run actually needs.
        ${concatStrings (map secretBlock secretEntries)}.SH FILES
        .TP
        .I harness.env
        Gitignored per-checkout config and secrets, sourced from the working
        directory at dispatch time.
        .SH EXAMPLES
        .TP
        Dispatch every ready issue, three containers at a time:
        .B spindrift dispatch \-\-max-parallel 3
        .TP
        Dispatch a single issue, skipping the image build:
        .B spindrift dispatch \-\-no-build 42
        .TP
        Preview the queue without launching anything:
        .B spindrift preview
        .TP
        Print the full flag reference in the terminal:
        .B spindrift \-\-help \-\-all
        .SH "SEE ALSO"
        .BR git (1),
        .BR gh (1)
      '';
}
