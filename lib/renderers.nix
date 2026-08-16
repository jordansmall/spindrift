# Shared render functions for the artifacts generated from lib/env-schema.nix,
# and the owner of the flag-group section taxonomy (groupOrder).
# nix/checks/schema-drift.nix (drift guards) and nix/regen.nix (the one-shot
# regenerator, `nix run .#regen`) call these — one renderer per artifact — so
# the guard and the regenerator can never drift from each other (issue #402).
# lib/mkHarness.nix and lib/flakeModule.nix import this file for the taxonomy
# and the man-page renderer, for the same reason (issue #461).
#
# Pure builtins only (no `pkgs.lib`): keeps this file evaluable and unit-
# testable with a bare `nix eval`, without needing a locked nixpkgs.
let
  mapAttrsToList = f: attrs: map (n: f n attrs.${n}) (builtins.attrNames attrs);
  filterAttrs =
    pred: attrs:
    builtins.listToAttrs (
      map (n: {
        name = n;
        value = attrs.${n};
      }) (builtins.filter (n: pred n attrs.${n}) (builtins.attrNames attrs))
    );
  concatStrings = builtins.concatStringsSep "";
  # ASCII-only; every caller here feeds it a SCREAMING_SNAKE_CASE env var name.
  chars = s: builtins.genList (i: builtins.substring i 1 s) (builtins.stringLength s);
  toLower = builtins.replaceStrings (chars "ABCDEFGHIJKLMNOPQRSTUVWXYZ") (
    chars "abcdefghijklmnopqrstuvwxyz"
  );
  toUpper = builtins.replaceStrings (chars "abcdefghijklmnopqrstuvwxyz") (
    chars "ABCDEFGHIJKLMNOPQRSTUVWXYZ"
  );
  # A resolved flake path (e.g. "git.merge.policy", derived by resolveNixPath
  # from a knob's group + optional nixSubPath — lib/nixpath.nix) -> its
  # dot-separated segments. Shared by renderTemplateSettingsBlock (builds the
  # nested domain-tree example) and renderFlakeOptionsDoc (groups by the first
  # segment, the domain) — ADR 0037.
  splitNixPath = path: builtins.filter builtins.isString (builtins.split "\\." path);
  resolveNixPath = import ./nixpath.nix;
  # A Go `[]string{"a", "b"}` literal's inner comma-joined, quoted contents.
  # Shared by every renderer emitting a flat string-slice var (groupOrder,
  # nixDriverNames, validRuntimeChoices) so the quote/join shape lives once.
  renderGoStringSlice = items: builtins.concatStringsSep ", " (map (s: "\"${s}\"") items);
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

  # Schema entry -> the type token the flag table and man page print.
  # A knob opts into the presence-style bool kind explicitly, with `kind =
  # "bool";` (issue #2145) — the CLI parses it by presence, not a following
  # value. It is deliberately not inferred from a boolean `default`: several
  # knobs already carry `default = false` purely to render as a `types.bool`
  # flake option (lib/flakeModule.nix), while their CLI flag stays a
  # space-separated value form with in-repo callers (e.g. dogfood.sh);
  # inferring bool from the default would silently flip all of them. Each
  # converts on its own ticket, migrating its callers atomically —
  # --continuous-dispatch was one such knob until issue #2147 converted it
  # to kind = "bool" and retired its hand-rolled --continuous passthrough in
  # favour of the schema alias.
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
  # cmd/launcher/flagtable_gen.go by renderFlagTableGo below, so the Go copy
  # can never drift from this one (issue #2523).
  groupOrder = [
    "agents"
    "git"
    "issues"
    "forge"
    "dispatch"
    "infra"
  ];

  # Subcommands that take a positional issue-number argument spindrift can
  # dynamically complete (issue #556): those whose lib/subcommands.nix entry
  # sets dynamicIssueCompletion = true — the same set discoverIssues'
  # label-query branch backs (`spindrift __complete-issues`). `research` also
  # takes an issue list (see its usage string) but is deliberately excluded
  # — issue #556 scopes dynamic completion to dispatch/preview/recover only,
  # and #1603 folded the gate into the registry itself (rather than a field
  # like `acceptsIssueArg` that would conflate "takes issue args" with "gets
  # dynamic completion" and invite research back in by mistake). Shared by
  # all three renderers below, each of which already receives
  # subcommandRegistry as an argument, and by nix/checks/schema-drift.nix's
  # coverage guard, so the gated-subcommand set can't drift between the
  # renderer and its check.
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
  # example, between its BEGIN/END GENERATED SETTINGS EXAMPLE markers: every
  # flakeOption knob, rendered as a nested domain tree keyed by its derived
  # flake path (group + optional nixSubPath — lib/nixpath.nix)
  # (ADR 0037; issue #2179 — supersedes the flat groupToAttr/groupOrder
  # `settings = { ... }` shape), with its doc string, so a new knob is
  # discoverable in the template without a hand-edit (issue #520).
  renderTemplateSettingsBlock =
    schema:
    let
      ind = "            # ";
      flakeOptionEntries = filterAttrs (_: e: e.flakeOption or false) schema;
      # Mirrors renderHarnessEnvExample's value rule (placeholder only for a
      # required knob) so a knob whose placeholder exists solely for the
      # bats fixture (e.g. gitUserName's "Test Bot") renders as "" here, not
      # as a fake identity in consumer-facing documentation.
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
      # Insert one schema entry into the nested domain tree at its derived
      # flake path, e.g. "agents.models.filer" -> tree.agents.models.filer.
      # Each leaf is
      # tagged (__leaf) so renderNode below can tell a schema entry apart
      # from a plain namespace node, even though both are attrsets.
      insertLeaf =
        tree: segs: entry:
        let
          seg = builtins.head segs;
          rest = builtins.tail segs;
        in
        if rest == [ ] then
          tree
          // {
            ${seg} = {
              __leaf = true;
              inherit entry;
            };
          }
        else
          tree // { ${seg} = insertLeaf (tree.${seg} or { }) rest entry; };
      domainTree = builtins.foldl' (
        acc: key:
        insertLeaf acc (splitNixPath (
          resolveNixPath key flakeOptionEntries.${key}
        )) flakeOptionEntries.${key}
      ) { } (builtins.attrNames flakeOptionEntries);
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
              "${ind}${pad}# ${child.entry.doc}\n" + "${ind}${pad}${name} = ${nixLiteral child.entry};\n"
            else
              "${ind}${pad}${name} = {\n" + renderNode (depth + 1) child + "${ind}${pad}};\n"
          ) node
        );
    in
    renderNode 0 domainTree;

  # templates/default/harness.env.example content: secrets only (ADR 0020).
  # Every other knob flows through the Launcher input document, seeded by
  # flake `settings` and overridable per-run by an explicit CLI flag; env
  # (including harness.env) configures nothing but secrets from #625 onward,
  # so an example file listing non-secret knobs would advertise a channel
  # that's deprecated the moment an operator uses it.
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
  # variable per lib/default-model-fixture.nix leaf, source-able directly from
  # a bats test (unlike renderSetBoxEnvFixture's set_box_env, this is a static
  # fixture of expected values, not environment to inject into a run, so it is
  # not wrapped in a function). schemaDefaults.{model,scoutModel,reviewModel,
  # filerModel,workerModel} become DEFAULT_MODEL/DEFAULT_SCOUT_MODEL/
  # DEFAULT_REVIEW_MODEL/DEFAULT_FILER_MODEL/DEFAULT_WORKER_MODEL, in that
  # order; dogfoodPins.filer becomes DOGFOOD_FILER_MODEL (issue #2514).
  renderDefaultModelFixtureBash =
    fixture:
    let
      inherit (fixture) schemaDefaults dogfoodPins;
    in
    "# Code generated by lib/renderers.nix renderDefaultModelFixtureBash from\n"
    + "# lib/default-model-fixture.nix. DO NOT EDIT.\n"
    + "# Regenerate with `nix run .#regen` after editing lib/default-model-fixture.nix.\n"
    + "\n"
    + "# The regen-rendered bats fixture form of lib/default-model-fixture.nix's\n"
    + "# schemaDefaults and dogfoodPins.filer, so a bats test asserts against this\n"
    + "# fixture's variables instead of hand-typing the expected default model\n"
    + "# literal at each assertion site (issue #2514).\n"
    + "export DEFAULT_MODEL=\"${schemaDefaults.model}\"\n"
    + "export DEFAULT_SCOUT_MODEL=\"${schemaDefaults.scoutModel}\"\n"
    + "export DEFAULT_REVIEW_MODEL=\"${schemaDefaults.reviewModel}\"\n"
    + "export DEFAULT_FILER_MODEL=\"${schemaDefaults.filerModel}\"\n"
    + "export DEFAULT_WORKER_MODEL=\"${schemaDefaults.workerModel}\"\n"
    + "export DOGFOOD_FILER_MODEL=\"${dogfoodPins.filer}\"\n";

  # cmd/launcher/defaultmodels_gen_test.go content: the regen-rendered Go form of
  # lib/default-model-fixture.nix, keyed by the schema's own env-var names
  # (lib/env-schema.nix's model/scoutModel/reviewModel/filerModel/workerModel
  # entries' `env` fields) so a launcher test asserts against this fixture
  # instead of hand-typing the expected default model literal (issue #2514).
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

  # docs/reference.md's generated "Default models" table body (issue #2514
  # AC2): a Markdown table row per lib/default-model-fixture.nix
  # schemaDefaults leaf (model/scoutModel/reviewModel/filerModel/
  # workerModel), so the doc's default-model statements regenerate from the
  # same fixture the bats/Go forms above do instead of drifting as
  # hand-typed prose. filerModel renders specially since its schema default
  # is the empty string -- the parenthetical instead states dogfoodPins.filer,
  # the value spindrift's own dogfood Consumer config pins locally. No
  # surrounding heading -- that stays hand-written in docs/reference.md; only
  # the table itself is generated.
  renderDefaultModelsDoc =
    fixture:
    let
      inherit (fixture) schemaDefaults dogfoodPins;
      filerCell = "*(empty; dogfood pins \`${dogfoodPins.filer}\`)*";
    in
    "| Agent | Default model |\n"
    + "| --- | --- |\n"
    + "| \`MODEL\` (coordinator) | \`${schemaDefaults.model}\` |\n"
    + "| \`scout\` | \`${schemaDefaults.scoutModel}\` |\n"
    + "| \`reviewer\` | \`${schemaDefaults.reviewModel}\` |\n"
    + "| \`filer\` | ${filerCell} |\n"
    + "| \`worker\` | \`${schemaDefaults.workerModel}\` |\n";

  # cmd/launcher/internal/driver/drivernames_gen.go content. driverEntries is
  # the registry's `entries` attrset (name -> Driver entry), not the whole
  # registry -- the registry also exports its shape-assertion and rendering
  # functions (issue #624), which are not Driver names.
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

  # cmd/launcher/internal/backend/registry_gen.go content (issue #2521):
  # one Go `Descriptor` var per lib/backends/default.nix row (keyed by its
  # goVar field), plus a Registry slice listing those vars in the nix list's
  # declaration order (load-bearing -- see that file's header). Emits
  # unaligned Go, like renderSchemaConfigGo/renderOutcomeStatusGo -- gofmt
  # (via `nix run .#regen` and this renderer's drift check) owns struct-
  # literal column alignment, not this function. Only emits a field line for
  # a Go-truthy value (non-empty string, or bool true), mirroring the
  # hand-written struct literals it replaces, which never wrote e.g.
  # `ValidAsTracker: false,`.
  renderBackendRegistryGo =
    backends:
    let
      # Every field lib/backends/default.nix's header documents -- a row
      # attribute outside this set is a typo (e.g. a misspelled field name),
      # not a new fact, and must fail the build rather than render silently
      # as if the field were never set (mirrors lib/drivers/default.nix's
      # assertShape, but catching an extra attribute instead of a missing
      # one).
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

  # cmd/launcher/quickstart/quickstart_runtime_gen.go content.
  renderQuickstartRuntimeGo =
    runtimeValues:
    "// Code generated by nix/regen.nix from lib/runtime-values.nix. DO NOT EDIT.\n"
    + "package main\n"
    + "\n"
    + "// validRuntimeChoices are the operator-facing runtime values the wizard\n"
    + "// accepts (lib/runtime-values.nix), the same list lib/flakeModule.nix's\n"
    + "// `runtime` option enum uses.\n"
    + "// Regenerate with `nix run .#regen` after editing lib/runtime-values.nix.\n"
    + "var validRuntimeChoices = []string{"
    + renderGoStringSlice runtimeValues
    + "}\n";

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

  # cmd/launcher/internal/outcome/status_gen.go content (issue #2504): typed
  # Go constants + ordered var slices for every lib/prompt-contract.nix
  # outcomeStatusSets row. One `const` per unique status word across all
  # kinds (a word shared between kinds, e.g. "blocked", gets exactly one Go
  # identifier), plus one exported []string var per kind in row order.
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

  # Oxford-joined "a, b, or c" prose rendering of an outcomeStatusSets row's
  # statuses (issue #2504) -- e.g. for agent/entrypoint.sh's nudge prompt.
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

  # Pipe-joined "a|b|c" grammar-placeholder rendering of an
  # outcomeStatusSets row's statuses (issue #2504) -- e.g. for a
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
      # The knob's valid-value enum, when the schema declares one (e.g.
      # mergeMode's choices = [ "immediate" "auto" "manual" ]) — carried onto
      # the generated flag row so a later slice can source a generic Go guard
      # from it instead of a hand-typed value list (issue #2520).
      flagChoices =
        e:
        if e ? choices && e.choices != [ ] then
          ", choices: []string{${builtins.concatStringsSep ", " (map (c: "\"${c}\"") e.choices)}}"
        else
          "";
      # The knob's derived domain-tree flake path (e.g. "git.merge.policy",
      # via resolveNixPath — lib/nixpath.nix) — the flake surface is now the
      # domain tree (ADR 0037 Pass 1), so the settings path IS the knob's
      # derived flake path — the provenance warning's second migration
      # target (ADR 0020), alongside the flag every non-secret knob already
      # carries. Empty for a knob with no flake-settings surface (e.g.
      # ISSUE_NUMBER, SPINDRIFT_PROMPT_DIR).
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

  # Which schema members belong to the launcher's host-config surface: not
  # secret and not boxEnvOnly, or explicitly hostConfig-overridden (the model
  # knob plus the six host-held secrets) — mirrors lib/env-schema.nix's
  # hostConfig header doc. Feeds renderSchemaConfigGo below, which its
  # drift check (nix/checks/schema-drift.nix) calls too, so struct
  # membership can't drift from the check.
  isHostConfigMember =
    e: ((!(e.secret or false)) && !(e.boxEnvOnly or false)) || (e.hostConfig or false);

  # cmd/launcher/schemaconfig_gen.go content: config's schema-derived
  # members (issue #2364), embedded by value in config (issue #2365) — an
  # unexported schemaConfig struct plus its loader, one field/loader line
  # per host-config member (isHostConfigMember above). Emits unaligned Go;
  # nix/regen.nix and this renderer's drift check both gofmt the output, so
  # column alignment lives in gofmt, not here.
  renderSchemaConfigGo =
    schema:
    let
      members = filterAttrs (_: isHostConfigMember) schema;
      isFloatTyped = e: builtins.isFloat (e.default or null);
      # Single source of truth for the flag's Go-side type shape; goType
      # and loaderLine both dispatch on this instead of repeating the
      # bool/int/float/string cascade, so they can't drift apart.
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
      # Secrets are always string-typed in schemaConfig regardless of
      # typeClass — none are int/bool/float-typed today, but a secret
      # ever becoming one must not silently mismatch its os.Getenv loader.
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

  # docs/flake-options.md content.
  renderFlakeOptionsDoc =
    schema:
    let
      flakeOptionEntries = filterAttrs (_: e: e.flakeOption or false) schema;
      flakeOptionNames = builtins.attrNames flakeOptionEntries;
      domainKnobs = domain: builtins.filter (n: flakeOptionEntries.${n}.group == domain) flakeOptionNames;
      renderDefault = entry: if entry ? default then "`${toString entry.default}`" else "—";
      capitalize =
        s: toUpper (builtins.substring 0 1 s) + builtins.substring 1 (builtins.stringLength s) s;
      renderRow =
        name:
        let
          entry = flakeOptionEntries.${name};
        in
        "| `perSystem.spindrift.${resolveNixPath name entry}` | `${entry.env}` | ${renderDefault entry} | ${entry.doc} |\n";
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
          "## ${capitalize domain} (`perSystem.spindrift.${domain}`)\n\n"
          + "| attr path | env var | default | description |\n"
          + "|---|---|---|---|\n"
          + concatStrings (map renderRow knobs)
          + "\n";
    in
    "<!-- Code generated by nix/checks.nix from lib/env-schema.nix. DO NOT EDIT. -->\n"
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
    + concatStrings (map renderSection domainOrder);

  # share/bash-completion/completions/spindrift content: subcommand
  # completion for the first word, flag completion (incl. the --issue alias
  # and secret --*-file/--*-cmd flags) anywhere after it, and filename
  # completion for a --*-file flag's argument. Tracer-bullet slice
  # (issue #551); zsh/fish crib this structure. Rendered fresh at build time,
  # no committed copy —
  # same as renderManpageRoff below.
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
      # A `case "$prev" in ) ... esac` (empty pattern) is a syntax error, so
      # the file-flag branch is omitted entirely if the schema ever has no
      # secret knobs.
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
      # A flag carrying `choices` (issue #554) completes to that value list
      # as its argument instead of falling through to the flag-name/file
      # branches below; one case arm per flag since each has its own list.
      # An `alias` (issue #874) matches in the same arm — bash `case`
      # supports `|`-separated patterns, mirroring fileFlagBranch above.
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
      # Dynamic positional issue-number completion (issue #556): on
      # dispatch/preview/recover, shell out to the hidden `__complete-issues`
      # subcommand (the same discovery seam dispatch itself uses) and offer
      # its candidate numbers, dropping each line's title (bash's compgen -W
      # carries no per-candidate description; zsh/fish keep it). Silenced
      # stderr and the subcommand's own bounded timeout mean a slow, offline,
      # or erroring query degrades to zero candidates rather than blocking or
      # erroring the completion.
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
  # renderBashCompletion above (subcommands as the first word, every flag
  # incl. --issue alias and secret --*-file/--*-cmd flags, file completion
  # for a --*-file flag's argument) using fish's `complete -c` syntax, with
  # each flag's schema doc string as its `-d` description. Rendered fresh at
  # build time, no committed copy — same as renderBashCompletion.
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
      # A flag carrying `choices` (issue #554) restricts its argument to that
      # value list (-x: require a value, no file completion) instead of the
      # plain flag-only completion below. No schema entry pairs `alias` with
      # `choices`, so only knobCompletions (the primary flag) uses it.
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
      # Dynamic positional issue-number completion (issue #556): on
      # dispatch/preview/recover, shell out to the hidden `__complete-issues`
      # subcommand and offer its output directly — fish's `complete -a`
      # auto-splits a tab-separated candidate into value and description, so
      # `__complete-issues`'s `<number>\t<title>` lines need no reformatting
      # here, unlike bash/zsh. Silenced stderr and the subcommand's own
      # bounded timeout mean a slow, offline, or erroring query degrades to
      # zero candidates rather than blocking or erroring the completion.
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
  # renderBashCompletion (subcommand completion for the first word, flag
  # completion incl. the --issue alias and secret --*-file/--*-cmd flags
  # anywhere after it, filename completion for a --*-file flag's argument),
  # plus a
  # per-candidate description — zsh completion carries them, bash's
  # compgen -W doesn't — sourced from each flag's schema `doc` string (the
  # same text `--help --all` prints). Rendered fresh at build time, no
  # committed copy — same as renderBashCompletion/renderManpageRoff.
  renderZshCompletion =
    schema: subcommandRegistry:
    let
      nonSecret = builtins.filter (e: !(e.secret or false)) (builtins.attrValues schema);
      secretEntries = builtins.filter (e: e.secret or false) (builtins.attrValues schema);
      subcommands = subcommandRegistry;
      issuePositionalSubcommands = issueCompletionSubcommands subcommandRegistry;
      # A `_describe` array entry is 'completion:description' (colon-split
      # on the first colon, same convention the subcommands array above
      # uses); the only character that needs escaping to survive the
      # surrounding single-quoted zsh string literal is an embedded "'"
      # itself (and a literal '\', so the quote-escape this function
      # inserts is never re-escaped — hence backslash first).
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
      # A `case "$prev" in ) ... esac` (empty pattern) is a syntax error, so
      # the file-flag branch is omitted entirely if the schema ever has no
      # secret knobs. Mirrors renderBashCompletion's fileFlagBranch.
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
      # A flag carrying `choices` (issue #554) completes to that value list
      # as its argument instead of falling through to the flag/file branches
      # below; one case arm per flag since each has its own list. Mirrors
      # renderBashCompletion's choicesFlagBranch, including its `alias`
      # (issue #874) handling.
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
      # Dynamic positional issue-number completion (issue #556): on
      # dispatch/preview/recover, shell out to the hidden `__complete-issues`
      # subcommand (the same discovery seam dispatch itself uses) and offer
      # `number:title` candidates via _describe, so zsh keeps the
      # per-candidate description bash's compgen -W can't carry — mirroring
      # allSubcommandSpecs/allFlagSpecs' own 'value:description' convention.
      # Silenced stderr and the subcommand's own bounded timeout mean a slow,
      # offline, or erroring query degrades to zero candidates rather than
      # blocking or erroring the completion.
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
          # Canonical first, then any live alias, then the deprecated old
          # name tagged "(deprecated)" — parity with `--help --all`'s flag
          # column (cmd/launcher/flags.go printHelp), so a man-page reader can
          # tell the retired spelling apart from the supported forms.
          renderName = n: "\\-\\-" + escFlag n;
          names = builtins.concatStringsSep ", " (
            [ (renderName (flagName e)) ]
            ++ map renderName (liveFlagAliases e)
            ++ map (n: renderName n + " (deprecated)") (deprecatedFlagAliases e)
          );
          dflt = flagDflt e;
          dfltSentence = if dflt == "" then "No default." else "Default: " + esc dflt + ".";
          # A presence-style bool flag takes no value, so render its name with
          # no italic type placeholder (issue #2145).
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
        spindrift \- launch waves of headless Claude Code agents across GitHub issues
        .SH SYNOPSIS
        .B spindrift
        [\fIflags\fR] \fIsubcommand\fR [\fIargs\fR]
        .SH DESCRIPTION
        .B spindrift
        dispatches one disposable, nix-built container per GitHub issue, runs a
        headless Claude Code agent inside it, and drives each resulting pull request
        through a merge gate. Every runtime knob is set by flag, by the Consumer
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
