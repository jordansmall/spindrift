# Every committed generated artifact must stay in sync with its schema source.
# These guards share their renderers with `nix run .#regen` via
# lib/renderers.nix, so a guard and the regenerator cannot drift (issue #402).
{
  pkgs,
  fixtures,
  nixpkgs,
  system,
  config,
  ...
}:
let
  inherit (fixtures) harness;
  renderers = import ../../lib/renderers.nix;
  schema = import ../../lib/env-schema.nix;
  # Shared with nix/regen.nix's marker-splice loop so a block's marker
  # literals and renderer call are typed exactly once (issue #2948).
  documentedFacts = import ../../lib/documented-facts.nix { inherit (pkgs) lib; };
  # Also imported by nix/checks/baked-skills.nix, so the two files never fork
  # hand-mirrored copies of the marker splice (issue #2949).
  documentedFactChecker = import ../../lib/documented-fact-checker.nix { inherit pkgs; };
  # regen-postsplice-dispatch-guard below exercises the exact per-row
  # postSplice dispatch `nix run .#regen` uses (issue #2949 review finding).
  regen = import ../regen.nix { inherit pkgs; };
  # Shared with nix/checks/promptassembly.nix's regen-goldens-app-wiring so
  # the two app-wiring guards cannot fork hand-copied variants (issue #3192).
  mkAppWiringCheck = import ../../lib/app-wiring-check.nix { inherit pkgs; };
  # One import shared by all three consumers of the byName and roster worked
  # examples instead of three copies (issue #2572 round 2).
  structuralTemplateExamples = import ../../lib/structural-template-examples.nix {
    inherit (pkgs) lib;
  };
  rosterLib = import ../../lib/roster.nix { inherit (pkgs) lib; };
  # Parses an example's rendered `lines` back as Nix. That rendered text is
  # what a Consumer pastes, and a renderer bug can desync it from the backing
  # `.example` value. builtins.toFile writes at eval time with no derivation
  # build, so this is not import-from-derivation. Gotcha: a genuine syntax
  # error in `lines` crashes eval before tryEval below can catch it.
  evalExampleLines =
    entry:
    let
      key = builtins.elemAt entry.path (builtins.length entry.path - 1);
      src = "{ ${builtins.concatStringsSep "\n" entry.lines} }";
    in
    (import (builtins.toFile "structural-example-${key}.nix" src)).${key};
  defaultModelFixture = import ../../lib/default-model-fixture.nix;
  legacySettingsSection = import ../../lib/legacy-settings-section.nix;

  # Defined once so schema-secret-choices-guard can exercise the predicate
  # against an injected schema, not only the real one (issue #872).
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

  # Throws on a bad schema, else returns it unchanged. The guard below runs
  # this exact path, so dropping the badSecret assert here makes that guard
  # fail rather than stay silently green.
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
  # fields (issue #2363). Factored out so the guard can exercise this exact
  # predicate against an injected schema. "Int member" means an int-typed
  # default carrying neither of the schema's two non-membership signals,
  # secret and boxEnvOnly; the real derivation lands in a later slice.
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
      intKindOnNonInt = filter (e: (e ? intKind) && !(isIntTyped e)) entries;
      # A typo like "positve" would otherwise pass the presence and
      # int-typedness checks silently.
      badIntKindValue = filter (
        e:
        (e ? intKind)
        && !(elem e.intKind [
          "positive"
          "nonneg"
        ])
      ) entries;
      # hostDerived implies host-config membership, so it must not also carry
      # a non-membership signal (secret, boxEnvOnly).
      hostDerivedExcluded = filter (
        e: (e.hostDerived or false) && ((e.secret or false) || (e.boxEnvOnly or false))
      ) entries;
      # emptyDisables is string-knobs-only and nothing else enforces that: the
      # schemaConfig loaderLine cascade (lib/renderers.nix) checks
      # bool/int/float/secret/hostDerived before consulting emptyDisables, so
      # on those it would be silently ignored rather than rejected.
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

  # Throws on a bad schema, else returns it unchanged. The guard below runs
  # this exact path, so dropping any of the five asserts makes that guard fail
  # rather than stay silently green.
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
  # Renders each segment-list value of a paths attrset as its dotted string,
  # so allNixPaths below does not repeat the same map once per source.
  dotted = attrs: map (pkgs.lib.concatStringsSep ".") (pkgs.lib.attrValues attrs);

  # Computed once rather than inside each collision guard, so a regression in
  # this fold-in, such as dropping the byNamePaths splice, is visible to every
  # consumer instead of one guard recomputing its own copy (issue #2731).
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

  # Frozen ground truth, in its own file so it is not a fourth hand-copy of a
  # knob list living only in this check (issue #2522 review finding).
  preFreezeFlakeOptionNames = import ../../lib/pre-freeze-flake-options.nix;

  # Every flakeOption knob needs a lib/legacy-settings-section.nix row or an
  # explicit legacySettingsExempt (a knob added after the ADR 0037 Pass 2
  # freeze, which never had a settings.<section> alias to preserve), and every
  # row must still name a live flakeOption knob: checking key existence alone
  # would miss a knob demoted to flakeOption = false; (issue #2522).
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
      # A knob marked legacySettingsExempt that still appears in the frozen
      # pre-freeze list predates the freeze, so it had a real old alias: the
      # exemption is wrong and it needs a real row instead.
      wronglyExempt = filter (
        n: (schema.${n}.legacySettingsExempt or false) && elem n preFreezeFlakeOptionNames
      ) flakeOptionNames;
    };

  # Throws on a bad map/schema pair, else returns legacySettingsSection
  # unchanged. The coverage guard runs this exact path, so dropping any of the
  # three asserts makes that guard fail rather than stay silently green.
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

  # Uniqueness and prefix-disjointness over a flat list of dotted nixPath
  # strings, factored out so a guard can inject a synthetic path set.
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

  # Throws on a non-disjoint or non-unique path set, else returns it
  # unchanged. The messages name no source file on purpose: the colliding path
  # may be a flakeOption knob or a structural domain-tree leaf.
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

  # Injects a synthetic path nesting under `leaf` into the real allNixPaths,
  # runs it through assertNixPathsOk (the function the real check calls), and
  # asserts eval failed, so the collision was rejected and not accepted.
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

  # Anti-vacuity check for lib/default-model-fixture.nix (issue #2514): a
  # schema default bump with the fixture left un-updated must fail here. The
  # other direction matters too, so every model-shaped schema key (named
  # "model" or ending in "Model") must be present in the fixture, or the
  # fixture-side filterAttrs would silently never look at a new one.
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

  # Checks MIGRATING.md's generated legacy-settings-to-domain-tree mapping
  # table against `generated` (issue #2558). Built on assertMarkedBlockOk so
  # the guard below can run this exact path against a synthetic doc.
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

  # Each marker-delimited sub-block lives in its own host file (a doc example
  # per ADR 0037, or a template, bash script, or Go source) and is checked the
  # same way: split docSrc on the markers and compare the committed slice
  # against generated, naming the blockName and sourceDesc that drifted.
  inherit (documentedFactChecker) assertMarkedBlockOk;

  # Every key in `keys` must appear in `generated` as a line whose left-hand
  # path is exactly resolveNixPath's output. The expected path is re-derived
  # through resolveNixPath rather than by calling the renderer again, so this
  # catches a renderer reverting to a hand-typed literal (issue #2557 review).
  assertRendererPathsResolveOk =
    { generated, keys }:
    let
      inherit (pkgs.lib)
        concatStringsSep
        filter
        splitString
        trim
        ;
      # The renderers right-pad each path to the block's widest before " = ",
      # and a substring check would also accept a wrong-but-prefix path such
      # as "git.merge" inside "git.merge.policy", so take the exact left side.
      lines = filter (l: l != "") (splitString "\n" generated);
      linePath = line: trim (builtins.head (splitString "=" line));
      actualPaths = map linePath lines;
      missing = filter (key: !(builtins.elem (resolveNixPath key schema.${key}) actualPaths)) keys;
    in
    if missing == [ ] then
      true
    else
      throw "assertRendererPathsResolveOk: generated output is missing the resolveNixPath-derived path for env-schema key(s): ${concatStringsSep ", " missing}";

  # The env-schema keys each settings-example renderer emits, shared between
  # settings-example-paths-resolve-nix-path and its guard sibling below.
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

  # builtins.listToAttrs silently keeps only the first of two rows sharing a
  # `name`, so a copy-pasted row name would delete that row's drift check from
  # the build with no warning. checkedMerge below guards the `//` case.
  duplicateNames =
    names:
    builtins.attrNames (
      pkgs.lib.filterAttrs (_: occurrences: builtins.length occurrences > 1) (
        builtins.groupBy (n: n) names
      )
    );

  # One named drift check per documentedFacts row (issue #2948). docPath is
  # read through `../../. + "/<docPath>"` because row.docPath is a runtime
  # string and Nix path interpolation needs a literal path prefix.
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
  # attrset with a duplicate key, which is a hard eval error. This restores
  # that safety, so a documentedFacts row named after an existing hand-written
  # check throws instead of silently replacing it (issue #2948).
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
  # Regenerate with `nix run .#regen` when lib/drivers/default.nix changes
  # (issue #436).
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

  # Regenerate with `nix run .#regen` when lib/agent-paths.nix changes.
  # runner/mount.go's SPINDRIFT_PROMPT_DIR target reads agentpaths.PromptsDir
  # rather than its own literal, so renaming a baked /agent/* path fails here
  # instead of silently mounting onto a dead in-box path (issue #2531).
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

  # Regenerate with `nix run .#regen` when lib/backends/default.nix changes.
  # The raw renderer output is gofmt-normalized here the same way regen
  # normalizes it (issue #2521).
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

  # Regenerate with `nix run .#regen` when lib/labels.nix changes. The raw
  # renderer output is gofmt-normalized here the same way regen normalizes it
  # (issue #2528).
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

  # Regenerate with `nix run .#regen` when lib/runtime-values.nix's enum
  # changes (issue #2561).
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

  # Regenerate with `nix run .#regen` when a quickstart knob's nix option path
  # changes, that is, lib/nixpath.nix over lib/env-schema.nix's
  # group/nixSubPath (issue #2556).
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

  # Regenerate with `nix run .#regen` when lib/subcommands.nix changes
  # (issue #1575).
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

  # Regenerate with `nix run .#regen` when lib/prompt-contract.nix's
  # outcomeStatusSets changes. The raw renderer output is intentionally
  # unaligned: gofmt owns the const block's column alignment (issue #2504).
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

  # Regenerate with `nix run .#regen` when lib/prompt-contract.nix's
  # markerChannels changes; gofmt-normalized the same way regen normalizes it
  # (issue #2974, parent #2972).
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

  # Regenerate with `nix run .#regen` when lib/env-schema.nix gains a knob
  # (issue #109).
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

  # Every env-var literal in cmd/launcher/main.go and backend.go must have a
  # lib/env-schema.nix entry and the reverse (presence only; pinning values is
  # refactor-brittle). backend.go because the per-row token knobs moved there
  # (issue #2267); not every file in package main, since flags.go's SECRET_CMD
  # fallback is a naming convention rather than a registered knob.
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
      # lib.hasInfix wraps builtins.match with a leading and trailing `.*`, and
      # that std::regex backtracking recurses per haystack character. These
      # three files exceed 100KB, enough to blow the evaluator's C stack
      # (issue #2533: flake check segfaulted, exit 139). splitString's regex
      # has no `.*` wrapper, so it does not recurse per byte.
      containsLiteral = needle: haystack: builtins.length (splitString needle haystack) > 1;
      launcherDir = ../../cmd/launcher;
      # schemaconfig_gen.go is listed before loadConfig embeds schemaConfig, so
      # the later slice wiring it in does not fail this check for knobs whose
      # env-var literal lives only in the generated file (issue #2364).
      mainGoSrc = concatStringsSep "\n" (
        map (name: builtins.readFile (launcherDir + "/${name}")) [
          "main.go"
          "backend.go"
          "schemaconfig_gen.go"
        ]
      );
      # Nix-computed plumbing main.go reads via getenvArtifact, not
      # user-facing knobs (ADR 0020, issue #810).
      documentArtifacts = preambles.documentArtifactKeys;
      schemaEnvNames = map (e: e.env) (attrValues schema);
      # Forwarded to containers via BOX_ENV_VARS only: the Go binary never
      # reads them, so they need no os.Getenv call.
      boxEnvOnly = map (e: e.env) (filter (e: e.boxEnvOnly or false) (attrValues schema));
      missingFromGo = filter (name: !containsLiteral ''"${name}"'' mainGoSrc) (
        subtractLists boxEnvOnly schemaEnvNames
      );
      # The reverse direction: the names main.go actually reads. docArtifact
      # carries issue #2527's capability signals.
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
  # the man page, and docs/flake-options.md, so a stale pointer is stale
  # everywhere. It must name docs/reference.md's Dogfood loop section, not the
  # nonexistent README exit-code table it once cited (issue #1879).
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
    # The deprecation notice rides this one doc string, which is what renders
    # it onto --help, the man page, and docs/flake-options.md (issue #3547).
    assert assertMsg (hasInfix "DEPRECATED" doc)
      "lib/env-schema.nix: continuousDispatch.doc must mark the knob DEPRECATED now that the daemon supersedes it (issue #3547), got: ${doc}";
    assert assertMsg (hasInfix "nix run .#daemon" doc)
      "lib/env-schema.nix: continuousDispatch.doc must name nix run .#daemon as the replacement (issue #3547), got: ${doc}";
    assert assertMsg (hasInfix "not removed" doc)
      "lib/env-schema.nix: continuousDispatch.doc must say plainly it is not removed — it stays for no-daemon operators and remains the Console's engine (issue #3547), got: ${doc}";
    pkgs.runCommand "continuous-dispatch-doc-reference" { } "touch $out";

  # A knob's `choices` must be a non-empty list of strings and its `default` a
  # member, or tab-completion would offer values the knob can never hold
  # (issue #554). The per-knob asserts below pin each exact value set, and the
  # set of choices-bearing knob names is itself asserted so a tenth knob
  # declaring `choices` cannot go unpinned silently (issue #2519).
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
        "signalCarrier"
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
    assert assertMsg (
      schema.signalCarrier.choices or [ ] == [
        "log"
        "socket"
      ]
    ) "lib/env-schema.nix: signalCarrier.choices must be [ log socket ]";
    pkgs.runCommand "schema-choices" { } "touch $out";

  # Regression guard (issue #2519): injects a tenth synthetic choices-bearing
  # knob, so the knob-set assertion above is known to reject one rather than
  # passing vacuously because the real schema has exactly the nine names.
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
        "signalCarrier"
      ];
      result = builtins.tryEval (
        assert choiceKnobNames == expectedChoiceKnobNames;
        true
      );
    in
    assert assertMsg (!result.success)
      "schema-choices-knobset-guard: expected the choices-bearing knob-set assertion to reject a schema with an injected tenth choices knob (extraChoiceKnob), but it evaluated successfully";
    pkgs.runCommand "schema-choices-knobset-guard" { } "touch $out";

  # Regression guard (issue #872): the completion renderers scope `choices` to
  # nonSecret knobs, since a secret gets only a `--*-file` flag, so `choices`
  # on a secret knob is a silent no-op. Injecting one into the real schema
  # keeps the badSecret assert non-vacuous.
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

  # Regression guard (issue #2519 slice 2): lib/flakeModule.nix's types.enum
  # only protects Consumers going through the flake module. A Consumer calling
  # mkHarness directly bypasses it, so prove lib/mkHarness.nix itself rejects
  # an invalid `mergeMethod`.
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

  # The gate-not-triggered counterpart: without it, an unrelated eval failure
  # in the mkHarness call above (a new required argument, say) would make the
  # guard pass vacuously. The same call shape must still evaluate cleanly for
  # an in-choice value, so the guard's failure comes from the choices assert.
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

  # Regression guard (issue #2519): choiceViolations once skipped a null choice
  # value outright, so `mergeMethod = null` passed and documentSettings
  # rendered MERGE_METHOD="" via `toString null`. The guard above cannot catch
  # a reintroduced skip, since its bogus string value never reaches the same
  # branch, so the null case is pinned separately here.
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

  # Regression guard (issue #2539): proves lib/jira-status-mapping.nix's
  # `parse` is wired into mkHarness's eval-time assert chain, not only
  # exercised in isolation by nix/checks/jira-status-mapping.nix.
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

  # The gate-not-triggered counterpart: the same call shape must evaluate
  # cleanly for a valid JIRA_STATUS_MAPPING, so the guard above is known to
  # fail on that knob and not on an incidental break elsewhere.
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

  # A non-jira tracker never reaches backend.go's jira.ParseStatusMapping, so
  # a stale JIRA_STATUS_MAPPING left from a prior ISSUE_TRACKER=jira config
  # must not fail a github-tracker build.
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

  # The set_box_env fixture must export every boxEnv = true knob, so the
  # entrypoint-*.bats suites exercise the same defaults the nix preamble bakes
  # into the image. Regenerate with `nix run .#regen` (issues #462, #520).
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

  # Regenerate with `nix run .#regen` when lib/env-schema.nix or
  # lib/renderers.nix's groupOrder changes.
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

  # Regenerate with `nix run .#regen` when a host-config schema member
  # changes. The raw renderer output is intentionally unaligned: gofmt owns
  # the struct and composite-literal column alignment (issue #2364).
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

  # Regenerate with `nix run .#regen` when a box-env row changes. The raw
  # renderer output is intentionally unaligned: gofmt owns the struct
  # literal's column alignment (issue #2979).
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

  # Regenerate with `nix run .#regen` when a flakeOption knob is added or
  # removed, or a structural knob's doc metadata changes (issue #2572).
  flake-options-doc =
    let
      schema = import ../../lib/env-schema.nix;
      structuralOptionsDoc = import ../../lib/structural-options-doc.nix;
      generated = pkgs.writeText "flake-options.md.generated" (
        renderers.renderFlakeOptionsDocFull schema structuralOptionsDoc structuralPaths byNamePaths
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

  # lib/structural-template-examples.nix only regex-matches each example's
  # rendered text, never the values, so an unusable example could ship
  # (issue #2572). Each entry must survive normalizeRoster, match its
  # defaultRoster counterpart on every field but `model` (the one a Consumer
  # is expected to customize), and name a promptFile that exists.
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

  # The byName example has no normalize function of its own, so it goes
  # through rosterLib.defaultRoster's byName argument as a proxy for
  # flakeModule.nix's byNameOption: types.attrsOf does not constrain key names
  # the way defaultRoster's runtime checks do (issue #2572 round 2).
  structural-template-examples-byname-valid =
    let
      inherit (pkgs.lib) assertMsg;
      byNameEntry = builtins.head (
        builtins.filter (e: e.path == byNamePaths.byName) structuralTemplateExamples
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

  # The generated man page must parse under mandoc and cover the schema in
  # full: every SH section, every OPTIONS group, every non-secret flag, and
  # every secret env var.
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
      # Roff escapes every hyphen, so match the \-\- form. toKebab is the same
      # helper the man page itself is rendered through.
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

  # Pure-eval pin on renderZshCompletion's shape (issue #552). Complements
  # launcher-zsh-completion below, which covers the built artifact end to end;
  # this one pins the renderer's output without a store build.
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
    # An `_arguments -C ... '*::arg:->args'` state machine with no `args)` case
    # arm silently swallows every word after the subcommand, so flags never
    # complete there even though the flags array looks complete. Pin an
    # unconditional _describe on the flags array instead (issue #552).
    assert assertMsg (hasInfix ''if [[ "$cur" == -* ]]'' out)
      "renderZshCompletion must branch on a literal cur/prev flag-prefix check, not an _arguments state machine, got: ${out}";
    assert assertMsg (hasInfix "_describe -t options 'spindrift flag' flags" out)
      "renderZshCompletion's flag-prefix branch must _describe the flags array directly (reachable, unconditional), got: ${out}";
    assert assertMsg (!hasInfix "_arguments" out)
      "renderZshCompletion must not use _arguments' '*::state:->state' catch-all — issue #552 review found it swallows post-subcommand words with no matching case arm, got: ${out}";
    pkgs.runCommand "renderer-zsh-completion-shape" { } "touch $out";

  # A knob carrying both `alias` and `choices` must complete its value list
  # for either flag form (issue #874). No real schema knob combines the two,
  # and the issue's research verdict kept the fixture off the production
  # schema, so this uses a synthetic one.
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

  # Dynamic issue-number completion must derive from each registry entry's
  # dynamicIssueCompletion field, not a list independent of the registry
  # passed in (issue #1603). None of the synthetic names are real subcommands,
  # so a renderer matching the production literal fails. "research" stays
  # unflagged here to pin issue #556's deliberate exclusion.
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
    # The closing quote right after "alpha" makes this exact membership rather
    # than a prefix check: any extra name would push the quote further along,
    # so this one assertion covers inclusion and exclusion.
    assert assertMsg (hasInfix "'__fish_seen_subcommand_from alpha'" fishOut)
      "renderFishCompletion's __fish_seen_subcommand_from predicate must be exactly the dynamicIssueCompletion = true entries, got: ${fishOut}";
    assert assertMsg (hasInfix "alpha)" zshOut)
      "renderZshCompletion's issue-completion case arm must include a dynamicIssueCompletion = true entry, got: ${zshOut}";
    assert assertMsg (builtins.all (n: !hasInfix "${n})" zshOut) excluded)
      "renderZshCompletion's issue-completion case arm must exclude entries without dynamicIssueCompletion = true, got: ${zshOut}";
    pkgs.runCommand "renderer-issue-completion-registry-shape" { } "touch $out";

  # The generated bash completion must cover the schema and lib/subcommands.nix
  # in full: every non-secret flag, the --issue alias, every secret --*-file
  # flag, and every registered subcommand.
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
      # Token-boundary match: a plain substring grep would pass `--issue` as
      # covered merely because `--issue-number` contains it as a prefix.
      flagChecks = concatMapStrings (e: "need '--${renderers.toKebab e.env}'\n") nonSecret;
      aliasChecks = concatMapStrings (e: if e ? alias then "need '--${e.alias}'\n" else "") nonSecret;
      secretChecks = concatMapStrings (e: "need '--${renderers.toKebab e.env}-file'\n") secretEntries;
      secretCmdChecks = concatMapStrings (e: "need '--${renderers.toKebab e.env}-cmd'\n") secretEntries;
      # Subcommand names are plain English words that can appear in a comment,
      # so a per-word check would pass with one missing. Require the exact
      # assembled list the renderer emits.
      subcommandLine = concatStringsSep " " subcommands;
      # Pin the exact `compgen -W "..."` string the renderer emits, not a
      # per-word substring check, so a value attached to the wrong flag or
      # dropped fails here (issue #554).
      choicesChecks = concatMapStrings (
        e:
        "grep -qF -- 'compgen -W \"${concatStringsSep " " e.choices}\"' \"$completion\" "
        + "|| { echo 'bash completion missing choices for --${renderers.toKebab e.env}' >&2; exit 1; }\n"
      ) choicesKnobs;
      # Dynamic issue-number completion gates on the registry's
      # dynamicIssueCompletion entries, not the full subcommand set (build and
      # doctor take no issue argument). Derived the same way the renderer
      # derives it, so the two cannot drift (issues #556, #1603).
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

  # The generated fish completion must cover the schema and lib/subcommands.nix
  # the same way launcher-bash-completion above does.
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
      # Subcommands render as `-a '<name>'`, and that quoted token cannot
      # appear incidentally in a comment the way the bare word can, so a
      # fixed-string search needs no boundary check.
      subcommandChecks = concatMapStrings (s: "needF \"-a '${s}'\"\n") subcommands;
      # Pin the exact `-a '...'` list the renderer emits, so a value attached
      # to the wrong flag or dropped fails here (issue #554).
      choicesChecks = concatMapStrings (
        e: "needF \"-a '${builtins.concatStringsSep " " e.choices}'\"\n"
      ) choicesKnobs;
      # Gates on the registry's dynamicIssueCompletion entries, not the full
      # subcommand set. Derived the same way renderFishCompletion derives it,
      # so the two cannot drift (issues #556, #1603).
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

  # zsh equivalent of launcher-bash-completion. renderZshCompletion emits each
  # entry as `'--flag:description'`, so the name followed by `:` inside its
  # opening quote is already a token boundary and a substring check suffices:
  # the colon only ever follows the exact name.
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
      # Pin the exact `compadd -- ...` list, not a per-word substring check, so
      # a value attached to the wrong flag or dropped fails here (issue #554).
      choicesChecks = concatMapStrings (
        e: "need 'compadd -- ${builtins.concatStringsSep " " e.choices}'\n"
      ) choicesKnobs;
      # Gates on the registry's dynamicIssueCompletion entries, not the full
      # subcommand set. Derived the same way renderZshCompletion derives it,
      # so the two cannot drift (issues #556, #1603).
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

  # ADR 0037 Pass 2 (issue #2188): the flake path is derived, not stored.
  # Every flakeOption knob must declare a non-empty `group`, which
  # resolveNixPath combines with the optional `nixSubPath` (defaulting to the
  # schema key) into a dotted leaf. Folded with the structural paths, those
  # leaves must be unique and prefix-disjoint so none nests inside another.
  flake-nixpath-exhaustive-disjoint =
    let
      inherit (pkgs.lib)
        assertMsg
        attrNames
        filter
        concatStringsSep
        ;
      # Only the `group` check needs this list; the cross-set disjointness fold
      # lives in the shared allNixPaths binding above.
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

  # The fixture's schemaDefaults must restate lib/env-schema.nix's own
  # .default values (issue #2514). It is the anti-vacuity root that
  # nix/checks/image.nix and nix/checks/equivalence.nix import instead of
  # re-typing the literals, so a bump with a stale fixture must fail here.
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

  # Regression guard (issue #2514 AC3): bumps reviewModel's default away from
  # the fixture, so the sync assertion above is known to reject drift rather
  # than passing vacuously.
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

  # Regression guard (issue #2514): the guard above only covers a mismatched
  # value, so this adds a model-shaped key absent from the fixture to keep the
  # missingFromFixture assert non-vacuous too.
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

  # Regenerate with `nix run .#regen` when lib/default-model-fixture.nix
  # changes (issue #2514, slice 2 of 3).
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

  # Regenerate with `nix run .#regen` when lib/default-model-fixture.nix
  # changes; gofmt-normalized the same way regen normalizes it (issue #2514,
  # slice 2 of 3).
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

  # Regenerate with `nix run .#regen` when lib/env-schema.nix's daemon tuning
  # knob defaults change; gofmt-normalized the same way regen normalizes it
  # (issue #3619).
  daemon-knob-defaults-gen-go =
    let
      raw = pkgs.writeText "shippeddefaults_gen_test.go.raw" (
        renderers.renderDaemonKnobDefaultsGo schema
      );
    in
    pkgs.runCommand "daemon-knob-defaults-gen-go"
      {
        nativeBuildInputs = [ pkgs.go ];
        inherit raw;
        committed = ../../cmd/launcher/internal/daemon/shippeddefaults_gen_test.go;
      }
      ''
        gofmt "$raw" > generated.go
        diff generated.go "$committed" \
          || { echo "cmd/launcher/internal/daemon/shippeddefaults_gen_test.go is out of sync with lib/env-schema.nix — regenerate it with \`nix run .#regen\`" >&2; exit 1; }
        touch $out
      '';

  # Proves assertMarkedBlockOk rejects a drifted block rather than passing
  # vacuously (issue #2948). Every row is exercised, not just the first, by
  # appending a content-agnostic sentinel to its own docSrc. `postSplice ==
  # "gofmt"` rows never take that path in production, so they drive
  # assertSplicedSpanOk's expectMismatch instead (issue #2949 review finding).
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

  # renderOptionSurfaceTableDoc must throw when a structuralPaths/byNamePaths
  # key has no matching table row, rather than letting it vanish from the
  # generated block (issues #2950, #3067). deepSeq forces the throw inside
  # tryEval instead of it escaping as a lazy thunk. Only the added-key
  # direction is testable here; a deleted row is caught by the row-count pin.
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
      "option-surface-doc-paths-exhaustive-guard (issue #2950): renderOptionSurfaceTableDoc must throw when structuralPaths/byNamePaths carries a key with no matching row in the rendered table, so a newly added structural path without a matching doc row fails loudly at eval time instead of silently vanishing from the generated option-surface table -- it did not throw for a synthetic unlisted key";
    pkgs.runCommand "option-surface-doc-paths-exhaustive-guard" { } "touch $out";

  # The guard above only sees tryEval's success or failure, so a renderer
  # regressed to an unanchored substring match over the whole table would
  # still throw and pass for the wrong reason. `allowUnfree` appears in the
  # config row's default cell but never as a first-column name.
  option-surface-doc-row-anchor-guard =
    let
      inherit (pkgs.lib) assertMsg;
      driftedResult = builtins.tryEval (
        let
          r = renderers.renderOptionSurfaceTableDoc {
            structuralPaths = structuralPaths // {
              allowUnfree = [
                "agents"
                "allowUnfree"
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
      "option-surface-doc-row-anchor-guard (issue #3067): renderOptionSurfaceTableDoc must throw for a synthetic key (\"allowUnfree\") that appears only inside the config row's prose, never as a first-column row name -- it did not throw, so the match is no longer anchored to the first column";
    pkgs.runCommand "option-surface-doc-row-anchor-guard" { } "touch $out";

  # The four editorial rows carry a literal em dash in their domain-path cell
  # rather than a real path, so a registry key whose name collides with one
  # (`settings` here) would render a silently wrong domain path unless the
  # match is anchored to rows that carry a path (issue #3067).
  option-surface-doc-editorial-name-guard =
    let
      inherit (pkgs.lib) assertMsg;
      driftedResult = builtins.tryEval (
        let
          r = renderers.renderOptionSurfaceTableDoc {
            structuralPaths = structuralPaths // {
              settings = [
                "agents"
                "settings"
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
      "option-surface-doc-editorial-name-guard (issue #3067): renderOptionSurfaceTableDoc must throw for a synthetic key (\"settings\") whose name matches an editorial row -- that row's domain-path cell is a literal em dash, not a path, so treating it as the key's row would render a silently wrong domain path";
    pkgs.runCommand "option-surface-doc-editorial-name-guard" { } "touch $out";

  # The editorial rows have no registry key by design, so a forward-only
  # registry-to-row check cannot notice one disappearing or a stray row
  # appearing. Rows are read back through the renderer's own
  # optionSurfaceRowNamePaths so the two cannot disagree about what a row is.
  option-surface-doc-editorial-rows-pin =
    let
      inherit (pkgs.lib) assertMsg;
      # `reviewPrompt` and `filerPrompt` share the `scoutPrompt` row, whose
      # cell reports only that first name: four editorial names, four rows.
      editorialRowNames = [
        "system"
        "scoutPrompt"
        "settings"
        "nixBuilderImage"
      ];
      rendered = renderers.renderOptionSurfaceTableDoc {
        inherit structuralPaths byNamePaths;
        nixBuilderImage = "pin-guard-image";
      };
      rowNames = map (cells: cells.name) (renderers.optionSurfaceRowNamePaths rendered);
      # Derived, not a literal: a legitimate new registry key brings its own
      # row and needs no number bumped here, while a stray or lost row still
      # breaks the count.
      expectedRowCount =
        builtins.length (builtins.attrNames (structuralPaths // byNamePaths))
        + builtins.length editorialRowNames;
      missingEditorialNames = builtins.filter (n: !(builtins.elem n rowNames)) editorialRowNames;
    in
    assert assertMsg (missingEditorialNames == [ ])
      "option-surface-doc-editorial-rows-pin (issue #3067): the editorial rows (no registry key by design) must still appear as first-column names in the rendered option-surface table -- missing: ${builtins.concatStringsSep ", " missingEditorialNames}";
    assert assertMsg (builtins.length rowNames == expectedRowCount)
      "option-surface-doc-editorial-rows-pin (issue #3067): expected exactly ${builtins.toString expectedRowCount} data rows in the rendered option-surface table (one per structuralPaths/byNamePaths key plus ${builtins.toString (builtins.length editorialRowNames)} editorial), got ${builtins.toString (builtins.length rowNames)}: ${builtins.concatStringsSep ", " rowNames}";
    pkgs.runCommand "option-surface-doc-editorial-rows-pin" { } "touch $out";

  # flake-options-doc above regenerates the byName row from the same source it
  # diffs against, so it cannot catch a path renderStructuralOptionsDoc
  # hardcodes. Feeding a synthetic byNamePaths proves the renderer derives the
  # path from its argument (issue #2796).
  structural-options-doc-byname-path-derived-guard =
    let
      inherit (pkgs.lib) assertMsg hasInfix;
      structuralOptionsDoc = import ../../lib/structural-options-doc.nix;
      syntheticByNamePaths = {
        byName = [
          "agents"
          "synthetic"
          "byName"
        ];
      };
      rendered =
        renderers.renderStructuralOptionsDoc structuralOptionsDoc structuralPaths
          syntheticByNamePaths;
    in
    assert assertMsg (hasInfix "perSystem.spindrift.agents.synthetic.byName" rendered)
      "structural-options-doc-byname-path-derived-guard (issue #2796): renderStructuralOptionsDoc's byName row must derive its path from the passed byNamePaths argument, not a hardcoded literal -- expected the rendered doc to contain \"perSystem.spindrift.agents.synthetic.byName\"";
    pkgs.runCommand "structural-options-doc-byname-path-derived-guard" { } "touch $out";

  # Pins regen.regenRowScript's dispatch against three synthetic rows: a
  # postSplice = "gofmt"; row emits the gofmt line, a row with no postSplice
  # field does not, and neither does a wrong-case "Gofmt" typo, which this
  # documents rather than fixes. The positive assertion pins the exact path,
  # so gofmt run against the wrong file is caught (issue #2949 review finding).
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
      # regenRowScript escapeShellArg's the docPath, so the emitted invocation
      # is a double-quoted "$root/" prefix followed by a single-quoted docPath
      # literal, not one double-quoted string (issue #2949 review finding).
      expectedGofmtInvocation = ''gofmt -w "$root/"${escapeShellArg gofmtRow.docPath}'';
    in
    assert assertMsg (hasInfix expectedGofmtInvocation gofmtScript)
      "regen-postsplice-dispatch-guard: expected regenRowScript to emit \"${expectedGofmtInvocation}\" for a postSplice = \"gofmt\"; row, but it did not";
    assert assertMsg (!(hasInfix "gofmt -w" plainScript))
      "regen-postsplice-dispatch-guard: expected regenRowScript NOT to emit \"gofmt -w\" for a row with no postSplice field, but it did";
    assert assertMsg (!(hasInfix "gofmt -w" typoScript))
      "regen-postsplice-dispatch-guard: expected regenRowScript NOT to emit \"gofmt -w\" for a postSplice = \"Gofmt\"; (wrong-case typo) row, but it did -- this pins the current typo-silently-no-ops behavior, not a validation guarantee";
    pkgs.runCommand "regen-postsplice-dispatch-guard" { } "touch $out";

  # flake.nix's apps.regen must resolve to the same derivation this check
  # builds from nix/regen.nix. Referencing regen's output path in the build
  # script forces this check to build it, including writeShellApplication's
  # shellcheck pass, so a broken regen script fails `nix build .#checks-inbox`
  # instead of only `nix run .#regen` (issue #3128).
  regen-app-wiring = mkAppWiringCheck {
    name = "regen";
    apps = config.apps;
    package = regen;
    exposedReason = "flake.nix must expose apps.regen at the top level so \`nix run .#regen\` actually resolves";
    sameEnvReason = "flake.nix's top-level apps.regen must be built from nix/regen.nix with the SAME pkgs this check uses -- otherwise \`nix run .#regen\` silently regenerates against a foreign env";
  };

  # write_between must preserve the target file's mode across its `mv`:
  # `splice` writes $file.regen-tmp fresh under the default umask, so a plain
  # `mv` onto an executable committed file such as agent/entrypoint.sh
  # silently dropped its exec bit on every `nix run .#regen`, including a
  # no-op run that must produce no diff at all (issue #3128).
  regen-write-between-preserves-mode =
    pkgs.runCommand "regen-write-between-preserves-mode"
      {
        nativeBuildInputs = [ pkgs.gawk ];
      }
      ''
        ${documentedFactChecker.spliceShellFn}
        ${regen.writeBetweenShellFn}

        root="$PWD"
        cat > fixture <<'EOF'
        before
        <!-- BEGIN GENERATED -->
        old content
        <!-- END GENERATED -->
        after
        EOF
        chmod 755 fixture

        write_between fixture '<!-- BEGIN GENERATED -->' '<!-- END GENERATED -->' 'new content
        '

        [ -x "$root/fixture" ] || {
          echo "regen-write-between-preserves-mode (issue #3128): write_between dropped the executable bit across its mv -- this is the real-world bug that silently turned agent/entrypoint.sh from 100755 to 100644 on every \`nix run .#regen\` run, including a no-op run against an unmodified tree" >&2
          exit 1
        }

        cat > fixture.expected <<'EOF'
        before
        <!-- BEGIN GENERATED -->
        new content
        <!-- END GENERATED -->
        after
        EOF
        diff -u fixture.expected fixture || {
          echo "regen-write-between-preserves-mode (issue #3128): write_between's mode-preservation fix must not come at the cost of the splice itself -- fixture content above does not match the expected before/BEGIN/new content/END/after shape" >&2
          exit 1
        }
        touch $out
      '';

  # write_between's mktemp'd content file must not survive a failed splice:
  # its own `rm -f` sits after the splice, so the EXIT trap is the only
  # cleanup there (issue #3128 review finding). The harness must be a
  # standalone script, because bash fires a subshell's EXIT trap with the
  # function frame still live, and a reintroduced `local` would pass there.
  regen-write-between-cleans-up-temp-on-failure =
    pkgs.runCommand "regen-write-between-cleans-up-temp-on-failure"
      {
        nativeBuildInputs = [ pkgs.gawk ];
      }
      ''
        # splice's awk cannot open a target that does not exist, so
        # write_between dies mid-function, before reaching its own `rm -f`.
        cat > harness.sh <<'HARNESS'
        set -euo pipefail

        ${documentedFactChecker.spliceShellFn}
        ${regen.writeBetweenShellFn}

        root="$PWD"
        write_between missing-fixture '<!-- BEGIN GENERATED -->' '<!-- END GENERATED -->' 'new content'
        HARNESS

        # A TMPDIR of our own reduces "did the trap delete the file" to "is
        # this directory still empty"; the ambient build TMPDIR already holds
        # unrelated files.
        export TMPDIR="$PWD/isolated-tmp"
        mkdir -p "$TMPDIR"

        set +e
        bash harness.sh 2> splice-failure.log
        status=$?
        set -e

        [ "$status" -ne 0 ] || {
          echo "regen-write-between-cleans-up-temp-on-failure (issue #3128): write_between was expected to fail against a nonexistent target file, but the harness exited 0 -- the cleanup path below is no longer being exercised at all" >&2
          cat splice-failure.log >&2
          exit 1
        }

        [ -z "$(ls -A "$TMPDIR")" ] || {
          echo "regen-write-between-cleans-up-temp-on-failure (issue #3128): write_between leaked its mktemp'd content file when the splice failed (leftover: $(ls -A "$TMPDIR")) -- the EXIT trap is the only cleanup on that path, and it fires after write_between's frame has unwound, so \$content_file must stay script-scope (never a \`local content_file\`) for the trap body to still resolve it" >&2
          exit 1
        }
        touch $out
      '';

  # Proves checkedMerge actually catches `//`'s silent overwrite on collision,
  # rather than the hazard being merely unreachable today (issue #2948).
  checked-merge-rejects-name-collision-guard =
    let
      inherit (pkgs.lib) assertMsg;
      result = builtins.tryEval (checkedMerge { foo = 1; } { foo = 2; });
    in
    assert assertMsg (!result.success)
      "checked-merge-rejects-name-collision-guard: expected checkedMerge to throw when the right-hand attrset's key collides with the left-hand attrset's, but it evaluated successfully";
    pkgs.runCommand "checked-merge-rejects-name-collision-guard" { } "touch $out";

  # Proves duplicateNames actually finds and names a duplicate, rather than
  # being guaranteed to see none today (issue #2948).
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

  # Proves the assert wired into documentedFactChecks throws on duplicate row
  # names, not just that duplicateNames works in isolation. Uses a synthetic
  # two-row list so the real registry stays untouched (issue #2948).
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

  # lib/documented-facts.nix's begin/end trailing-newline contract was once
  # enforced only by its header comment. This drives the shared
  # lib/documented-fact-shape.nix function so the two cannot drift
  # (issue #2948).
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

  # MIGRATING.md's generated table maps each legacy `settings.<section>` alias
  # to its canonical `perSystem.spindrift.*` path, so it cannot drift from the
  # frozen alias map the way the hand-picked prose examples around it could
  # (issue #2558). Regenerate with `nix run .#regen`.
  legacy-settings-mapping-doc =
    let
      generated = renderers.renderLegacySettingsMappingDoc legacySettingsSection schema;
      docSrc = builtins.readFile ../../MIGRATING.md;
    in
    assert (assertLegacySettingsMappingDocOk { inherit docSrc generated; }) == docSrc;
    pkgs.runCommand "legacy-settings-mapping-doc" { } "touch $out";

  # Regression guard (issue #2558): drifts `filerModel`'s row to a wrong
  # canonical path, the shape a `group`/`nixSubPath` rename would leave, so
  # the assertion above is known to reject drift rather than pass vacuously.
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

  # builtins.split's pattern argument is a POSIX extended regex, so a marker
  # carrying a metacharacter must still be treated as literal text rather than
  # silently mis-split. Both the accepted and the rejected case are proved
  # against synthetic markers (issue #2948).
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

  # The three settings-example renderers must derive each line's left-hand
  # path through resolveNixPath, never a hand-typed literal: otherwise a
  # `group`/`nixSubPath` rename leaves them emitting a stale path while
  # settings-example-*-doc stays green, since that check only compares a
  # renderer against its own committed output (issue #2557 review finding).
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

  # Proves assertRendererPathsResolveOk rejects renderer output whose path has
  # reverted to a hand-typed literal, rather than passing vacuously. The
  # synthetic string mimics renderSettingsExampleModelsDoc's shape.
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

  # The disjointness assertion must cover the structural domain-tree paths
  # too, not just the flakeOption nixPaths: a prefix collision between the two
  # otherwise appears as an opaque buildTree throw at flake eval.
  # "agents.driver" is a real structural leaf (issue #2184, ADR 0037).
  flake-nixpath-disjointness-collision-guard = mkNixPathCollisionGuard {
    name = "flake-nixpath-disjointness-collision-guard";
    leaf = "agents.driver";
  };

  # The same for the byName leaf (issue #2731): it fails if allNixPaths ever
  # stops folding in the byName paths, or assertNixPathsOk ever stops
  # rejecting a collision under them. Such a collision would otherwise appear
  # as an opaque buildTree throw at flake eval.
  flake-nixpath-byname-collision-guard = mkNixPathCollisionGuard {
    name = "flake-nixpath-byname-collision-guard";
    leaf = builtins.concatStringsSep "." byNamePaths.byName;
  };

  # Runs assertMarkerConsistencyOk's five invariants against the real schema
  # (issue #2363; emptyDisables added for #3048).
  marker-consistency =
    let
      schema = import ../../lib/env-schema.nix;
    in
    assert (assertMarkerConsistencyOk schema) == schema;
    pkgs.runCommand "marker-consistency" { } "touch $out";

  # Regression guard (issue #2363): five copies of the real schema, each
  # violating exactly one invariant, so dropping any of the five asserts from
  # assertMarkerConsistencyOk fails here instead of passing vacuously.
  marker-consistency-guard =
    let
      schema = import ../../lib/env-schema.nix;
      inherit (pkgs.lib) assertMsg;
      # maxParallel is a real int-typed, non-boxEnvOnly member, so stripping
      # intKind must be caught by missingIntKind.
      missingIntKindSchema = schema // {
        maxParallel = builtins.removeAttrs schema.maxParallel [ "intKind" ];
      };
      # label is a real string-typed member, so an injected intKind must be
      # caught by intKindOnNonInt.
      intKindOnNonIntSchema = schema // {
        label = schema.label // {
          intKind = "nonneg";
        };
      };
      # gitUserName is a real hostDerived member, so marking it boxEnvOnly, a
      # non-membership signal, must be caught by hostDerivedExcluded.
      hostDerivedExcludedSchema = schema // {
        gitUserName = schema.gitUserName // {
          boxEnvOnly = true;
        };
      };
      # intKind is an enum of exactly "positive" or "nonneg", so a typo like
      # "positve" must be caught by badIntKindValue.
      badIntKindValueSchema = schema // {
        maxParallel = schema.maxParallel // {
          intKind = "positve";
        };
      };
      # localIssueReference is a real bool-typed member, so emptyDisables,
      # which is string-knobs-only, must be caught by emptyDisablesOnNonString.
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

  # Runs assertLegacySettingsSectionOk's coverage invariants against the real
  # map and schema (issue #2522).
  legacy-settings-section-coverage =
    assert
      (assertLegacySettingsSectionOk { inherit legacySettingsSection schema; }) == legacySettingsSection;
    pkgs.runCommand "legacy-settings-section-coverage" { } "touch $out";

  # Regression guard (issue #2522): mutated copies of the real map and schema,
  # each exercising exactly one failure shape, so dropping any assert from
  # assertLegacySettingsSectionOk fails here instead of passing vacuously. The
  # exemption escape hatch is covered too, in its accepting direction, for a
  # knob that genuinely postdates the freeze.
  legacy-settings-section-coverage-guard =
    let
      inherit (pkgs.lib) assertMsg;
      # filerModel is a real row for a flakeOption knob with no exemption, so
      # dropping its row must be caught by missing.
      missingLegacySettingsSection = builtins.removeAttrs legacySettingsSection [ "filerModel" ];
      # A row naming a schema key that does not exist, the shape a removed
      # knob leaves behind, must be caught by stale.
      staleLegacySettingsSection = legacySettingsSection // {
        removedKnobNeverInSchema = "someSection";
      };
      # A knob demoted to flakeOption = false; while its row survives. The key
      # still exists, so a stale predicate checking key existence alone would
      # miss it and must consult flakeOption too.
      deadAliasSchema = schema // {
        branchPrefix = schema.branchPrefix // {
          flakeOption = false;
        };
      };
      # A knob in neither the real schema nor preFreezeFlakeOptionNames
      # genuinely postdates the freeze, which is the case legacySettingsExempt
      # exists for, so assertLegacySettingsSectionOk must accept it.
      exemptSkipSchema = schema // {
        syntheticPostFreezeKnob = {
          flakeOption = true;
          legacySettingsExempt = true;
        };
      };
      # mergeMode is a real pre-freeze knob that already has a row, so an
      # injected legacySettingsExempt reproduces the mergeMethod bug. That bug
      # lacked a row entirely; keeping mergeMode's row proves wronglyExempt
      # fires either way.
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
