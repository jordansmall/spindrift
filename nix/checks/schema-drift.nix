# Schema drift guards: every committed generated artifact (Driver name table,
# harness.env.example, launcher flag table, flake-options doc, template settings
# example, man page) must stay in sync with its schema source. Shares its
# renderers with `nix run .#regen` via lib/renderers.nix so the guard and the
# regenerator can never drift from each other.
{
  pkgs,
  fixtures,
  nixpkgs,
  system,
  ...
}:
let
  inherit (fixtures) harness;
  renderers = import ../../lib/renderers.nix;
  schema = import ../../lib/env-schema.nix;
  # The documentedFact registry, shared with nix/regen.nix's marker-splice loop
  # so a block's marker literals/renderer call are typed exactly once.
  # documentedFactChecks below derives one named check per row.
  documentedFacts = import ../../lib/documented-facts.nix { inherit (pkgs) lib; };
  # The shared marker-splice + drift-comparison implementation backing
  # assertMarkedBlockOk below -- also imported by nix/checks/baked-skills.nix so
  # the two never fork hand-mirrored copies.
  documentedFactChecker = import ../../lib/documented-fact-checker.nix { inherit pkgs; };
  # regenRowScript: the exact per-row postSplice-dispatch function
  # `nix run .#regen` uses, exercised directly by regen-postsplice-dispatch-guard
  # below against synthetic rows.
  regen = import ../regen.nix { inherit pkgs; };
  # Shared by template-settings-block and the structural-template-examples-*-valid
  # checks below so all three consumers of the byName/roster worked examples share
  # one import.
  structuralTemplateExamples = import ../../lib/structural-template-examples.nix {
    inherit (pkgs) lib;
  };
  rosterLib = import ../../lib/roster.nix { inherit (pkgs) lib; };
  # Parses an example's rendered `lines` (the exact Nix source text
  # templates/default/flake.nix ships) back as real Nix. The
  # structural-template-examples-*-valid checks need the *rendered text*
  # validated, not just the backing `.example` value, since a renderer bug (e.g.
  # emitting a JSON-style comma-separated list) can desync the two.
  # builtins.toFile writes content-addressed text at eval time with no
  # derivation build, so this isn't import-from-derivation.
  #
  # GOTCHA: a genuine Nix *syntax* error in the rendered lines crashes eval
  # during `import` before `builtins.tryEval` below can catch it, surfacing as a
  # raw parse error rather than the friendly `parsedFromLines.success` message.
  # The build still fails either way; only other catchable failures inside this
  # function (e.g. an out-of-bounds `builtins.elemAt`) reach that assertMsg.
  evalExampleLines =
    entry:
    let
      key = builtins.elemAt entry.path (builtins.length entry.path - 1);
      src = "{ ${builtins.concatStringsSep "\n" entry.lines} }";
    in
    (import (builtins.toFile "structural-example-${key}.nix" src)).${key};
  defaultModelFixture = import ../../lib/default-model-fixture.nix;
  legacySettingsSection = import ../../lib/legacy-settings-section.nix;

  # Shared by schema-choices and schema-secret-choices-guard (issue #872) so
  # the guard predicate is defined exactly once and can be exercised against
  # a synthetic/injected schema in a test, not only the real one.
  schemaChoiceIssues =
    schema:
    let
      inherit (pkgs.lib) filter;
      withChoices = filter (e: e ? choices) (builtins.attrValues schema);
    in
    {
      badShape = filter (
        e: !(builtins.isList e.choices) || e.choices == [ ] || !(builtins.all builtins.isString e.choices)
      ) withChoices;
      badDefault = filter (e: (e ? default) && !(builtins.elem e.default e.choices)) withChoices;
      badSecret = filter (e: e.secret or false) withChoices;
    };

  # Throws via schemaChoiceIssues on a bad schema, else returns it unchanged.
  # Shared so schema-secret-choices-guard exercises this exact assertion path
  # (not just schemaChoiceIssues in isolation) — dropping the badSecret assert
  # here would make that guard fail too, not stay silently green.
  assertSchemaChoicesOk =
    schema:
    let
      inherit (pkgs.lib) assertMsg concatStringsSep;
      issues = schemaChoiceIssues schema;
    in
    assert assertMsg (issues.badShape == [ ])
      "lib/env-schema.nix: choices must be a non-empty list of strings for: ${
        concatStringsSep ", " (map (e: e.env) issues.badShape)
      }";
    assert assertMsg (issues.badDefault == [ ])
      "lib/env-schema.nix: default is not a member of choices for: ${
        concatStringsSep ", " (map (e: e.env) issues.badDefault)
      }";
    assert assertMsg (issues.badSecret == [ ])
      "lib/env-schema.nix: choices is not supported on secret knobs — renderers only ever honor choices on nonSecret knobs (secrets get a --*-file flag, never a value-taking one): ${
        concatStringsSep ", " (map (e: e.env) issues.badSecret)
      }";
    schema;

  # Marker consistency for lib/env-schema.nix's intKind/hostConfig/hostDerived
  # fields, factored like schemaChoiceIssues so the guard can exercise this exact
  # predicate against a synthetic/injected schema, not only the real one. "Int
  # member" mirrors the isInt default test used elsewhere in this file, narrowed
  # to the schema's two known non-membership signals (secret, boxEnvOnly).
  markerConsistencyIssues =
    schema:
    let
      inherit (pkgs.lib) filter attrValues elem;
      entries = attrValues schema;
      isIntTyped = e: builtins.isInt (e.default or null);
      isFloatTyped = e: builtins.isFloat (e.default or null);
      isIntMember = e: isIntTyped e && !(e.secret or false) && !(e.boxEnvOnly or false);
    in
    {
      # Every int-typed, non-secret, non-boxEnvOnly (host-config) member must
      # declare intKind so loadConfig() knows which parser (atoiSchema vs
      # atoiNonnegSchema) it takes.
      missingIntKind = filter (e: isIntMember e && !(e ? intKind)) entries;
      # intKind must never decorate a member whose default isn't int-typed.
      intKindOnNonInt = filter (e: (e ? intKind) && !(isIntTyped e)) entries;
      # intKind, when present, must be exactly one of the two documented enum
      # values (lib/env-schema.nix header) — a typo like "positve" would
      # otherwise silently pass the presence/int-typedness checks.
      badIntKindValue = filter (
        e:
        (e ? intKind)
        && !(elem e.intKind [
          "positive"
          "nonneg"
        ])
      ) entries;
      # hostDerived implies host-config membership — it must not also carry
      # one of the schema's two known non-membership signals (secret,
      # boxEnvOnly).
      hostDerivedExcluded = filter (
        e: (e.hostDerived or false) && ((e.secret or false) || (e.boxEnvOnly or false))
      ) entries;
      # emptyDisables is documented (lib/env-schema.nix header) as
      # string-knobs-only, but nothing else enforces that: the schemaConfig
      # loaderLine cascade (lib/renderers.nix) checks bool/int/float/secret/
      # hostDerived before ever consulting emptyDisables, so a knob author
      # who puts emptyDisables on one of those would see it silently
      # ignored rather than rejected.
      emptyDisablesOnNonString = filter (
        e:
        (e.emptyDisables or false)
        && (
          renderers.flagKind e == "bool"
          || isIntTyped e
          || isFloatTyped e
          || (e.secret or false)
          || (e.hostDerived or false)
        )
      ) entries;
    };

  # Throws via markerConsistencyIssues on a bad schema, else returns it
  # unchanged. Shared so marker-consistency-guard exercises this exact assertion
  # path — dropping any one of the five asserts makes that guard fail too, rather
  # than staying silently green.
  assertMarkerConsistencyOk =
    schema:
    let
      inherit (pkgs.lib) assertMsg concatStringsSep;
      issues = markerConsistencyIssues schema;
    in
    assert assertMsg (issues.missingIntKind == [ ])
      "lib/env-schema.nix: every int-typed, non-secret, non-boxEnvOnly member must declare intKind: ${
        concatStringsSep ", " (map (e: e.env) issues.missingIntKind)
      }";
    assert assertMsg (issues.intKindOnNonInt == [ ])
      "lib/env-schema.nix: intKind must only appear on int-typed members: ${
        concatStringsSep ", " (map (e: e.env) issues.intKindOnNonInt)
      }";
    assert assertMsg (issues.badIntKindValue == [ ])
      "lib/env-schema.nix: intKind must be exactly \"positive\" or \"nonneg\": ${
        concatStringsSep ", " (map (e: e.env) issues.badIntKindValue)
      }";
    assert assertMsg (issues.hostDerivedExcluded == [ ])
      "lib/env-schema.nix: hostDerived implies host-config membership — must not also be secret or boxEnvOnly: ${
        concatStringsSep ", " (map (e: e.env) issues.hostDerivedExcluded)
      }";
    assert assertMsg (issues.emptyDisablesOnNonString == [ ])
      "lib/env-schema.nix: emptyDisables must only appear on string-typed members: ${
        concatStringsSep ", " (map (e: e.env) issues.emptyDisablesOnNonString)
      }";
    schema;

  structuralPaths = import ../../lib/structural-paths.nix;
  byNamePaths = import ../../lib/byname-paths.nix;
  resolveNixPath = import ../../lib/nixpath.nix;
  # Renders every segment-list value of a structural/byName paths attrset as its
  # dotted string form.
  dotted = attrs: map (pkgs.lib.concatStringsSep ".") (pkgs.lib.attrValues attrs);

  # The single real combined nixPath set, computed once rather than separately
  # inside flake-nixpath-exhaustive-disjoint and each collision guard, so a
  # regression here (e.g. dropping the byNamePaths splice) is visible to every
  # consumer instead of invisible to a guard recomputing its own copy.
  allNixPaths =
    let
      inherit (pkgs.lib)
        attrNames
        filter
        ;
      flakeOptionNames = filter (n: schema.${n}.flakeOption or false) (attrNames schema);
    in
    (map (n: resolveNixPath n schema.${n}) flakeOptionNames)
    ++ (dotted structuralPaths)
    ++ (dotted byNamePaths);

  # Frozen ground truth, factored into lib/pre-freeze-flake-options.nix so it
  # isn't a fourth hand-copy of a knob list living only in this check.
  preFreezeFlakeOptionNames = import ../../lib/pre-freeze-flake-options.nix;

  # Three invariants over the legacy-settings alias map:
  #  - every flakeOption knob has a lib/legacy-settings-section.nix row or is
  #    explicitly `legacySettingsExempt = true;` (a knob added after the ADR 0037
  #    Pass 2 freeze, which never had an old `settings.<section>` alias);
  #  - every row still names a live flakeOption knob -- a knob demoted to
  #    flakeOption = false; leaves a dead entry a key-existence check would miss;
  #  - legacySettingsExempt is cross-checked against preFreezeFlakeOptionNames
  #    rather than trusted at face value: the flag and the map row are hand-edited
  #    in the same PR, so they can be wrong together.
  # Factored like schemaChoiceIssues so the guard can exercise this predicate
  # against a synthetic pair, not only the real data.
  legacySettingsSectionIssues =
    { legacySettingsSection, schema }:
    let
      inherit (pkgs.lib) filter attrNames elem;
      flakeOptionNames = filter (n: schema.${n}.flakeOption or false) (attrNames schema);
    in
    {
      missing = filter (
        n: !(schema.${n}.legacySettingsExempt or false) && !(legacySettingsSection ? ${n})
      ) flakeOptionNames;
      stale = filter (n: !(schema.${n}.flakeOption or false)) (attrNames legacySettingsSection);
      # A knob marked exempt whose name appears in the frozen pre-freeze list
      # predates the freeze, so it had a real old alias -- the exemption is wrong
      # and it needs a real lib/legacy-settings-section.nix row instead.
      wronglyExempt = filter (
        n: (schema.${n}.legacySettingsExempt or false) && elem n preFreezeFlakeOptionNames
      ) flakeOptionNames;
    };

  # Throws via legacySettingsSectionIssues on a bad map/schema pair, else returns
  # legacySettingsSection unchanged. Shared so
  # legacy-settings-section-coverage-guard exercises this exact assertion path.
  assertLegacySettingsSectionOk =
    { legacySettingsSection, schema }:
    let
      inherit (pkgs.lib) assertMsg concatStringsSep;
      issues = legacySettingsSectionIssues { inherit legacySettingsSection schema; };
    in
    assert assertMsg (issues.missing == [ ])
      "lib/legacy-settings-section.nix: every flakeOption knob must have a row here or be lib/env-schema.nix legacySettingsExempt = true;: ${concatStringsSep ", " issues.missing}";
    assert assertMsg (issues.stale == [ ])
      "lib/legacy-settings-section.nix: entry has no matching lib/env-schema.nix knob (stale alias) -- remove it: ${concatStringsSep ", " issues.stale}";
    assert assertMsg (issues.wronglyExempt == [ ])
      "lib/env-schema.nix: legacySettingsExempt = true; but the knob appears in nix/checks/schema-drift.nix's frozen preFreezeFlakeOptionNames list, i.e. it predates the ADR 0037 Pass 2 freeze and had a real old settings.<section> alias -- give it a real lib/legacy-settings-section.nix row instead of an exemption: ${concatStringsSep ", " issues.wronglyExempt}";
    legacySettingsSection;

  # Uniqueness + prefix-disjointness predicate over a flat list of dotted nixPath
  # strings, factored so the guard can be exercised against a synthetic path set.
  nixPathIssues =
    nixPaths:
    let
      inherit (pkgs.lib) filter splitString;
      segmentsOf = p: splitString "." p;
      isPrefixOf =
        a: b:
        let
          la = builtins.length a;
        in
        la <= builtins.length b && a == (builtins.genList (i: builtins.elemAt b i) la);
      pairs = builtins.concatMap (
        i:
        map (j: {
          a = builtins.elemAt nixPaths i;
          b = builtins.elemAt nixPaths j;
        }) (filter (j: j != i) (builtins.genList (x: x) (builtins.length nixPaths)))
      ) (builtins.genList (x: x) (builtins.length nixPaths));
    in
    {
      collidingPairs = filter (p: isPrefixOf (segmentsOf p.a) (segmentsOf p.b)) (
        filter (p: p.a != p.b) pairs
      );
      duplicatePaths = filter (p: p.a == p.b) pairs;
    };

  # Throws via nixPathIssues on a non-disjoint / non-unique path set, else
  # returns it unchanged. Messages are deliberately source-agnostic: the colliding
  # path may be a flakeOption knob or a structural domain-tree leaf, so no single
  # source file is implicated.
  assertNixPathsOk =
    nixPaths:
    let
      inherit (pkgs.lib) assertMsg concatStringsSep;
      issues = nixPathIssues nixPaths;
    in
    assert assertMsg (issues.duplicatePaths == [ ])
      "flake nixPath values must be unique — duplicate: ${
        concatStringsSep ", " (map (p: p.a) issues.duplicatePaths)
      }";
    assert assertMsg (issues.collidingPairs == [ ])
      "flake nixPath values must be prefix-disjoint (no leaf may be an ancestor of another) — colliding pair: ${
        concatStringsSep ", " (map (p: "${p.a} vs ${p.b}") issues.collidingPairs)
      }";
    nixPaths;

  # Injects a synthetic path nesting under `leaf` into the real allNixPaths set,
  # runs it through assertNixPathsOk via tryEval, and asserts eval failed — i.e.
  # the synthetic collision was rejected, not silently accepted.
  mkNixPathCollisionGuard =
    { name, leaf }:
    let
      inherit (pkgs.lib) assertMsg;
      badPaths = allNixPaths ++ [ "${leaf}.injected" ];
      result = builtins.tryEval (assertNixPathsOk badPaths);
    in
    assert assertMsg (!result.success)
      "${name}: expected assertNixPathsOk to reject a synthetic path nesting under the leaf ${leaf}, but it evaluated successfully";
    pkgs.runCommand name { } "touch $out";

  # The anti-vacuity check for lib/default-model-fixture.nix, in both directions:
  # a schema default bump with the fixture left un-updated must fail here, and a
  # *new* model-shaped schema key (attr name "model" or ending in "Model") never
  # added to the fixture must fail too, rather than the fixture-side filterAttrs
  # silently never looking at it. Factored out so the two sync guards can
  # exercise these assertion paths against synthetic drifted schemas.
  assertFixtureMatchesSchemaOk =
    { schema, fixtureSchemaDefaults }:
    let
      inherit (pkgs.lib)
        assertMsg
        filterAttrs
        concatStringsSep
        attrNames
        hasSuffix
        filter
        ;
      mismatches = filterAttrs (
        schemaKey: expected: (schema.${schemaKey}.default or null) != expected
      ) fixtureSchemaDefaults;
      isModelShaped = name: name == "model" || hasSuffix "Model" name;
      missingFromFixture = filter (name: isModelShaped name && !(fixtureSchemaDefaults ? ${name})) (
        attrNames schema
      );
    in
    assert assertMsg (missingFromFixture == [ ])
      "lib/default-model-fixture.nix: schemaDefaults is missing model-shaped lib/env-schema.nix keys -- a new *Model schema default was added but the fixture was never updated to mirror it -- missing keys: ${concatStringsSep ", " missingFromFixture} -- update the fixture (issue #2514)";
    assert assertMsg (mismatches == { })
      "lib/default-model-fixture.nix: schemaDefaults has drifted from lib/env-schema.nix's own .default values -- mismatched keys: ${concatStringsSep ", " (attrNames mismatches)} -- update the fixture (issue #2514)";
    fixtureSchemaDefaults;

  # Asserts docSrc's generated legacy-settings-to-domain-tree mapping table
  # matches `generated`. Factored onto the shared assertMarkedBlockOk so
  # legacy-settings-mapping-doc-guard can exercise this exact path against a
  # synthetic doc, not only the real MIGRATING.md.
  assertLegacySettingsMappingDocOk =
    { docSrc, generated }:
    assertMarkedBlockOk {
      blockName = "LEGACY SETTINGS MAPPING";
      sourceDesc = "lib/legacy-settings-section.nix";
      docPath = "MIGRATING.md";
      beginMarker = "<!-- BEGIN GENERATED LEGACY SETTINGS MAPPING -- nix run .#regen -- DO NOT EDIT -->\n";
      endMarker = "<!-- END GENERATED LEGACY SETTINGS MAPPING -->";
      inherit docSrc generated;
    };

  # Shared by every documentedFacts row's check and by
  # assertLegacySettingsMappingDocOk: split docSrc on the block's BEGIN/END
  # markers, compare the committed slice against generated, else throw naming the
  # sub-block and the schema file it drifted from.
  inherit (documentedFactChecker) assertMarkedBlockOk;

  # Asserts `generated` (one of renderSettingsExampleModelsDoc/LabelsDoc/
  # ConfigDoc's output) contains, for every schema `key` in `keys`, a line
  # whose left-hand path is exactly resolveNixPath's output for that key --
  # else throws naming the offending key(s). Re-derives the expected path
  # independently via resolveNixPath rather than re-invoking the renderer,
  # so this actually catches a renderer that reverted to a hand-typed path
  # literal (issue #2557 review finding), not just a renderer disagreeing
  # with itself. Shared by settings-example-paths-resolve-nix-path and its
  # -guard sibling below, so the check and its regression guard exercise
  # this exact assertion.
  assertRendererPathsResolveOk =
    { generated, keys }:
    let
      inherit (pkgs.lib)
        concatStringsSep
        filter
        splitString
        trim
        ;
      # Each non-empty line's exact left-hand path: everything before its first
      # "=", padding trimmed. Exact rather than hasInfix, because the renderers
      # right-pad to the block's widest path, so a substring check would also
      # accept a wrong-but-prefix path ("git.merge" inside "git.merge.policy").
      lines = filter (l: l != "") (splitString "\n" generated);
      linePath = line: trim (builtins.head (splitString "=" line));
      actualPaths = map linePath lines;
      missing = filter (key: !(builtins.elem (resolveNixPath key schema.${key}) actualPaths)) keys;
    in
    if missing == [ ] then
      true
    else
      throw "assertRendererPathsResolveOk: generated output is missing the resolveNixPath-derived path for env-schema key(s): ${concatStringsSep ", " missing}";

  # The env-schema keys each of the three settings-example renderers emits
  # (lib/renderers.nix renderSettingsExampleModelsDoc/LabelsDoc/ConfigDoc),
  # shared between settings-example-paths-resolve-nix-path and its -guard
  # sibling below.
  settingsExampleModelsKeys = [
    "model"
    "scoutModel"
    "reviewModel"
    "filerModel"
  ];
  settingsExampleLabelsKeys = [
    "label"
    "inProgressLabel"
    "failedLabel"
    "completeLabel"
  ];
  settingsExampleConfigKeys = [
    "baseBranch"
    "branchPrefix"
    "mergeMode"
    "mergeGuardPaths"
    "mergePollInterval"
    "mergePollTimeout"
    "maxParallel"
    "maxJobs"
  ];

  # builtins.listToAttrs silently keeps only the FIRST of two rows sharing a
  # `name`, so a copy-pasted row `name` would delete that row's drift check from
  # the build with no warning. Distinct from checkedMerge below, which guards
  # attrset `//` rather than list-to-attrset construction.
  duplicateNames =
    names:
    builtins.attrNames (
      pkgs.lib.filterAttrs (_: occurrences: builtins.length occurrences > 1) (
        builtins.groupBy (n: n) names
      )
    );

  # One named drift check per documentedFacts row. docPath is read via
  # `../../. + "/${row.docPath}"` rather than a literal path expression, since
  # row.docPath is a runtime string and Nix path interpolation requires a literal
  # path prefix.
  documentedFactChecks =
    let
      inherit (pkgs.lib) assertMsg concatStringsSep;
      dupes = duplicateNames (map (row: row.name) documentedFacts);
    in
    assert assertMsg (dupes == [ ])
      "documented-facts registry (lib/documented-facts.nix) has duplicate row name(s), which builtins.listToAttrs would silently collapse to the first one: ${concatStringsSep ", " dupes}";
    builtins.listToAttrs (
      map (row: {
        name = row.name;
        value =
          if (row.postSplice or null) == "gofmt" then
            documentedFactChecker.assertSplicedSpanOk {
              inherit (row)
                name
                blockName
                sourceDesc
                beginMarker
                endMarker
                generated
                ;
              file = ../../. + "/${row.docPath}";
              gofmt = true;
            }
          else
            let
              docSrc = builtins.readFile (../../. + "/${row.docPath}");
            in
            assert
              (assertMarkedBlockOk {
                inherit (row)
                  blockName
                  sourceDesc
                  beginMarker
                  endMarker
                  docPath
                  generated
                  ;
                inherit docSrc;
              }) == docSrc;
            pkgs.runCommand row.name { } "touch $out";
      }) documentedFacts
    );

  # `//`'s right-hand side silently wins on a key collision, unlike a literal
  # attrset with a duplicate key. checkedMerge restores that safety where this
  # file merges documentedFactChecks into the hand-written checks below, so a
  # registry row named after an existing check throws instead of replacing it
  # with a no-op.
  checkedMerge =
    a: b:
    let
      inherit (pkgs.lib) assertMsg concatStringsSep filter;
      collisions = filter (n: builtins.hasAttr n a) (builtins.attrNames b);
    in
    assert assertMsg (collisions == [ ])
      "checkedMerge: right-hand attrset name(s) collide with the left-hand attrset and would silently overwrite it: ${concatStringsSep ", " collisions}";
    a // b;
in
checkedMerge {
  # The *-gen checks below all follow one shape: a committed generated Go file
  # must match what lib/renderers.nix produces from its Nix source registry, so
  # editing the registry without rerunning `nix run .#regen` fails the build.
  # Each shares its renderer with regen, so guard and regenerator cannot drift.
  # Only the per-check deviations are commented individually.
  driver-names-gen =
    let
      driverRegistry = import ../../lib/drivers/default.nix { inherit (pkgs) lib; };
      generated = pkgs.writeText "drivernames_gen.go.generated" (
        renderers.renderDriverNamesGo driverRegistry.entries
      );
    in
    pkgs.runCommand "driver-names-gen"
      {
        inherit generated;
        committed = ../../cmd/launcher/internal/driver/drivernames_gen.go;
      }
      ''
        diff "$generated" "$committed" \
          || { echo "cmd/launcher/internal/driver/drivernames_gen.go is out of sync with lib/drivers/default.nix — regenerate it with \`nix run .#regen\`" >&2; exit 1; }
        touch $out
      '';

  # cmd/launcher/internal/runner/mount.go's SPINDRIFT_PROMPT_DIR mount target
  # reads agentpaths.PromptsDir rather than its own literal, so renaming a baked
  # /agent/* path fails here instead of silently mounting onto a dead in-box
  # path.
  agent-paths-gen =
    let
      agentPaths = import ../../lib/agent-paths.nix;
      generated = pkgs.writeText "agentpaths_gen.go.generated" (renderers.renderAgentPathsGo agentPaths);
    in
    pkgs.runCommand "agent-paths-gen"
      {
        inherit generated;
        committed = ../../cmd/launcher/internal/agentpaths/agentpaths_gen.go;
      }
      ''
        diff "$generated" "$committed" \
          || { echo "cmd/launcher/internal/agentpaths/agentpaths_gen.go is out of sync with lib/agent-paths.nix — regenerate it with \`nix run .#regen\`" >&2; exit 1; }
        touch $out
      '';

  # gofmt-normalized before diffing, the same way `nix run .#regen` normalizes it.
  backend-registry-gen =
    let
      backends = import ../../lib/backends/default.nix;
      raw = pkgs.writeText "registry_gen.go.raw" (renderers.renderBackendRegistryGo backends);
    in
    pkgs.runCommand "backend-registry-gen"
      {
        nativeBuildInputs = [ pkgs.go ];
        inherit raw;
        committed = ../../cmd/launcher/internal/backend/registry_gen.go;
      }
      ''
        gofmt "$raw" > generated.go
        diff generated.go "$committed" \
          || { echo "cmd/launcher/internal/backend/registry_gen.go is out of sync with lib/backends/default.nix — regenerate it with \`nix run .#regen\`" >&2; exit 1; }
        touch $out
      '';

  # gofmt-normalized before diffing, as above.
  label-registry-gen =
    let
      labels = import ../../lib/labels.nix;
      raw = pkgs.writeText "labelmeta_gen.go.raw" (renderers.renderLabelRegistryGo labels);
    in
    pkgs.runCommand "label-registry-gen"
      {
        nativeBuildInputs = [ pkgs.go ];
        inherit raw;
        committed = ../../cmd/launcher/internal/doctor/labelmeta_gen.go;
      }
      ''
        gofmt "$raw" > generated.go
        diff generated.go "$committed" \
          || { echo "cmd/launcher/internal/doctor/labelmeta_gen.go is out of sync with lib/labels.nix — regenerate it with \`nix run .#regen\`" >&2; exit 1; }
        touch $out
      '';

  runtime-values-gen =
    let
      runtimeValues = import ../../lib/runtime-values.nix;
      generated = pkgs.writeText "runtimevalues_gen.go.generated" (
        renderers.renderRuntimeValuesGo runtimeValues
      );
    in
    pkgs.runCommand "runtime-values-gen"
      {
        inherit generated;
        committed = ../../cmd/launcher/internal/runner/runtimevalues_gen.go;
      }
      ''
        diff "$generated" "$committed" \
          || { echo "cmd/launcher/internal/runner/runtimevalues_gen.go is out of sync with lib/runtime-values.nix — regenerate it with \`nix run .#regen\`" >&2; exit 1; }
        touch $out
      '';

  # Source is lib/quickstart-path-table.nix, itself derived from lib/nixpath.nix
  # over lib/env-schema.nix's group/nixSubPath.
  quickstart-paths-gen =
    let
      quickstartPathTable = import ../../lib/quickstart-path-table.nix;
      generated = pkgs.writeText "quickstart_paths_gen.go.generated" (
        renderers.renderQuickstartPathsGo quickstartPathTable
      );
    in
    pkgs.runCommand "quickstart-paths-gen"
      {
        inherit generated;
        committed = ../../cmd/launcher/quickstart/quickstart_paths_gen.go;
      }
      ''
        diff "$generated" "$committed" \
          || { echo "cmd/launcher/quickstart/quickstart_paths_gen.go is out of sync with lib/quickstart-path-table.nix — regenerate it with \`nix run .#regen\`" >&2; exit 1; }
        touch $out
      '';

  subcommands-gen =
    let
      subcommands = import ../../lib/subcommands.nix;
      generated = pkgs.writeText "subcommands_gen.go.generated" (
        renderers.renderSubcommandsGo subcommands
      );
    in
    pkgs.runCommand "subcommands-gen"
      {
        inherit generated;
        committed = ../../cmd/launcher/subcommands_gen.go;
      }
      ''
        diff "$generated" "$committed" \
          || { echo "cmd/launcher/subcommands_gen.go is out of sync with lib/subcommands.nix — regenerate it with \`nix run .#regen\`" >&2; exit 1; }
        touch $out
      '';

  # gofmt-normalized before diffing: the raw renderer output is intentionally
  # unaligned, and gofmt owns the const block's column alignment.
  outcome-status-gen =
    let
      promptContract = import ../../lib/prompt-contract.nix;
      raw = pkgs.writeText "status_gen.go.raw" (
        renderers.renderOutcomeStatusGo promptContract.outcomeStatusSets
      );
    in
    pkgs.runCommand "outcome-status-gen"
      {
        nativeBuildInputs = [ pkgs.go ];
        inherit raw;
        committed = ../../cmd/launcher/internal/outcome/status_gen.go;
      }
      ''
        gofmt "$raw" > generated.go
        diff generated.go "$committed" \
          || { echo "cmd/launcher/internal/outcome/status_gen.go is out of sync with lib/prompt-contract.nix — regenerate it with \`nix run .#regen\`" >&2; exit 1; }
        touch $out
      '';

  # gofmt-normalized before diffing, as above.
  marker-channels-gen =
    let
      promptContract = import ../../lib/prompt-contract.nix;
      raw = pkgs.writeText "markerchannels_gen.go.raw" (
        renderers.renderMarkerChannelsGo promptContract.markerChannels
      );
    in
    pkgs.runCommand "marker-channels-gen"
      {
        nativeBuildInputs = [ pkgs.go ];
        inherit raw;
        committed = ../../cmd/launcher/internal/outcome/markerchannels_gen.go;
      }
      ''
        gofmt "$raw" > generated.go
        diff generated.go "$committed" \
          || { echo "cmd/launcher/internal/outcome/markerchannels_gen.go is out of sync with lib/prompt-contract.nix — regenerate it with \`nix run .#regen\`" >&2; exit 1; }
        touch $out
      '';

  # Same shape as the *-gen checks, over a template rather than Go source.
  harness-env-example =
    let
      schema = import ../../lib/env-schema.nix;
      generated = pkgs.writeText "harness.env.example.generated" (
        renderers.renderHarnessEnvExample schema
      );
    in
    pkgs.runCommand "harness-env-example"
      {
        inherit generated;
        committed = ../../templates/default/harness.env.example;
      }
      ''
        diff "$generated" "$committed" \
          || { echo "templates/default/harness.env.example is out of sync with lib/env-schema.nix — regenerate it" >&2; exit 1; }
        touch $out
      '';

  # Every env-var string literal in main.go and backend.go must have a matching
  # lib/env-schema.nix entry, and vice-versa (presence-only; value-level pinning
  # would be refactor-brittle). Scoped to those two files rather than all of
  # package main, which would also pull in flags.go's SECRET_CMD fallback -- a
  # sibling-naming convention, not a schema-registered knob.
  # lib/preambles.nix's documentArtifactKeys is the schema for what main.go may
  # read outside lib/env-schema.nix, via getenvArtifact rather than os.Getenv.
  launcher-env-coverage =
    let
      schema = import ../../lib/env-schema.nix;
      preambles = import ../../lib/preambles.nix;
      inherit (pkgs.lib)
        attrValues
        concatStringsSep
        filter
        splitString
        subtractLists
        ;
      # NOT lib.hasInfix: it wraps builtins.match with a leading/trailing `.*`,
      # whose C++ std::regex backtracking recurses per haystack character —
      # main.go + backend.go + schemaconfig_gen.go is >100KB, enough to blow the
      # evaluator's C stack (segfault, exit 139). splitString's regex has no `.*`
      # wrapper, so it doesn't recurse per haystack byte.
      containsLiteral = needle: haystack: builtins.length (splitString needle haystack) > 1;
      launcherDir = ../../cmd/launcher;
      # schemaconfig_gen.go is scanned even though loadConfig does not yet embed
      # schemaConfig, so wiring it in later doesn't fail this check for the
      # dozens of knobs whose env-var literal only lives in the generated file.
      mainGoSrc = concatStringsSep "\n" (
        map (name: builtins.readFile (launcherDir + "/${name}")) [
          "main.go"
          "backend.go"
          "schemaconfig_gen.go"
        ]
      );
      # Nix-computed plumbing main.go reads via getenvArtifact, not user-facing
      # knobs.
      documentArtifacts = preambles.documentArtifactKeys;
      schemaEnvNames = map (e: e.env) (attrValues schema);
      # Schema knobs forwarded to containers via BOX_ENV_VARS only — the Go
      # binary never reads them directly, so they need no os.Getenv call.
      boxEnvOnly = map (e: e.env) (filter (e: e.boxEnvOnly or false) (attrValues schema));
      # Forward: every schema name (that Go reads directly) must appear as a
      # string literal in main.go.
      missingFromGo = filter (name: !containsLiteral ''"${name}"'' mainGoSrc) (
        subtractLists boxEnvOnly schemaEnvNames
      );
      # Reverse: extract names from os.Getenv/getenv (1-arg), getenvArtifact
      # (2-arg), and docArtifact (1-arg) calls in main.go.
      parts = builtins.split ''(os\.Getenv|getenv|getenvArtifact|docArtifact)\("([A-Z_][A-Z0-9_]*)"[,)]'' mainGoSrc;
      goEnvNames = map (m: builtins.elemAt m 1) (filter builtins.isList parts);
      extraInGo = subtractLists (schemaEnvNames ++ documentArtifacts) goEnvNames;
    in
    assert pkgs.lib.assertMsg (
      missingFromGo == [ ]
    ) "schema knobs absent from main.go: ${concatStringsSep ", " missingFromGo}";
    assert pkgs.lib.assertMsg (extraInGo == [ ])
      "main.go reads env vars absent from schema/documentArtifactKeys: ${concatStringsSep ", " extraInGo}";
    pkgs.runCommand "launcher-env-coverage" { } "touch $out";

  # continuousDispatch's doc string is the single source rendered onto --help,
  # the man page, and docs/flake-options.md, so a stale pointer there is stale
  # everywhere. The exit-code table lives in docs/reference.md's Dogfood loop
  # section, not the README.
  continuous-dispatch-doc-reference =
    let
      schema = import ../../lib/env-schema.nix;
      inherit (pkgs.lib) assertMsg hasInfix;
      doc = schema.continuousDispatch.doc;
    in
    assert assertMsg (!hasInfix "README" doc)
      "lib/env-schema.nix: continuousDispatch.doc must not point at README for the exit-code table (issue #1879) — it lives in docs/reference.md's Dogfood loop section, got: ${doc}";
    assert assertMsg (hasInfix "docs/reference.md" doc)
      "lib/env-schema.nix: continuousDispatch.doc must point at docs/reference.md's exit-code table (issue #1879), got: ${doc}";
    assert assertMsg (hasInfix "Dogfood loop" doc)
      "lib/env-schema.nix: continuousDispatch.doc must name docs/reference.md's Dogfood loop section, not just the file (issue #1879), got: ${doc}";
    pkgs.runCommand "continuous-dispatch-doc-reference" { } "touch $out";

  # `choices` must be a non-empty list of strings and a knob's `default` must be
  # a member of its own `choices` — a knob completing values it can never legally
  # hold would silently mislead a user tab-completing it. Each of the eight
  # choice-knobs' exact value set is pinned below, so a typo or dropped value
  # fails here instead of silently narrowing what `spindrift --merge-mode <TAB>`
  # offers. The *set* of choices-bearing knob names is pinned too: the per-knob
  # asserts only fire for a knob already named here, so a ninth knob declaring
  # `choices` would otherwise go unpinned.
  schema-choices =
    let
      schema = assertSchemaChoicesOk (import ../../lib/env-schema.nix);
      inherit (pkgs.lib)
        assertMsg
        attrNames
        filter
        sort
        concatStringsSep
        ;
      choiceKnobNames = sort builtins.lessThan (filter (n: schema.${n} ? choices) (attrNames schema));
      expectedChoiceKnobNames = sort builtins.lessThan [
        "mergeMode"
        "codeForge"
        "issueTracker"
        "overlapGate"
        "mergeMethod"
        "syncMethod"
        "boxForgeAndIssueAccess"
        "networkMode"
      ];
    in
    assert assertMsg (choiceKnobNames == expectedChoiceKnobNames)
      "lib/env-schema.nix: choices-bearing knob set changed — expected exactly [ ${concatStringsSep " " expectedChoiceKnobNames} ], got [ ${concatStringsSep " " choiceKnobNames} ]; pin the new/removed knob's exact choices list in nix/checks/schema-drift.nix's schema-choices check (issue #2519)";
    assert assertMsg (
      schema.mergeMode.choices or [ ] == [
        "immediate"
        "auto"
        "manual"
      ]
    ) "lib/env-schema.nix: mergeMode.choices must be [ immediate auto manual ]";
    assert assertMsg (
      schema.codeForge.choices or [ ] == [
        "github"
        "git"
        "local"
        "forgejo"
      ]
    ) "lib/env-schema.nix: codeForge.choices must be [ github git local forgejo ]";
    assert assertMsg (
      schema.issueTracker.choices or [ ] == [
        "github"
        "local"
        "jira"
        "forgejo"
      ]
    ) "lib/env-schema.nix: issueTracker.choices must be [ github local jira forgejo ]";
    assert assertMsg (
      schema.overlapGate.choices or [ ] == [
        "defer"
        "off"
      ]
    ) "lib/env-schema.nix: overlapGate.choices must be [ defer off ]";
    assert assertMsg (
      schema.mergeMethod.choices or [ ] == [
        "merge"
        "squash"
        "rebase"
      ]
    ) "lib/env-schema.nix: mergeMethod.choices must be [ merge squash rebase ]";
    assert assertMsg (
      schema.syncMethod.choices or [ ] == [
        "rebase"
        "merge"
      ]
    ) "lib/env-schema.nix: syncMethod.choices must be [ rebase merge ]";
    assert assertMsg (
      schema.boxForgeAndIssueAccess.choices or [ ] == [
        "read-write"
        "read-only"
      ]
    ) "lib/env-schema.nix: boxForgeAndIssueAccess.choices must be [ read-write read-only ]";
    assert assertMsg (
      schema.networkMode.choices or [ ] == [
        "open"
        "no-host-loopback"
        "none"
        "host"
      ]
    ) "lib/env-schema.nix: networkMode.choices must be [ open no-host-loopback none host ]";
    pkgs.runCommand "schema-choices" { } "touch $out";

  # Anti-vacuity guard: the knob-set assertion above must actually detect an
  # added/renamed choices-bearing knob, not pass because the real schema happens
  # to have exactly the eight pinned names. Injects a ninth synthetic knob and
  # asserts via tryEval that the set-equality check rejects it.
  schema-choices-knobset-guard =
    let
      schema = import ../../lib/env-schema.nix;
      inherit (pkgs.lib)
        assertMsg
        attrNames
        filter
        sort
        ;
      badSchema = schema // {
        extraChoiceKnob = (schema.mergeMode) // {
          choices = [
            "a"
            "b"
          ];
          default = "a";
        };
      };
      choiceKnobNames = sort builtins.lessThan (
        filter (n: badSchema.${n} ? choices) (attrNames badSchema)
      );
      expectedChoiceKnobNames = sort builtins.lessThan [
        "mergeMode"
        "codeForge"
        "issueTracker"
        "overlapGate"
        "mergeMethod"
        "syncMethod"
        "boxForgeAndIssueAccess"
        "networkMode"
      ];
      result = builtins.tryEval (
        assert choiceKnobNames == expectedChoiceKnobNames;
        true
      );
    in
    assert assertMsg (!result.success)
      "schema-choices-knobset-guard: expected the choices-bearing knob-set assertion to reject a schema with an injected ninth choices knob (extraChoiceKnob), but it evaluated successfully";
    pkgs.runCommand "schema-choices-knobset-guard" { } "touch $out";

  # The completion renderers scope `choices` to nonSecret knobs (a secret gets
  # only a `--*-file` path flag, never a value-taking one), so a `choices` field
  # on a secret knob would be a silent no-op. Runs assertSchemaChoicesOk — the
  # exact function schema-choices calls — against a schema with one secret knob's
  # `choices` injected, so this also fails if the badSecret assert is dropped from
  # assertSchemaChoicesOk rather than only from schemaChoiceIssues.
  schema-secret-choices-guard =
    let
      schema = import ../../lib/env-schema.nix;
      inherit (pkgs.lib) assertMsg;
      badSchema = schema // {
        jiraToken = schema.jiraToken // {
          choices = [
            "a"
            "b"
          ];
          default = "a";
        };
      };
      result = builtins.tryEval (assertSchemaChoicesOk badSchema);
    in
    assert assertMsg (!result.success)
      "schema-secret-choices-guard: expected assertSchemaChoicesOk to reject the injected secret+choices fixture (jiraToken), but it evaluated successfully";
    pkgs.runCommand "schema-secret-choices-guard" { } "touch $out";

  # lib/flakeModule.nix's `types.enum` only protects Consumers going through the
  # flake module; a direct `mkHarness { defaults = {...}; }` caller bypasses it.
  # Proves mkHarness itself rejects an invalid `mergeMethod`, and so also fails
  # if the assert is dropped from mkHarness.nix.
  mkharness-direct-choices-guard =
    let
      inherit (pkgs.lib) assertMsg;
      result = builtins.tryEval (
        import ../../lib/mkHarness.nix {
          inherit nixpkgs system;
          defaults = {
            mergeMethod = "bogus-merge-method";
          };
          packages = p: [ p.hello ];
        }
      );
    in
    assert assertMsg (!result.success)
      "mkharness-direct-choices-guard: expected mkHarness to reject a direct-caller `defaults.mergeMethod = \"bogus-merge-method\"` (not a member of lib/env-schema.nix's mergeMethod.choices), but it evaluated successfully";
    pkgs.runCommand "mkharness-direct-choices-guard" { } "touch $out";

  # Gate-not-triggered counterpart: without it, an unrelated eval failure in the
  # mkHarness call above (a new required arg, an added unrelated assert) would
  # make the guard pass vacuously even with the choices assert deleted. Proves
  # the same call shape evaluates cleanly for an in-choice value, so the guard's
  # failure is attributable to the choices assert specifically.
  mkharness-direct-choices-guard-not-triggered =
    let
      inherit (pkgs.lib) assertMsg;
      result = builtins.tryEval (
        import ../../lib/mkHarness.nix {
          inherit nixpkgs system;
          defaults = {
            mergeMethod = "squash";
          };
          packages = p: [ p.hello ];
        }
      );
    in
    assert assertMsg result.success
      "mkharness-direct-choices-guard-not-triggered: expected mkHarness to accept a direct-caller `defaults.mergeMethod = \"squash\"` (a member of lib/env-schema.nix's mergeMethod.choices), but it failed to evaluate";
    pkgs.runCommand "mkharness-direct-choices-guard-not-triggered" { } "touch $out";

  # A null choice value must be rejected like any other non-member value —
  # a `value == null -> null` skip in choiceViolations would let it through and
  # documentSettings would render `MERGE_METHOD=""` via `toString null`.
  # mkharness-direct-choices-guard above cannot catch that regression, since null
  # never reaches its `lib.elem value choices` check.
  mkharness-direct-choices-guard-null =
    let
      inherit (pkgs.lib) assertMsg;
      result = builtins.tryEval (
        import ../../lib/mkHarness.nix {
          inherit nixpkgs system;
          defaults = {
            mergeMethod = null;
          };
          packages = p: [ p.hello ];
        }
      );
    in
    assert assertMsg (!result.success)
      "mkharness-direct-choices-guard-null: expected mkHarness to reject a direct-caller `defaults.mergeMethod = null` (not a member of lib/env-schema.nix's mergeMethod.choices), but it evaluated successfully";
    pkgs.runCommand "mkharness-direct-choices-guard-null" { } "touch $out";

  # Proves lib/jira-status-mapping.nix's `parse` is actually wired into
  # mkHarness's eval-time assert chain, not just exercised in isolation by
  # nix/checks/jira-status-mapping.nix.
  mkharness-jira-status-mapping-guard =
    let
      inherit (pkgs.lib) assertMsg;
      result = builtins.tryEval (
        import ../../lib/mkHarness.nix {
          inherit nixpkgs system;
          defaults = {
            issueTracker = "jira";
            jiraStatusMapping = builtins.toJSON { bogusKey = "Done"; };
          };
          packages = p: [ p.hello ];
        }
      );
    in
    assert assertMsg (!result.success)
      "mkharness-jira-status-mapping-guard: expected mkHarness to reject a direct-caller `defaults.jiraStatusMapping` with an unknown key (\"bogusKey\", not a member of lib/jira-status-mapping.nix's validKeys) under ISSUE_TRACKER=jira, but it evaluated successfully";
    pkgs.runCommand "mkharness-jira-status-mapping-guard" { } "touch $out";

  # Gate-not-triggered counterpart, as above.
  mkharness-jira-status-mapping-guard-not-triggered =
    let
      inherit (pkgs.lib) assertMsg;
      result = builtins.tryEval (
        import ../../lib/mkHarness.nix {
          inherit nixpkgs system;
          defaults = {
            issueTracker = "jira";
            jiraStatusMapping = builtins.toJSON { inProgress = "In Progress"; };
          };
          packages = p: [ p.hello ];
        }
      );
    in
    assert assertMsg result.success
      "mkharness-jira-status-mapping-guard-not-triggered: expected mkHarness to accept a direct-caller `defaults.jiraStatusMapping` with only valid keys (\"inProgress\") under ISSUE_TRACKER=jira, but it failed to evaluate";
    pkgs.runCommand "mkharness-jira-status-mapping-guard-not-triggered" { } "touch $out";

  # Proves the ISSUE_TRACKER gate itself (lib/mkHarness.nix's
  # jiraStatusMappingOk): a non-jira tracker never reaches
  # backend.go's jira.ParseStatusMapping call, so a stale/typoed
  # JIRA_STATUS_MAPPING left over from a prior ISSUE_TRACKER=jira
  # configuration must not fail a github-tracker build.
  mkharness-jira-status-mapping-guard-non-jira-tracker-not-triggered =
    let
      inherit (pkgs.lib) assertMsg;
      result = builtins.tryEval (
        import ../../lib/mkHarness.nix {
          inherit nixpkgs system;
          defaults = {
            issueTracker = "github";
            jiraStatusMapping = builtins.toJSON { bogusKey = "Done"; };
          };
          packages = p: [ p.hello ];
        }
      );
    in
    assert assertMsg result.success
      "mkharness-jira-status-mapping-guard-non-jira-tracker-not-triggered: expected mkHarness to accept a direct-caller `defaults.jiraStatusMapping` with an unknown key under ISSUE_TRACKER=github (the knob is dead config there), but it failed to evaluate";
    pkgs.runCommand "mkharness-jira-status-mapping-guard-non-jira-tracker-not-triggered" { }
      "touch $out";

  # tests/helper.bash's set_box_env fixture must export every boxEnv = true
  # schema knob, so the entrypoint-*.bats suites exercise the same defaults the
  # nix preamble bakes into the image at build time.
  box-env-fixture-coverage =
    let
      schema = import ../../lib/env-schema.nix;
      generated = pkgs.writeText "box_env_gen.bash.generated" (renderers.renderSetBoxEnvFixture schema);
    in
    pkgs.runCommand "box-env-fixture-coverage"
      {
        inherit generated;
        committed = ../../tests/box_env_gen.bash;
      }
      ''
        diff "$generated" "$committed" \
          || { echo "tests/box_env_gen.bash is out of sync with lib/env-schema.nix — regenerate it with \`nix run .#regen\`" >&2; exit 1; }
        touch $out
      '';

  launcher-flag-table =
    let
      schema = import ../../lib/env-schema.nix;
      generated = pkgs.writeText "flagtable_gen.go.generated" (renderers.renderFlagTableGo schema);
    in
    pkgs.runCommand "launcher-flag-table"
      {
        inherit generated;
        committed = ../../cmd/launcher/flagtable_gen.go;
      }
      ''
        diff "$generated" "$committed" \
          || { echo "cmd/launcher/flagtable_gen.go is out of sync with lib/env-schema.nix or lib/renderers.nix's groupOrder — regenerate it" >&2; exit 1; }
        touch $out
      '';

  # gofmt-normalized before diffing: the raw renderer output is intentionally
  # unaligned, and gofmt owns the struct/composite-literal column alignment.
  launcher-schema-config =
    let
      schema = import ../../lib/env-schema.nix;
      raw = pkgs.writeText "schemaconfig_gen.go.raw" (renderers.renderSchemaConfigGo schema);
    in
    pkgs.runCommand "launcher-schema-config"
      {
        nativeBuildInputs = [ pkgs.go ];
        inherit raw;
        committed = ../../cmd/launcher/schemaconfig_gen.go;
      }
      ''
        gofmt "$raw" > generated.go
        diff generated.go "$committed" \
          || { echo "cmd/launcher/schemaconfig_gen.go is out of sync with lib/env-schema.nix — regenerate it" >&2; exit 1; }
        touch $out
      '';

  # gofmt-normalized before diffing, as above.
  promptassembly-boxenv-gen =
    let
      promptAssemblyBoxEnv = import ../../lib/promptassembly-boxenv.nix;
      raw = pkgs.writeText "boxenv_gen.go.raw" (
        renderers.renderPromptAssemblyBoxEnvGo promptAssemblyBoxEnv
      );
    in
    pkgs.runCommand "promptassembly-boxenv-gen"
      {
        nativeBuildInputs = [ pkgs.go ];
        inherit raw;
        committed = ../../cmd/launcher/internal/promptassembly/boxenv_gen.go;
      }
      ''
        gofmt "$raw" > generated.go
        diff generated.go "$committed" \
          || { echo "cmd/launcher/internal/promptassembly/boxenv_gen.go is out of sync with lib/promptassembly-boxenv.nix — regenerate it with \`nix run .#regen\`" >&2; exit 1; }
        touch $out
      '';

  # Sources are lib/env-schema.nix plus the hand-declared structural knobs in
  # lib/structural-options-doc.nix.
  flake-options-doc =
    let
      schema = import ../../lib/env-schema.nix;
      structuralOptionsDoc = import ../../lib/structural-options-doc.nix;
      structuralPaths = import ../../lib/structural-paths.nix;
      generated = pkgs.writeText "flake-options.md.generated" (
        renderers.renderFlakeOptionsDocFull schema structuralOptionsDoc structuralPaths
      );
    in
    pkgs.runCommand "flake-options-doc"
      {
        inherit generated;
        committed = ../../docs/flake-options.md;
      }
      ''
        diff "$generated" "$committed" \
          || { echo "docs/flake-options.md is out of sync with lib/env-schema.nix — regenerate it" >&2; exit 1; }
        touch $out
      '';

  # checkEntry inside lib/structural-template-examples.nix only regex-matches the
  # *rendered text* of each worked example; it never runs the example *values*
  # through real validation, so an unusable example could ship silently. This
  # check evaluates the roster example's `.example` value for real:
  #  - it must survive normalizeRoster unchanged;
  #  - every entry must carry a non-empty description and tools list, since a
  #    Driver renders `description: ""` / `tools: [ ]` for either omission,
  #    producing a capability-less agent;
  #  - every entry's mode/description/tools/promptFile/effort must equal its
  #    defaultRoster counterpart's. `model` is exempt -- it is the one field a
  #    Consumer copying this example is expected to freely customize;
  #  - every entry's normalizeRoster-resolved promptFile must name a file that
  #    actually exists under templates/default/prompts/.
  # The field-equality and promptFile arms close a bug class rather than one
  # field: covering description/tools by name let the same bug recur through
  # promptFile, where normalizeRoster silently injected a wrong default.
  structural-template-examples-roster-valid =
    let
      inherit (pkgs.lib) assertMsg;
      rosterEntry = builtins.head (
        builtins.filter (
          e:
          e.path == [
            "agents"
            "models"
            "roster"
          ]
        ) structuralTemplateExamples
      );
      parsedFromLines = builtins.tryEval (
        let
          r = evalExampleLines rosterEntry;
        in
        builtins.deepSeq r r
      );
      roster = if parsedFromLines.success then parsedFromLines.value else rosterEntry.example;
      normalizeResult = builtins.tryEval (
        let
          r = rosterLib.normalizeRoster roster;
        in
        builtins.deepSeq r r
      );
      missingDescription = builtins.filter (e: (e.description or "") == "") roster;
      missingTools = builtins.filter (e: (e.tools or [ ]) == [ ]) roster;
      defaultRosterEntries = rosterLib.defaultRoster { };
      knownNames = map (d: d.name) defaultRosterEntries;
      entryFor = name: builtins.head (builtins.filter (d: d.name == name) defaultRosterEntries);
      fieldsMustMatchDefault = [
        "mode"
        "description"
        "tools"
        "promptFile"
        "effort"
      ];
      fieldMismatches = builtins.concatMap (
        e:
        if !(builtins.elem e.name knownNames) then
          [ ]
        else
          let
            defaults = entryFor e.name;
          in
          builtins.concatMap (
            f: if (e.${f} or null) != (defaults.${f} or null) then [ "${e.name}.${f}" ] else [ ]
          ) fieldsMustMatchDefault
      ) roster;
      normalized = if normalizeResult.success then normalizeResult.value else roster;
      promptsDir = ../../templates/default/prompts;
      missingPromptFiles = builtins.filter (
        e: !(builtins.pathExists (promptsDir + "/${e.promptFile}"))
      ) normalized;
    in
    assert assertMsg (parsedFromLines.success)
      "structural-template-examples-roster-valid (issue #2572): lib/structural-template-examples.nix's roster example's rendered `lines` threw a catchable error when parsed back as Nix (a renderer bug can desync `lines` from the backing `.example` value even though the value itself stays valid) -- note a genuine Nix syntax error in `lines` instead crashes eval with a raw parse error before this assert is reached, so it fails the build with different text but still fails it";
    assert assertMsg (parsedFromLines.value == rosterEntry.example)
      "structural-template-examples-roster-valid (issue #2572): the roster example's rendered `lines`, parsed back as Nix, must equal its backing `.example` value -- they have desynced";
    assert assertMsg (normalizeResult.success)
      "structural-template-examples-roster-valid (issue #2572): lib/structural-template-examples.nix's roster example must survive rosterLib.normalizeRoster without throwing";
    assert assertMsg (missingDescription == [ ])
      "structural-template-examples-roster-valid (issue #2572): every roster example entry must carry a non-empty description (a Driver renders description: \"\" otherwise, producing a broken agent) -- offending entries: ${
        builtins.toJSON (map (e: e.name) missingDescription)
      }";
    assert assertMsg (missingTools == [ ])
      "structural-template-examples-roster-valid (issue #2572): every roster example entry must carry a non-empty tools list (a Driver renders tools: [ ] otherwise, producing a capability-less agent) -- offending entries: ${
        builtins.toJSON (map (e: e.name) missingTools)
      }";
    assert assertMsg (fieldMismatches == [ ])
      "structural-template-examples-roster-valid (issue #2572 round 3): every roster example entry's mode/description/tools/promptFile/effort must match rosterLib.defaultRoster { }'s entry of the same name (model is exempt -- it's the field a Consumer is expected to customize) -- mismatched fields: ${builtins.toJSON fieldMismatches}";
    assert assertMsg (missingPromptFiles == [ ])
      "structural-template-examples-roster-valid (issue #2572 round 3): every roster example entry's promptFile (after rosterLib.normalizeRoster's default injection) must resolve to a file under templates/default/prompts/ -- offending entries: ${
        builtins.toJSON (map (e: { inherit (e) name promptFile; }) missingPromptFiles)
      }";
    pkgs.runCommand "structural-template-examples-roster-valid" { } "touch $out";

  # The byName half. It has no dedicated normalize function the way roster does,
  # so this runs it through rosterLib.defaultRoster's own byName argument --
  # a deliberate proxy for flakeModule.nix's byNameOption submodule shape, since
  # types.attrsOf doesn't constrain key names the way defaultRoster's runtime
  # checks do (it throws on an unknown byName agent name or field).
  structural-template-examples-byname-valid =
    let
      inherit (pkgs.lib) assertMsg;
      byNameEntry = builtins.head (
        builtins.filter (
          e:
          e.path == [
            "agents"
            "models"
            "byName"
          ]
        ) structuralTemplateExamples
      );
      parsedFromLines = builtins.tryEval (
        let
          r = evalExampleLines byNameEntry;
        in
        builtins.deepSeq r r
      );
      byName = if parsedFromLines.success then parsedFromLines.value else byNameEntry.example;
      result = builtins.tryEval (
        let
          r = rosterLib.defaultRoster { inherit byName; };
        in
        builtins.deepSeq r r
      );
    in
    assert assertMsg (parsedFromLines.success)
      "structural-template-examples-byname-valid (issue #2572): lib/structural-template-examples.nix's byName example's rendered `lines` threw a catchable error when parsed back as Nix (a renderer bug can desync `lines` from the backing `.example` value even though the value itself stays valid) -- note a genuine Nix syntax error in `lines` instead crashes eval with a raw parse error before this assert is reached, so it fails the build with different text but still fails it";
    assert assertMsg (parsedFromLines.value == byNameEntry.example)
      "structural-template-examples-byname-valid (issue #2572): the byName example's rendered `lines`, parsed back as Nix, must equal its backing `.example` value -- they have desynced";
    assert assertMsg (result.success)
      "structural-template-examples-byname-valid (issue #2572): lib/structural-template-examples.nix's byName example must survive rosterLib.defaultRoster { byName = ...; } without throwing";
    pkgs.runCommand "structural-template-examples-byname-valid" { } "touch $out";

  # The generated man page must render (mandoc parses it) and totally cover the
  # schema: every SH section, every OPTIONS group, every non-secret flag, and
  # every secret env var. A new knob with no man-page presence fails here.
  launcher-manpage =
    let
      schema = import ../../lib/env-schema.nix;
      inherit (pkgs.lib)
        filter
        attrValues
        concatMapStrings
        replaceStrings
        unique
        ;
      # Roff renders the flag as \-\- with every hyphen escaped; match that form.
      roffFlag = e: "\\-\\-" + replaceStrings [ "-" ] [ "\\-" ] (renderers.toKebab e.env);
      nonSecret = filter (e: !(e.secret or false)) (attrValues schema);
      secretEntries = filter (e: e.secret or false) (attrValues schema);
      groups = unique (map (e: e.group) nonSecret);
      groupChecks = concatMapStrings (g: "need -F '.SS ${g}'\n") groups;
      flagChecks = concatMapStrings (e: "need -F '${roffFlag e}'\n") nonSecret;
      secretChecks = concatMapStrings (e: "need -F '${e.env}'\n") secretEntries;
      subcommands = import ../../lib/subcommands.nix;
      subcommandChecks = concatMapStrings (s: "need -F '.B ${s.name}'\n") subcommands;
    in
    pkgs.runCommand "launcher-manpage"
      {
        nativeBuildInputs = [ pkgs.mandoc ];
        man = "${harness.packages.spindrift-manpage}/share/man/man1/spindrift.1";
      }
      ''
        need() { grep -q "$@" "$man" || { echo "man page missing: $*" >&2; exit 1; }; }
        # Renders without a fatal parse error.
        mandoc -man -Tascii "$man" >/dev/null
        for s in NAME SYNOPSIS DESCRIPTION SUBCOMMANDS OPTIONS ENVIRONMENT FILES EXAMPLES; do
          grep -Eq "^\.SH \"?$s" "$man" || { echo "man page missing .SH $s" >&2; exit 1; }
        done
        ${groupChecks}
        ${flagChecks}
        ${secretChecks}
        ${subcommandChecks}
        # A presence-style bool flag (kind = "bool", issue #2145) renders its
        # name with no italic type placeholder; it never emits \fIbool\fR.
        if grep -qF '\fIbool\fR' "$man"; then
          echo "man page renders a \\fIbool\\fR type placeholder for a presence flag" >&2
          exit 1
        fi
        touch $out
      '';

  # Pure-eval pin on renderZshCompletion's shape: a schema flag, its alias, and a
  # secret file flag must each carry a `[description]` annotation sourced from
  # the schema's `doc` string, and a secret file flag's argument must complete via
  # `_files`. Complements launcher-zsh-completion below, which covers the built
  # artifact end to end; this pins the renderer's output without a store build.
  renderer-zsh-completion-shape =
    let
      schema = import ../../lib/env-schema.nix;
      subcommandRegistry = import ../../lib/subcommands.nix;
      inherit (pkgs.lib) assertMsg hasInfix;
      out = renderers.renderZshCompletion schema subcommandRegistry;
    in
    assert assertMsg (hasInfix "#compdef spindrift" out)
      "renderZshCompletion must emit a #compdef spindrift header, got: ${out}";
    assert assertMsg (hasInfix "'--issue-number:${schema.issueNumber.doc}'" out)
      "renderZshCompletion must annotate --issue-number with its schema doc string, got: ${out}";
    assert assertMsg (hasInfix "'--issue:${schema.issueNumber.doc}'" out)
      "renderZshCompletion must complete the --issue alias with a description, got: ${out}";
    assert assertMsg (hasInfix "'--gh-token-file:${schema.ghToken.doc}'" out)
      "renderZshCompletion must annotate --gh-token-file with its schema doc string, got: ${out}";
    assert assertMsg (hasInfix ''case "$prev" in'' out && hasInfix "--gh-token-file" out)
      "renderZshCompletion must complete a --*-file flag's argument via a case \"$prev\" branch, got: ${out}";
    assert assertMsg (hasInfix "_files" out)
      "renderZshCompletion must complete a --*-file flag's argument via _files, got: ${out}";
    # Regression pin for issue #552's review round 1: an `_arguments -C
    # ... '*::arg:->args'` state machine with no `args)` case arm silently
    # swallows every word after the subcommand, so flags never complete
    # post-subcommand even though the flags array itself looks complete.
    # Pin the flag-prefix branch to an unconditional, reachable _describe
    # call on the flags array instead of a case-dispatched state.
    assert assertMsg (hasInfix ''if [[ "$cur" == -* ]]'' out)
      "renderZshCompletion must branch on a literal cur/prev flag-prefix check, not an _arguments state machine, got: ${out}";
    assert assertMsg (hasInfix "_describe -t options 'spindrift flag' flags" out)
      "renderZshCompletion's flag-prefix branch must _describe the flags array directly (reachable, unconditional), got: ${out}";
    assert assertMsg (!hasInfix "_arguments" out)
      "renderZshCompletion must not use _arguments' '*::state:->state' catch-all — issue #552 review found it swallows post-subcommand words with no matching case arm, got: ${out}";
    pkgs.runCommand "renderer-zsh-completion-shape" { } "touch $out";

  # A knob carrying both `alias` and `choices` must complete its value list for
  # *either* flag form. No real schema knob combines the two, so this uses a
  # synthetic schema deliberately isolated from lib/env-schema.nix, rather than
  # coupling fixture data to the runtime schema.
  renderer-choices-alias-shape =
    let
      inherit (pkgs.lib) assertMsg hasInfix;
      syntheticSchema = {
        aliasedChoice = {
          env = "ALIASED_CHOICE";
          doc = "test-only knob carrying both alias and choices";
          alias = "ac";
          choices = [
            "one"
            "two"
          ];
        };
      };
      bashOut = renderers.renderBashCompletion syntheticSchema [ ];
      zshOut = renderers.renderZshCompletion syntheticSchema [ ];
    in
    assert assertMsg
      (hasInfix ''
        --aliased-choice|--ac)
          # shellcheck disable=SC2207 # COMPREPLY split-on-space is the standard completion idiom; mapfile needs bash 4+
          COMPREPLY=($(compgen -W "one two" -- "$cur"))
          return 0
          ;;
      '' bashOut)
      "renderBashCompletion's choicesFlagBranch must complete both the canonical flag name and the --ac alias to the choices list in one case arm, got: ${bashOut}";
    assert assertMsg
      (hasInfix ''
        --aliased-choice|--ac)
          compadd -- one two
          return
          ;;
      '' zshOut)
      "renderZshCompletion's choicesFlagBranch must complete both the canonical flag name and the --ac alias to the choices list in one case arm, got: ${zshOut}";
    pkgs.runCommand "renderer-choices-alias-shape" { } "touch $out";

  # Dynamic issue-number completion gating must be *derived* from each registry
  # entry's dynamicIssueCompletion field, not a list independent of the passed-in
  # subcommandRegistry. The synthetic registry's names are not real subcommands,
  # so a renderer coincidentally matching the production dispatch/preview/recover
  # literal fails here. "research" is mirrored by name (unflagged, as in the real
  # registry) to pin the exclusion: a subcommand can carry issue-shaped `usage`
  # text and still be deliberately absent from dynamic completion.
  renderer-issue-completion-registry-shape =
    let
      inherit (pkgs.lib) assertMsg hasInfix;
      syntheticSubcommandRegistry = [
        {
          name = "alpha";
          doc = "opts in to dynamic issue completion";
          dynamicIssueCompletion = true;
        }
        {
          name = "beta";
          doc = "leaves dynamicIssueCompletion unset";
        }
        {
          name = "gamma";
          doc = "opts out explicitly";
          dynamicIssueCompletion = false;
        }
        {
          name = "research";
          doc = "takes an issue list but stays excluded (issue #556)";
        }
      ];
      bashOut = renderers.renderBashCompletion { } syntheticSubcommandRegistry;
      fishOut = renderers.renderFishCompletion { } syntheticSubcommandRegistry;
      zshOut = renderers.renderZshCompletion { } syntheticSubcommandRegistry;
      excluded = [
        "beta"
        "gamma"
        "research"
      ];
    in
    assert assertMsg (hasInfix "alpha)" bashOut)
      "renderBashCompletion's issue-completion case arm must include a dynamicIssueCompletion = true entry, got: ${bashOut}";
    assert assertMsg (builtins.all (n: !hasInfix "${n})" bashOut) excluded)
      "renderBashCompletion's issue-completion case arm must exclude entries without dynamicIssueCompletion = true, got: ${bashOut}";
    # The closing "'" right after "alpha" makes this an exact-membership pin, not
    # a prefix check: any excluded name appended or prepended would push the
    # closing quote along, so this one assertion covers inclusion and exclusion.
    assert assertMsg (hasInfix "'__fish_seen_subcommand_from alpha'" fishOut)
      "renderFishCompletion's __fish_seen_subcommand_from predicate must be exactly the dynamicIssueCompletion = true entries, got: ${fishOut}";
    assert assertMsg (hasInfix "alpha)" zshOut)
      "renderZshCompletion's issue-completion case arm must include a dynamicIssueCompletion = true entry, got: ${zshOut}";
    assert assertMsg (builtins.all (n: !hasInfix "${n})" zshOut) excluded)
      "renderZshCompletion's issue-completion case arm must exclude entries without dynamicIssueCompletion = true, got: ${zshOut}";
    pkgs.runCommand "renderer-issue-completion-registry-shape" { } "touch $out";

  # The three launcher-*-completion checks below share one shape: the generated
  # script must totally cover the schema and lib/subcommands.nix — every
  # non-secret flag, the --issue alias, every secret --*-file/--*-cmd flag, every
  # choices value list, and every registered subcommand — so a new knob or
  # subcommand with no completion presence fails here. Each shell's own quoting
  # dictates how a needle is boundary-checked; only those differences are
  # commented per-check below.
  launcher-bash-completion =
    let
      schema = import ../../lib/env-schema.nix;
      subcommandRegistry = import ../../lib/subcommands.nix;
      inherit (pkgs.lib)
        filter
        attrValues
        concatMapStrings
        concatStringsSep
        ;
      nonSecret = filter (e: !(e.secret or false)) (attrValues schema);
      secretEntries = filter (e: e.secret or false) (attrValues schema);
      choicesKnobs = filter (e: e ? choices) nonSecret;
      subcommands = map (s: s.name) subcommandRegistry;
      # Token-boundary match (quote or whitespace on both sides): a plain
      # substring grep would let `--issue` pass as covered merely because
      # `--issue-number` contains it as a prefix.
      flagChecks = concatMapStrings (e: "need '--${renderers.toKebab e.env}'\n") nonSecret;
      aliasChecks = concatMapStrings (e: if e ? alias then "need '--${e.alias}'\n" else "") nonSecret;
      secretChecks = concatMapStrings (e: "need '--${renderers.toKebab e.env}-file'\n") secretEntries;
      secretCmdChecks = concatMapStrings (e: "need '--${renderers.toKebab e.env}-cmd'\n") secretEntries;
      # Subcommand names are plain English words that can show up in a comment,
      # so a per-word boundary check would pass even with one missing. Require
      # the exact assembled list, so a dropped/renamed/reordered subcommand fails.
      subcommandLine = concatStringsSep " " subcommands;
      # Pin the exact `compgen -W "..."` string, not a per-word check, so a value
      # attached to the wrong flag (or dropped) fails here.
      choicesChecks = concatMapStrings (
        e:
        "grep -qF -- 'compgen -W \"${concatStringsSep " " e.choices}\"' \"$completion\" "
        + "|| { echo 'bash completion missing choices for --${renderers.toKebab e.env}' >&2; exit 1; }\n"
      ) choicesKnobs;
      # Dynamic issue-number completion gates on exactly the
      # dynamicIssueCompletion = true entries, not the full subcommand set
      # (build/doctor take no issue argument). Derived the same way
      # renderBashCompletion derives it, so this can't drift from the renderer.
      issueCaseLine = concatStringsSep "|" (renderers.issueCompletionSubcommands subcommandRegistry);
    in
    pkgs.runCommand "launcher-bash-completion"
      {
        nativeBuildInputs = [
          pkgs.bash
          pkgs.shellcheck
        ];
        completion = "${harness.packages.spindrift-bash-completion}/share/bash-completion/completions/spindrift";
      }
      ''
        need() {
          grep -Eq -- "(^|[\"[:space:]])$1([\"[:space:]]|\$)" "$completion" \
            || { echo "bash completion missing: $1" >&2; exit 1; }
        }
        bash -n "$completion"
        shellcheck --shell=bash "$completion"
        ${flagChecks}
        ${aliasChecks}
        ${secretChecks}
        ${secretCmdChecks}
        grep -qF -- '${subcommandLine}' "$completion" \
          || { echo "bash completion missing subcommand list: ${subcommandLine}" >&2; exit 1; }
        ${choicesChecks}
        grep -qF -- '${issueCaseLine})' "$completion" \
          || { echo "bash completion missing issue-completion case arm: ${issueCaseLine}" >&2; exit 1; }
        grep -qF -- 'spindrift __complete-issues' "$completion" \
          || { echo "bash completion never shells out to __complete-issues" >&2; exit 1; }
        touch $out
      '';

  launcher-fish-completion =
    let
      schema = import ../../lib/env-schema.nix;
      subcommandRegistry = import ../../lib/subcommands.nix;
      inherit (pkgs.lib)
        filter
        attrValues
        concatMapStrings
        ;
      nonSecret = filter (e: !(e.secret or false)) (attrValues schema);
      secretEntries = filter (e: e.secret or false) (attrValues schema);
      choicesKnobs = filter (e: e ? choices) nonSecret;
      subcommands = map (s: s.name) subcommandRegistry;
      # fish's `-l LONG_OPTION` takes the flag name without its leading `--`,
      # so the needle is `-l <name>` (still boundary-checked on both sides:
      # `-l issue` must not match inside `-l issue-number`).
      flagChecks = concatMapStrings (e: "need '-l ${renderers.toKebab e.env}'\n") nonSecret;
      aliasChecks = concatMapStrings (e: if e ? alias then "need '-l ${e.alias}'\n" else "") nonSecret;
      secretChecks = concatMapStrings (e: "need '-l ${renderers.toKebab e.env}-file'\n") secretEntries;
      secretCmdChecks = concatMapStrings (e: "need '-l ${renderers.toKebab e.env}-cmd'\n") secretEntries;
      # Subcommands render one per line as `-a '<name>'`; that exact quoted token
      # can't appear incidentally in a comment, so a fixed-string search suffices.
      subcommandChecks = concatMapStrings (s: "needF \"-a '${s}'\"\n") subcommands;
      choicesChecks = concatMapStrings (
        e: "needF \"-a '${builtins.concatStringsSep " " e.choices}'\"\n"
      ) choicesKnobs;
      # Derived the same way renderFishCompletion derives it, as above.
      issueSeenFrom = "__fish_seen_subcommand_from ${builtins.concatStringsSep " " (renderers.issueCompletionSubcommands subcommandRegistry)}";
    in
    pkgs.runCommand "launcher-fish-completion"
      {
        nativeBuildInputs = [ pkgs.fish ];
        completion = "${harness.packages.spindrift-fish-completion}/share/fish/vendor_completions.d/spindrift.fish";
      }
      ''
        need() {
          grep -Eq -- "(^|[\"'[:space:]])$1([\"'[:space:]]|\$)" "$completion" \
            || { echo "fish completion missing: $1" >&2; exit 1; }
        }
        needF() {
          grep -qF -- "$1" "$completion" \
            || { echo "fish completion missing: $1" >&2; exit 1; }
        }
        fish -n "$completion"
        ${flagChecks}
        ${aliasChecks}
        ${secretChecks}
        ${secretCmdChecks}
        ${subcommandChecks}
        ${choicesChecks}
        needF "${issueSeenFrom}"
        needF "-a '(spindrift __complete-issues 2>/dev/null)'"
        touch $out
      '';

  # renderZshCompletion emits each entry as a single-quoted `_describe` pair
  # `'--flag:description'`, so name-followed-by-`:` inside the opening quote is
  # itself an unambiguous token boundary — a substring check suffices, with no
  # --issue vs --issue-number collision.
  launcher-zsh-completion =
    let
      schema = import ../../lib/env-schema.nix;
      subcommandRegistry = import ../../lib/subcommands.nix;
      inherit (pkgs.lib)
        filter
        attrValues
        concatMapStrings
        ;
      nonSecret = filter (e: !(e.secret or false)) (attrValues schema);
      secretEntries = filter (e: e.secret or false) (attrValues schema);
      choicesKnobs = filter (e: e ? choices) nonSecret;
      subcommands = map (s: s.name) subcommandRegistry;
      flagChecks = concatMapStrings (e: "need \"'--${renderers.toKebab e.env}:\"\n") nonSecret;
      aliasChecks = concatMapStrings (e: if e ? alias then "need \"'--${e.alias}:\"\n" else "") nonSecret;
      secretChecks = concatMapStrings (e: "need \"'--${renderers.toKebab e.env}-file:\"\n") secretEntries;
      secretCmdChecks = concatMapStrings (
        e: "need \"'--${renderers.toKebab e.env}-cmd:\"\n"
      ) secretEntries;
      subcommandChecks = concatMapStrings (s: "need \"'${s}:\"\n") subcommands;
      # Pin the exact `compadd -- ...` list, not a per-word check, as above.
      choicesChecks = concatMapStrings (
        e: "need 'compadd -- ${builtins.concatStringsSep " " e.choices}'\n"
      ) choicesKnobs;
      # Dynamic issue-number completion (issue #556) must gate on exactly
      # the registry's dynamicIssueCompletion = true entries, not the full
      # Derived the same way renderZshCompletion derives it, as above.
      issueCaseLine = builtins.concatStringsSep "|" (
        renderers.issueCompletionSubcommands subcommandRegistry
      );
    in
    pkgs.runCommand "launcher-zsh-completion"
      {
        nativeBuildInputs = [ pkgs.zsh ];
        completion = "${harness.packages.spindrift-zsh-completion}/share/zsh/site-functions/_spindrift";
      }
      ''
        need() {
          grep -qF -- "$1" "$completion" \
            || { echo "zsh completion missing: $1" >&2; exit 1; }
        }
        zsh -n "$completion"
        ${flagChecks}
        ${aliasChecks}
        ${secretChecks}
        ${secretCmdChecks}
        ${subcommandChecks}
        ${choicesChecks}
        need '${issueCaseLine})'
        need '_describe -t issues'
        need 'spindrift __complete-issues'
        touch $out
      '';

  # ADR 0037 Pass 2: the flake path is derived, not stored — every
  # flakeOption = true knob declares a non-empty string `group`, and
  # lib/nixpath.nix's resolveNixPath combines it with the knob's optional
  # `nixSubPath` (defaulting to the schema key) to produce its dotted leaf.
  # Asserts every knob has a usable `group`, and that all derived paths — folded
  # together with the structural domain-tree paths — are unique and
  # prefix-disjoint, so no leaf collides with or nests inside another's namespace.
  flake-nixpath-exhaustive-disjoint =
    let
      inherit (pkgs.lib)
        assertMsg
        attrNames
        filter
        concatStringsSep
        ;
      # The cross-set disjointness fold lives in the shared allNixPaths binding
      # above; these names are only used for the missingGroup check.
      flakeOptionNames = filter (n: schema.${n}.flakeOption or false) (attrNames schema);
      missingGroup = filter (
        n:
        let
          e = schema.${n};
        in
        !(e ? group) || !builtins.isString e.group || e.group == ""
      ) flakeOptionNames;
    in
    assert assertMsg (missingGroup == [ ])
      "lib/env-schema.nix: every flakeOption knob must declare a non-empty group (ADR 0037 Pass 2): ${concatStringsSep ", " missingGroup}";
    assert (assertNixPathsOk allNixPaths) == allNixPaths;
    pkgs.runCommand "flake-nixpath-exhaustive-disjoint" { } "touch $out";

  # The fixture is the anti-vacuity root nix/checks/image.nix and
  # nix/checks/equivalence.nix import instead of re-typing model literals, so a
  # schema default bump with the fixture left un-updated must fail here rather
  # than silently validating against itself.
  default-model-fixture-schema-sync =
    let
      schema = import ../../lib/env-schema.nix;
    in
    assert
      (assertFixtureMatchesSchemaOk {
        inherit schema;
        fixtureSchemaDefaults = defaultModelFixture.schemaDefaults;
      }) == defaultModelFixture.schemaDefaults;
    pkgs.runCommand "default-model-fixture-schema-sync" { } "touch $out";

  # Anti-vacuity guard for the value-mismatch direction: runs the exact function
  # default-model-fixture-schema-sync calls against a schema whose reviewModel
  # default has been bumped away from the fixture's, so this fails if the
  # equality assert is ever dropped from assertFixtureMatchesSchemaOk.
  default-model-fixture-schema-sync-guard =
    let
      inherit (pkgs.lib) assertMsg;
      schema = import ../../lib/env-schema.nix;
      driftedSchema = schema // {
        reviewModel = schema.reviewModel // {
          default = "claude-opus-6";
        };
      };
      result = builtins.tryEval (assertFixtureMatchesSchemaOk {
        schema = driftedSchema;
        fixtureSchemaDefaults = defaultModelFixture.schemaDefaults;
      });
    in
    assert assertMsg (!result.success)
      "default-model-fixture-schema-sync-guard: expected assertFixtureMatchesSchemaOk to reject a synthetic schema whose reviewModel default has drifted from the fixture, but it evaluated successfully";
    pkgs.runCommand "default-model-fixture-schema-sync-guard" { } "touch $out";

  # The other direction: a *new* model-shaped schema key never added to the
  # fixture must also be rejected, which the value-mismatch guard above cannot
  # prove.
  default-model-fixture-schema-sync-completeness-guard =
    let
      inherit (pkgs.lib) assertMsg;
      schema = import ../../lib/env-schema.nix;
      driftedSchema = schema // {
        extraModel = {
          default = "claude-extra-1";
        };
      };
      result = builtins.tryEval (assertFixtureMatchesSchemaOk {
        schema = driftedSchema;
        fixtureSchemaDefaults = defaultModelFixture.schemaDefaults;
      });
    in
    assert assertMsg (!result.success)
      "default-model-fixture-schema-sync-completeness-guard: expected assertFixtureMatchesSchemaOk to reject a synthetic schema with a model-shaped key missing from the fixture, but it evaluated successfully";
    pkgs.runCommand "default-model-fixture-schema-sync-completeness-guard" { } "touch $out";

  default-models-gen-bash =
    let
      generated = pkgs.writeText "default_models_gen.bash.generated" (
        renderers.renderDefaultModelFixtureBash defaultModelFixture
      );
    in
    pkgs.runCommand "default-models-gen-bash"
      {
        inherit generated;
        committed = ../../tests/default_models_gen.bash;
      }
      ''
        diff "$generated" "$committed" \
          || { echo "tests/default_models_gen.bash is out of sync with lib/default-model-fixture.nix — regenerate it with \`nix run .#regen\`" >&2; exit 1; }
        touch $out
      '';

  # gofmt-normalized before diffing, as above.
  default-models-gen-go =
    let
      raw = pkgs.writeText "defaultmodels_gen_test.go.raw" (
        renderers.renderDefaultModelFixtureGo defaultModelFixture
      );
    in
    pkgs.runCommand "default-models-gen-go"
      {
        nativeBuildInputs = [ pkgs.go ];
        inherit raw;
        committed = ../../cmd/launcher/defaultmodels_gen_test.go;
      }
      ''
        gofmt "$raw" > generated.go
        diff generated.go "$committed" \
          || { echo "cmd/launcher/defaultmodels_gen_test.go is out of sync with lib/default-model-fixture.nix — regenerate it with \`nix run .#regen\`" >&2; exit 1; }
        touch $out
      '';

  # Family anti-vacuity guard: assertMarkedBlockOk must actually reject a drifted
  # block. Exercises EVERY row, not just the head — rows 1+ would otherwise go
  # untouched, and an emptied registry would fail with an unhelpful "list is
  # empty" instead of a guard-specific message. Drift is a sentinel appended
  # after each row's `generated`, content-agnostic on purpose so this guard needs
  # no knowledge of any row's business content (per-row content coverage stays
  # with documentedFactChecks).
  # `postSplice == "gofmt"` rows never go through assertMarkedBlockOk in
  # production, so running them through it here would prove nothing about the
  # path they actually take. Those rows instead drive assertSplicedSpanOk's own
  # diff-rejection path via its `expectMismatch` flag, collected into
  # gofmtDriftGuards and forced to build alongside. Still ONE derivation, not
  # fanned out per row, so the check-name surface stays unchanged.
  documented-fact-guard =
    let
      inherit (pkgs.lib) assertMsg concatStringsSep filter;
      markedBlockRows = filter (row: (row.postSplice or null) != "gofmt") documentedFacts;
      gofmtRows = filter (row: (row.postSplice or null) == "gofmt") documentedFacts;
      results = map (row: {
        inherit (row) name;
        result = builtins.tryEval (assertMarkedBlockOk {
          inherit (row)
            blockName
            sourceDesc
            beginMarker
            endMarker
            generated
            ;
          docPath = "<synthetic-test-doc>";
          docSrc = row.beginMarker + row.generated + "DRIFTED-SENTINEL" + row.endMarker + "\n";
        });
      }) markedBlockRows;
      unexpectedlySucceeded = map (r: r.name) (filter (r: r.result.success) results);
      gofmtDriftGuards = map (
        row:
        documentedFactChecker.assertSplicedSpanOk {
          name = "${row.name}-drift-guard";
          file = ../../. + "/${row.docPath}";
          generated = row.generated + "// DRIFTED-SENTINEL\n";
          inherit (row)
            blockName
            sourceDesc
            beginMarker
            endMarker
            ;
          gofmt = true;
          expectMismatch = true;
        }
      ) gofmtRows;
    in
    assert assertMsg (unexpectedlySucceeded == [ ])
      "documented-fact-guard: expected assertMarkedBlockOk to reject a synthetic drifted docSrc for every documentedFacts row, but it evaluated successfully for: ${concatStringsSep ", " unexpectedlySucceeded}";
    pkgs.runCommand "documented-fact-guard" { inherit gofmtDriftGuards; } "touch $out";

  # renderOptionSurfaceTableDoc's table rows are a fixed literal list keyed by
  # name, not derived from structuralPaths/byNamePaths, so a NEW key added to
  # either registry without a matching table row could vanish from the generated
  # block silently. Proves the renderer throws on an unlisted key by feeding it a
  # synthetic key its known-row list can't contain. deepSeq forces the throw
  # inside the tryEval instead of letting it escape as an unforced thunk.
  option-surface-doc-paths-exhaustive-guard =
    let
      inherit (pkgs.lib) assertMsg;
      driftedResult = builtins.tryEval (
        let
          r = renderers.renderOptionSurfaceTableDoc {
            structuralPaths = structuralPaths // {
              syntheticUnknownKnob = [
                "agents"
                "syntheticKnob"
              ];
            };
            inherit byNamePaths;
            nixBuilderImage = "synthetic-guard-image";
          };
        in
        builtins.deepSeq r r
      );
    in
    assert assertMsg (!driftedResult.success)
      "option-surface-doc-paths-exhaustive-guard (issue #2950): renderOptionSurfaceTableDoc must throw when structuralPaths/byNamePaths carries a key outside its 14 known table row names, so a newly added structural path without a matching doc row fails loudly at eval time instead of silently vanishing from the generated option-surface table -- it did not throw for a synthetic unlisted key";
    pkgs.runCommand "option-surface-doc-paths-exhaustive-guard" { } "touch $out";

  # Pins regen's postSplice dispatch: without this, a typo in the field (wrong
  # case, misspelling) silently takes the no-gofmt branch. Calls
  # regen.regenRowScript directly -- the exact function nix/regen.nix uses, not a
  # reimplementation -- against three synthetic rows, and pins the *current*
  # behavior: "gofmt" fires the gofmt -w line, an absent field doesn't, and
  # neither does a wrong-case "Gofmt" (documenting rather than fixing the typo
  # hazard; validating the field itself is a separate concern). The positive
  # assertion pins the exact `gofmt -w "$root/<docPath>"` substring so a
  # regression running gofmt against the wrong path is caught too.
  regen-postsplice-dispatch-guard =
    let
      inherit (pkgs.lib) assertMsg hasInfix escapeShellArg;
      gofmtRow = {
        docPath = "docs/placeholder.md";
        beginMarker = "<!-- BEGIN PLACEHOLDER -->\n";
        endMarker = "<!-- END PLACEHOLDER -->";
        generated = "placeholder generated content\n";
        postSplice = "gofmt";
      };
      plainRow = builtins.removeAttrs gofmtRow [ "postSplice" ];
      typoRow = gofmtRow // {
        postSplice = "Gofmt";
      };
      gofmtScript = regen.regenRowScript gofmtRow;
      plainScript = regen.regenRowScript plainRow;
      typoScript = regen.regenRowScript typoRow;
      # regenRowScript escapeShellArg's the docPath, so the emitted invocation is
      # a double-quoted "$root/" prefix immediately followed by a single-quoted
      # docPath literal, not one double-quoted string.
      expectedGofmtInvocation = ''gofmt -w "$root/"${escapeShellArg gofmtRow.docPath}'';
    in
    assert assertMsg (hasInfix expectedGofmtInvocation gofmtScript)
      "regen-postsplice-dispatch-guard: expected regenRowScript to emit \"${expectedGofmtInvocation}\" for a postSplice = \"gofmt\"; row, but it did not";
    assert assertMsg (!(hasInfix "gofmt -w" plainScript))
      "regen-postsplice-dispatch-guard: expected regenRowScript NOT to emit \"gofmt -w\" for a row with no postSplice field, but it did";
    assert assertMsg (!(hasInfix "gofmt -w" typoScript))
      "regen-postsplice-dispatch-guard: expected regenRowScript NOT to emit \"gofmt -w\" for a postSplice = \"Gofmt\"; (wrong-case typo) row, but it did -- this pins the current typo-silently-no-ops behavior, not a validation guarantee";
    pkgs.runCommand "regen-postsplice-dispatch-guard" { } "touch $out";

  # Proves `//`'s silent-overwrite-on-collision hazard is actually caught, not
  # just structurally impossible to hit today.
  checked-merge-rejects-name-collision-guard =
    let
      inherit (pkgs.lib) assertMsg;
      result = builtins.tryEval (checkedMerge { foo = 1; } { foo = 2; });
    in
    assert assertMsg (!result.success)
      "checked-merge-rejects-name-collision-guard: expected checkedMerge to throw when the right-hand attrset's key collides with the left-hand attrset's, but it evaluated successfully";
    pkgs.runCommand "checked-merge-rejects-name-collision-guard" { } "touch $out";

  # Proves duplicateNames actually finds and names a duplicate.
  documented-fact-registry-rejects-duplicate-name-guard =
    let
      inherit (pkgs.lib) assertMsg;
      result = builtins.tryEval (duplicateNames [
        "a"
        "b"
        "a"
      ]);
    in
    assert assertMsg (result.success && result.value == [ "a" ])
      "documented-fact-registry-rejects-duplicate-name-guard: expected duplicateNames to return the offending duplicate name(s) rather than throwing or missing them, got: ${builtins.toJSON result}";
    pkgs.runCommand "documented-fact-registry-rejects-duplicate-name-guard" { } "touch $out";

  # Proves the assert wired into documentedFactChecks throws given duplicate row
  # names, not just that duplicateNames works in isolation.
  documented-fact-checks-throws-on-duplicate-registry-row-name-guard =
    let
      inherit (pkgs.lib) assertMsg;
      dupes = duplicateNames (
        map (row: row.name) [
          { name = "dup"; }
          { name = "dup"; }
        ]
      );
      result = builtins.tryEval (
        assert assertMsg (dupes == [ ]) "synthetic duplicate should have been detected";
        null
      );
    in
    assert assertMsg (!result.success)
      "documented-fact-checks-throws-on-duplicate-registry-row-name-guard: expected the duplicate-name assert to throw given two synthetic rows sharing a name, but it evaluated successfully";
    pkgs.runCommand "documented-fact-checks-throws-on-duplicate-registry-row-name-guard" { }
      "touch $out";

  # Exercises lib/documented-fact-shape.nix's begin/end trailing-newline contract
  # directly against synthetic rows, using the same function
  # lib/documented-facts.nix self-validates with, so the two cannot drift.
  documented-fact-marker-shape-guard =
    let
      inherit (pkgs.lib) assertMsg;
      inherit (import ../../lib/documented-fact-shape.nix) assertMarkerShape;
      okRow = {
        name = "synthetic";
        beginMarker = "<!-- BEGIN SYNTHETIC -->\n";
        endMarker = "<!-- END SYNTHETIC -->";
        generated = "synthetic content\n";
      };
      okResult = builtins.tryEval (assertMarkerShape okRow);
      noTrailingNewlineBeginResult = builtins.tryEval (
        assertMarkerShape (okRow // { beginMarker = "<!-- BEGIN SYNTHETIC -->"; })
      );
      trailingNewlineEndResult = builtins.tryEval (
        assertMarkerShape (okRow // { endMarker = "<!-- END SYNTHETIC -->\n"; })
      );
      noTrailingNewlineGeneratedResult = builtins.tryEval (
        assertMarkerShape (okRow // { generated = "synthetic content"; })
      );
    in
    assert assertMsg okResult.success
      "documented-fact-marker-shape-guard: expected assertMarkerShape to accept a row whose beginMarker ends with a trailing newline and whose endMarker doesn't, but it threw";
    assert assertMsg (!noTrailingNewlineBeginResult.success)
      "documented-fact-marker-shape-guard: expected assertMarkerShape to reject a beginMarker with no trailing newline, but it evaluated successfully";
    assert assertMsg (!trailingNewlineEndResult.success)
      "documented-fact-marker-shape-guard: expected assertMarkerShape to reject an endMarker with a trailing newline, but it evaluated successfully";
    assert assertMsg (!noTrailingNewlineGeneratedResult.success)
      "documented-fact-marker-shape-guard: expected assertMarkerShape to reject a generated value with no trailing newline, but it evaluated successfully";
    pkgs.runCommand "documented-fact-marker-shape-guard" { } "touch $out";

  # MIGRATING.md's generated mapping table must match what
  # lib/legacy-settings-section.nix renders -- one row per legacy
  # `settings.<section>` alias mapped to its canonical `perSystem.spindrift.*`
  # path, so the table can't drift from the frozen alias map the way the
  # hand-picked prose examples in the surrounding section could.
  legacy-settings-mapping-doc =
    let
      generated = renderers.renderLegacySettingsMappingDoc legacySettingsSection schema;
      docSrc = builtins.readFile ../../MIGRATING.md;
    in
    assert (assertLegacySettingsMappingDocOk { inherit docSrc generated; }) == docSrc;
    pkgs.runCommand "legacy-settings-mapping-doc" { } "touch $out";

  # Anti-vacuity guard: runs the exact function legacy-settings-mapping-doc calls
  # against a synthetic doc whose `filerModel` row states a wrong canonical path
  # -- the drift a schema `group`/`nixSubPath` rename would leave behind.
  legacy-settings-mapping-doc-guard =
    let
      inherit (pkgs.lib) assertMsg replaceStrings;
      generated = renderers.renderLegacySettingsMappingDoc legacySettingsSection schema;
      beginMarker = "<!-- BEGIN GENERATED LEGACY SETTINGS MAPPING -- nix run .#regen -- DO NOT EDIT -->\n";
      endMarker = "<!-- END GENERATED LEGACY SETTINGS MAPPING -->";
      filerModelPath = "perSystem.spindrift.${resolveNixPath "filerModel" schema.filerModel}";
      driftedFilerModelPath =
        if filerModelPath == "perSystem.spindrift.agents.models.filerModel" then
          "perSystem.spindrift.agents.models.wrongPath"
        else
          "perSystem.spindrift.agents.models.filerModel";
      driftedGenerated =
        replaceStrings [ "`${filerModelPath}`" ] [ "`${driftedFilerModelPath}`" ]
          generated;
      driftedDocSrc = beginMarker + driftedGenerated + endMarker + "\n";
      result = builtins.tryEval (assertLegacySettingsMappingDocOk {
        docSrc = driftedDocSrc;
        inherit generated;
      });
    in
    assert assertMsg (!result.success)
      "legacy-settings-mapping-doc-guard: expected assertLegacySettingsMappingDocOk to reject a synthetic doc whose generated legacy settings mapping table has drifted, but it evaluated successfully";
    pkgs.runCommand "legacy-settings-mapping-doc-guard" { } "touch $out";

  # builtins.split's pattern argument is a POSIX extended regex, so a
  # begin/endMarker carrying a metacharacter ("(", ")", ".", "*") must still be
  # treated as literal marker text, not silently mis-split. Proves both the
  # accept and the reject path hold with regex-special marker text.
  marked-block-escaping-guard =
    let
      inherit (pkgs.lib) assertMsg;
      beginMarker = "<!-- BEGIN (TEST).* -->\n";
      endMarker = "<!-- END (TEST).* -->";
      generated = "committed content line";
      okDocSrc = beginMarker + generated + endMarker + "\n";
      driftedDocSrc = beginMarker + "drifted content line" + endMarker + "\n";
      okResult = builtins.tryEval (assertMarkedBlockOk {
        blockName = "TEST";
        sourceDesc = "synthetic";
        docPath = "<synthetic-test-doc>";
        inherit beginMarker endMarker generated;
        docSrc = okDocSrc;
      });
      driftedResult = builtins.tryEval (assertMarkedBlockOk {
        blockName = "TEST";
        sourceDesc = "synthetic";
        docPath = "<synthetic-test-doc>";
        inherit beginMarker endMarker generated;
        docSrc = driftedDocSrc;
      });
    in
    assert assertMsg okResult.success
      "marked-block-escaping-guard: expected assertMarkedBlockOk to accept a docSrc whose block content matches generated even with regex-metacharacter markers, but it threw";
    assert assertMsg (!driftedResult.success)
      "marked-block-escaping-guard: expected assertMarkedBlockOk to reject a synthetic doc whose block content has drifted, even with regex-metacharacter markers, but it evaluated successfully";
    pkgs.runCommand "marked-block-escaping-guard" { } "touch $out";

  # The three renderSettingsExample*Doc renderers must derive every emitted
  # line's left-hand domain path via resolveNixPath from the knob's own schema
  # entry, never a hand-typed literal -- otherwise a `group`/`nixSubPath` rename
  # leaves them emitting a stale path while settings-example-*-doc stays green,
  # since that only compares the renderer's output against the committed doc.
  # assertRendererPathsResolveOk re-derives each path independently rather than
  # calling the renderer a second time, which would only prove self-agreement.
  settings-example-paths-resolve-nix-path =
    let
      modelsOk = assertRendererPathsResolveOk {
        generated = renderers.renderSettingsExampleModelsDoc defaultModelFixture schema;
        keys = settingsExampleModelsKeys;
      };
      labelsOk = assertRendererPathsResolveOk {
        generated = renderers.renderSettingsExampleLabelsDoc schema;
        keys = settingsExampleLabelsKeys;
      };
      configOk = assertRendererPathsResolveOk {
        generated = renderers.renderSettingsExampleConfigDoc schema;
        keys = settingsExampleConfigKeys;
      };
    in
    assert modelsOk && labelsOk && configOk;
    pkgs.runCommand "settings-example-paths-resolve-nix-path" { } "touch $out";

  # Anti-vacuity guard: proves assertRendererPathsResolveOk rejects a renderer
  # output whose path has reverted to a hand-typed literal.
  settings-example-paths-resolve-nix-path-guard =
    let
      inherit (pkgs.lib) assertMsg;
      driftedGenerated = ''
        agents.models.WRONG_HAND_TYPED_PATH = "x";
        agents.models.scout = "x";
        agents.models.review = "x";
        agents.models.filer = "x";
      '';
      result = builtins.tryEval (assertRendererPathsResolveOk {
        generated = driftedGenerated;
        keys = settingsExampleModelsKeys;
      });
    in
    assert assertMsg (!result.success)
      "settings-example-paths-resolve-nix-path-guard: expected assertRendererPathsResolveOk to reject a synthetic generated string whose path has reverted to a hand-typed literal, but it evaluated successfully";
    pkgs.runCommand "settings-example-paths-resolve-nix-path-guard" { } "touch $out";

  # The disjointness assertion must cover structural domain-tree paths too, not
  # just flakeOption nixPaths — a structural-vs-flakeOption prefix collision
  # otherwise surfaces as an opaque buildTree throw at flake eval.
  # "agents.driver" is a real structural leaf; a knob landing under it collides.
  flake-nixpath-disjointness-collision-guard = mkNixPathCollisionGuard {
    name = "flake-nixpath-disjointness-collision-guard";
    leaf = "agents.driver";
  };

  # Regression guard (issue #2731): the disjointness assertion must cover the
  # byNameTreeEntries domain path too, not just the flakeOption nixPaths and
  # structural paths — a future collision under the byName leaf otherwise
  # slips past this check and surfaces as an opaque buildTree throw at flake
  # eval. Runs assertNixPathsOk — the exact function the real check calls —
  # against the real combined path set (flakeOptionNames ++ structuralPaths
  # ++ byNamePaths) with one synthetic path injected that nests under the
  # byName leaf `agents.models.byName`, via tryEval, so it fails if either
  # allNixPaths ever stops folding in the byName paths, or assertNixPathsOk
  # ever stops rejecting a byName collision.
  flake-nixpath-byname-collision-guard = mkNixPathCollisionGuard {
    name = "flake-nixpath-byname-collision-guard";
    leaf = "agents.models.byName";
  };

  # lib/env-schema.nix's intKind/hostConfig/hostDerived/emptyDisables markers
  # must stay internally consistent: every int-typed, non-secret, non-boxEnvOnly
  # member declares intKind; intKind never decorates a non-int member; intKind is
  # exactly "positive" or "nonneg"; hostDerived never contradicts host-config
  # membership; emptyDisables never decorates a non-string-typed member.
  marker-consistency =
    let
      schema = import ../../lib/env-schema.nix;
    in
    assert (assertMarkerConsistencyOk schema) == schema;
    pkgs.runCommand "marker-consistency" { } "touch $out";

  # Anti-vacuity guard: runs the exact function marker-consistency calls against
  # five mutated schemas, each violating exactly one invariant, so this fails if
  # any assert is dropped from assertMarkerConsistencyOk rather than only from
  # markerConsistencyIssues.
  marker-consistency-guard =
    let
      schema = import ../../lib/env-schema.nix;
      inherit (pkgs.lib) assertMsg;
      # missingIntKind: strip intKind from a real int-typed member.
      missingIntKindSchema = schema // {
        maxParallel = builtins.removeAttrs schema.maxParallel [ "intKind" ];
      };
      # intKindOnNonInt: decorate a real string-typed member with intKind.
      intKindOnNonIntSchema = schema // {
        label = schema.label // {
          intKind = "nonneg";
        };
      };
      # hostDerivedExcluded: mark a real hostDerived member boxEnvOnly.
      hostDerivedExcludedSchema = schema // {
        gitUserName = schema.gitUserName // {
          boxEnvOnly = true;
        };
      };
      # badIntKindValue: an off-enum intKind spelling.
      badIntKindValueSchema = schema // {
        maxParallel = schema.maxParallel // {
          intKind = "positve";
        };
      };
      # emptyDisablesOnNonString: decorate a real bool-typed member with
      # emptyDisables, which is string-knobs-only.
      emptyDisablesOnNonStringSchema = schema // {
        localIssueReference = schema.localIssueReference // {
          emptyDisables = true;
        };
      };
      missingIntKindResult = builtins.tryEval (assertMarkerConsistencyOk missingIntKindSchema);
      intKindOnNonIntResult = builtins.tryEval (assertMarkerConsistencyOk intKindOnNonIntSchema);
      hostDerivedExcludedResult = builtins.tryEval (assertMarkerConsistencyOk hostDerivedExcludedSchema);
      badIntKindValueResult = builtins.tryEval (assertMarkerConsistencyOk badIntKindValueSchema);
      emptyDisablesOnNonStringResult = builtins.tryEval (
        assertMarkerConsistencyOk emptyDisablesOnNonStringSchema
      );
    in
    assert assertMsg (!missingIntKindResult.success)
      "marker-consistency-guard: expected assertMarkerConsistencyOk to reject maxParallel with intKind removed, but it evaluated successfully";
    assert assertMsg (!intKindOnNonIntResult.success)
      "marker-consistency-guard: expected assertMarkerConsistencyOk to reject label decorated with an injected intKind, but it evaluated successfully";
    assert assertMsg (!hostDerivedExcludedResult.success)
      "marker-consistency-guard: expected assertMarkerConsistencyOk to reject gitUserName (hostDerived) decorated with an injected boxEnvOnly, but it evaluated successfully";
    assert assertMsg (!badIntKindValueResult.success)
      "marker-consistency-guard: expected assertMarkerConsistencyOk to reject maxParallel with intKind mistyped as \"positve\", but it evaluated successfully";
    assert assertMsg (!emptyDisablesOnNonStringResult.success)
      "marker-consistency-guard: expected assertMarkerConsistencyOk to reject localIssueReference (bool) decorated with an injected emptyDisables, but it evaluated successfully";
    pkgs.runCommand "marker-consistency-guard" { } "touch $out";

  # lib/legacy-settings-section.nix must totally cover the schema's flakeOption
  # knobs: every such knob has a row here or is legacySettingsExempt = true;, and
  # no row here outlives its schema knob.
  legacy-settings-section-coverage =
    assert
      (assertLegacySettingsSectionOk { inherit legacySettingsSection schema; }) == legacySettingsSection;
    pkgs.runCommand "legacy-settings-section-coverage" { } "touch $out";

  # Anti-vacuity guard covering every failure shape: a knob with no alias and no
  # exemption; a row whose knob is gone from the schema; a row whose knob is
  # still present but demoted out of flakeOption; a knob marked exempt despite
  # predating the freeze. Also proves the exemption escape hatch still accepts a
  # knob that genuinely postdates the freeze.
  legacy-settings-section-coverage-guard =
    let
      inherit (pkgs.lib) assertMsg;
      # missing: drop the row of a real flakeOption knob carrying no exemption.
      missingLegacySettingsSection = builtins.removeAttrs legacySettingsSection [ "filerModel" ];
      # stale: a row naming a schema key that does not exist at all.
      staleLegacySettingsSection = legacySettingsSection // {
        removedKnobNeverInSchema = "someSection";
      };
      # stale, second shape: a demoted knob whose row survives. The schema key
      # still exists, so a predicate checking only key existence misses this.
      deadAliasSchema = schema // {
        branchPrefix = schema.branchPrefix // {
          flakeOption = false;
        };
      };
      # The accept case: a knob genuinely postdating the freeze -- absent from
      # preFreezeFlakeOptionNames, no map row, legacySettingsExempt = true;.
      exemptSkipSchema = schema // {
        syntheticPostFreezeKnob = {
          flakeOption = true;
          legacySettingsExempt = true;
        };
      };
      # wronglyExempt: a real pre-freeze knob marked exempt. Deliberately one
      # that still HAS a map row, since wronglyExempt must fire regardless of
      # whether a row exists.
      wronglyExemptSchema = schema // {
        mergeMode = schema.mergeMode // {
          legacySettingsExempt = true;
        };
      };
      missingResult = builtins.tryEval (assertLegacySettingsSectionOk {
        legacySettingsSection = missingLegacySettingsSection;
        inherit schema;
      });
      staleResult = builtins.tryEval (assertLegacySettingsSectionOk {
        legacySettingsSection = staleLegacySettingsSection;
        inherit schema;
      });
      deadAliasResult = builtins.tryEval (assertLegacySettingsSectionOk {
        inherit legacySettingsSection;
        schema = deadAliasSchema;
      });
      exemptSkipResult = builtins.tryEval (assertLegacySettingsSectionOk {
        inherit legacySettingsSection;
        schema = exemptSkipSchema;
      });
      wronglyExemptResult = builtins.tryEval (assertLegacySettingsSectionOk {
        inherit legacySettingsSection;
        schema = wronglyExemptSchema;
      });
    in
    assert assertMsg (!missingResult.success)
      "legacy-settings-section-coverage-guard: expected assertLegacySettingsSectionOk to reject a legacySettingsSection with filerModel's row dropped (a flakeOption knob left with no alias and no exemption), but it evaluated successfully";
    assert assertMsg (!staleResult.success)
      "legacy-settings-section-coverage-guard: expected assertLegacySettingsSectionOk to reject a legacySettingsSection with a stale row injected (no matching schema entry), but it evaluated successfully";
    assert assertMsg (!deadAliasResult.success)
      "legacy-settings-section-coverage-guard: expected assertLegacySettingsSectionOk to reject a legacySettingsSection row (branchPrefix) whose schema entry lost flakeOption = true; (a dead alias row), but it evaluated successfully";
    assert assertMsg exemptSkipResult.success
      "legacy-settings-section-coverage-guard: expected assertLegacySettingsSectionOk to accept a synthetic knob genuinely postdating the freeze (legacySettingsExempt = true;, not in preFreezeFlakeOptionNames, no map row), but it failed";
    assert assertMsg (!wronglyExemptResult.success)
      "legacy-settings-section-coverage-guard: expected assertLegacySettingsSectionOk to reject mergeMode (a pre-freeze knob per preFreezeFlakeOptionNames, with a real map row) decorated with an injected legacySettingsExempt = true; -- the same wrongly-exempt-despite-predating-the-freeze mistake as the mergeMethod bug -- but it evaluated successfully";
    pkgs.runCommand "legacy-settings-section-coverage-guard" { } "touch $out";
} documentedFactChecks
