# The error mkHarness throws for a knob set to a value its schema entry lists in
# `removedChoices`, or null when none is. A pure value so the migration message
# is checkable by equality, which tryEval cannot do. `entries` is the schema
# attrset, `values` the merged knob values keyed alike.
{ lib }:
entries: values:
let
  violations = lib.concatLists (
    lib.mapAttrsToList (
      key: entry:
      let
        value = values.${key} or null;
      in
      lib.optional (
        entry ? removedChoices && builtins.isString value && entry.removedChoices ? ${value}
      ) entry.removedChoices.${value}
    ) entries
  );
in
if violations == [ ] then null else "mkHarness: ${lib.concatStringsSep "; " violations}"
