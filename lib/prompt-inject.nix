# Prompt slicing/injection machinery (issue #512): the marker-delimited text
# surgery that slices the outcome/COMMS/CHECK contract blocks out of
# issue-prompt.md and injects them into a prompt that lacks them.
# lib/mkHarness.nix imports this file and wires the results where the inline
# block used to sit; nix/checks/prompt-inject.nix pins each primitive's
# behavior at the eval level, ahead of nix/checks/prompts.nix's integration
# coverage through built store paths.
#
# Pure builtins only (no `pkgs.lib`): keeps this file evaluable and unit-
# testable with a bare `nix eval`, without needing a locked nixpkgs (mirrors
# lib/renderers.nix, issue #402).
let
  assertMsg = cond: msg: if cond then true else throw msg;

  # Splits `text` on the literal (non-regex) `marker`, asserting it appears
  # exactly once, and returns the text before and after it. `builtins.split`
  # represents a match of a pattern with no capture groups as an empty list,
  # so a text with exactly one match splits into exactly 3 parts:
  # [ before matchMarker after ].
  splitOnce =
    marker: text:
    let
      parts = builtins.split (escapeRegex marker) text;
    in
    assert assertMsg (builtins.length parts == 3)
      "prompt-inject: source must contain marker '${marker}' exactly once, found ${
        toString ((builtins.length parts - 1) / 2)
      }";
    {
      before = builtins.elemAt parts 0;
      after = builtins.elemAt parts 2;
    };

  # Escapes a literal string's regex metacharacters so it can be used as a
  # builtins.split/builtins.match pattern without them being read as regex.
  escapeRegex = builtins.replaceStrings
    [ "\\" "^" "$" "." "|" "?" "*" "+" "(" ")" "[" "]" "{" "}" ]
    [ "\\\\" "\\^" "\\$" "\\." "\\|" "\\?" "\\*" "\\+" "\\(" "\\)" "\\[" "\\]" "\\{" "\\}" ];

  hasSuffix =
    suffix: content:
    let
      lenContent = builtins.stringLength content;
      lenSuffix = builtins.stringLength suffix;
    in
    lenContent >= lenSuffix && builtins.substring (lenContent - lenSuffix) lenSuffix content == suffix;

  removeSuffix =
    suffix: content:
    if hasSuffix suffix content then
      builtins.substring 0 (builtins.stringLength content - builtins.stringLength suffix) content
    else
      content;
in
rec {
  # Slices `text` from `startMarker` (inclusive) up to `endMarker`
  # (exclusive), asserting each marker appears exactly once — the same
  # single-occurrence guarantee sliceFromMarker below relies on, so a
  # heading collision fails loudly at eval time instead of silently
  # slicing the wrong span.
  sliceBetween =
    startMarker: endMarker: text:
    let
      afterStart = startMarker + (splitOnce startMarker text).after;
    in
    (splitOnce endMarker afterStart).before;

  # Slices `text` from `marker` (inclusive) to the end of the string,
  # asserting it appears exactly once.
  sliceFromMarker =
    marker: text:
    marker + (splitOnce marker text).after;

  # A sliced shared block already ends with the blank line that separated it
  # from the next heading in its source file, so chaining two of them back
  # to back must not double that blank line up. Strips one, if present; a
  # no-op on text that ends with a single "\n" (e.g. a plain Consumer
  # `prompt` string).
  trimTrailingBlankLine = s: if hasSuffix "\n\n" s then removeSuffix "\n" s else s;

  # Appends `block` to `promptText` unless it already contains `marker` (the
  # default prompt's own copy, or a Consumer prompt that kept it) — so
  # injection is idempotent.
  injectSection =
    marker: block: promptText:
    if builtins.length (builtins.split (escapeRegex marker) promptText) > 1 then
      promptText
    else
      removeSuffix "\n" (trimTrailingBlankLine promptText) + "\n\n" + block;
}
