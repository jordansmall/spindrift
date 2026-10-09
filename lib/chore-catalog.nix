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
# the tuning Chore's agent-tuning-finding, ADR 0062).
let
  # Throws unless every chore's scopeSource is "tree" or "records" (absent
  # means tree), every chore's promotionClasses and patchClasses sit on its
  # classList, and a records-scoped chore declares neither (it files
  # findings for a human and never promotes or patches, ADR 0062); exported
  # so nix/checks/chore-catalog.nix can feed it broken catalogs.
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
    {
      name = "tuning";
      classList = [
        "cost-waste"
        "quality-regression"
        "prompt-gap"
        "model-fit"
        "knob-tuning"
        "evidence-gap"
      ];
      promotionClasses = [ ];
      patchClasses = [ ];
      scopeSource = "records";
      every = "24h";
      findingLabel = "agent-tuning-finding";
    }
  ];
  names = map (c: c.name) chores;
  recordsScoped = map (c: c.name) (
    builtins.filter (c: (c.scopeSource or "tree") == "records") chores
  );
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
    everyDefaults
    findingLabels
    classesDefault
    patchClassesDefault
    checkClassLists
    ;
}
