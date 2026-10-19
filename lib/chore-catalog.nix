# Single declaration of the built-in butler Chores (ADR 0056, issue #3991).
# lib/env-schema.nix's butlerChoreClasses default, the generated Go builtins
# (cmd/launcher/internal/chore/builtins_gen.go), and docs all derive from
# this one list. List order is load-bearing: it fixes classesDefault's byte
# layout, which the generated docs and flag table render verbatim. This list
# declares names, each Chore's closed classList (the classes its Box
# classifies a finding into, issue #4766), and default promotion/patch
# classes -- prompt files resolve by name from mkHarness's choresDir (read
# by, e.g., the BUTLER_CHORES eval assertion and the image copy), which a
# Consumer may replace.
#
# Optional row fields: scopeSource ("tree", the default, sweeps the Target
# repo's tree; "records" sweeps the dispatch record store, ADR 0062),
# every (a catalog default sweep interval, Go duration string), and
# findingLabel (a provenance label every finding of the Chore wears, e.g.
# the tuning Chore's agent-tuning-finding, ADR 0062), and mergeByHand (a
# bool, default false: the host never merges a PR closing one of the
# Chore's findings and a human merges it, ADR 0070), knobs (env-schema
# entries keyed by env-schema key that the row owns, folded in by
# lib/row-knobs.nix; nix/checks/row-knobs.nix requires a row with knobs to
# live in lib/chores/<name>.nix), and
# minRecordsKnob (required of a records-scoped row: the key of one of its
# own knobs holding its minimum new-Record count, issue #5700).
let
  # Throws unless every chore's scopeSource is "tree" or "records" (absent
  # means tree), every chore's promotionClasses and patchClasses sit on its
  # classList, a records-scoped chore declares neither (it files findings
  # for a human and never promotes or patches, ADR 0062), and a mergeByHand
  # chore carries a non-empty findingLabel (the hold's comment names it, ADR
  # 0070), and a records-scoped chore's minRecordsKnob names a key in its own
  # knobs; exported so nix/checks/chore-catalog.nix can feed it broken
  # catalogs.
  checkClassLists =
    cs:
    let
      missing =
        c: builtins.filter (k: !builtins.elem k c.classList) (c.promotionClasses ++ c.patchClasses);
      isRecords = c: (c.scopeSource or "tree") == "records";
      badScope = builtins.filter (
        c:
        !builtins.elem (c.scopeSource or "tree") [
          "tree"
          "records"
        ]
      ) cs;
      recordsBad = builtins.filter (c: isRecords c && (c.promotionClasses ++ c.patchClasses) != [ ]) cs;
      bad = builtins.filter (c: missing c != [ ]) cs;
      noLabel = builtins.filter (c: (c.mergeByHand or false) && (c.findingLabel or "") == "") cs;
      noMinRecords = builtins.filter (
        c: isRecords c && !(c ? minRecordsKnob && builtins.hasAttr c.minRecordsKnob (c.knobs or { }))
      ) cs;
    in
    if badScope != [ ] then
      throw (
        "lib/chore-catalog.nix: scopeSource must be \"tree\" or \"records\": "
        + builtins.concatStringsSep ", " (map (c: "${c.name}=${c.scopeSource}") badScope)
      )
    else if recordsBad != [ ] then
      throw (
        "lib/chore-catalog.nix: records-scoped chores must declare no promotionClasses or patchClasses: "
        + builtins.concatStringsSep ", " (map (c: c.name) recordsBad)
      )
    else if noLabel != [ ] then
      throw (
        "lib/chore-catalog.nix: mergeByHand chores must declare a non-empty findingLabel: "
        + builtins.concatStringsSep ", " (map (c: c.name) noLabel)
      )
    else if noMinRecords != [ ] then
      throw (
        "lib/chore-catalog.nix: records-scoped chores must declare a minRecordsKnob naming one of their own knobs: "
        + builtins.concatStringsSep ", " (map (c: c.name) noMinRecords)
      )
    else if bad == [ ] then
      cs
    else
      throw (
        "lib/chore-catalog.nix: classes not on the chore's classList: "
        + builtins.concatStringsSep "; " (
          map (c: "${c.name}: ${builtins.concatStringsSep ", " (missing c)}") bad
        )
      );

  chores = checkClassLists [
    {
      name = "bugs";
      classList = [
        "error-handling"
        "resource-leak"
        "correctness"
        "input-validation"
        "concurrency"
        "other"
      ];
      # Default promotion allow-list (BUTLER_CHORE_CLASSES), a subset of
      # classList.
      promotionClasses = [
        "error-handling"
        "resource-leak"
      ];
      # Patch rung (ADR 0057): a chore's patch-eligible subset of its
      # classList, [] until a class earns patch trust.
      patchClasses = [ ];
    }
    {
      name = "refactor";
      classList = [
        "dead-code"
        "duplication"
        "other"
      ];
      promotionClasses = [ "dead-code" ];
      patchClasses = [ ];
    }
    {
      name = "docs-drift";
      classList = [
        "stale-reference"
        "wrong-behaviour"
        "wrong-example"
        "wrong-code-comment"
        "other"
      ];
      promotionClasses = [ "stale-reference" ];
      patchClasses = [ "stale-reference" ];
    }
    (import ./chores/tuning.nix)
  ];
  names = map (c: c.name) chores;
  recordsScoped = map (c: c.name) (
    builtins.filter (c: (c.scopeSource or "tree") == "records") chores
  );
  mergeByHand = map (c: c.name) (builtins.filter (c: c.mergeByHand or false) chores);
  everyDefaults = builtins.listToAttrs (
    map (c: {
      inherit (c) name;
      value = c.every;
    }) (builtins.filter (c: c ? every) chores)
  );
  findingLabels = builtins.listToAttrs (
    map (c: {
      inherit (c) name;
      value = c.findingLabel;
    }) (builtins.filter (c: c ? findingLabel) chores)
  );
  # Chore name -> env name of its minimum new-Record knob (records-scoped
  # chores only); checkClassLists guarantees the key names an own knob.
  minRecordsKnobs = builtins.listToAttrs (
    map (c: {
      inherit (c) name;
      value = c.knobs.${c.minRecordsKnob}.env;
    }) (builtins.filter (c: c ? minRecordsKnob) chores)
  );
  classLists = builtins.listToAttrs (
    map (c: {
      inherit (c) name;
      value = c.classList;
    }) chores
  );
  # Skips a chore with no promotion classes (a records-scoped one), as
  # patchClassesDefault does: parseClasses rejects an empty class list.
  classesDefault = builtins.concatStringsSep " " (
    map (c: "${c.name}=${builtins.concatStringsSep "," c.promotionClasses}") (
      builtins.filter (c: c.promotionClasses != [ ]) chores
    )
  );
  # Same rendering as classesDefault, but only for a chore with a non-empty
  # patchClasses -- an entry naming a chore with [] would fail
  # env-schema.nix's own grammar (parseClasses rejects an empty class list).
  patchClassesDefault = builtins.concatStringsSep " " (
    map (c: "${c.name}=${builtins.concatStringsSep "," c.patchClasses}") (
      builtins.filter (c: c.patchClasses != [ ]) chores
    )
  );
in
{
  inherit
    chores
    names
    classLists
    recordsScoped
    mergeByHand
    everyDefaults
    findingLabels
    minRecordsKnobs
    classesDefault
    patchClassesDefault
    checkClassLists
    ;
}
