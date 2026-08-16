# Eval-level pins for lib/builtins-compat.nix (issue #2535): one assertion
# per pure-builtins primitive shared across lib/prompt-inject.nix,
# lib/prompt-contract.nix, and lib/preambles.nix, ahead of those files'
# own checks (nix/checks/prompt-inject.nix, nix/checks/preambles.nix)
# covering the higher-level renderers built on top of these primitives.
{ pkgs, ... }:
let
  builtinsCompat = import ../../lib/builtins-compat.nix;
  inherit (pkgs.lib) assertMsg;
in
{
  builtins-compat-escape-regex-escapes-metacharacters =
    let
      out = builtinsCompat.escapeRegex ''\^$.|?*+()[]{}'';
      expected = ''\\\^\$\.\|\?\*\+\(\)\[\]\{\}'';
    in
    assert assertMsg (out == expected)
      "escapeRegex must backslash-escape every regex metacharacter, got: ${out}";
    pkgs.runCommand "builtins-compat-escape-regex-escapes-metacharacters" { } "touch $out";

  builtins-compat-escape-regex-leaves-plain-characters-untouched =
    let
      out = builtinsCompat.escapeRegex "plain text 123";
    in
    assert assertMsg (out == "plain text 123")
      "escapeRegex must leave a string with no regex metacharacters unchanged, got: ${out}";
    pkgs.runCommand "builtins-compat-escape-regex-leaves-plain-characters-untouched" { } "touch $out";

  builtins-compat-has-suffix-true =
    assert assertMsg (builtinsCompat.hasSuffix "lo" "hello")
      "hasSuffix must return true when content ends with suffix";
    pkgs.runCommand "builtins-compat-has-suffix-true" { } "touch $out";

  builtins-compat-has-suffix-false =
    assert assertMsg (!(builtinsCompat.hasSuffix "xy" "hello"))
      "hasSuffix must return false when content does not end with suffix";
    pkgs.runCommand "builtins-compat-has-suffix-false" { } "touch $out";

  builtins-compat-has-suffix-empty-suffix =
    assert assertMsg (builtinsCompat.hasSuffix "" "hello")
      "hasSuffix must return true for an empty suffix (every string ends with the empty string)";
    pkgs.runCommand "builtins-compat-has-suffix-empty-suffix" { } "touch $out";

  builtins-compat-has-suffix-longer-than-content =
    assert assertMsg (!(builtinsCompat.hasSuffix "muchlongersuffix" "hi"))
      "hasSuffix must return false when suffix is longer than content";
    pkgs.runCommand "builtins-compat-has-suffix-longer-than-content" { } "touch $out";

  builtins-compat-remove-suffix-strips-when-present =
    let
      out = builtinsCompat.removeSuffix "\n" "one\ntwo\n\n";
    in
    assert assertMsg (out == "one\ntwo\n")
      "removeSuffix must strip the suffix when present, got: ${out}";
    pkgs.runCommand "builtins-compat-remove-suffix-strips-when-present" { } "touch $out";

  builtins-compat-remove-suffix-noop-when-absent =
    let
      out = builtinsCompat.removeSuffix "xyz" "one\ntwo\n";
    in
    assert assertMsg (out == "one\ntwo\n")
      "removeSuffix must be a no-op when the suffix is absent, got: ${out}";
    pkgs.runCommand "builtins-compat-remove-suffix-noop-when-absent" { } "touch $out";

  builtins-compat-escape-shell-arg-safe-passes-through-unquoted =
    let
      out = builtinsCompat.escapeShellArg "safe-value_1.2:3@4%5/6,7";
    in
    assert assertMsg (out == "safe-value_1.2:3@4%5/6,7")
      "escapeShellArg must pass a shell-safe string through unquoted, got: ${out}";
    pkgs.runCommand "builtins-compat-escape-shell-arg-safe-passes-through-unquoted" { } "touch $out";

  # Mirrors nix/checks/preambles.nix's preambles-defaults-quote-containing
  # fixture: a string containing a single quote must be single-quote-wrapped
  # with the embedded `'` escaped as `'\''`.
  builtins-compat-escape-shell-arg-quotes-single-quote =
    let
      out = builtinsCompat.escapeShellArg "it's good";
      expected = "'it'\\''s good'";
    in
    assert assertMsg (out == expected)
      "escapeShellArg must single-quote-wrap a string containing a single quote, escaping it as '\\'', got: ${out}";
    pkgs.runCommand "builtins-compat-escape-shell-arg-quotes-single-quote" { } "touch $out";
}
