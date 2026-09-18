# Render functions for the artifacts generated from lib/env-schema.nix, and the
# owner of the flag-group taxonomy (groupOrder). One renderer per artifact, so a
# drift guard and the regenerator can never disagree (issue #402, issue #461).
# Pure builtins only (no `pkgs.lib`) so a bare `nix eval` can unit-test this file
# without a locked nixpkgs (issue #402, issue #2535).
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
  # A resolved flake path (e.g. "git.merge.policy", from lib/nixpath.nix) split
  # into its dot-separated segments. ADR 0037.
  splitNixPath = path: builtins.filter builtins.isString (builtins.split "\\." path);
  resolveNixPath = import ./nixpath.nix;
  # A Go `[]string{"a", "b"}` literal's inner comma-joined, quoted contents.
  renderGoStringSlice = items: builtins.concatStringsSep ", " (map (s: "\"${s}\"") items);
  # Collapse every run of whitespace, newlines included, to a single space and
  # trim the ends. A naive "\n" to " " replaceStrings would instead leave
  # doubled spaces at each line-wrap and trailing-newline boundary.
  oneLine =
    s:
    builtins.concatStringsSep " " (
      builtins.filter (p: p != "") (builtins.filter builtins.isString (builtins.split "[ \t\n]+" s))
    );
  # A markdown table cell cannot carry an unescaped "|": it reads as a column
  # separator.
  escapeCell = builtins.replaceStrings [ "|" ] [ "\\|" ];
  # Uppercase a string's first character, leaving the rest untouched.
  upperFirst =
    s: toUpper (builtins.substring 0 1 s) + builtins.substring 1 (builtins.stringLength s - 1) s;
  padRight =
    width: s:
    let
      pad = width - builtins.stringLength s;
    in
    s + concatStrings (builtins.genList (_: " ") (if pad > 0 then pad else 0));
  # Renders `path = value;` lines with every `=` aligned to the widest path.
  # The width is computed per block, never hand-typed (issue #2557 review
  # finding).
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
  # Env var name -> flag name (e.g. MAX_PARALLEL -> max-parallel).
  toKebab = env: toLower (builtins.replaceStrings [ "_" ] [ "-" ] env);

  # ADR 0037 Pass 2: a knob's canonical CLI flag is its `flag` override when set,
  # else its env-derived kebab name. When `flag` is set, the env-derived name
  # stays on as a deprecated alias so operator scripts keep working until 1.0.
  flagName = e: e.flag or (toKebab e.env);
  deprecatedFlagAliases = e: if e ? flag then [ (toKebab e.env) ] else [ ];
  liveFlagAliases = e: if e ? alias then [ e.alias ] else [ ];
  # Non-canonical forms, live alias before deprecated old name.
  secondaryFlagNames = e: liveFlagAliases e ++ deprecatedFlagAliases e;
  allFlagNames = e: [ (flagName e) ] ++ secondaryFlagNames e;

  # Case-arm flag patterns for a choices knob. Shared by the bash and zsh
  # completions, whose `case` patterns use the same `|`-joined syntax.
  choicesFlagPatterns = e: map (n: "--${n}") (allFlagNames e);

  # The type token the flag table and man page print. A knob opts into the
  # presence-style bool kind explicitly with `kind = "bool";` (issue #2145);
  # bool is never inferred from a boolean `default`, because several knobs carry
  # `default = false` only to render as a `types.bool` flake option while their
  # CLI flag stays a value form, and inferring would silently flip all of them.
  flagKind =
    e:
    if e ? kind then
      e.kind
    else if builtins.isInt (e.default or null) then
      "int"
    else
      "string";

  flagDflt = e: if e ? default then builtins.toString e.default else "";

  # Display order for the full flag reference: the six domains (ADR 0037).
  # renderFlagTableGo below renders this into cmd/launcher/flagtable_gen.go, so
  # the Go copy cannot drift from it (issue #2523).
  groupOrder = [
    "agents"
    "git"
    "issues"
    "forge"
    "dispatch"
    "infra"
  ];

  # Subcommands whose positional issue-number argument spindrift completes
  # dynamically (issue #556). `research` takes an issue list too but stays out:
  # #1603 put the gate in the registry rather than an `acceptsIssueArg` field
  # that would conflate taking issue args with getting dynamic completion.
  issueCompletionSubcommands =
    subcommandRegistry:
    map (s: s.name) (builtins.filter (s: s.dynamicIssueCompletion or false) subcommandRegistry);

  # tests/box_env_gen.bash content: a set_box_env function exporting every
  # boxEnv knob at its schema default, so the entrypoint-*.bats suites exercise
  # the same defaults the nix preamble bakes into the image.
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

  # templates/default/flake.nix's generated settings example: every flakeOption
  # knob as a nested domain tree keyed by its derived flake path, so a new knob
  # reaches the template without a hand-edit (ADR 0037, issue #520, #2179).
  # structuralExamples (issue #2572) splices in roster/byName, which have no
  # schema-default literal a `nixLiteral` line could be derived from.
  renderTemplateSettingsBlock =
    schema: structuralExamples:
    let
      ind = "            # ";
      flakeOptionEntries = filterAttrs (_: e: e.flakeOption or false) schema;
      # Placeholder only for a required knob, so a knob whose placeholder exists
      # only for the bats fixture (gitUserName's "Test Bot") renders as "" here
      # rather than as a fake identity in consumer-facing documentation.
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
      # path. Each leaf is tagged `__leaf` so renderNode can tell a schema entry
      # from a namespace node, since both are attrsets.
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
      # Structural examples are not env-schema.nix knobs, so they carry their own
      # hand-given path instead of a resolveNixPath call.
      domainTree = builtins.foldl' (
        acc: ex:
        insertLeaf acc ex.path {
          doc = ex.doc;
          lines = ex.lines;
        }
      ) schemaDomainTree structuralExamples;
      # Children come out ordered by attribute name because mapAttrsToList walks
      # builtins.attrNames, which sorts.
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
              # A structural example entry carries `lines` instead of a
              # schema-default-derived `nixLiteral` value.
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
  # From #625 onward env configures nothing but secrets, so listing a non-secret
  # knob here would advertise a channel deprecated the moment it is used.
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

  # tests/default_models_gen.bash content: one exported variable per
  # lib/default-model-fixture.nix schemaDefaults leaf (issue #2514). Unwrapped by
  # a function, unlike set_box_env, because a bats test sources it as expected
  # values. dogfoodPins.filer stays out: the Nix checks that assert against it
  # import the fixture directly, so an export here would have no consumer.
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
  # lib/default-model-fixture.nix keyed by the schema's env-var names, so a
  # launcher test asserts against it instead of hand-typing the expected default
  # model literal (issue #2514).
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

  # docs/reference.md's "Default models" table body (issue #2514 AC2), drawn from
  # the same fixture the bash and Go forms above use. filerModel renders
  # specially because its schema default is empty, so the cell states
  # dogfoodPins.filer instead. The surrounding heading stays hand-written.
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

  # docs/reference.md restates roster's flake path as prose; this pins that
  # string to lib/structural-paths.nix's `roster` entry so the two cannot drift
  # silently (issue #2436, documentedFact registry by issue #2950).
  renderRosterFlakePathDoc =
    rosterPath: "`perSystem.spindrift.${builtins.concatStringsSep "." rosterPath}`\n";

  # docs/reference.md restates rosterDefaults' effort values as prose; this pins
  # that string to the real values (issue #2506, documentedFact registry by issue
  # #2950). `rosterNames` is the default roster's own entry order, passed in
  # rather than hand-listed so a new roster entry cannot silently go missing from
  # the rendered block (issue #3447).
  renderRosterEffortsDoc =
    rosterDefaults: rosterNames:
    "`"
    + (builtins.concatStringsSep "/" (map (n: "${n}=${rosterDefaults.${n}.effort}") rosterNames))
    + "`\n";

  # docs/reference.md restates the dogfood Consumer config's Filer pin as prose;
  # this pins that string to lib/default-model-fixture.nix's dogfoodPins.filer
  # (issue #2514, documentedFact registry by issue #2950).
  renderDogfoodFilerPinDoc =
    fixture:
    "`roster = rosterLib.defaultRoster { models = { filer = \"${fixture.dogfoodPins.filer}\"; }; };`\n";

  # docs/reference.md restates the scout, reviewer, and worker model literals as
  # prose; this pins that string to the fixture's values (issue #2514,
  # documentedFact registry by issue #2950). Filer is the separate local pin
  # renderDogfoodFilerPinDoc handles.
  renderDogfoodModelsDoc =
    fixture:
    "`${fixture.schemaDefaults.scoutModel}`, `${fixture.schemaDefaults.reviewModel}` (issue #2433), and `${fixture.schemaDefaults.workerModel}` respectively.\n";

  # Data rows of a rendered option-surface table as `{ name; domainPath; }`, the
  # two cells callers key on. The header and separator lines have no backticked
  # first cell, so they drop out. The combined scoutPrompt / reviewPrompt /
  # filerPrompt row reports only its first name.
  optionSurfaceRowNamePaths =
    table:
    let
      matches = map (line: builtins.match "\\| `([^`|]+)`[^|]*\\|([^|]*)\\|.*" line) (
        builtins.filter builtins.isString (builtins.split "\n" table)
      );
    in
    map (m: {
      name = builtins.elemAt m 0;
      domainPath = builtins.elemAt m 1;
    }) (builtins.filter (m: m != null) matches);

  # docs/reference.md's "### Option surface" table (issue #2739, documentedFact
  # registry by issue #2950), header through last row as one block: an
  # HTML-comment marker line between GFM table rows terminates the table, so a
  # per-row generated span is impossible. The four rows with no domain path are
  # copied verbatim as editorial text (out of scope per spec #2921).
  renderOptionSurfaceTableDoc =
    {
      structuralPaths,
      byNamePaths,
      # From lib/build-constants.nix, the source lib/mkHarness.nix itself
      # defaults from, so this row's default cell is not a second hand-typed
      # digest that could drift from it (issue #2950 review finding).
      nixBuilderImage,
    }:
    let
      dotted = key: registry: builtins.concatStringsSep "." registry.${key};
      # The row-anchoring regex below must spell this prefix exactly as `path`
      # writes it or no row matches at all, so derive its escaped form from the
      # same string.
      domainPathPrefix = "perSystem.spindrift.";
      domainPathPrefixRe = builtins.replaceStrings [ "." ] [ "\\." ] domainPathPrefix;
      path = key: registry: "${domainPathPrefix}${dotted key registry}";
      table = ''
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
        | `skills`    | `${path "skills" structuralPaths}` | shared         | list of path/derivation/`{ name; src; }` | `[]`  | skills baked into the image at the fixed `/agent/skills` path alongside the harness-owned skills (e.g. `auto-format`, `auto-lint`, `check-hygiene`, `code-comments`, baked regardless of this list), each as a `<name>/SKILL.md` directory (the only layout Claude Code discovers — a flat `<name>.md` is ignored) so the headless agent can `/invoke` them; a `{ name; src; }` content entry (name + SKILL.md body) is realized with the image's own Linux `pkgs` rather than copied from a pre-built host derivation, keeping the agent-image drvPath host-independent (issue #597); `agent/entrypoint.sh` copies both into the Driver's actual runtime skills dir at box startup, then copies `SPINDRIFT_SKILLS_DIR` (staged at `/operator-skills`) over the top so runtime overrides win without erasing the baked set (issue #2489) |
        | `settings`  | — | **flake-module only** | submodule, grouped by section (see below) | `{}` | **deprecated** compat shim (ADR 0037): every schema-generated knob is now a first-class option in the domain tree below; this submodule still works pre-1.0 (forwards with an eval warning) but new config should use the domain paths directly |
        | `runtime`   | `${path "runtime" structuralPaths}` | shared         | `"podman"` \| `"docker"` \| `"rancher"` \| `"bwrap"` | `"podman"` | runner the `spindrift build`/`dispatch` commands drive: an OCI runtime (`"rancher"` is an alias for Rancher Desktop's containerd mode, driven via `nerdctl`), or the daemonless bubblewrap sandbox (`bwrap`, Linux-only, no image build/load) |
        | `driver`    | `${path "driver" structuralPaths}` | shared         | string                      | `"claude"`         | the agent CLI Driver baked into the image and threaded to the launcher (ADR 0009); `"claude"` (default) and `"opencode"` are the Drivers today. A non-`claude` Driver realises its own `spindrift-<driver>` image (e.g. `spindrift-opencode`) so per-Driver artifacts never collide |
        | `nixInBox`  | `${path "nixInBox" structuralPaths}` | shared         | bool                        | `true`             | bake a usable nix (binary + registered store DB + sandbox-off, `cores = 4`-bounded config) into the box so `nix flake check` / `nix develop` work inside it; set `false` for a lean, nix-free image (ADR 0008) |
        | `nixStoreWritable` | `${path "nixStoreWritable" structuralPaths}` | shared  | bool                 | `false`            | self-test mode (ADR 0018): make `/nix/store` itself (not its existing contents) agent-writable so in-box `nix flake check` can substitute/build new paths instead of hitting EACCES; new paths live only in the container's ephemeral copy-on-write layer. Not hermetic — the entrypoint prints a loud `==> WARNING`; both runners support it (ADR 0042) — bwrap overlays an ephemeral tmpfs upper on the store instead |
        | `extraClosures` | `${path "extraClosures" structuralPaths}` | shared     | `pkgs -> [pkg]`         | `[]`               | extra derivations, as a function of the (Linux) `pkgs` (like `packages`), whose closures are baked into the image and registered in the store DB alongside the runtime closure, so in-box nix sees them as already present (ADR 0018) |
        | `nixBuilderImage` | — | **`mkHarness` only** | string        | `"${nixBuilderImage}"` (pinned reference — the real default lives in `lib/build-constants.nix`) | Nix image `spindrift build` uses as a fallback Linux builder when the host can't realize the image; pinned by digest for supply-chain safety (see [Building on macOS](#building-on-macos)) |
        | `roster`    | `${path "roster" structuralPaths}` | shared         | list of subagent-entry attrs | `lib/roster.nix`'s `defaultRoster` | supersedes the four legacy model knobs; see [Subagent roster](#subagent-roster) |
        | `byName`    | `${path "byName" byNamePaths}` | shared         | attrset of `{ model?; effort?; }` keyed by roster entry name | `{}` (this row is the `mkHarness` parameter; the flake option, `${dotted "byName" byNamePaths}`, defaults to `null`) | name-keyed model/effort shorthand (issue #2560), forwarded into `defaultRoster`; only takes effect when `roster` is unset; no flat `perSystem.spindrift.byName` alias — see [Subagent roster](#subagent-roster) |
      '';
      # Row names are read back off `table` itself, not a hand-kept side list, so
      # a wrong name breaks the render everyone reads (issue #2950 review
      # finding). Only rows carrying a domain path count: a registry key
      # colliding with an editorial row's name would otherwise pass the check
      # while that row's dash rendered where its real path belongs (issue #3067).
      domainPathRowNames = map (cells: cells.name) (
        builtins.filter (
          cells: builtins.match " *`${domainPathPrefixRe}[^`|]+` *" cells.domainPath != null
        ) (optionSurfaceRowNamePaths table)
      );
      # Forward direction only. A row with no registry key is legitimate (the
      # editorial rows), so schema-drift.nix's editorial-rows-pin holds that side.
      rowlessKeys = builtins.filter (k: !(builtins.elem k domainPathRowNames)) (
        builtins.attrNames (structuralPaths // byNamePaths)
      );
    in
    if rowlessKeys != [ ] then
      throw "renderOptionSurfaceTableDoc: structuralPaths/byNamePaths carries key(s) with no matching option-surface table row: ${builtins.concatStringsSep ", " rowlessKeys} -- add a matching row to this function (lib/renderers.nix)"
    else
      table;

  # MIGRATING.md's "Flag names re-cut to domains" table (issue #2558): one row
  # per lib/legacy-settings-section.nix entry, mapping a frozen settings alias to
  # its current home. Both columns carry the full `perSystem.spindrift.` prefix
  # so the paths read exactly as flakeModule.nix's deprecation warning and
  # docs/flake-options.md spell them. Sorted so rows group by section.
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

  # docs/reference.md's domain-tree example's `agents.models.*` lines (issue
  # #2514, ADR 0037, issue #2557): the four schemaDefaults leaves
  # renderDefaultModelsDoc already draws from, as flat assignments. workerModel
  # is absent because the example never carried it. Takes both the fixture (the
  # values) and the schema (the entries resolveNixPath resolves paths from).
  renderSettingsExampleModelsDoc =
    fixture: schema:
    let
      inherit (fixture) schemaDefaults;
      # builtins.toJSON, not "${value}", so a default containing `"` or `\`
      # still renders as a valid quoted literal.
      inherit (builtins) toJSON;
      # Paths come from resolveNixPath, never hand-typed, so a `group` or
      # `nixSubPath` rename cannot leave this example stale while the drift
      # check stays green (issue #2557 review finding).
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

  # docs/reference.md's domain-tree example's `issues.labels.*` lines (issue
  # #2537, ADR 0037, issue #2557): the dispatch label and the three lifecycle
  # labels the launcher swaps it through, as flat assignments. Takes the whole
  # schema and no fixture, since these four are plain env-schema.nix knobs.
  renderSettingsExampleLabelsDoc =
    schema:
    let
      # builtins.toJSON, not "${value}", so a default containing `"` or `\`
      # still renders as a valid quoted literal.
      inherit (builtins) toJSON;
      # Paths come from resolveNixPath, never hand-typed, so a `group` or
      # `nixSubPath` rename cannot leave this example stale while the drift
      # check stays green (issue #2557 review finding).
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

  # docs/reference.md's domain-tree example's `git.*` and `dispatch.*` lines
  # (issue #2537, ADR 0037, issue #2557): the eight knobs driving branch naming,
  # merge behavior, and dispatch concurrency, as flat assignments. The four int
  # knobs render unquoted via toString, matching how the doc already spells them.
  renderSettingsExampleConfigDoc =
    schema:
    let
      # builtins.toJSON, not "${value}", so a string default containing `"` or
      # `\` still renders as a valid quoted literal.
      inherit (builtins) toJSON;
      # Paths come from resolveNixPath, never hand-typed, so a `group` or
      # `nixSubPath` rename cannot leave this example stale while the drift
      # check stays green (issue #2557 review finding).
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
  # registry's `entries` attrset, not the whole registry: the registry also
  # exports shape-assertion and rendering functions, which are not Driver names
  # (issue #624).
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

  # agent/entrypoint.sh's generated skill-baked probe block (issue #2532):
  # one `[ -f ... ] && _ap_args+=(...)` line per lib/baked-skills.nix row.
  renderBakedSkillProbesShell =
    bakedSkills:
    concatStrings (
      map (
        s: "  [ -f \"$DRIVER_SKILLS_DIR/${s.name}/SKILL.md\" ] && _ap_args+=(--${s.name}-skill-baked)\n"
      ) bakedSkills
    );

  # cmd/launcher/driver-exec/assembleprompt_cmd.go's generated skill-baked
  # flag declarations (issue #2532).
  renderBakedSkillFlagsGo =
    bakedSkills:
    concatStrings (
      map (
        s:
        "\t${s.goVar} := fs.Bool(\"${s.name}-skill-baked\", false, \"true when DRIVER_SKILLS_DIR/${s.name}/SKILL.md was baked\")\n"
      ) bakedSkills
    );

  # cmd/launcher/driver-exec/assembleprompt_cmd.go's generated skill-baked
  # env.Field assignments (issue #2979). env comes from
  # promptassembly.EnvFromEnviron(), not a struct literal, so each row is a plain
  # statement with a 1-tab indent and no trailing comma.
  renderBakedSkillEnvAssignGo =
    bakedSkills: concatStrings (map (s: "\tenv.${s.field} = *${s.goVar}\n") bakedSkills);

  # cmd/launcher/internal/promptassembly/env.go's generated skill-baked
  # struct fields (issue #2532).
  renderBakedSkillFieldsGo =
    bakedSkills:
    concatStrings (
      map (
        s: "\t${s.field} bool // entrypoint.sh: -f \"$DRIVER_SKILLS_DIR/${s.name}/SKILL.md\" (${s.gate})\n"
      ) bakedSkills
    );

  # cmd/launcher/internal/promptassembly/gates.go's generated skill-baked
  # Gates() map assignments (issue #2532).
  renderBakedSkillGatesGo =
    bakedSkills: concatStrings (map (s: "\tg[\"${s.gate}\"] = e.${s.field}\n") bakedSkills);

  # cmd/launcher/internal/backend/registry_gen.go content (issue #2521): one Go
  # `Descriptor` var per lib/backends/default.nix row, plus a Registry slice in
  # the nix list's declaration order, which is load-bearing (see that file's
  # header). Emits unaligned Go; gofmt owns column alignment. Only Go-truthy
  # values get a field line, mirroring the struct literals this replaced.
  renderBackendRegistryGo =
    backends:
    let
      # Every field lib/backends/default.nix's header documents. A row attribute
      # outside this set is a misspelling, not a new fact, and must fail the
      # build rather than render as if the field were never set.
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

  # cmd/launcher/internal/doctor/labelmeta_gen.go content (issue #2528): a
  # `Meta<Role>` var per work-tier row, the `TriageLabelMeta` map every tier but
  # recoverable, findingType, and triggerOnly feeds, and the separate
  # `FindingTypeLabels` map (issue #2594, ADR 0041). That map is kept apart so
  # ensureTypeLabel's `type` token can never collide with a real label name.
  renderLabelRegistryGo =
    labels:
    let
      metaLit =
        row:
        "LabelMeta{Description: ${builtins.toJSON row.description}, Color: ${builtins.toJSON row.color}}";
      workVar = row: "Meta${row.role}";
      workVarDecl = row: "var ${workVar row} = ${metaLit row}\n";
      workVarDecls = concatStrings (map workVarDecl labels.work);
      # A work-tier row's map entry reuses the Meta<Role> var declared above, so
      # the map and the per-role var can never disagree. Every other tier has no
      # per-role var, so its entry is a literal.
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

  # cmd/launcher/internal/runner/runtimevalues_gen.go content (issue #2561:
  # the runner module is the single home of all runtime vocabulary).
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

  # cmd/launcher/quickstart/quickstart_paths_gen.go content (issue #2556): one Go
  # const per lib/quickstart-path-table.nix key, so the wizard's rendered
  # flake.nix reads the same option paths the schema resolves to and a
  # group/nixSubPath rename cannot leave a hand-typed copy stale. The consts stay
  # unexported: quickstart.go consumes them from the same package.
  renderQuickstartPathsGo =
    quickstartPaths:
    let
      # builtins.toJSON, not "${value}", so a path round-trips into a valid Go
      # string literal.
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

  # cmd/launcher/internal/agentpaths/agentpaths_gen.go content (issue #2531): one
  # Go const per lib/agent-paths.nix key, so the launcher's host-side mount and
  # path logic reads the same baked /agent/* literals the image is built from and
  # a rename cannot leave a hardcoded string stale. Each SCREAMING_SNAKE_CASE key
  # becomes a PascalCase Go identifier (PROMPTS_DIR -> PromptsDir).
  renderAgentPathsGo =
    agentPaths:
    let
      splitWords = key: builtins.filter builtins.isString (builtins.split "_" key);
      capitalizeWord = w: upperFirst (toLower w);
      pascalCase = key: concatStrings (map capitalizeWord (splitWords key));
      # builtins.toJSON, not "${value}", so a path containing `"` or `\`
      # round-trips into a valid Go string literal.
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
    + "// The 8 baked /agent/* path literals (lib/agent-paths.nix), single-sourced\n"
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

  # cmd/launcher/internal/outcome/status_gen.go content (issue #2504): one Go
  # const per unique status word across all kinds, so a word shared between kinds
  # (e.g. "blocked") gets exactly one identifier, plus one exported []string var
  # per kind in row order.
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

  # cmd/launcher/internal/outcome/markerchannels_gen.go content (issue #2974,
  # parent #2972): one unexported Go const per markerChannels row, plus the
  # ordered MarkerChannelTokens the caveman parity test iterates. Unexported so
  # they never collide with outcome.go's hand-written consts, which alias these
  # generated values rather than redeclaring the literals.
  renderMarkerChannelsGo =
    markerChannels:
    let
      # An explicit lookup table, not a camel-case derivation of the hyphenated
      # id, because "pr-intent" needs the acronym form "PRIntent" that a
      # capitalize-each-part heuristic would render "PrIntent".
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
      # builtins.toJSON, not "${row.token}", so a token containing a quote or
      # backslash still emits a valid Go string literal.
      constLines = concatStrings (
        map (row: "\t${constName row} = ${builtins.toJSON row.token}\n") markerChannels
      );
      items = builtins.concatStringsSep ",\n\t" (map constName markerChannels);
      # Keyed by the constName const, not a re-emitted token literal, so this
      # map's keys stay the same constants MarkerChannelTokens uses.
      fieldShapeLines = concatStrings (
        map (row: "\t${constName row}: ${builtins.toJSON row.fieldShape},\n") markerChannels
      );
    in
    "// Code generated by nix/regen.nix from lib/prompt-contract.nix. DO NOT EDIT.\n"
    + "package outcome\n"
    + "\n"
    + "// Regenerate with `nix run .#regen` after editing lib/prompt-contract.nix's\n"
    + "// markerChannels.\n"
    + "//\n"
    + "// One row per marker channel (issue #2974, parent #2972): outcome, comment,\n"
    + "// pr-intent, issue-intent, review-verdict. Each channel's token and\n"
    + "// fieldShape are rendered into Go (issue #2996); its defense (structural /\n"
    + "// nonce / fold) and carrier stay Nix-only, recorded in that registry, the\n"
    + "// citable home for the trust model.\n"
    + "const (\n"
    + constLines
    + ")\n"
    + "\n"
    + "// MarkerChannelTokens is the ordered list of every lib/prompt-contract.nix\n"
    + "// markerChannels registry token, iterated by the caveman marker-exemption\n"
    + "// parity test instead of a hand-listed subset.\n"
    + "var MarkerChannelTokens = []string{\n\t"
    + items
    + ",\n}\n"
    + "\n"
    + "// MarkerChannelFieldShapes maps each lib/prompt-contract.nix markerChannels\n"
    + "// registry token to its fieldShape, the token's documented placeholder\n"
    + "// grammar (e.g. \"<nonce> <base64-payload>\"), for a caller that needs to\n"
    + "// derive an emitted marker's expected shape without hand-transcribing it.\n"
    + "var MarkerChannelFieldShapes = map[string]string{\n"
    + fieldShapeLines
    + "}\n";

  # Oxford-joined "a, b, or c" prose rendering of an outcomeStatusSets row's
  # statuses, for agent/entrypoint.sh's nudge prompt (issue #2504).
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

  # Pipe-joined "a|b|c" grammar-placeholder rendering of an outcomeStatusSets
  # row's statuses, for a `status=<...>` grammar example (issue #2504).
  renderOutcomeStatusPipe = statuses: builtins.concatStringsSep "|" statuses;

  # cmd/launcher/flagtable_gen.go content.
  renderFlagTableGo =
    schema:
    let
      nonSecretSchema = filterAttrs (_: e: !(e.secret or false)) schema;
      secretSchema = filterAttrs (_: e: (e.secret or false)) schema;
      flagAlias = e: if e ? alias then ", alias: \"${e.alias}\"" else "";
      flagDeprecatedAlias = e: if e ? flag then ", deprecatedAlias: \"${toKebab e.env}\"" else "";
      # The knob's valid-value enum, carried onto the generated flag row so a Go
      # guard can source it instead of a hand-typed value list (issue #2520).
      flagChoices =
        e:
        if e ? choices && e.choices != [ ] then
          ", choices: []string{${builtins.concatStringsSep ", " (map (c: "\"${c}\"") e.choices)}}"
        else
          "";
      # The knob's derived domain-tree flake path. Since ADR 0037 Pass 1 the
      # settings path is that path. Empty for a knob with no flake-settings
      # surface, such as ISSUE_NUMBER or SPINDRIFT_PROMPT_DIR.
      flagSettingsPath = key: e: if e.flakeOption or false then resolveNixPath key e else "";
      # Every non-secret knob must declare a group so the full reference can file
      # it under a heading. A missing group is a schema error, not a silent "".
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

  # Which schema members the launcher's host config holds: not secret and not
  # boxEnvOnly, or explicitly hostConfig-overridden. renderSchemaConfigGo and its
  # drift check both call this, so struct membership cannot drift from the check.
  isHostConfigMember =
    e: ((!(e.secret or false)) && !(e.boxEnvOnly or false)) || (e.hostConfig or false);

  # cmd/launcher/schemaconfig_gen.go content: an unexported schemaConfig struct
  # plus its loader, one field and loader line per host-config member (issue
  # #2364, embedded by value in config per issue #2365). Emits unaligned Go;
  # gofmt owns column alignment.
  renderSchemaConfigGo =
    schema:
    let
      members = filterAttrs (_: isHostConfigMember) schema;
      isFloatTyped = e: builtins.isFloat (e.default or null);
      # goType and loaderLine both dispatch on this rather than repeating the
      # bool/int/float/string cascade, so the two cannot drift apart.
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
      # Secrets stay string-typed whatever typeClass says. None is int, bool, or
      # float today, and one becoming so must not silently mismatch its
      # os.Getenv loader.
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

  # cmd/launcher/internal/promptassembly/boxenv_gen.go content (issue #2979): one
  # `Env{}` field assignment per lib/promptassembly-boxenv.nix row, read from the
  # Box's process environment. Emits unaligned Go; gofmt owns column alignment.
  # Every other Env field stays at its zero value and assembleprompt_cmd.go
  # layers it on from a flag, since those were never env reads.
  renderPromptAssemblyBoxEnvGo =
    rows:
    let
      # Keyed the same way as lib/promptassembly-boxenv.nix's per-row `kind`, so
      # the four kinds cannot drift apart across call sites.
      loaderLineByKind = {
        presence = row: "\t\t${row.field}: os.Getenv(\"${row.env}\") != \"\",\n";
        string = row: "\t\t${row.field}: os.Getenv(\"${row.env}\"),\n";
        int = row: "\t\t${row.field}: boxenvAtoi(os.Getenv(\"${row.env}\")),\n";
        equals1 = row: "\t\t${row.field}: os.Getenv(\"${row.env}\") == \"1\",\n";
      };
      loaderLine =
        row:
        (loaderLineByKind.${row.kind}
          or (throw "renderPromptAssemblyBoxEnvGo: row '${row.field}' has unknown kind '${row.kind}'")
        )
          row;
      loaderLines = concatStrings (map loaderLine rows);
      rowCount = toString (builtins.length rows);
    in
    "// Code generated by nix/regen.nix from lib/promptassembly-boxenv.nix. DO NOT EDIT.\n"
    + "package promptassembly\n"
    + "\n"
    + "import (\n"
    + "\t\"os\"\n"
    + "\t\"strconv\"\n"
    + ")\n"
    + "\n"
    + "// EnvFromEnviron reads Env's ${rowCount} Box-env-sourced fields directly from the\n"
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
    + "// back to a per-key schema default (intSchemaDefault), these ${rowCount} rows are\n"
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

  # Domain section order for docs/flake-options.md and the nested domain tree
  # (ADR 0037): the first segment of each flakeOption knob's derived flake path.
  domainOrder = [
    "agents"
    "git"
    "issues"
    "forge"
    "dispatch"
    "infra"
  ];

  # docs/flake-options.md's structural-options section (issue #2572): the
  # hand-declared structural knobs plus byNameOption, documented from
  # lib/structural-options-doc.nix. A "type" column stands in for "env var",
  # since a structural option has none. `doc` may be multi-line prose, collapsed
  # here because an embedded newline would break the markdown table's shape.
  renderStructuralOptionsDoc =
    structuralOptionsDoc: structuralPaths: byNamePaths:
    let
      byNameStructuralPath = builtins.concatStringsSep "." byNamePaths.byName;
      pathFor =
        name:
        if name == "byName" then
          byNameStructuralPath
        else
          builtins.concatStringsSep "." structuralPaths.${name};
      # docType is the field where escapeCell earns its keep: runtime's
      # `"podman"` | `"docker"` | ... reliably contains a literal "|".
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

  # docs/flake-options.md's full content: the banner, the schema-generated
  # sections grouped by domain (ADR 0037), then the structural-options section
  # (issue #2572). Both nix/regen.nix and the flake-options-doc check call this
  # one renderer (CONTRIBUTING.md's one-renderer-per-artifact contract).
  renderFlakeOptionsDocFull =
    schema: structuralOptionsDoc: structuralPaths: byNamePaths:
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
    + renderStructuralOptionsDoc structuralOptionsDoc structuralPaths byNamePaths;

  # share/bash-completion/completions/spindrift content: subcommand completion
  # for the first word, flag completion anywhere after it, and filename
  # completion for a --*-file flag's argument (issue #551). The zsh and fish
  # renderers below crib this structure. Rendered fresh at build time, with no
  # committed copy, like renderManpageRoff.
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
      # instead of falling through to the flag-name and file branches below. One
      # case arm per flag, since each has its own list; an `alias` (issue #874)
      # joins the same arm through a `|`-separated pattern.
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
      # Dynamic positional issue-number completion (issue #556) shells out to the
      # hidden `__complete-issues` subcommand. Each line's title is dropped
      # because bash's compgen -W carries no per-candidate description. Silenced
      # stderr plus the subcommand's bounded timeout make a slow or offline query
      # degrade to zero candidates rather than blocking the completion.
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

  # share/fish/vendor_completions.d/spindrift.fish content: renderBashCompletion's
  # coverage in fish's `complete -c` syntax, with each flag's schema doc string
  # as its `-d` description. Rendered fresh at build time, no committed copy.
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
      # list. `-x` requires a value and suppresses file completion. No schema
      # entry pairs `alias` with `choices`, so only knobCompletions uses it.
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
      # Dynamic positional issue-number completion (issue #556). fish's
      # `complete -a` splits a tab-separated candidate into value and
      # description, so `__complete-issues`'s output needs no reformatting here,
      # unlike bash and zsh. Silenced stderr plus the subcommand's bounded
      # timeout make a slow or offline query degrade to zero candidates.
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

  # share/zsh/site-functions/_spindrift content: renderBashCompletion's coverage
  # plus a per-candidate description from each flag's schema `doc` string, which
  # zsh carries and bash's compgen -W cannot. Rendered fresh at build time, no
  # committed copy.
  renderZshCompletion =
    schema: subcommandRegistry:
    let
      nonSecret = builtins.filter (e: !(e.secret or false)) (builtins.attrValues schema);
      secretEntries = builtins.filter (e: e.secret or false) (builtins.attrValues schema);
      subcommands = subcommandRegistry;
      issuePositionalSubcommands = issueCompletionSubcommands subcommandRegistry;
      # A `_describe` array entry is 'completion:description', split on the first
      # colon. Only "'" and "\" need escaping to survive the surrounding
      # single-quoted zsh literal, and the backslash must go first so the
      # quote-escape this inserts is never re-escaped.
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
      # instead of falling through to the flag and file branches below. Mirrors
      # renderBashCompletion's choicesFlagBranch, `alias` handling included.
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
      # Dynamic positional issue-number completion (issue #556) offers
      # `number:title` candidates through _describe, so zsh keeps the
      # per-candidate description bash cannot carry. Silenced stderr plus the
      # subcommand's bounded timeout make a slow or offline query degrade to zero
      # candidates rather than blocking the completion.
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
          # tagged "(deprecated)", matching `--help --all`'s flag column so a
          # man-page reader can tell the retired spelling from the live ones.
          renderName = n: "\\-\\-" + escFlag n;
          names = builtins.concatStringsSep ", " (
            [ (renderName (flagName e)) ]
            ++ map renderName (liveFlagAliases e)
            ++ map (n: renderName n + " (deprecated)") (deprecatedFlagAliases e)
          );
          dflt = flagDflt e;
          dfltSentence = if dflt == "" then "No default." else "Default: " + esc dflt + ".";
          # A presence-style bool flag takes no value, so it gets no italic type
          # placeholder (issue #2145).
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
