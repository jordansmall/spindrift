# Pure-builtins primitives shared across the "Pure builtins only" lib
# files (issue #2535): the regex/suffix/shell-escaping/list helpers each
# used to reimplement independently. This is the bottom of that
# dependency graph -- other pure-builtins lib files import from here
# instead of duplicating these bodies.
#
# Pure builtins only (no `pkgs.lib`, no imports of other lib/*.nix files):
# keeps this file evaluable and unit-testable with a bare `nix eval`,
# without needing a locked nixpkgs (mirrors lib/renderers.nix, issue #402).
rec {
  # Escapes a literal string's regex metacharacters so it can be used as a
  # builtins.split/builtins.match pattern without them being read as regex --
  # exported so other marker-splitting call sites (e.g.
  # nix/checks/baked-skills.nix's `between`) can split on a literal marker
  # without the same risk lib/prompt-inject.nix's own splitOnce/injectSection
  # guard against.
  escapeRegex =
    builtins.replaceStrings
      [
        "\\"
        "^"
        "$"
        "."
        "|"
        "?"
        "*"
        "+"
        "("
        ")"
        "["
        "]"
        "{"
        "}"
      ]
      [
        "\\\\"
        "\\^"
        "\\$"
        "\\."
        "\\|"
        "\\?"
        "\\*"
        "\\+"
        "\\("
        "\\)"
        "\\["
        "\\]"
        "\\{"
        "\\}"
      ];

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

  # Matches `lib.escapeShellArg` byte for byte without depending on pkgs.lib:
  # a string of only shell-safe characters passes through unquoted; anything
  # else gets single-quote-wrapped, with embedded `'` escaped as `'\''`.
  escapeShellArg =
    arg:
    let
      string = builtins.toString arg;
    in
    if builtins.match "[[:alnum:],._+:@%/-]+" string == null then
      "'" + builtins.replaceStrings [ "'" ] [ "'\\''" ] string + "'"
    else
      string;

  concatStrings = builtins.concatStringsSep "";

  mapAttrsToList = f: attrs: map (n: f n attrs.${n}) (builtins.attrNames attrs);
}
