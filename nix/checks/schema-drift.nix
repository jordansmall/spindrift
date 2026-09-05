# Schema drift guards: every committed generated artifact (Driver name table,
# harness.env.example, launcher flag table, flake-options doc, template
# settings example, man page) must stay in sync with its schema source.
# Shares its renderers with `nix run .#regen` via lib/renderers.nix so the
# guard and the regenerator can never drift from each other (issue #402).
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
  # The documentedFact registry (issue #2948): shared with nix/regen.nix's
  # marker-splice loop so a block's marker literals/renderer call are typed
  # exactly once. documentedFactChecks below derives one named check per row.
  documentedFacts = import ../../lib/documented-facts.nix { inherit (pkgs) lib; };
  # The shared marker-splice + drift-comparison implementation (issue #2949)
  # backing assertMarkedBlockOk below -- also imported by
  # nix/checks/baked-skills.nix so the two files never fork their own
  # hand-mirrored copies again.
  documentedFactChecker = import ../../lib/documented-fact-checker.nix { inherit pkgs; };
  # regenRowScript (issue #2949 review finding): the exact per-row
  # postSplice-dispatch function `nix run .#regen` uses, exercised directly
  # by regen-postsplice-dispatch-guard below against synthetic rows.
  regen = import ../regen.nix { inherit pkgs; };
  # Shared by template-settings-block and the
  # structural-template-examples-*-valid checks below (issue #2572 round 2)
  # so all three consumers of lib/structural-template-examples.nix's byName/
  # roster worked examples share one import instead of three copies.
  structuralTemplateExamples = import ../../lib/structural-template-examples.nix {
    inherit (pkgs) lib;
  };
  rosterLib = import ../../lib/roster.nix { inherit (pkgs) lib; };
  # Parses an example's rendered `lines` (the exact Nix source text
  # templates/default/flake.nix ships, and a Consumer would paste) back as
  # real Nix, wrapped as `{ <key> = <value>; }` -- the
  # structural-template-examples-*-valid checks below need the *rendered
  # text* validated, not just lib/structural-template-examples.nix's backing
  # `.example` value, since a bug in its renderer (e.g. emitting a
  # JSON-style comma-separated list) can desync the two even though
  # `.example` itself stays valid. builtins.toFile writes content-addressed
  # text at eval time with no derivation build, so this isn't
  # import-from-derivation. Note: a genuine Nix *syntax* error in the
  # rendered lines (like that comma-separated-list example) crashes eval
  # during `import` before `builtins.tryEval` below can catch it -- it
  # surfaces as a raw parse-error build failure, not the friendly
  # `parsedFromLines.success` assertMsg text. The build still fails either
  # way (bad renderer output still fails `nix build .#checks-inbox`); only
  # *other* catchable failures inside this function (e.g. an out-of-bounds
  # `builtins.elemAt`) actually reach that assertMsg.
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
  # fields (issue #2363), factored like schemaChoiceIssues/nixPathIssues so
  # the guard can exercise this exact predicate against a synthetic/injected
  # schema, not only the real one. "Int member" here mirrors the isInt
  # default test used elsewhere in this file (and lib/flakeModule.nix:109),
  # narrowed to the schema's two known non-membership signals (secret,
  # boxEnvOnly — the same pair the header's hostConfig doc and
  # hostDerivedExcluded below use to define host-config membership) — the
  # real host-config membership derivation is narrative-only as of this issue
  # and lands in a later slice.
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
      # values (lib/env-schema.nix header) — a typo (e.g. "positve") would
      # otherwise silently pass presence/int-typedness checks. A fourth
      # invariant beyond missingIntKind/intKindOnNonInt/hostDerivedExcluded,
      # added defensively since presence+int-typedness checks alone don't
      # catch a misspelled enum value.
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
  # unchanged. Shared so marker-consistency-guard exercises this exact
  # assertion path (not just markerConsistencyIssues in isolation) — dropping
  # any one of the five asserts here would make that guard fail too, not
  # stay silently green.
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
  # Renders every segment-list value of a structural/byName paths attrset
  # (e.g. lib/structural-paths.nix, lib/byname-paths.nix) as its dotted
  # string form. Shared so allNixPaths below doesn't eta-expand the same
  # `map (segs: concatStringsSep "." segs)` twice, once per source.
  dotted = attrs: map (pkgs.lib.concatStringsSep ".") (pkgs.lib.attrValues attrs);

  # Single real combined nixPath set (issue #2731 review finding): computed
  # once here instead of separately inside flake-nixpath-exhaustive-disjoint
  # and each collision guard below, so a regression in this fold-in (e.g.
  # dropping the byNamePaths splice) is visible to every consumer instead of
  # staying invisible to a guard that silently recomputes its own copy.
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

  # Frozen ground truth (issue #2522 review finding), factored into
  # lib/pre-freeze-flake-options.nix (mirroring lib/legacy-settings-section.nix
  # and lib/structural-paths.nix) so it isn't a fourth hand-copy of a knob
  # list living only in this check.
  preFreezeFlakeOptionNames = import ../../lib/pre-freeze-flake-options.nix;

  # Coverage predicate (issue #2522): every flakeOption knob must either have
  # a row in lib/legacy-settings-section.nix or be explicitly
  # `legacySettingsExempt = true;` in lib/env-schema.nix (a knob added after
  # the ADR 0037 Pass 2 freeze, which never had an old
  # `settings.<section>` alias to preserve) -- a knob added with neither
  # would silently lose alias coverage. And every legacySettingsSection row
  # must still name a live flakeOption schema knob -- a knob removed from
  # the schema, or demoted to flakeOption = false;, leaving its row behind
  # would be a dead entry (checking key existence alone would miss the
  # demoted case). A third invariant
  # cross-checks legacySettingsExempt itself against
  # preFreezeFlakeOptionNames above, rather than trusting the hand-set flag
  # at face value: legacySettingsExempt and a knob's map row are both
  # hand-edited in the same PR, so they can be wrong together (the
  # mergeMethod bug this closes -- wrongly marked exempt despite predating
  # the freeze). Factored like schemaChoiceIssues so the guard can exercise
  # this exact predicate against a synthetic/injected
  # legacySettingsSection/schema pair, not only the real data.
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
      # A knob marked legacySettingsExempt = true; whose name nonetheless
      # appears in the frozen pre-freeze list unconditionally predates the
      # freeze, so it must have had a real old alias -- the exemption is
      # wrong and it needs a real lib/legacy-settings-section.nix row
      # instead.
      wronglyExempt = filter (
        n: (schema.${n}.legacySettingsExempt or false) && elem n preFreezeFlakeOptionNames
      ) flakeOptionNames;
    };

  # Throws via legacySettingsSectionIssues on a bad map/schema pair, else
  # returns legacySettingsSection unchanged. Shared so
  # legacy-settings-section-coverage-guard exercises this exact assertion
  # path (not just legacySettingsSectionIssues in isolation) -- dropping
  # any one of the three asserts here would make that guard fail too, not
  # stay silently green.
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

  # Uniqueness + prefix-disjointness predicate over a flat list of dotted
  # nixPath strings, factored (like schemaChoiceIssues) so the guard can be
  # exercised against a synthetic/injected path set in a test, not only the
  # real one.
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
  # returns it unchanged. Shared so the collision guard exercises this exact
  # assertion path. Messages are source-agnostic (no lib/env-schema.nix:
  # prefix) on purpose: the colliding path may be a flakeOption knob or a
  # structural domain-tree leaf, so a single source file is not implicated.
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

  # Shared by the two nixPath collision guards below: injects a synthetic
  # path nesting under `leaf` into the real combined allNixPaths set, runs
  # it through assertNixPathsOk (the exact function the real
  # flake-nixpath-exhaustive-disjoint check calls) via tryEval, and asserts
  # that eval failed — i.e. the synthetic collision was actually rejected,
  # not silently accepted.
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

  # Asserts fixture.schemaDefaults restates schema's own .default per key,
  # else throws -- the anti-vacuity check for lib/default-model-fixture.nix
  # (issue #2514 AC3): a lib/env-schema.nix default bump with the fixture
  # left un-updated must fail here, not pass because the check happens to
  # read the schema instead of the fixture. Also asserts, in the other
  # direction, that every model-shaped schema key (attr name "model" or
  # ending in "Model" -- lib/env-schema.nix's model/scoutModel/reviewModel/
  # filerModel/workerModel naming convention) is present in the fixture, so a
  # *new* model default added to the schema but never added to the fixture
  # fails here too, instead of the fixture-side filterAttrs above silently
  # never looking at it (issue #2514). Factored out so
  # default-model-fixture-schema-sync-guard and
  # default-model-fixture-schema-sync-completeness-guard can exercise these
  # exact assertion paths against synthetic drifted schemas, not only the
  # real one.
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
  # (between its BEGIN/END GENERATED LEGACY SETTINGS MAPPING markers, issue
  # #2558) matches generated, else throws. Factored out onto the shared
  # assertMarkedBlockOk above (with docPath = "MIGRATING.md", since this
  # table lives in MIGRATING.md rather than docs/reference.md), so
  # legacy-settings-mapping-doc-guard can exercise this exact marker-split +
  # equality assertion path against a synthetic doc, not only the real
  # MIGRATING.md content.
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

  # Shared by every documentedFacts row's check (documentedFactChecks below)
  # and by assertLegacySettingsMappingDocOk: each marker-delimited sub-block
  # lives inside its own host file (a doc's illustrative example per
  # ADR 0037, for the docs/reference.md rows; a template, bash script, or Go
  # source file for the others), between its own BEGIN/END marker pair, and
  # is checked the same way -- split docSrc
  # on the markers, compare the committed slice against generated, else
  # throw a message naming which sub-block (blockName) and which schema file
  # (sourceDesc) it drifted from. Body now lives in
  # lib/documented-fact-checker.nix (issue #2949) so
  # nix/checks/baked-skills.nix shares this exact implementation instead of
  # hand-mirroring its own copy.
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
      # Each non-empty line's exact left-hand path, i.e. everything before
      # its first "=" with the column-alignment padding trimmed off (the
      # renderers right-pad the path to the block's widest path before
      # " = ", so a naive substring/hasInfix check would also accept a
      # wrong-but-prefix path, e.g. "git.merge" matching inside
      # "git.merge.policy").
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
  # `name` (verified: listToAttrs [{name="a";}{name="a";}] -> the first
  # wins, second is dropped with no error) -- a copy-pasted row `name` would
  # otherwise delete that row's drift check from the build with no warning.
  # Named plainly (not folded into checkedMerge below, which guards a
  # different merge -- an attrset `//` onto another attrset -- this guards
  # list-to-attrset construction instead) but shares the same
  # duplicate-detection shape.
  duplicateNames =
    names:
    builtins.attrNames (
      pkgs.lib.filterAttrs (_: occurrences: builtins.length occurrences > 1) (
        builtins.groupBy (n: n) names
      )
    );

  # One named drift check per documentedFacts row (issue #2948), replacing
  # the four hand-written default-models-doc/settings-example-*-doc
  # derivations that used to each hardcode their own marker/source literals
  # and call a thin assert*Ok wrapper. docPath is read via `../../. +
  # "/${row.docPath}"` rather than a literal `../../docs/reference.md` path
  # expression, since row.docPath is a runtime string and Nix path
  # interpolation (`../../${row.docPath}`) requires a literal path prefix.
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

  # `//`'s right-hand side silently wins on a key collision -- unlike a
  # literal Nix attrset with a duplicate key, which is a hard eval error.
  # checkedMerge restores that safety for the one place this file merges two
  # dynamically-built attrsets (documentedFactChecks into the hand-written
  # checks below), so a documentedFacts row named after an existing
  # hand-written check throws instead of silently replacing it with a
  # registry-derived no-op (issue #2948).
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
  # cmd/launcher/internal/driver/drivernames_gen.go must match the key list
  # derived from lib/drivers/default.nix. Fails when a Driver is added to the
  # Nix registry but the committed generated file is not regenerated. Shares
  # its renderer with `nix run .#regen` via lib/renderers.nix (issue #436).
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

  # cmd/launcher/internal/agentpaths/agentpaths_gen.go must match the
  # content generated from lib/agent-paths.nix by lib/renderers.nix's
  # renderAgentPathsGo. Fails when a baked /agent/* path is renamed in the
  # Nix source but the committed generated Go constants aren't regenerated
  # — the host-side gap issue #2531 closes: cmd/launcher/internal/runner/
  # mount.go's SPINDRIFT_PROMPT_DIR mount target reads agentpaths.PromptsDir
  # instead of an independent hardcoded literal, so a rename now fails here
  # instead of silently mounting onto a dead in-box path. Shares its
  # renderer with `nix run .#regen` via lib/renderers.nix.
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

  # cmd/launcher/internal/backend/registry_gen.go must match the content
  # generated from lib/backends/default.nix by lib/renderers.nix's
  # renderBackendRegistryGo, gofmt-normalized the same way `nix run .#regen`
  # normalizes it. Fails when a backend descriptor is added/edited in the Nix
  # registry but the committed generated file is not regenerated. Shares its
  # renderer with `nix run .#regen` via lib/renderers.nix (issue #2521).
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

  # cmd/launcher/internal/doctor/labelmeta_gen.go must match the content
  # generated from lib/labels.nix by lib/renderers.nix's
  # renderLabelRegistryGo, gofmt-normalized the same way `nix run .#regen`
  # normalizes it. Fails when a label row is added/edited in the Nix
  # registry but the committed generated file is not regenerated. Shares its
  # renderer with `nix run .#regen` via lib/renderers.nix (issue #2528).
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

  # cmd/launcher/internal/runner/runtimevalues_gen.go must match the content
  # generated from lib/runtime-values.nix. Fails when the runtime enum
  # changes but the committed generated file isn't regenerated. Shares its
  # renderer with `nix run .#regen` via lib/renderers.nix (issue #2561).
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

  # cmd/launcher/quickstart/quickstart_paths_gen.go must match the content
  # generated from lib/quickstart-path-table.nix. Fails when a quickstart
  # knob's nix option path (lib/nixpath.nix over lib/env-schema.nix's
  # group/nixSubPath) changes but the committed generated file isn't
  # regenerated. Shares its renderer with `nix run .#regen` via
  # lib/renderers.nix (issue #2556).
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

  # cmd/launcher/subcommands_gen.go must match the content generated from
  # lib/subcommands.nix. Fails when a subcommand is added/edited in the Nix
  # registry but the committed generated file is not regenerated. Shares its
  # renderer with `nix run .#regen` via lib/renderers.nix (issue #1575).
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

  # cmd/launcher/internal/outcome/status_gen.go must match the content
  # generated from lib/prompt-contract.nix's outcomeStatusSets, gofmt-
  # normalized the same way `nix run .#regen` normalizes it (the raw
  # renderer output is intentionally unaligned; gofmt owns the const block's
  # column alignment, mirroring launcher-schema-config below). Fails when a
  # status word is added/edited in the Nix registry but the committed
  # generated file is not regenerated. Shares its renderer with
  # `nix run .#regen` via lib/renderers.nix (issue #2504).
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

  # cmd/launcher/internal/outcome/markerchannels_gen.go must match the
  # content generated from lib/prompt-contract.nix's markerChannels,
  # gofmt-normalized the same way `nix run .#regen` normalizes it. Fails
  # when a marker channel is added/edited in the Nix registry but the
  # committed generated file is not regenerated. Shares its renderer with
  # `nix run .#regen` via lib/renderers.nix (issue #2974, parent #2972).
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

  # harness.env.example must match the content generated from env-schema.nix.
  # Fails when a new schema knob is added but the committed file is not
  # regenerated (golden-file drift; resolves issue #109). Shares its renderer
  # with `nix run .#regen` (nix/regen.nix) via lib/renderers.nix — the guard
  # and the regenerator cannot drift from each other (issue #402).
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

  # Every env-var string literal in cmd/launcher/main.go (plus backend.go,
  # issue #2267 — the backend-descriptor registry's per-row token knobs, e.g.
  # BOX_GH_TOKEN/BOX_FORGEJO_TOKEN, moved out of main.go's own resolver
  # functions and into backend.go's row literals, so this check's source
  # scan follows them there rather than widening to every file in package
  # main, which would also pull in flags.go's separately-documented
  # SECRET_CMD fallback — a deliberate sibling-naming convention, not a
  # schema-registered knob, and out of scope for this coverage check to
  # start policing) must have a matching entry in lib/env-schema.nix, and
  # vice-versa (presence-only; value-level pinning would be
  # refactor-brittle). The document's artifact keys (lib/preambles.nix
  # documentArtifactKeys — derived from what runArtifacts/buildArtifacts
  # actually render into the Launcher input document's `artifacts` section,
  # ADR 0020, issue #810) are the schema for what main.go may read outside
  # lib/env-schema.nix, read via getenvArtifact instead of os.Getenv/getenv.
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
      # lib.hasInfix wraps builtins.match with a leading/trailing `.*`, whose
      # C++ std::regex backtracking recurses per character of the haystack —
      # main.go plus backend.go plus schemaconfig_gen.go is >100KB, deep
      # enough to blow the evaluator's C stack (issue #2533 CI: "flake check"
      # segfaulted, exit 139). splitString's regex has no `.*` wrapper (it
      # only escapes the needle), so it doesn't recurse per haystack byte.
      containsLiteral = needle: haystack: builtins.length (splitString needle haystack) > 1;
      launcherDir = ../../cmd/launcher;
      # schemaconfig_gen.go (issue #2364) lands here early — before config/
      # loadConfig embeds schemaConfig — so a later slice wiring it in
      # doesn't fail this check for dozens of knobs whose env-var literal
      # would otherwise only live in the generated file.
      mainGoSrc = concatStringsSep "\n" (
        map (name: builtins.readFile (launcherDir + "/${name}")) [
          "main.go"
          "backend.go"
          "schemaconfig_gen.go"
        ]
      );
      # Document artifact keys: nix-computed plumbing main.go reads via
      # getenvArtifact, not user-facing knobs. Derived from
      # lib/preambles.nix documentArtifactKeys, not hand-maintained here.
      documentArtifacts = preambles.documentArtifactKeys;
      schemaEnvNames = map (e: e.env) (attrValues schema);
      # Schema knobs forwarded to containers via BOX_ENV_VARS only — the Go
      # binary never reads them directly, so they need no os.Getenv call.
      # Derived from each entry's boxEnvOnly field (lib/env-schema.nix) so a
      # new such knob needs no matching edit here.
      boxEnvOnly = map (e: e.env) (filter (e: e.boxEnvOnly or false) (attrValues schema));
      # Forward: every schema name (that Go reads directly) must appear as a
      # string literal in main.go.
      missingFromGo = filter (name: !containsLiteral ''"${name}"'' mainGoSrc) (
        subtractLists boxEnvOnly schemaEnvNames
      );
      # Reverse: extract names from os.Getenv/getenv (1-arg),
      # getenvArtifact (2-arg), and docArtifact (1-arg, issue #2527
      # capability signals) calls in main.go.
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

  # continuousDispatch's doc string must point readers at the exit-code
  # table's actual home, docs/reference.md's Dogfood loop (Termination)
  # section, not the nonexistent "README's exit-code table" it used to cite
  # (issue #1879) — this doc string is the single source rendered onto
  # --help, the man page, and docs/flake-options.md, so a stale pointer
  # there is stale everywhere.
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

  # lib/env-schema.nix's optional `choices` field (issue #554) must be a
  # non-empty list of strings, and a knob's `default` (if any) must be a
  # member of its own `choices` — a knob completing values it can never
  # legally hold would silently mislead a user tab-completing it. Also pins
  # the exact value set for all eight choice-knobs the issue names by name —
  # mergeMode, codeForge, issueTracker, overlapGate, mergeMethod, syncMethod,
  # boxForgeAndIssueAccess, networkMode — so a typo or dropped value fails
  # here instead of silently narrowing/widening what
  # `spindrift --merge-mode <TAB>` etc. offer.
  # Also asserts the *set* of choices-bearing knob names itself (issue #2519)
  # — the eight per-knob asserts below only fire for a knob already listed
  # here by name, so an added ninth knob declaring `choices` would otherwise
  # go unpinned silently. Derives the actual set the same way
  # flake-nixpath-exhaustive-disjoint derives flakeOptionNames above, from
  # the schema itself rather than a second hand-typed list.
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

  # Regression guard (issue #2519): the choices-bearing knob-set assertion
  # above must actually detect an added/renamed choices-bearing knob, not
  # just pass vacuously because the real schema currently has exactly the
  # eight pinned names. Injects a ninth synthetic knob declaring `choices`
  # into a copy of the real schema and asserts, via tryEval, that
  # schema-choices' own set-equality check (reimplemented here against the
  # injected schema, the same way schema-secret-choices-guard reruns
  # assertSchemaChoicesOk rather than schema-choices itself) rejects it.
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

  # Regression guard (issue #872): lib/renderers.nix's bash/fish/zsh
  # completion renderers always scope `choices` to nonSecret knobs (a secret
  # gets only a `--*-file` path flag, never a value-taking one), but
  # schema-choices above used to validate `choices` shape/default on every
  # knob, secret or not. A `choices` field on a secret knob would therefore
  # pass validation yet never render anywhere — a silent no-op. Runs
  # assertSchemaChoicesOk — the exact function schema-choices calls — against
  # the real schema with one secret knob's `choices` injected, via tryEval so
  # this fails independently of whether any real secret knob currently
  # declares choices, and would also fail if the badSecret assert were ever
  # dropped from assertSchemaChoicesOk (not just from schemaChoiceIssues).
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

  # Regression guard (issue #2519 slice 2): lib/flakeModule.nix's generated
  # Consumer options use `types.enum` for every choices-bearing knob, but that
  # only protects Consumers going through the flake module. A Consumer calling
  # `mkHarness { defaults = {...}; }` directly (bypassing the flake module,
  # e.g. fixtures.nix's `minimalDirect`/`harness`/etc. wiring, or a downstream
  # flake-parts-free consumer) had no eval-time protection against an invalid
  # choice value at all. Proves lib/mkHarness.nix itself rejects a
  # direct-caller-supplied invalid `mergeMethod` (one of the 7 choice-bearing
  # knobs named in lib/env-schema.nix), via tryEval so this fails
  # independently of any real Consumer ever getting this wrong, and would
  # also fail if the assert were ever dropped from mkHarness.nix.
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

  # The gate-not-triggered counterpart (mirrors
  # build-time-reject-orchestrator-verdict-not-triggered in
  # nix/checks/prompts.nix and flakemodule-rejects-invalid-choice in
  # nix/checks/equivalence.nix): without this, an unrelated eval failure in
  # the `import ../../lib/mkHarness.nix { ... }` call above (a new required
  # arg, an added unrelated assert) would make mkharness-direct-choices-guard
  # pass vacuously even with the choices assert deleted from mkHarness.nix.
  # Proves the same direct-call shape still evaluates cleanly for an in-choice
  # `mergeMethod` value, so badResult.success == false is known to come from
  # the choices assert specifically, not from an incidental break elsewhere.
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

  # Regression guard (issue #2519): choiceViolations in lib/mkHarness.nix
  # used to special-case `value == null -> null` (skip the guard entirely
  # for a null choice value), so a direct caller passing
  # `defaults.mergeMethod = null` silently passed and documentSettings went
  # on to render `MERGE_METHOD=""` via `toString null`. The null-choice fix
  # dropped that skip so a null choice value is rejected like any other
  # non-member value.
  # Distinct from mkharness-direct-choices-guard above, which only pins the
  # non-null-bogus-value case ("bogus-merge-method") -- that check alone
  # would keep passing even if a `value == null -> null` skip were
  # reintroduced into choiceViolations, since null never reaches its
  # `lib.elem value choices` check. This check closes that gap by asserting
  # mkHarness still rejects `defaults.mergeMethod = null` from a direct
  # caller.
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
  # `parse` is actually wired into mkHarness's eval-time assert chain, not
  # just exercised in isolation by nix/checks/jira-status-mapping.nix. A
  # direct caller supplying a JIRA_STATUS_MAPPING knob with an unknown key
  # must fail the build.
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

  # The gate-not-triggered counterpart (mirrors
  # mkharness-direct-choices-guard-not-triggered above): proves the same
  # direct-call shape still evaluates cleanly for a valid JIRA_STATUS_MAPPING
  # value, so mkharness-jira-status-mapping-guard's failure is known to come
  # from the JIRA_STATUS_MAPPING guard specifically, not an incidental break
  # elsewhere in the call shape.
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
  # schema knob, so the entrypoint-*.bats suites exercise the same defaults the nix
  # preamble bakes into the image at build time (issue #462). Fails when a new
  # boxEnv knob is added to the schema but the committed generated fixture is
  # not regenerated (golden-file drift, same treatment as harness-env-example
  # above). Shares its renderer with `nix run .#regen` via lib/renderers.nix
  # (issue #520).
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

  # cmd/launcher/flagtable_gen.go must match the content generated from
  # env-schema.nix by mkHarness.nix renderFlagTableGo.  Fails when a new
  # schema knob is added but the committed generated file is not regenerated.
  # Shares its renderer with `nix run .#regen` via lib/renderers.nix.
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

  # cmd/launcher/schemaconfig_gen.go must match the content generated from
  # env-schema.nix by lib/renderers.nix renderSchemaConfigGo, gofmt-
  # normalized the same way `nix run .#regen` normalizes it (the raw
  # renderer output is intentionally unaligned; gofmt owns column
  # alignment for the struct/composite-literal blocks, issue #2364).
  # Fails when a host-config schema member changes but the committed
  # generated file is not regenerated.
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

  # cmd/launcher/internal/promptassembly/boxenv_gen.go must match the
  # content generated from lib/promptassembly-boxenv.nix by lib/renderers.nix
  # renderPromptAssemblyBoxEnvGo, gofmt-normalized the same way `nix run
  # .#regen` normalizes it (the raw renderer output is intentionally
  # unaligned; gofmt owns column alignment for the struct-literal block,
  # issue #2979). Fails when a box-env row changes but the committed
  # generated file is not regenerated.
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

  # docs/flake-options.md must match the reference generated from
  # env-schema.nix plus the hand-declared structural knobs
  # (lib/structural-options-doc.nix, issue #2572). Fails when a flakeOption
  # knob is added/removed or a structural knob's doc metadata changes but
  # the committed file is not regenerated (same treatment as
  # harness.env.example and flagtable_gen.go). Shares its renderers with
  # `nix run .#regen` via lib/renderers.nix.
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

  # Issue #2572 round 2 (blocking finding 2): checkEntry inside
  # lib/structural-template-examples.nix only regex-matches the *rendered
  # text* of each worked example -- it never runs the example *values*
  # through real validation, so an unusable example (e.g. finding 1's
  # roster entries missing description/tools) could ship silently. This
  # check finds the roster example's `.example` field (the real Nix list
  # lib/structural-template-examples.nix now also exports alongside its
  # rendered `lines`) and actually evaluates it: it must survive
  # normalizeRoster unchanged, and -- the regression guard for finding 1
  # specifically -- every entry must carry a non-empty description and a
  # non-empty tools list, since a Driver renders `description: ""` /
  # `tools: [ ]` for either omission (lib/drivers/claude.nix:173,175;
  # lib/drivers/opencode.nix:153,159), producing a capability-less agent.
  #
  # Issue #2572 round 3 (blocking findings 1 and 2): round 2's guard only
  # covered description/tools by name -- the same class of bug recurred
  # through promptFile (round 2's fix didn't inherit it, so normalizeRoster
  # silently injected a wrong default for reviewer specifically). Two more
  # checks close the class instead of the one field: every example entry's
  # mode/description/tools/promptFile/effort must equal its defaultRoster
  # counterpart's -- only `model` is exempted from this check, since it's the
  # one field a Consumer copying this example is expected to freely
  # customize (today's shipped values happen to equal defaultRoster's own,
  # but nothing requires that) -- and every entry's normalizeRoster-resolved
  # promptFile must resolve to a file that actually exists under
  # templates/default/prompts/.
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

  # Issue #2572 round 2 (blocking finding 2), byName half: the byName
  # example has no dedicated normalize function the way roster does, so this
  # runs it through rosterLib.defaultRoster's own byName argument instead --
  # a deliberate proxy for flakeModule.nix's byNameOption submodule shape,
  # since types.attrsOf doesn't itself constrain key names the way
  # defaultRoster's runtime checks do (it throws on an unknown byName agent
  # name or an unknown byName field, the same two invariants byNameOption's
  # real Driver-facing consumers depend on).
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
      # Roff renders the flag as \-\- with every hyphen escaped; match that
      # form. toKebab comes from lib/renderers.nix — the same helper the man
      # page itself is rendered through.
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

  # Pure-eval pin on renderZshCompletion's shape (issue #552): a schema flag,
  # its alias, and a secret file flag must each carry a `[description]` zsh
  # completion annotation sourced from the schema's `doc` string, and a
  # secret file flag's argument must complete via `_files`. Complements
  # launcher-zsh-completion below, which covers the built artifact end to
  # end; this one pins the renderer's output shape without a store build.
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

  # Pure-eval pin (issue #874): a knob carrying both `alias` and `choices`
  # must complete its value list for *either* flag form. No real schema knob
  # combines the two today (only issueNumber has an alias; none of the four
  # choices knobs do), so this exercises a hand-built synthetic schema rather
  # than lib/env-schema.nix — deliberately isolated from production schema
  # per the issue's research verdict, to avoid coupling test fixture data to
  # runtime schema.
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

  # Pure-eval pin (issue #1603): dynamic issue-number completion gating must
  # be *derived* from each registry entry's dynamicIssueCompletion field, not
  # a list independent of the passed-in subcommandRegistry. A hand-built
  # synthetic registry — none of its names are real subcommands — proves the
  # renderers actually read the field instead of coincidentally matching the
  # production dispatch/preview/recover literal. Mirrors "research" by name
  # (unflagged, like the real registry entry) to pin the issue #556 exclusion
  # this field must preserve: a subcommand can carry issue-shaped `usage`
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
    # The closing "'" right after "alpha" makes this an exact-membership
    # pin, not just a prefix check: any excluded name appended (or
    # prepended) after alpha would push the closing quote further along the
    # string, so this single assertion covers both inclusion and exclusion.
    assert assertMsg (hasInfix "'__fish_seen_subcommand_from alpha'" fishOut)
      "renderFishCompletion's __fish_seen_subcommand_from predicate must be exactly the dynamicIssueCompletion = true entries, got: ${fishOut}";
    assert assertMsg (hasInfix "alpha)" zshOut)
      "renderZshCompletion's issue-completion case arm must include a dynamicIssueCompletion = true entry, got: ${zshOut}";
    assert assertMsg (builtins.all (n: !hasInfix "${n})" zshOut) excluded)
      "renderZshCompletion's issue-completion case arm must exclude entries without dynamicIssueCompletion = true, got: ${zshOut}";
    pkgs.runCommand "renderer-issue-completion-registry-shape" { } "touch $out";

  # The generated bash completion script must totally cover the schema and the
  # registry's subcommand set (lib/subcommands.nix): every non-secret flag,
  # the --issue alias, every secret --*-file flag, and every registered
  # subcommand. A new knob or subcommand with no completion presence fails
  # here. Mirrors launcher-manpage.
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
      # substring grep would let e.g. `--issue` pass as "covered" merely
      # because `--issue-number` contains it as a prefix.
      flagChecks = concatMapStrings (e: "need '--${renderers.toKebab e.env}'\n") nonSecret;
      aliasChecks = concatMapStrings (e: if e ? alias then "need '--${e.alias}'\n" else "") nonSecret;
      secretChecks = concatMapStrings (e: "need '--${renderers.toKebab e.env}-file'\n") secretEntries;
      secretCmdChecks = concatMapStrings (e: "need '--${renderers.toKebab e.env}-cmd'\n") secretEntries;
      # Subcommand names are plain English words that can legitimately show
      # up in a comment (e.g. "rendered at build time"); a per-word boundary
      # check would pass even with a subcommand missing. Require the exact
      # assembled list the renderer emits for the first-word case, so a
      # dropped/renamed/reordered subcommand fails here.
      subcommandLine = concatStringsSep " " subcommands;
      # A choices-bearing knob must complete to exactly its own value list
      # (issue #554): pin the exact `compgen -W "..."` string the renderer
      # emits for that flag, not a per-word substring check, so a value
      # attached to the wrong flag (or dropped) fails here.
      choicesChecks = concatMapStrings (
        e:
        "grep -qF -- 'compgen -W \"${concatStringsSep " " e.choices}\"' \"$completion\" "
        + "|| { echo 'bash completion missing choices for --${renderers.toKebab e.env}' >&2; exit 1; }\n"
      ) choicesKnobs;
      # Dynamic issue-number completion (issue #556) must gate on exactly
      # the registry's dynamicIssueCompletion = true entries, not the full
      # subcommand set (build/doctor take no issue argument) — pin the exact
      # case-arm pattern the renderer emits, mirroring subcommandLine's
      # exact-list rationale above. Derived the same way renderBashCompletion
      # derives it (issue #1603), so this can't drift from the renderer.
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

  # The generated fish completion script must totally cover the schema and the
  # registry's subcommand set (lib/subcommands.nix): every non-secret flag,
  # the --issue alias, every secret --*-file flag, and every registered
  # subcommand. Mirrors launcher-bash-completion above.
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
      # Subcommands render one per line as `-a '<name>'`; that exact quoted
      # token can't appear incidentally in a comment (unlike the bare word),
      # so a plain fixed-string search is enough — no boundary check needed.
      subcommandChecks = concatMapStrings (s: "needF \"-a '${s}'\"\n") subcommands;
      # Pin the exact `-a '...'` argument list the renderer emits for each
      # choices-bearing flag (issue #554): an exact quoted token, like the
      # subcommand check above, so a value attached to the wrong flag (or
      # dropped) fails here.
      choicesChecks = concatMapStrings (
        e: "needF \"-a '${builtins.concatStringsSep " " e.choices}'\"\n"
      ) choicesKnobs;
      # Dynamic issue-number completion (issue #556) must gate on exactly
      # the registry's dynamicIssueCompletion = true entries, not the full
      # subcommand set — pin the exact `__fish_seen_subcommand_from`
      # condition the renderer emits. Derived the same way
      # renderFishCompletion derives it (issue #1603).
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

  # zsh equivalent of launcher-bash-completion: every non-secret flag, the
  # --issue alias, every secret --*-file flag, and every registry subcommand
  # (lib/subcommands.nix) must appear in the rendered zsh completion
  # function. renderZshCompletion emits each as a single-quoted `_describe`
  # entry `'--flag:description'` (or `'name:description'` for a subcommand),
  # so the flag/subcommand name immediately followed by `:` inside its
  # opening quote is itself an unambiguous token boundary — a substring
  # check suffices (no --issue vs --issue-number collision, since the colon
  # only follows the exact name).
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
      # Pin the exact `compadd -- ...` argument list the renderer emits for
      # each choices-bearing flag (issue #554), not a per-word substring
      # check, so a value attached to the wrong flag (or dropped) fails here.
      choicesChecks = concatMapStrings (
        e: "need 'compadd -- ${builtins.concatStringsSep " " e.choices}'\n"
      ) choicesKnobs;
      # Dynamic issue-number completion (issue #556) must gate on exactly
      # the registry's dynamicIssueCompletion = true entries, not the full
      # subcommand set — pin the exact case-arm pattern the renderer emits,
      # mirroring the bash guard above. Derived the same way
      # renderZshCompletion derives it (issue #1603).
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

  # ADR 0037 Pass 2 (issue #2188): the flake path is derived, not stored —
  # every flakeOption = true knob must declare a non-empty string `group`
  # (the domain segment), and lib/nixpath.nix's resolveNixPath combines it
  # with the knob's optional `nixSubPath` (defaulting to the knob's own
  # schema key) to produce its dotted leaf in the flake surface's domain
  # tree. This check asserts every flakeOption knob has a usable `group` and
  # that all derived paths — folded together with the structural domain-tree
  # paths — are unique and prefix-disjoint, so no leaf can collide with (or
  # nest inside) another knob's namespace.
  flake-nixpath-exhaustive-disjoint =
    let
      inherit (pkgs.lib)
        assertMsg
        attrNames
        filter
        concatStringsSep
        ;
      # Every flakeOption knob, used below to check each one declares a
      # usable `group` (missingGroup). The cross-set disjointness fold
      # (flakeOption leaves + structural leaves, checked via assertNixPathsOk
      # below) now lives in the shared allNixPaths binding above, not here.
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

  # lib/default-model-fixture.nix's schemaDefaults must restate
  # lib/env-schema.nix's own .default values per key (issue #2514): a schema
  # default bump with the fixture left un-updated fails here instead of
  # silently validating against itself, since the fixture is the anti-vacuity
  # root the two Nix check files (nix/checks/image.nix,
  # nix/checks/equivalence.nix) import instead of re-typing the literals.
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

  # Regression guard (issue #2514 AC3): the sync assertion above must actually
  # detect a drifted schema default, not just pass vacuously because
  # lib/env-schema.nix currently agrees with the fixture. Runs
  # assertFixtureMatchesSchemaOk -- the exact function
  # default-model-fixture-schema-sync calls -- against a synthetic schema
  # whose reviewModel default has been bumped away from the fixture's
  # claude-opus-5, via tryEval, so this fails if the equality assert is ever
  # dropped from assertFixtureMatchesSchemaOk.
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

  # Regression guard (issue #2514): the sync assertion above must also detect
  # a *new* model-shaped schema key that was never added to the fixture, not
  # just a mismatched value on a key the fixture already tracks --
  # default-model-fixture-schema-sync-guard above only proves the
  # value-mismatch direction is non-vacuous. Runs assertFixtureMatchesSchemaOk
  # against a synthetic schema equal to the real one plus one extra
  # model-shaped key (extraModel) absent from
  # defaultModelFixture.schemaDefaults, via tryEval, so this fails if the
  # missingFromFixture assert is ever dropped from assertFixtureMatchesSchemaOk.
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

  # tests/default_models_gen.bash must match the content generated from
  # lib/default-model-fixture.nix by lib/renderers.nix
  # renderDefaultModelFixtureBash. Fails when the fixture is edited but the
  # committed generated file is not regenerated. Shares its renderer with
  # `nix run .#regen` via lib/renderers.nix, so guard and regenerator cannot
  # drift from each other (issue #2514, slice 2 of 3).
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

  # cmd/launcher/defaultmodels_gen_test.go must match the content generated
  # from lib/default-model-fixture.nix by lib/renderers.nix
  # renderDefaultModelFixtureGo, gofmt-normalized the same way `nix run
  # .#regen` normalizes it. Fails when the fixture is edited but the
  # committed generated file is not regenerated. Shares its renderer with
  # `nix run .#regen` via lib/renderers.nix (issue #2514, slice 2 of 3).
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

  # Family guard (issue #2948): one derivation now covers what
  # default-models-doc-guard/settings-example-{models,labels,config}-doc-guard
  # used to prove separately -- that assertMarkedBlockOk, driven by a
  # documentedFacts-shaped row, actually rejects a drifted block, not just
  # pass vacuously. Exercises EVERY row (not just builtins.head, issue #2948
  # review finding: rows 1+ were never touched by this guard, and an emptied
  # registry would fail with an unhelpful "list is empty" instead of a
  # guard-specific message) by drifting each row's own docSrc by appending a
  # sentinel after its `generated`, content-agnostic on purpose so this guard
  # doesn't need to know anything about any one row's actual business content
  # (per-row content coverage stays with documentedFactChecks;
  # marked-block-escaping-guard above separately covers the regex-
  # metacharacter marker hazard with a fully synthetic row). Via tryEval per
  # row, so this fails if the equality assert is ever dropped from
  # assertMarkedBlockOk, naming which row(s) it failed to catch. `postSplice
  # == "gofmt"` rows never go through assertMarkedBlockOk in production
  # (documentedFactChecks above routes them to assertSplicedSpanOk instead),
  # so running them through assertMarkedBlockOk here would prove nothing
  # about the path they actually take -- those rows instead drive
  # assertSplicedSpanOk's own diff-rejection path (via its `expectMismatch`
  # flag) against a synthetic drift, collected into gofmtDriftGuards below
  # and forced to build alongside the assertMarkedBlockOk rows (issue #2949
  # review finding) so a `postSplice == "gofmt"` row's rejection path stays
  # covered too. Still ONE derivation (not fanned out per row) so the
  # check-name surface stays unchanged.
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

  # Issue #2950 review finding: renderOptionSurfaceTableDoc's 14 table rows
  # are a fixed literal list keyed by name, not derived from
  # structuralPaths/byNamePaths, so a NEW key added to either registry
  # without a matching table row used to vanish from the generated doc
  # block silently -- the old hand-written assertOptionSurfaceDocPathsOk
  # check (removed when this table moved to a renderer) used to catch that.
  # Proves the renderer itself now throws on an unlisted key: feeds it
  # structuralPaths plus one synthetic key the renderer's known-row list
  # can't contain, and asserts that eval FAILS. tryEval + deepSeq forces the
  # throw during the tryEval instead of it escaping lazily as an unforced
  # thunk.
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

  # Issue #2796: flake-options-doc above regenerates the byName row from the
  # same source it diffs against, so it structurally cannot catch a desync
  # between lib/byname-paths.nix and a path renderStructuralOptionsDoc
  # hardcodes. This proves the derivation at the renderer instead: feed it a
  # synthetic byNamePaths with an unmistakable, non-default path and assert
  # the rendered byName row carries *that* path.
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

  # regen's postSplice dispatch had zero test coverage before this (issue
  # #2949 review finding): nothing proved `nix run .#regen` actually runs
  # `gofmt -w` on a postSplice == "gofmt" row's host file after splicing, and
  # a typo in the field (wrong case, misspelling) would silently take the
  # no-gofmt branch with nothing catching it. Calls regen.regenRowScript
  # directly -- the exact function nix/regen.nix's text uses for real, not a
  # hand-mirrored reimplementation -- against three synthetic rows sharing a
  # documentedFacts row's shape, and pins its current, exact dispatch
  # behavior: "gofmt" fires the gofmt -w line, no postSplice field doesn't,
  # and (deliberately, to document rather than fix the typo hazard --
  # validating the field itself is a separate concern) neither does a
  # wrong-case "Gofmt" typo. The positive assertion checks for the exact
  # `gofmt -w "$root/<docPath>"` substring (not just the bare text
  # "gofmt -w"), so a regression that ran gofmt against the wrong path (e.g.
  # a copy-pasted literal from a different row) would actually be caught --
  # the two negative assertions stay bare substring checks since they're
  # proving absence, not correctness-of-path.
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
      # regenRowScript escapeShellArg's the docPath (issue #2949 review
      # finding: the old `"$root/${row.docPath}"` form spliced docPath raw
      # into the generated script), so the emitted invocation is the
      # double-quoted "$root/" prefix immediately followed by a
      # single-quoted docPath literal, not one single double-quoted string.
      expectedGofmtInvocation = ''gofmt -w "$root/"${escapeShellArg gofmtRow.docPath}'';
    in
    assert assertMsg (hasInfix expectedGofmtInvocation gofmtScript)
      "regen-postsplice-dispatch-guard: expected regenRowScript to emit \"${expectedGofmtInvocation}\" for a postSplice = \"gofmt\"; row, but it did not";
    assert assertMsg (!(hasInfix "gofmt -w" plainScript))
      "regen-postsplice-dispatch-guard: expected regenRowScript NOT to emit \"gofmt -w\" for a row with no postSplice field, but it did";
    assert assertMsg (!(hasInfix "gofmt -w" typoScript))
      "regen-postsplice-dispatch-guard: expected regenRowScript NOT to emit \"gofmt -w\" for a postSplice = \"Gofmt\"; (wrong-case typo) row, but it did -- this pins the current typo-silently-no-ops behavior, not a validation guarantee";
    pkgs.runCommand "regen-postsplice-dispatch-guard" { } "touch $out";

  # Wiring guard (issue #3128), same shape as promptassembly.nix's
  # regen-goldens-app-wiring: flake.nix's apps.regen must resolve to the SAME
  # derivation this check builds from nix/regen.nix, and referencing regen's
  # own output path in the build script forces this check to actually build
  # it -- including writeShellApplication's build-time shellcheck pass -- so
  # a broken regen script (e.g. shellcheck findings in its hand-written
  # trap/write helpers) fails `nix build .#checks-inbox` instead of `nix run
  # .#regen` silently being the only place the build ever gets exercised.
  regen-app-wiring =
    let
      inherit (pkgs.lib) assertMsg;
      expectedProgram = "${regen}/bin/regen";
    in
    assert assertMsg (config.apps ? regen)
      "flake.nix must expose apps.regen at the top level so \`nix run .#regen\` actually resolves, got top-level app names: ${builtins.toJSON (builtins.attrNames config.apps)}";
    assert assertMsg (
      config.apps.regen.type == "app"
    ) "flake.nix's top-level apps.regen must be a real app, got: ${builtins.toJSON config.apps.regen}";
    assert assertMsg (config.apps.regen.program == expectedProgram)
      "flake.nix's top-level apps.regen must be built from nix/regen.nix with the SAME pkgs this check uses -- otherwise \`nix run .#regen\` silently regenerates against a foreign env: ${config.apps.regen.program} != ${expectedProgram}";
    pkgs.runCommand "regen-app-wiring" { } ''
      [ -x ${regen}/bin/regen ]
      touch $out
    '';

  # write_between must preserve the target file's mode across its `mv`
  # (issue #3128): `splice` writes $file.regen-tmp fresh under the default
  # umask, so a plain `mv` onto an executable committed file (e.g.
  # agent/entrypoint.sh, 100755) silently dropped its exec bit on every
  # `nix run .#regen` -- including a no-op run against an unmodified tree,
  # which the issue's acceptance criterion requires to produce no diff at
  # all. Exercises the real write_between (via writeBetweenShellFn) plus the
  # real splice (via spliceShellFn) against a synthetic 755 fixture, rather
  # than a hand-mirrored reimplementation, so a regression in either shared
  # function is actually caught here.
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

  # write_between's mktemp'd content file must not survive a failed splice
  # (issue #3128 review finding): its own `rm -f` sits *after* the splice, so
  # on that path the EXIT trap is the sole cleanup, and one temp file leaks
  # per failed `nix run .#regen` if the trap is dropped or its body cannot
  # resolve $content_file at fire time. The harness has to be a standalone
  # script run under its own top-level `set -euo pipefail` -- the same regime
  # writeShellApplication puts regen's script under -- because bash fires a
  # `( set -e; write_between ... )` subshell's EXIT trap with the function
  # frame still live: a reintroduced `local content_file` resolves there and
  # nothing leaks, so a subshell harness would pass against the very
  # regression this check names. Drives the real write_between/splice pair,
  # like the mode check above.
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

  # Regression guard for checkedMerge (issue #2948 blocking review finding):
  # proves `//`'s silent-overwrite-on-collision hazard is actually caught,
  # not just structurally impossible to hit today.
  checked-merge-rejects-name-collision-guard =
    let
      inherit (pkgs.lib) assertMsg;
      result = builtins.tryEval (checkedMerge { foo = 1; } { foo = 2; });
    in
    assert assertMsg (!result.success)
      "checked-merge-rejects-name-collision-guard: expected checkedMerge to throw when the right-hand attrset's key collides with the left-hand attrset's, but it evaluated successfully";
    pkgs.runCommand "checked-merge-rejects-name-collision-guard" { } "touch $out";

  # Regression guard (issue #2948 blocking review finding): proves
  # duplicateNames actually finds and names a duplicate, not just
  # structurally guaranteed to see none today.
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

  # Regression guard (issue #2948 blocking review finding): proves the real
  # assert wired into documentedFactChecks actually throws given duplicate
  # row names, not just that duplicateNames works in isolation. Exercises the
  # same shape documentedFactChecks uses (dupes == [ ] assertMsg) against a
  # synthetic 2-row name list, without touching the real documentedFacts
  # registry.
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

  # Regression guard (issue #2948 blocking review finding): lib/documented-
  # facts.nix's begin/end trailing-newline contract (see that file's header
  # comment) used to be enforced only by the comment -- nothing checked it.
  # Exercises the shared lib/documented-fact-shape.nix function directly
  # against synthetic rows so this guard and the real self-validation in
  # lib/documented-facts.nix can never drift from each other.
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

  # MIGRATING.md's generated legacy-settings-to-domain-tree mapping table
  # (between its BEGIN/END GENERATED LEGACY SETTINGS MAPPING markers, issue
  # #2558) must match the content rendered from
  # lib/legacy-settings-section.nix -- one row per legacy `settings.<section>`
  # alias mapped to its canonical `perSystem.spindrift.*` domain-tree path, so
  # the table can't drift from the frozen alias map the way the four
  # hand-picked prose examples in the surrounding "Flag names re-cut to
  # domains" section could. Shares its renderer with `nix run .#regen` via
  # lib/renderers.nix, so guard and regenerator cannot drift from each other
  # (issue #402). Mirrors default-models-doc above.
  legacy-settings-mapping-doc =
    let
      generated = renderers.renderLegacySettingsMappingDoc legacySettingsSection schema;
      docSrc = builtins.readFile ../../MIGRATING.md;
    in
    assert (assertLegacySettingsMappingDocOk { inherit docSrc generated; }) == docSrc;
    pkgs.runCommand "legacy-settings-mapping-doc" { } "touch $out";

  # Regression guard (issue #2558): the doc-drift assertion above must
  # actually detect a drifted generated legacy settings mapping table, not
  # just pass vacuously because MIGRATING.md's table currently agrees with
  # lib/legacy-settings-section.nix. Runs assertLegacySettingsMappingDocOk --
  # the exact function legacy-settings-mapping-doc calls -- against a
  # synthetic doc whose row for `filerModel` states a wrong canonical path (a
  # plausible drift a schema `group`/`nixSubPath` rename could leave behind),
  # via tryEval, so this fails if the equality assert is ever dropped from
  # assertLegacySettingsMappingDocOk. Mirrors documented-fact-guard's tryEval
  # regression-guard pattern above.
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

  # Regression guard for assertMarkedBlockOk itself, not one of its per-fact
  # callers (issue #2948): builtins.split's pattern argument is a POSIX
  # extended regex, so a beginMarker/endMarker carrying a regex metacharacter
  # ("(", ")", ".", "*") must still be treated as literal marker text, not
  # silently mis-split. Runs assertMarkedBlockOk directly against a synthetic
  # doc using such markers, via tryEval, proving both that a non-drifted
  # block is accepted and a drifted block is rejected even with
  # regex-special marker text.
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

  # Regression guard for issue #2557's review finding: renderSettingsExampleModelsDoc/
  # LabelsDoc/ConfigDoc (lib/renderers.nix) must derive every emitted line's
  # left-hand domain path via resolveNixPath (lib/nixpath.nix) from the
  # knob's own lib/env-schema.nix entry, never a hand-typed path literal --
  # otherwise a `group`/`nixSubPath` rename could leave these three
  # renderers emitting a stale path while settings-example-*-doc above
  # stays green (it only compares the renderer's own output against the
  # committed doc, so it can't catch the renderer itself drifting from
  # resolveNixPath). assertRendererPathsResolveOk (defined above, alongside
  # the other assert*Ok helpers) re-derives each knob's expected path
  # independently via resolveNixPath (not by calling the renderer a second
  # time, which would only prove the renderer agrees with itself) and
  # asserts it appears as an exact left-hand path among the renderer's
  # generated lines -- a substring match was deliberately rejected (see
  # assertRendererPathsResolveOk's own comment above) since it would also
  # accept a wrong-but-prefix path like "git.merge" inside "git.merge.policy".
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

  # Regression guard for settings-example-paths-resolve-nix-path above:
  # proves assertRendererPathsResolveOk actually rejects a renderer output
  # whose path has reverted to a hand-typed (wrong/stale) literal, instead
  # of passing vacuously (mirrors marker-consistency-guard's tryEval
  # pattern). Runs it against a synthetic "generated" string that mimics
  # renderSettingsExampleModelsDoc's shape but with the first line's path
  # replaced by a literal that does not match resolveNixPath's output.
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

  # Regression guard (issue #2184, ADR 0037): the disjointness assertion must
  # cover the structural domain-tree paths too, not just the flakeOption
  # nixPaths — a future structural-vs-flakeOption prefix collision otherwise
  # slips past this check and surfaces as an opaque buildTree throw at flake
  # eval. "agents.driver" is a real structural leaf; a knob landing under it
  # would collide — exactly the latent cross-set failure this guards.
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
  # (issue #2363, emptyDisables added for #3048) must stay internally
  # consistent: every int-typed, non-secret, non-boxEnvOnly member declares
  # intKind; intKind never decorates a non-int member; intKind, when
  # present, is exactly "positive" or "nonneg"; hostDerived never
  # contradicts host-config membership (secret or boxEnvOnly); and
  # emptyDisables never decorates a non-string-typed member. Runs
  # assertMarkerConsistencyOk against the real schema.
  marker-consistency =
    let
      schema = import ../../lib/env-schema.nix;
    in
    assert (assertMarkerConsistencyOk schema) == schema;
    pkgs.runCommand "marker-consistency" { } "touch $out";

  # Regression guard (issue #2363): the marker-consistency check above must
  # actually detect a violation of each of its five invariants, not just
  # pass vacuously because the real schema already satisfies them. Runs
  # assertMarkerConsistencyOk — the exact function marker-consistency calls —
  # against five independently-mutated copies of the real schema, each
  # violating exactly one invariant, via tryEval, so this fails if any one of
  # the five asserts is ever dropped from assertMarkerConsistencyOk (not
  # just from markerConsistencyIssues).
  marker-consistency-guard =
    let
      schema = import ../../lib/env-schema.nix;
      inherit (pkgs.lib) assertMsg;
      # maxParallel is a real int-typed, non-boxEnvOnly member (intKind =
      # "positive") — stripping intKind must be caught by missingIntKind.
      missingIntKindSchema = schema // {
        maxParallel = builtins.removeAttrs schema.maxParallel [ "intKind" ];
      };
      # label is a real string-typed member — decorating it with an intKind
      # it has no business carrying must be caught by intKindOnNonInt.
      intKindOnNonIntSchema = schema // {
        label = schema.label // {
          intKind = "nonneg";
        };
      };
      # gitUserName is a real hostDerived member — marking it boxEnvOnly (a
      # non-membership signal) must be caught by hostDerivedExcluded.
      hostDerivedExcludedSchema = schema // {
        gitUserName = schema.gitUserName // {
          boxEnvOnly = true;
        };
      };
      # maxParallel again — this time with a typo'd intKind value. intKind is
      # documented (lib/env-schema.nix header) as an enum of exactly
      # "positive" / "nonneg"; a typo like "positve" must be caught by
      # badIntKindValue.
      badIntKindValueSchema = schema // {
        maxParallel = schema.maxParallel // {
          intKind = "positve";
        };
      };
      # localIssueReference is a real bool-typed member — decorating it with
      # emptyDisables (documented as string-knobs-only, lib/env-schema.nix
      # header) must be caught by emptyDisablesOnNonString.
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

  # lib/legacy-settings-section.nix must totally cover the schema's
  # flakeOption knobs (issue #2522): every such knob either has a row here or
  # is lib/env-schema.nix legacySettingsExempt = true;, and no row here
  # outlives its schema knob. Runs assertLegacySettingsSectionOk against the
  # real map/schema.
  legacy-settings-section-coverage =
    assert
      (assertLegacySettingsSectionOk { inherit legacySettingsSection schema; }) == legacySettingsSection;
    pkgs.runCommand "legacy-settings-section-coverage" { } "touch $out";

  # Regression guard (issue #2522): the coverage assert above must actually
  # detect all failure shapes -- a flakeOption knob left with no alias and
  # no exemption (missing), a legacySettingsSection row whose knob no longer
  # exists in the schema or lost flakeOption = true; (stale, in two
  # shapes -- key gone entirely, and key present but demoted), and a knob
  # marked legacySettingsExempt despite predating the freeze
  # (wronglyExempt) -- not just pass vacuously
  # because the real data already satisfies every invariant. Also proves the
  # exemption escape hatch itself still works for a knob that genuinely
  # postdates the freeze (exemptSkip). Runs assertLegacySettingsSectionOk --
  # the exact function legacy-settings-section-coverage calls -- against
  # independently mutated copies of the real map/schema, each exercising
  # exactly one invariant, via tryEval, so this fails if any assert is ever
  # dropped from assertLegacySettingsSectionOk (not just from
  # legacySettingsSectionIssues).
  legacy-settings-section-coverage-guard =
    let
      inherit (pkgs.lib) assertMsg;
      # filerModel is a real row for a real flakeOption knob that carries no
      # legacySettingsExempt -- dropping its row must be caught by missing.
      missingLegacySettingsSection = builtins.removeAttrs legacySettingsSection [ "filerModel" ];
      # A synthetic row naming a schema key that does not exist at all --
      # the shape a removed knob would leave behind -- must be caught by
      # stale.
      staleLegacySettingsSection = legacySettingsSection // {
        removedKnobNeverInSchema = "someSection";
      };
      # A schema entry whose flakeOption flag is turned off while its
      # legacySettingsSection row survives -- the shape a knob demoted out
      # of the flakeOption surface would leave behind. The schema key still
      # exists, so a stale predicate that only checks key existence would
      # miss this; it must also consult flakeOption. Must be caught by
      # stale.
      deadAliasSchema = schema // {
        branchPrefix = schema.branchPrefix // {
          flakeOption = false;
        };
      };
      # A synthetic flakeOption knob whose name appears in neither the real
      # schema nor preFreezeFlakeOptionNames -- a knob that genuinely
      # postdates the freeze, the case legacySettingsExempt exists for. No
      # map row, legacySettingsExempt = true; -- assertLegacySettingsSectionOk
      # must accept it (no missing, no wronglyExempt).
      exemptSkipSchema = schema // {
        syntheticPostFreezeKnob = {
          flakeOption = true;
          legacySettingsExempt = true;
        };
      };
      # mergeMode is a real pre-freeze knob (in preFreezeFlakeOptionNames)
      # that already has a real row in legacySettingsSection -- decorating
      # its schema entry with legacySettingsExempt = true; reproduces the
      # same wrongly-exempt-despite-predating-the-freeze mistake this closes
      # (the actual mergeMethod bug lacked a map row entirely; wronglyExempt
      # must fire regardless of whether a row exists, so this fixture keeps
      # mergeMode's real row to prove that) and must be caught by
      # wronglyExempt.
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
