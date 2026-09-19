# Marker-delimited slicing that lifts the outcome/COMMS/CHECK contract blocks
# out of issue-prompt.md and injects them into a prompt that lacks them
# (issue #512).

# Pure builtins only, no `pkgs.lib`: this file must stay evaluable and unit
# testable with a bare `nix eval`, without a locked nixpkgs (issue #402).
let
  assertMsg = cond: msg: if cond then true else throw msg;

  builtinsCompat = import ./builtins-compat.nix;

  # Splits `text` on the literal (non-regex) `marker`, asserting it appears
  # exactly once. `builtins.split` represents a match of a pattern with no
  # capture groups as an empty list, so one match yields exactly 3 parts:
  # [ before matchMarker after ].
  splitOnce =
    marker: text:
    let
      parts = builtins.split (builtinsCompat.escapeRegex marker) text;
    in
    assert assertMsg (builtins.length parts == 3)
      "prompt-inject: source must contain marker '${marker}' exactly once, found ${
        toString ((builtins.length parts - 1) / 2)
      }";
    {
      before = builtins.elemAt parts 0;
      after = builtins.elemAt parts 2;
    };
in
rec {
  # Each marker must appear exactly once, so a heading collision fails loudly
  # at eval time instead of silently slicing the wrong span.
  sliceBetween =
    startMarker: endMarker: text:
    let
      afterStart = startMarker + (splitOnce startMarker text).after;
    in
    (splitOnce endMarker afterStart).before;

  sliceFromMarker = marker: text: marker + (splitOnce marker text).after;

  # A sliced block already ends with the blank line that separated it from the
  # next heading in its source file, so chaining two blocks back to back must
  # not double that blank line up. A no-op on text ending in a single "\n".
  trimTrailingBlankLine =
    s: if builtinsCompat.hasSuffix "\n\n" s then builtinsCompat.removeSuffix "\n" s else s;

  # Skips the append when `promptText` already carries `marker`, so injection
  # is idempotent.
  injectSection =
    marker: block: promptText:
    if builtins.length (builtins.split (builtinsCompat.escapeRegex marker) promptText) > 1 then
      promptText
    else
      builtinsCompat.removeSuffix "\n" (trimTrailingBlankLine promptText) + "\n\n" + block;
}
