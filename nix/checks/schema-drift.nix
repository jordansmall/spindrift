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
  ...
}:
let
  inherit (fixtures) harness;
  renderers = import ../../lib/renderers.nix;
  schema = import ../../lib/env-schema.nix;
  rosterDefaults =
    (import ../../lib/roster-schema-defaults.nix { inherit (pkgs) lib; }).rosterDefaults;
  defaultModelFixture = import ../../lib/default-model-fixture.nix;
  # Shared by roster-doc-efforts and roster-doc-efforts-guard so the doc's
  # expected effort string and its name list/format can't drift between the
  # check and its regression guard.
  wantEfforts = pkgs.lib.concatStringsSep "/" (
    map (n: "${n}=${rosterDefaults.${n}.effort}") [
      "scout"
      "reviewer"
      "filer"
      "worker"
    ]
  );

  # Shared by dogfood-doc-models-guard and dogfood-doc-models-guard-regression
  # so the Subagent roster section's hand-written restatement of scout/
  # reviewer/worker's schema-default models (the dogfood paragraph naming
  # only Filer as a local pin) can't drift between the check and its
  # regression guard (issue #2514 AC2).
  wantDogfoodModels = "`${defaultModelFixture.schemaDefaults.scoutModel}`, `${defaultModelFixture.schemaDefaults.reviewModel}` (issue #2433), and `${defaultModelFixture.schemaDefaults.workerModel}` respectively";

  # Shared by dogfood-doc-filer-pin-guard and
  # dogfood-doc-filer-pin-guard-regression so the Subagent roster section's
  # dogfood paragraph's hand-written Filer pin literal can't drift from
  # lib/default-model-fixture.nix's dogfoodPins.filer between the check and
  # its regression guard (issue #2514 AC2).
  wantDogfoodFilerPin = "filer = \"${defaultModelFixture.dogfoodPins.filer}\"";

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
    };

  # Throws via markerConsistencyIssues on a bad schema, else returns it
  # unchanged. Shared so marker-consistency-guard exercises this exact
  # assertion path (not just markerConsistencyIssues in isolation) — dropping
  # any one of the four asserts here would make that guard fail too, not
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
    schema;

  structuralPaths = import ../../lib/structural-paths.nix;
  resolveNixPath = import ../../lib/nixpath.nix;

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

  # Isolates the "#### Subagent roster" section of a docs/reference.md-shaped
  # doc string, else throws. Shared by assertRosterDocFlakePathOk and
  # assertRosterDocEffortsOk so the split -- needed because running the
  # regex-based `hasInfix`, i.e. `builtins.match ".*x.*"`, over the whole
  # ~190KB doc blows the evaluator's stack (issue #2436) -- is defined once,
  # not once per assertion.
  rosterDocSection =
    doc:
    let
      afterHeading = builtins.split "\n#### Subagent roster\n" doc;
    in
    if builtins.length afterHeading < 3 then
      throw "docs/reference.md: missing the \"#### Subagent roster\" heading"
    else
      builtins.elemAt (builtins.split "\n#+ " (builtins.elemAt afterHeading 2)) 0;

  # Isolates the dogfood paragraph (the one starting "spindrift's own dogfood
  # Consumer config") out of the wider Subagent roster section, else throws.
  # rosterDocSection's own section is too wide a scope for the dogfood
  # restatement guards below: the section also holds an unrelated
  # `defaultRoster` syntax example (issue #2426, docs/reference.md's "filer =
  # ..." line a few paragraphs above) that can incidentally restate the same
  # literal, which would let a `hasInfix` check against the whole section
  # pass vacuously even though the dogfood paragraph itself has drifted
  # (issue #2514 review). Shared by assertDogfoodDocModelsOk and
  # assertDogfoodDocFilerPinOk so both guards check this same narrow scope.
  dogfoodParagraph =
    doc:
    let
      startMarker = "spindrift's own dogfood Consumer config";
      afterStart = builtins.split startMarker (rosterDocSection doc);
    in
    if builtins.length afterStart < 3 then
      throw "docs/reference.md: missing the dogfood paragraph (\"${startMarker}\") inside \"#### Subagent roster\""
    else
      startMarker + builtins.elemAt (builtins.split "\n\n" (builtins.elemAt afterStart 2)) 0;

  # Asserts the isolated Subagent roster section states the given flake path,
  # else throws. Factored out (like assertSchemaChoicesOk/assertNixPathsOk
  # above) so roster-doc-flake-path-guard can exercise this exact assertion
  # path against a synthetic doc, not only the real docs/reference.md
  # content — dropping the hasInfix assert here would make that guard fail
  # too, not stay silently green.
  assertRosterDocFlakePathOk =
    { doc, wantPath }:
    let
      inherit (pkgs.lib) assertMsg hasInfix;
    in
    assert assertMsg (hasInfix wantPath (rosterDocSection doc))
      "docs/reference.md: Subagent roster section must state roster's flake path as `${wantPath}` (derived from lib/structural-paths.nix's roster entry) — it has drifted from the registry, update the doc (issue #2436)";
    doc;

  # Asserts the isolated Subagent roster section restates rosterDefaults'
  # effort literals verbatim, else throws (issue #2506). Factored out the
  # same way assertRosterDocFlakePathOk is, so roster-doc-efforts-guard can
  # exercise this exact assertion path against a synthetic doc.
  assertRosterDocEffortsOk =
    { doc, wantEfforts }:
    let
      inherit (pkgs.lib) assertMsg hasInfix;
    in
    assert assertMsg (hasInfix wantEfforts (rosterDocSection doc))
      "docs/reference.md: Subagent roster section must restate lib/roster-schema-defaults.nix's rosterDefaults effort literals as `${wantEfforts}` — it has drifted from the table, update the doc (issue #2506)";
    doc;

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

  # Asserts the isolated Subagent roster section restates
  # lib/default-model-fixture.nix's schemaDefaults scout/reviewer/worker
  # model literals verbatim, else throws (issue #2514 AC2). The dogfood
  # paragraph inside that section hand-restates those three as prose (Filer
  # is a local dogfoodPins pin, stated separately, and out of scope here).
  # Factored out the same way assertRosterDocEffortsOk is, so
  # dogfood-doc-models-guard-regression can exercise this exact assertion
  # path against a synthetic doc.
  assertDogfoodDocModelsOk =
    { doc, wantModels }:
    let
      inherit (pkgs.lib) assertMsg hasInfix;
    in
    assert assertMsg (hasInfix wantModels (dogfoodParagraph doc))
      "docs/reference.md: Subagent roster section's dogfood paragraph must restate lib/default-model-fixture.nix's schemaDefaults scout/reviewer/worker model literals as `${wantModels}` — it has drifted from the fixture, update the doc (issue #2514)";
    doc;

  # Asserts the isolated Subagent roster section's dogfood paragraph restates
  # lib/default-model-fixture.nix's dogfoodPins.filer literal verbatim, else
  # throws (issue #2514 AC2). The dogfood paragraph's opening sentence
  # (`it sets roster = rosterLib.defaultRoster { models = { filer = "..."; };
  # }`) hand-types this pin ahead of the scout/reviewer/worker restatement
  # assertDogfoodDocModelsOk already guards. Factored out the same way
  # assertDogfoodDocModelsOk is, so dogfood-doc-filer-pin-guard-regression can
  # exercise this exact assertion path against a synthetic doc.
  assertDogfoodDocFilerPinOk =
    { doc, wantFilerPin }:
    let
      inherit (pkgs.lib) assertMsg hasInfix;
    in
    assert assertMsg (hasInfix wantFilerPin (dogfoodParagraph doc))
      "docs/reference.md: Subagent roster section's dogfood paragraph must restate lib/default-model-fixture.nix's dogfoodPins.filer literal as `${wantFilerPin}` — it has drifted from the fixture, update the doc (issue #2514)";
    doc;

  # Asserts docSrc's generated Default models table (between its BEGIN/END
  # GENERATED DEFAULT MODELS markers) matches generated, else throws (issue
  # #2514 AC2). Factored out (like assertRosterDocFlakePathOk above) so
  # default-models-doc-guard can exercise this exact marker-split + equality
  # assertion path against a synthetic doc, not only the real
  # docs/reference.md content — dropping the equality assert here would make
  # that guard fail too, not stay silently green.
  assertDefaultModelsDocOk =
    { docSrc, generated }:
    let
      inherit (pkgs.lib) assertMsg;
      beginMarker = "<!-- BEGIN GENERATED DEFAULT MODELS -- nix run .#regen -- DO NOT EDIT -->\n";
      endMarker = "<!-- END GENERATED DEFAULT MODELS -->";
      afterBegin =
        let
          parts = builtins.split beginMarker docSrc;
        in
        if builtins.length parts >= 3 then
          builtins.elemAt parts 2
        else
          throw "docs/reference.md: BEGIN GENERATED DEFAULT MODELS marker not found";
      committed =
        let
          parts = builtins.split endMarker afterBegin;
        in
        if builtins.length parts >= 3 then
          builtins.elemAt parts 0
        else
          throw "docs/reference.md: END GENERATED DEFAULT MODELS marker not found";
    in
    assert assertMsg (committed == generated) ''
      docs/reference.md generated Default models table is out of sync with lib/default-model-fixture.nix — regenerate it with `nix run .#regen`
        got:  ${committed}
        want: ${generated}'';
    docSrc;

  # Shared by assertSettingsExampleModelsDocOk/assertSettingsExampleLabelsDocOk/
  # assertSettingsExampleConfigDocOk: each settings-example sub-block lives
  # between its own BEGIN/END marker pair inside the doc's illustrative
  # `settings = { ... }` example and is checked the same way -- split docSrc
  # on the markers, compare the committed slice against generated, else
  # throw a message naming which sub-block (blockName) and which schema file
  # (sourceDesc) it drifted from. Parameterized so the three callers below
  # stay one-liners instead of ~28-line copies of the same marker-split +
  # equality assertion (issue #2537).
  assertMarkedBlockOk =
    {
      blockName,
      sourceDesc,
      beginMarker,
      endMarker,
      docSrc,
      generated,
    }:
    let
      inherit (pkgs.lib) assertMsg;
      afterBegin =
        let
          parts = builtins.split beginMarker docSrc;
        in
        if builtins.length parts >= 3 then
          builtins.elemAt parts 2
        else
          throw "docs/reference.md: BEGIN GENERATED ${blockName} marker not found";
      committed =
        let
          parts = builtins.split endMarker afterBegin;
        in
        if builtins.length parts >= 3 then
          builtins.elemAt parts 0
        else
          throw "docs/reference.md: END GENERATED ${blockName} marker not found";
    in
    assert assertMsg (committed == generated) ''
      docs/reference.md generated ${blockName} sub-block is out of sync with ${sourceDesc} — regenerate it with `nix run .#regen`
        got:  ${committed}
        want: ${generated}'';
    docSrc;

  # Asserts docSrc's generated settings example `models` sub-block (between
  # its BEGIN/END GENERATED SETTINGS EXAMPLE MODELS markers) matches
  # generated, else throws (issue #2514 AC1). Factored out the same way
  # assertDefaultModelsDocOk is, so settings-example-models-doc-guard can
  # exercise this exact marker-split + equality assertion path against a
  # synthetic doc, not only the real docs/reference.md content.
  assertSettingsExampleModelsDocOk =
    { docSrc, generated }:
    assertMarkedBlockOk {
      blockName = "SETTINGS EXAMPLE MODELS";
      sourceDesc = "lib/default-model-fixture.nix";
      beginMarker = "  # BEGIN GENERATED SETTINGS EXAMPLE MODELS -- nix run .#regen -- DO NOT EDIT\n";
      endMarker = "  # END GENERATED SETTINGS EXAMPLE MODELS";
      inherit docSrc generated;
    };

  # Asserts docSrc's generated settings example `issueDiscovery`/
  # `lifecycleLabels` sub-block (between its BEGIN/END GENERATED SETTINGS
  # EXAMPLE LABELS markers) matches generated, else throws (issue #2537): the
  # example's label/inProgressLabel/failedLabel/completeLabel literals
  # restate lib/env-schema.nix's schema.label.default,
  # schema.inProgressLabel.default, schema.failedLabel.default, and
  # schema.completeLabel.default verbatim, so a schema-default bump for any
  # of those four triage-label knobs must not be able to leave this
  # illustrative example stale with no drift check. Factored out onto the
  # shared assertMarkedBlockOk above, so settings-example-labels-doc-guard
  # can exercise this exact marker-split + equality assertion path against a
  # synthetic doc, not only the real docs/reference.md content.
  assertSettingsExampleLabelsDocOk =
    { docSrc, generated }:
    assertMarkedBlockOk {
      blockName = "SETTINGS EXAMPLE LABELS";
      sourceDesc = "lib/env-schema.nix";
      beginMarker = "  # BEGIN GENERATED SETTINGS EXAMPLE LABELS -- nix run .#regen -- DO NOT EDIT\n";
      endMarker = "  # END GENERATED SETTINGS EXAMPLE LABELS";
      inherit docSrc generated;
    };

  # Asserts docSrc's generated settings example `branches`/`concurrency`
  # sub-block (between its BEGIN/END GENERATED SETTINGS EXAMPLE CONFIG
  # markers) matches generated, else throws (issue #2537): the example's
  # baseBranch/branchPrefix/mergeMode/mergeGuardPaths/mergePollInterval/
  # mergePollTimeout/maxParallel/maxJobs literals restate lib/env-schema.nix's
  # schema.baseBranch.default, schema.branchPrefix.default,
  # schema.mergeMode.default, schema.mergeGuardPaths.default,
  # schema.mergePollInterval.default, schema.mergePollTimeout.default,
  # schema.maxParallel.default, and schema.maxJobs.default verbatim, so a
  # schema-default bump for any of those eight branch/merge/concurrency knobs
  # must not be able to leave this illustrative example stale with no drift
  # check. Factored out onto the shared assertMarkedBlockOk above, so
  # settings-example-config-doc-guard can exercise this exact marker-split +
  # equality assertion path against a synthetic doc, not only the real
  # docs/reference.md content.
  assertSettingsExampleConfigDocOk =
    { docSrc, generated }:
    assertMarkedBlockOk {
      blockName = "SETTINGS EXAMPLE CONFIG";
      sourceDesc = "lib/env-schema.nix";
      beginMarker = "  # BEGIN GENERATED SETTINGS EXAMPLE CONFIG -- nix run .#regen -- DO NOT EDIT\n";
      endMarker = "  # END GENERATED SETTINGS EXAMPLE CONFIG";
      inherit docSrc generated;
    };
in
{
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

  # cmd/launcher/quickstart/quickstart_runtime_gen.go must match the content
  # generated from lib/runtime-values.nix. Fails when the runtime enum
  # changes but the committed generated file isn't regenerated. Shares its
  # renderer with `nix run .#regen` via lib/renderers.nix.
  quickstart-runtime-gen =
    let
      runtimeValues = import ../../lib/runtime-values.nix;
      generated = pkgs.writeText "quickstart_runtime_gen.go.generated" (
        renderers.renderQuickstartRuntimeGo runtimeValues
      );
    in
    pkgs.runCommand "quickstart-runtime-gen"
      {
        inherit generated;
        committed = ../../cmd/launcher/quickstart/quickstart_runtime_gen.go;
      }
      ''
        diff "$generated" "$committed" \
          || { echo "cmd/launcher/quickstart/quickstart_runtime_gen.go is out of sync with lib/runtime-values.nix — regenerate it with \`nix run .#regen\`" >&2; exit 1; }
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
  # the exact value set for all seven choice-knobs the issue names by name —
  # mergeMode, codeForge, issueTracker, overlapGate, mergeMethod, syncMethod,
  # boxForgeAndIssueAccess — so a typo or dropped value fails here instead of
  # silently narrowing/widening what `spindrift --merge-mode <TAB>` etc. offer.
  # Also asserts the *set* of choices-bearing knob names itself (issue #2519)
  # — the seven per-knob asserts below only fire for a knob already listed
  # here by name, so an added eighth knob declaring `choices` would otherwise
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
    pkgs.runCommand "schema-choices" { } "touch $out";

  # Regression guard (issue #2519): the choices-bearing knob-set assertion
  # above must actually detect an added/renamed choices-bearing knob, not
  # just pass vacuously because the real schema currently has exactly the
  # seven pinned names. Injects an eighth synthetic knob declaring `choices`
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
      ];
      result = builtins.tryEval (
        assert choiceKnobNames == expectedChoiceKnobNames;
        true
      );
    in
    assert assertMsg (!result.success)
      "schema-choices-knobset-guard: expected the choices-bearing knob-set assertion to reject a schema with an injected eighth choices knob (extraChoiceKnob), but it evaluated successfully";
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

  # docs/flake-options.md must match the reference generated from env-schema.nix.
  # Fails when a flakeOption knob is added or removed but the committed file is
  # not regenerated (same treatment as harness.env.example and flagtable_gen.go).
  # Shares its renderer with `nix run .#regen` via lib/renderers.nix.
  flake-options-doc =
    let
      schema = import ../../lib/env-schema.nix;
      generated = pkgs.writeText "flake-options.md.generated" (renderers.renderFlakeOptionsDoc schema);
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

  # The generated settings block between templates/default/flake.nix's
  # BEGIN/END GENERATED SETTINGS EXAMPLE markers must match the content
  # rendered from env-schema.nix — every flakeOption knob, exhaustively,
  # with its doc string (issue #520). Shares its renderer with
  # `nix run .#regen` via lib/renderers.nix, so guard and regenerator cannot
  # drift from each other (issue #402).
  template-settings-block =
    let
      schema = import ../../lib/env-schema.nix;
      inherit (pkgs.lib) assertMsg;
      generated = renderers.renderTemplateSettingsBlock schema;
      templateSrc = builtins.readFile ../../templates/default/flake.nix;
      beginMarker = "BEGIN GENERATED SETTINGS EXAMPLE -- nix run .#regen -- DO NOT EDIT\n";
      endMarker = "            # END GENERATED SETTINGS EXAMPLE";
      afterBegin =
        let
          parts = builtins.split beginMarker templateSrc;
        in
        if builtins.length parts >= 3 then
          builtins.elemAt parts 2
        else
          throw "templates/default/flake.nix: BEGIN GENERATED SETTINGS EXAMPLE marker not found";
      committed =
        let
          parts = builtins.split endMarker afterBegin;
        in
        if builtins.length parts >= 3 then
          builtins.elemAt parts 0
        else
          throw "templates/default/flake.nix: END GENERATED SETTINGS EXAMPLE marker not found";
    in
    assert assertMsg (committed == generated) ''
      templates/default/flake.nix generated settings block is out of sync with lib/env-schema.nix — regenerate it with `nix run .#regen`
        got:  ${committed}
        want: ${generated}'';
    pkgs.runCommand "template-settings-block" { } "touch $out";

  # The generated status-word span between agent/entrypoint.sh's BEGIN/END
  # GENERATED OUTCOME STATUS WORDS markers must match the content rendered
  # from lib/prompt-contract.nix's outcomeStatusSets (issue #2504). Shares
  # its renderers with `nix run .#regen` via lib/renderers.nix.
  outcome-status-words =
    let
      promptContract = import ../../lib/prompt-contract.nix;
      inherit (pkgs.lib) assertMsg;
      researchStatusPipe = renderers.renderOutcomeStatusPipe (
        builtins.filter (s: s != "blocked") (promptContract.outcomeStatusesFor "research")
      );
      generated =
        "# shellcheck disable=SC2034 # consumed by _subst's envsubst allowlist, wired in a later slice (issue #2504)\n"
        + "RESEARCH_STATUS_ENUM=\"${researchStatusPipe}\"\n";
      entrypointSrc = builtins.readFile ../../agent/entrypoint.sh;
      beginMarker = "# BEGIN GENERATED OUTCOME STATUS WORDS -- nix run .#regen -- DO NOT EDIT\n";
      endMarker = "# END GENERATED OUTCOME STATUS WORDS";
      afterBegin =
        let
          parts = builtins.split beginMarker entrypointSrc;
        in
        if builtins.length parts >= 3 then
          builtins.elemAt parts 2
        else
          throw "agent/entrypoint.sh: BEGIN GENERATED OUTCOME STATUS WORDS marker not found";
      committed =
        let
          parts = builtins.split endMarker afterBegin;
        in
        if builtins.length parts >= 3 then
          builtins.elemAt parts 0
        else
          throw "agent/entrypoint.sh: END GENERATED OUTCOME STATUS WORDS marker not found";
    in
    assert assertMsg (committed == generated) ''
      agent/entrypoint.sh generated outcome-status-words block is out of sync with lib/prompt-contract.nix — regenerate it with `nix run .#regen`
        got:  ${committed}
        want: ${generated}'';
    pkgs.runCommand "outcome-status-words" { } "touch $out";

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
        attrValues
        filter
        concatStringsSep
        ;
      # Fold the structural domain-tree paths into the same disjointness set:
      # both flakeOption leaves and structural leaves are merged into one tree
      # by buildTree at flake eval (lib/flakeModule.nix), so a cross-set prefix
      # collision is just as fatal — catch it here as a clear error instead of
      # an opaque buildTree throw (issue #2184).
      flakeOptionNames = filter (n: schema.${n}.flakeOption or false) (attrNames schema);
      missingGroup = filter (
        n:
        let
          e = schema.${n};
        in
        !(e ? group) || !builtins.isString e.group || e.group == ""
      ) flakeOptionNames;
      allNixPaths =
        (map (n: resolveNixPath n schema.${n}) flakeOptionNames)
        ++ (map (segs: concatStringsSep "." segs) (attrValues structuralPaths));
    in
    assert assertMsg (missingGroup == [ ])
      "lib/env-schema.nix: every flakeOption knob must declare a non-empty group (ADR 0037 Pass 2): ${concatStringsSep ", " missingGroup}";
    assert (assertNixPathsOk allNixPaths) == allNixPaths;
    pkgs.runCommand "flake-nixpath-exhaustive-disjoint" { } "touch $out";

  # docs/reference.md's Subagent roster section states roster's flake path
  # as literal prose (`perSystem.spindrift.agents.models.roster`); this pins
  # that string to lib/structural-paths.nix's actual `roster` entry instead
  # of letting the two drift silently — a rename of any of roster's path
  # segments in the registry fails this check instead of leaving the doc
  # wrong (issue #2436). Isolates the "#### Subagent roster" section before
  # running the regex-based `hasInfix` (builtins.match ".*x.*") — running
  # that over the whole ~190KB doc blows the evaluator's stack.
  roster-doc-flake-path =
    let
      inherit (pkgs.lib) concatStringsSep;
      wantPath = "perSystem.spindrift.${concatStringsSep "." structuralPaths.roster}";
      doc = builtins.readFile ../../docs/reference.md;
    in
    assert (assertRosterDocFlakePathOk { inherit doc wantPath; }) == doc;
    pkgs.runCommand "roster-doc-flake-path" { } "touch $out";

  # Regression guard (issue #2436): the doc-drift assertion above must
  # actually detect a wrong flake path, not just pass vacuously because
  # docs/reference.md's Subagent roster section currently agrees with the
  # registry. Runs assertRosterDocFlakePathOk — the exact function
  # roster-doc-flake-path calls — against a synthetic doc whose Subagent
  # roster section states the real wantPath with one segment renamed
  # ("models" -> "model", a plausible drift a registry rename could leave
  # behind), via tryEval, so this fails if the hasInfix assert is ever
  # dropped from assertRosterDocFlakePathOk. The renamed segment sits before
  # "roster", not appended after it, so the drifted string can never
  # accidentally contain wantPath as a substring (which a suffix-only change
  # like "roster" -> "rosterOld" would).
  roster-doc-flake-path-guard =
    let
      inherit (pkgs.lib) assertMsg concatStringsSep replaceStrings;
      wantPath = "perSystem.spindrift.${concatStringsSep "." structuralPaths.roster}";
      driftedPath = replaceStrings [ "models" ] [ "model" ] wantPath;
      badDoc = ''
        intro text

        #### Subagent roster

        The roster's flake path is `${driftedPath}`.

        #### Next heading
      '';
      result = builtins.tryEval (assertRosterDocFlakePathOk {
        doc = badDoc;
        inherit wantPath;
      });
    in
    assert assertMsg (!result.success)
      "roster-doc-flake-path-guard: expected assertRosterDocFlakePathOk to reject a synthetic doc whose Subagent roster section states a wrong flake path, but it evaluated successfully";
    pkgs.runCommand "roster-doc-flake-path-guard" { } "touch $out";

  # docs/reference.md's Subagent roster section restates
  # lib/roster-schema-defaults.nix's rosterDefaults effort values as prose
  # (name=effort per agent, slash-separated); this pins that string to
  # rosterDefaults' actual effort values instead of letting the two drift
  # silently (issue #2506), the same way roster-doc-flake-path pins the
  # section's flake-path prose to lib/structural-paths.nix.
  roster-doc-efforts =
    let
      doc = builtins.readFile ../../docs/reference.md;
    in
    assert (assertRosterDocEffortsOk { inherit doc wantEfforts; }) == doc;
    pkgs.runCommand "roster-doc-efforts" { } "touch $out";

  # Regression guard (issue #2506): the doc-drift assertion above must
  # actually detect a wrong effort restatement, not just pass vacuously
  # because docs/reference.md's Subagent roster section currently agrees
  # with rosterDefaults. Runs assertRosterDocEffortsOk — the exact function
  # roster-doc-efforts calls — against a synthetic doc whose Subagent roster
  # section states the real wantEfforts with one effort flipped
  # ("high" -> "medium" on reviewer, a plausible drift a rosterDefaults edit
  # could leave behind), via tryEval, so this fails if the hasInfix assert is
  # ever dropped from assertRosterDocEffortsOk.
  roster-doc-efforts-guard =
    let
      inherit (pkgs.lib) assertMsg replaceStrings;
      reviewerEffort = rosterDefaults.reviewer.effort;
      driftedReviewerEffort = if reviewerEffort == "high" then "medium" else "high";
      driftedEfforts =
        replaceStrings
          [ "reviewer=${reviewerEffort}" ]
          [
            "reviewer=${driftedReviewerEffort}"
          ]
          wantEfforts;
      badDoc = ''
        intro text

        #### Subagent roster

        The default efforts are `${driftedEfforts}`.

        #### Next heading
      '';
      result = builtins.tryEval (assertRosterDocEffortsOk {
        doc = badDoc;
        wantEfforts = wantEfforts;
      });
    in
    assert assertMsg (!result.success)
      "roster-doc-efforts-guard: expected assertRosterDocEffortsOk to reject a synthetic doc whose Subagent roster section states a wrong effort restatement, but it evaluated successfully";
    pkgs.runCommand "roster-doc-efforts-guard" { } "touch $out";

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

  # The generated Default models table between docs/reference.md's BEGIN/END
  # GENERATED DEFAULT MODELS markers must match the content rendered from
  # lib/default-model-fixture.nix (issue #2514 AC2) — one row per
  # schemaDefaults leaf, exhaustively. Shares its renderer with
  # `nix run .#regen` via lib/renderers.nix, so guard and regenerator cannot
  # drift from each other (issue #402).
  default-models-doc =
    let
      generated = renderers.renderDefaultModelsDoc defaultModelFixture;
      docSrc = builtins.readFile ../../docs/reference.md;
    in
    assert (assertDefaultModelsDocOk { inherit docSrc generated; }) == docSrc;
    pkgs.runCommand "default-models-doc" { } "touch $out";

  # Regression guard (issue #2514 AC2): the doc-drift assertion above must
  # actually detect a drifted generated Default models table, not just pass
  # vacuously because docs/reference.md's table currently agrees with
  # lib/default-model-fixture.nix. Runs assertDefaultModelsDocOk — the exact
  # function default-models-doc calls — against a synthetic doc whose table
  # row for `worker` states a wrong model literal (a plausible drift a
  # fixture edit could leave behind), via tryEval, so this fails if the
  # equality assert is ever dropped from assertDefaultModelsDocOk.
  default-models-doc-guard =
    let
      inherit (pkgs.lib) assertMsg replaceStrings;
      generated = renderers.renderDefaultModelsDoc defaultModelFixture;
      beginMarker = "<!-- BEGIN GENERATED DEFAULT MODELS -- nix run .#regen -- DO NOT EDIT -->\n";
      endMarker = "<!-- END GENERATED DEFAULT MODELS -->";
      workerModel = defaultModelFixture.schemaDefaults.workerModel;
      driftedWorkerModel =
        if workerModel == "claude-sonnet-5" then "claude-sonnet-6" else "claude-sonnet-5";
      driftedGenerated = replaceStrings [ "`${workerModel}`" ] [ "`${driftedWorkerModel}`" ] generated;
      driftedDocSrc = beginMarker + driftedGenerated + endMarker + "\n";
      result = builtins.tryEval (assertDefaultModelsDocOk {
        docSrc = driftedDocSrc;
        inherit generated;
      });
    in
    assert assertMsg (!result.success)
      "default-models-doc-guard: expected assertDefaultModelsDocOk to reject a synthetic doc whose generated Default models table has drifted, but it evaluated successfully";
    pkgs.runCommand "default-models-doc-guard" { } "touch $out";

  # The generated `models` sub-block of docs/reference.md's illustrative
  # `settings = { ... }` example (between its BEGIN/END GENERATED SETTINGS
  # EXAMPLE MODELS markers) must match the content rendered from
  # lib/default-model-fixture.nix (issue #2514 AC1): a schema-default bump
  # must not be able to leave this example's hand-typed model/scoutModel/
  # reviewModel/filerModel literals stale with no drift check, the exact
  # failure mode this check closes. Shares its renderer with `nix run
  # .#regen` via lib/renderers.nix, so guard and regenerator cannot drift
  # from each other (issue #402). Mirrors default-models-doc above.
  settings-example-models-doc =
    let
      generated = renderers.renderSettingsExampleModelsDoc defaultModelFixture;
      docSrc = builtins.readFile ../../docs/reference.md;
    in
    assert (assertSettingsExampleModelsDocOk { inherit docSrc generated; }) == docSrc;
    pkgs.runCommand "settings-example-models-doc" { } "touch $out";

  # Regression guard for settings-example-models-doc above: proves its
  # equality assertion actually rejects a drifted value instead of passing
  # vacuously (mirrors marker-consistency-guard's tryEval pattern). Runs
  # assertSettingsExampleModelsDocOk — the exact function
  # settings-example-models-doc calls — against a synthetic doc whose models
  # sub-block carries wrong model literals, via tryEval, so this fails if the
  # equality assert is ever dropped from assertSettingsExampleModelsDocOk.
  settings-example-models-doc-guard =
    let
      inherit (pkgs.lib) assertMsg;
      generated = renderers.renderSettingsExampleModelsDoc defaultModelFixture;
      beginMarker = "  # BEGIN GENERATED SETTINGS EXAMPLE MODELS -- nix run .#regen -- DO NOT EDIT\n";
      endMarker = "  # END GENERATED SETTINGS EXAMPLE MODELS";
      driftedBlock = ''
        models          = { model = "wrong-model";
                            scoutModel  = "wrong-scout-model";
                            reviewModel = "wrong-review-model";
                            filerModel  = "wrong-filer-model"; };
      '';
      driftedDocSrc = beginMarker + driftedBlock + endMarker + "\n";
      result = builtins.tryEval (assertSettingsExampleModelsDocOk {
        docSrc = driftedDocSrc;
        inherit generated;
      });
    in
    assert assertMsg (!result.success)
      "settings-example-models-doc-guard: expected assertSettingsExampleModelsDocOk to reject a synthetic doc whose models sub-block has drifted, but it evaluated successfully";
    pkgs.runCommand "settings-example-models-doc-guard" { } "touch $out";

  # The generated `issueDiscovery`/`lifecycleLabels` sub-block of docs/
  # reference.md's illustrative `settings = { ... }` example (between its
  # BEGIN/END GENERATED SETTINGS EXAMPLE LABELS markers) must match the
  # content rendered from lib/env-schema.nix (issue #2537): a schema-default
  # bump to schema.label.default, schema.inProgressLabel.default,
  # schema.failedLabel.default, or schema.completeLabel.default must not be
  # able to leave this example's hand-typed label/inProgressLabel/
  # failedLabel/completeLabel literals stale with no drift check, the exact
  # failure mode this check closes. Shares its renderer with `nix run
  # .#regen` via lib/renderers.nix, so guard and regenerator cannot drift
  # from each other (issue #402). Mirrors settings-example-models-doc above.
  settings-example-labels-doc =
    let
      generated = renderers.renderSettingsExampleLabelsDoc schema;
      docSrc = builtins.readFile ../../docs/reference.md;
    in
    assert (assertSettingsExampleLabelsDocOk { inherit docSrc generated; }) == docSrc;
    pkgs.runCommand "settings-example-labels-doc" { } "touch $out";

  # Regression guard for settings-example-labels-doc above: proves its
  # equality assertion actually rejects a drifted value instead of passing
  # vacuously (mirrors marker-consistency-guard's tryEval pattern). Runs
  # assertSettingsExampleLabelsDocOk — the exact function
  # settings-example-labels-doc calls — against a synthetic doc whose labels
  # sub-block carries a wrong label literal, via tryEval, so this fails if
  # the equality assert is ever dropped from assertSettingsExampleLabelsDocOk.
  settings-example-labels-doc-guard =
    let
      inherit (pkgs.lib) assertMsg;
      generated = renderers.renderSettingsExampleLabelsDoc schema;
      beginMarker = "  # BEGIN GENERATED SETTINGS EXAMPLE LABELS -- nix run .#regen -- DO NOT EDIT\n";
      endMarker = "  # END GENERATED SETTINGS EXAMPLE LABELS";
      # Built via string concatenation, not a ''-string: a ''-string strips
      # the block's common leading indentation (down to 0 here), which would
      # make committed's indentation itself mismatch generated's real
      # 2-space-indented output regardless of the wrong-* literals below --
      # proving only that the assert rejects a whitespace difference, not
      # the drifted label value it's meant to catch.
      driftedBlock =
        "  issueDiscovery  = { label          = \"wrong-label\"; };\n"
        + "  lifecycleLabels = { inProgressLabel = \"wrong-in-progress-label\";\n"
        + "                      failedLabel     = \"wrong-failed-label\";\n"
        + "                      completeLabel   = \"wrong-complete-label\"; };\n";
      driftedDocSrc = beginMarker + driftedBlock + endMarker + "\n";
      result = builtins.tryEval (assertSettingsExampleLabelsDocOk {
        docSrc = driftedDocSrc;
        inherit generated;
      });
    in
    assert assertMsg (!result.success)
      "settings-example-labels-doc-guard: expected assertSettingsExampleLabelsDocOk to reject a synthetic doc whose labels sub-block has drifted, but it evaluated successfully";
    pkgs.runCommand "settings-example-labels-doc-guard" { } "touch $out";

  # The generated `branches`/`concurrency` sub-block of docs/reference.md's
  # illustrative `settings = { ... }` example (between its BEGIN/END
  # GENERATED SETTINGS EXAMPLE CONFIG markers) must match the content
  # rendered from lib/env-schema.nix (issue #2537): a schema-default bump to
  # schema.baseBranch.default, schema.branchPrefix.default,
  # schema.mergeMode.default, schema.mergeGuardPaths.default,
  # schema.mergePollInterval.default, schema.mergePollTimeout.default,
  # schema.maxParallel.default, or schema.maxJobs.default must not be able to
  # leave this example's hand-typed baseBranch/branchPrefix/mergeMode/
  # mergeGuardPaths/mergePollInterval/mergePollTimeout/maxParallel/maxJobs
  # literals stale with no drift check, the exact failure mode this check
  # closes. Shares its renderer with `nix run .#regen` via
  # lib/renderers.nix, so guard and regenerator cannot drift from each other
  # (issue #402). Mirrors settings-example-models-doc above.
  settings-example-config-doc =
    let
      generated = renderers.renderSettingsExampleConfigDoc schema;
      docSrc = builtins.readFile ../../docs/reference.md;
    in
    assert (assertSettingsExampleConfigDocOk { inherit docSrc generated; }) == docSrc;
    pkgs.runCommand "settings-example-config-doc" { } "touch $out";

  # Regression guard for settings-example-config-doc above: proves its
  # equality assertion actually rejects a drifted value instead of passing
  # vacuously (mirrors marker-consistency-guard's tryEval pattern). Runs
  # assertSettingsExampleConfigDocOk — the exact function
  # settings-example-config-doc calls — against a synthetic doc whose
  # branches/concurrency sub-block carries a wrong baseBranch literal, via
  # tryEval, so this fails if the equality assert is ever dropped from
  # assertSettingsExampleConfigDocOk.
  settings-example-config-doc-guard =
    let
      inherit (pkgs.lib) assertMsg;
      generated = renderers.renderSettingsExampleConfigDoc schema;
      beginMarker = "  # BEGIN GENERATED SETTINGS EXAMPLE CONFIG -- nix run .#regen -- DO NOT EDIT\n";
      endMarker = "  # END GENERATED SETTINGS EXAMPLE CONFIG";
      # Built via string concatenation, not a ''-string: a ''-string strips
      # the block's common leading indentation (down to 0 here), which would
      # make committed's indentation itself mismatch generated's real
      # 2-space-indented output regardless of the wrong-base-branch literal
      # below -- proving only that the assert rejects a whitespace
      # difference, not the drifted config value it's meant to catch.
      driftedBlock =
        "  branches        = { baseBranch = \"wrong-base-branch\"; branchPrefix = \"agent/issue-\";\n"
        + "                      mergeMode  = \"manual\";\n"
        + "                      mergeGuardPaths = \".github/**,.forgejo/**,**/CLAUDE.md,**/AGENTS.md,.claude/**,.opencode/**\";\n"
        + "                      mergePollInterval = 30; mergePollTimeout = 1800; };\n"
        + "  concurrency     = { maxParallel = 3; maxJobs = 0; };\n";
      driftedDocSrc = beginMarker + driftedBlock + endMarker + "\n";
      result = builtins.tryEval (assertSettingsExampleConfigDocOk {
        docSrc = driftedDocSrc;
        inherit generated;
      });
    in
    assert assertMsg (!result.success)
      "settings-example-config-doc-guard: expected assertSettingsExampleConfigDocOk to reject a synthetic doc whose branches/concurrency sub-block has drifted, but it evaluated successfully";
    pkgs.runCommand "settings-example-config-doc-guard" { } "touch $out";

  # docs/reference.md's Subagent roster section's dogfood paragraph restates
  # lib/default-model-fixture.nix's schemaDefaults scout/reviewer/worker
  # model literals as prose (Filer is a separate, hand-stated local pin); this
  # pins that string to the fixture's actual values instead of letting the
  # two drift silently (issue #2514 AC2), the same way roster-doc-efforts
  # pins the section's effort prose to lib/roster-schema-defaults.nix.
  dogfood-doc-models-guard =
    let
      doc = builtins.readFile ../../docs/reference.md;
    in
    assert
      (assertDogfoodDocModelsOk {
        inherit doc;
        wantModels = wantDogfoodModels;
      }) == doc;
    pkgs.runCommand "dogfood-doc-models-guard" { } "touch $out";

  # Regression guard (issue #2514 AC2): the doc-drift assertion above must
  # actually detect a wrong model restatement, not just pass vacuously
  # because docs/reference.md's Subagent roster section currently agrees
  # with the fixture. Runs assertDogfoodDocModelsOk — the exact function
  # dogfood-doc-models-guard calls — against a synthetic doc whose dogfood
  # paragraph states the real wantDogfoodModels with the worker model
  # literal flipped (a plausible drift a fixture edit could leave behind),
  # via tryEval, so this fails if the hasInfix assert is ever dropped from
  # assertDogfoodDocModelsOk.
  dogfood-doc-models-guard-regression =
    let
      inherit (pkgs.lib) assertMsg replaceStrings;
      workerModel = defaultModelFixture.schemaDefaults.workerModel;
      driftedWorkerModel =
        if workerModel == "claude-sonnet-5" then "claude-sonnet-6" else "claude-sonnet-5";
      driftedModels =
        replaceStrings [ "`${workerModel}`" ] [ "`${driftedWorkerModel}`" ]
          wantDogfoodModels;
      badDoc = ''
        intro text

        #### Subagent roster

        spindrift's own dogfood Consumer config: Scout, reviewer, and worker inherit their defaults instead: ${driftedModels}.

        #### Next heading
      '';
      result = builtins.tryEval (assertDogfoodDocModelsOk {
        doc = badDoc;
        wantModels = wantDogfoodModels;
      });
    in
    assert assertMsg (!result.success)
      "dogfood-doc-models-guard-regression: expected assertDogfoodDocModelsOk to reject a synthetic doc whose dogfood paragraph states a wrong model restatement, but it evaluated successfully";
    pkgs.runCommand "dogfood-doc-models-guard-regression" { } "touch $out";

  # docs/reference.md's Subagent roster section's dogfood paragraph's opening
  # sentence hand-restates lib/default-model-fixture.nix's dogfoodPins.filer
  # literal as prose (`it sets roster = rosterLib.defaultRoster { models = {
  # filer = "..."; }; }`); this pins that string to the fixture's actual
  # value instead of letting the two drift silently (issue #2514 AC2), the
  # same way dogfood-doc-models-guard pins the section's scout/reviewer/
  # worker restatement.
  dogfood-doc-filer-pin-guard =
    let
      doc = builtins.readFile ../../docs/reference.md;
    in
    assert
      (assertDogfoodDocFilerPinOk {
        inherit doc;
        wantFilerPin = wantDogfoodFilerPin;
      }) == doc;
    pkgs.runCommand "dogfood-doc-filer-pin-guard" { } "touch $out";

  # Regression guard (issue #2514 AC2): the doc-drift assertion above must
  # actually detect a wrong Filer pin restatement, not just pass vacuously
  # because docs/reference.md's Subagent roster section currently agrees
  # with the fixture. Runs assertDogfoodDocFilerPinOk — the exact function
  # dogfood-doc-filer-pin-guard calls — against a synthetic doc whose dogfood
  # paragraph states the real wantDogfoodFilerPin with the Filer model
  # literal flipped (a plausible drift a fixture edit could leave behind),
  # via tryEval, so this fails if the hasInfix assert is ever dropped from
  # assertDogfoodDocFilerPinOk.
  dogfood-doc-filer-pin-guard-regression =
    let
      inherit (pkgs.lib) assertMsg replaceStrings;
      filerPin = defaultModelFixture.dogfoodPins.filer;
      driftedFilerPin =
        if filerPin == "claude-haiku-4-5-20251001" then
          "claude-haiku-4-6-20251001"
        else
          "claude-haiku-4-5-20251001";
      driftedFilerPinStatement = replaceStrings [ filerPin ] [ driftedFilerPin ] wantDogfoodFilerPin;
      badDoc = ''
        intro text

        #### Subagent roster

        spindrift's own dogfood Consumer config: it sets `roster = rosterLib.defaultRoster { models = { ${driftedFilerPinStatement} }; }`, naming only the Filer.

        #### Next heading
      '';
      result = builtins.tryEval (assertDogfoodDocFilerPinOk {
        doc = badDoc;
        wantFilerPin = wantDogfoodFilerPin;
      });
    in
    assert assertMsg (!result.success)
      "dogfood-doc-filer-pin-guard-regression: expected assertDogfoodDocFilerPinOk to reject a synthetic doc whose dogfood paragraph states a wrong Filer pin restatement, but it evaluated successfully";
    pkgs.runCommand "dogfood-doc-filer-pin-guard-regression" { } "touch $out";

  # Regression guard (issue #2514 AC2, review counterexample): a duplicate of
  # the wanted Filer pin literal elsewhere in the Subagent roster section
  # (e.g. the unrelated `defaultRoster` syntax example docs/reference.md
  # states a few paragraphs above the dogfood paragraph) must never let a
  # wrong dogfood-paragraph restatement pass -- dogfoodParagraph's own
  # isolation, not just hasInfix's substring match against the whole
  # section, is what has to catch this. Before dogfoodParagraph existed, a
  # doc shaped exactly like this synthetic one (correct pin once, outside
  # the dogfood paragraph; wrong pin once, inside it) evaluated successfully
  # against assertDogfoodDocFilerPinOk's rosterDocSection-scoped hasInfix --
  # this fails if that vacuity is ever reintroduced.
  dogfood-doc-filer-pin-guard-nonvacuity-regression =
    let
      inherit (pkgs.lib) assertMsg;
      filerPin = defaultModelFixture.dogfoodPins.filer;
      driftedFilerPin =
        if filerPin == "claude-haiku-4-5-20251001" then
          "claude-haiku-4-6-20251001"
        else
          "claude-haiku-4-5-20251001";
      badDoc = ''
        intro text

        #### Subagent roster

        unrelated example: `defaultRoster { models = { filer = "${filerPin}"; }; }`.

        spindrift's own dogfood Consumer config: it sets `roster = rosterLib.defaultRoster { models = { filer = "${driftedFilerPin}"; }; }`, naming only the Filer.

        #### Next heading
      '';
      result = builtins.tryEval (assertDogfoodDocFilerPinOk {
        doc = badDoc;
        wantFilerPin = wantDogfoodFilerPin;
      });
    in
    assert assertMsg (!result.success)
      "dogfood-doc-filer-pin-guard-nonvacuity-regression: expected assertDogfoodDocFilerPinOk to reject a synthetic doc whose dogfood paragraph restates a wrong Filer pin even though the correct pin appears elsewhere in the roster section, but it evaluated successfully";
    pkgs.runCommand "dogfood-doc-filer-pin-guard-nonvacuity-regression" { } "touch $out";

  # Regression guard: rosterDocSection's own throw branch (missing "####
  # Subagent roster" heading) is otherwise never exercised -- every other
  # fixture in this file, real or synthetic, feeds a doc that contains the
  # heading. Runs rosterDocSection directly against a doc without it, via
  # tryEval, so this fails if that throw is ever dropped or the heading
  # match silently starts tolerating its absence.
  roster-doc-section-throws-on-missing-heading =
    let
      inherit (pkgs.lib) assertMsg;
      result = builtins.tryEval (rosterDocSection "intro text with no roster heading at all");
    in
    assert assertMsg (!result.success)
      "roster-doc-section-throws-on-missing-heading: expected rosterDocSection to throw on a doc missing the \"#### Subagent roster\" heading, but it evaluated successfully";
    pkgs.runCommand "roster-doc-section-throws-on-missing-heading" { } "touch $out";

  # Regression guard (issue #2184, ADR 0037): the disjointness assertion must
  # cover the structural domain-tree paths too, not just the flakeOption
  # nixPaths — a future structural-vs-flakeOption prefix collision otherwise
  # slips past this check and surfaces as an opaque buildTree throw at flake
  # eval. Runs assertNixPathsOk — the exact function the real check calls —
  # against the real combined path set with one synthetic path injected that
  # nests under the structural leaf `agents.driver`, via tryEval, so it fails
  # if assertNixPathsOk ever stops folding in / rejecting a structural
  # collision.
  flake-nixpath-disjointness-collision-guard =
    let
      inherit (pkgs.lib)
        assertMsg
        attrNames
        attrValues
        filter
        concatStringsSep
        ;
      flakeOptionNames = filter (n: schema.${n}.flakeOption or false) (attrNames schema);
      realNixPaths =
        (map (n: resolveNixPath n schema.${n}) flakeOptionNames)
        ++ (map (segs: concatStringsSep "." segs) (attrValues structuralPaths));
      # "agents.driver" is a real structural leaf; a knob landing under it
      # would collide — exactly the latent cross-set failure this guards.
      badPaths = realNixPaths ++ [ "agents.driver.injected" ];
      result = builtins.tryEval (assertNixPathsOk badPaths);
    in
    assert assertMsg (!result.success)
      "flake-nixpath-disjointness-collision-guard: expected assertNixPathsOk to reject a synthetic path nesting under the structural leaf agents.driver, but it evaluated successfully";
    pkgs.runCommand "flake-nixpath-disjointness-collision-guard" { } "touch $out";

  # lib/env-schema.nix's intKind/hostConfig/hostDerived markers (issue #2363)
  # must stay internally consistent: every int-typed, non-secret,
  # non-boxEnvOnly member declares intKind; intKind never decorates a
  # non-int member; intKind, when present, is exactly "positive" or
  # "nonneg"; and hostDerived never contradicts host-config membership
  # (secret or boxEnvOnly). Runs assertMarkerConsistencyOk against the real
  # schema.
  marker-consistency =
    let
      schema = import ../../lib/env-schema.nix;
    in
    assert (assertMarkerConsistencyOk schema) == schema;
    pkgs.runCommand "marker-consistency" { } "touch $out";

  # Regression guard (issue #2363): the marker-consistency check above must
  # actually detect a violation of each of its four invariants, not just
  # pass vacuously because the real schema already satisfies them. Runs
  # assertMarkerConsistencyOk — the exact function marker-consistency calls —
  # against four independently-mutated copies of the real schema, each
  # violating exactly one invariant, via tryEval, so this fails if any one of
  # the four asserts is ever dropped from assertMarkerConsistencyOk (not
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
      missingIntKindResult = builtins.tryEval (assertMarkerConsistencyOk missingIntKindSchema);
      intKindOnNonIntResult = builtins.tryEval (assertMarkerConsistencyOk intKindOnNonIntSchema);
      hostDerivedExcludedResult = builtins.tryEval (assertMarkerConsistencyOk hostDerivedExcludedSchema);
      badIntKindValueResult = builtins.tryEval (assertMarkerConsistencyOk badIntKindValueSchema);
    in
    assert assertMsg (!missingIntKindResult.success)
      "marker-consistency-guard: expected assertMarkerConsistencyOk to reject maxParallel with intKind removed, but it evaluated successfully";
    assert assertMsg (!intKindOnNonIntResult.success)
      "marker-consistency-guard: expected assertMarkerConsistencyOk to reject label decorated with an injected intKind, but it evaluated successfully";
    assert assertMsg (!hostDerivedExcludedResult.success)
      "marker-consistency-guard: expected assertMarkerConsistencyOk to reject gitUserName (hostDerived) decorated with an injected boxEnvOnly, but it evaluated successfully";
    assert assertMsg (!badIntKindValueResult.success)
      "marker-consistency-guard: expected assertMarkerConsistencyOk to reject maxParallel with intKind mistyped as \"positve\", but it evaluated successfully";
    pkgs.runCommand "marker-consistency-guard" { } "touch $out";

  # lib/legacy-settings-section.nix must totally cover the schema's
  # flakeOption knobs (issue #2522): every such knob either has a row here or
  # is lib/env-schema.nix legacySettingsExempt = true;, and no row here
  # outlives its schema knob. Runs assertLegacySettingsSectionOk against the
  # real map/schema.
  legacy-settings-section-coverage =
    let
      legacySettingsSection = import ../../lib/legacy-settings-section.nix;
    in
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
      legacySettingsSection = import ../../lib/legacy-settings-section.nix;
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
}
