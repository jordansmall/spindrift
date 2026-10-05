# Pure-builtins primitives shared by the "Pure builtins only" lib files
# (issue #2535). Pure builtins only (no `pkgs.lib`, no imports of other
# lib/*.nix files) so a bare `nix eval` can evaluate and unit-test this file
# without a locked nixpkgs (mirrors lib/renderers.nix, issue #402).
rec {
  # Marker-splitting callers pass a literal string to builtins.split or
  # builtins.match, so this escapes its regex metacharacters first.
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

  # Matches `lib.escapeShellArg` byte for byte without depending on pkgs.lib.
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

  # Collapse every run of whitespace, newlines included, to a single space and
  # trim the ends. A naive "\n" to " " replaceStrings would instead leave
  # doubled spaces at each line-wrap and trailing-newline boundary.
  oneLine =
    s:
    builtins.concatStringsSep " " (
      builtins.filter (p: p != "") (builtins.filter builtins.isString (builtins.split "[[:space:]]+" s))
    );

  mapAttrsToList = f: attrs: map (n: f n attrs.${n}) (builtins.attrNames attrs);
}
