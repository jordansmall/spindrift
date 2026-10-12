# The error mkHarness throws for a bad `defaults` key, or null when every key is
# valid. A pure value so the removed-knob message choice is checkable by
# equality, which tryEval cannot do (issue #5070).
{ lib, validKeys }:
defaults:
let
  # Detects unknown defaults keys so mkHarness can throw on them. A typo like
  # `basebranch` would otherwise be silently ignored and never baked.
  unknownKeys = lib.filter (k: !(lib.elem k validKeys)) (lib.attrNames defaults);
  # A removed knob (lib/removed-knobs.nix) gets its own message rather than the
  # generic unknown-key one, which would not say it once existed.
  removedKnobs = import ./removed-knobs.nix;
  removedKeys = lib.filter (k: removedKnobs ? ${k}) unknownKeys;
  removedKey = lib.head removedKeys;
in
if removedKeys != [ ] then
  "mkHarness: defaults.${removedKey}: ${removedKnobs.${removedKey}.message}"
else if unknownKeys != [ ] then
  "mkHarness: unknown defaults key(s): ${lib.concatStringsSep ", " unknownKeys}; valid keys: ${lib.concatStringsSep ", " validKeys}"
else
  null
