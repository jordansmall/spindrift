# Eval-level pins for lib/prompt-inject.nix (issue #512): one assertion per
# slicing/injection primitive, pinning marker handling, missing-marker
# errors, and trailing-newline discipline ahead of nix/checks/prompts.nix's
# integration coverage of the same behavior through built store paths.
{ pkgs, ... }:
let
  promptInject = import ../../lib/prompt-inject.nix;
  inherit (pkgs.lib) assertMsg;
in
{
  prompt-inject-slice-between-basic =
    let
      out = promptInject.sliceBetween "# START" "# END" "before\n# START\nmiddle\n# END\nafter\n";
    in
    assert assertMsg (out == "# START\nmiddle\n")
      "sliceBetween must return the span from startMarker (inclusive) to endMarker (exclusive), got: ${out}";
    pkgs.runCommand "prompt-inject-slice-between-basic" { } "touch $out";

  prompt-inject-slice-between-missing-start-marker-throws =
    let
      result = builtins.tryEval (
        promptInject.sliceBetween "# NOPE" "# END" "before\n# START\nmiddle\n# END\nafter\n"
      );
    in
    assert assertMsg (!result.success)
      "sliceBetween must throw when startMarker is absent from the source text";
    pkgs.runCommand "prompt-inject-slice-between-missing-start-marker-throws" { } "touch $out";

  prompt-inject-slice-between-duplicate-start-marker-throws =
    let
      result = builtins.tryEval (
        promptInject.sliceBetween "# START" "# END" "# START\nmiddle\n# START\nmore\n# END\nafter\n"
      );
    in
    assert assertMsg (!result.success)
      "sliceBetween must throw when startMarker appears more than once in the source text";
    pkgs.runCommand "prompt-inject-slice-between-duplicate-start-marker-throws" { } "touch $out";

  prompt-inject-slice-from-marker =
    let
      out = promptInject.sliceFromMarker "# LAND" "before\n# LAND\nthe change\nmore\n";
    in
    assert assertMsg (out == "# LAND\nthe change\nmore\n")
      "sliceFromMarker must return the span from marker (inclusive) to the end of the text, got: ${out}";
    pkgs.runCommand "prompt-inject-slice-from-marker" { } "touch $out";

  prompt-inject-trim-trailing-blank-line-double-newline =
    let
      out = promptInject.trimTrailingBlankLine "one\ntwo\n\n";
    in
    assert assertMsg (out == "one\ntwo\n")
      "trimTrailingBlankLine must strip one trailing newline when the text ends with a blank line, got: ${out}";
    pkgs.runCommand "prompt-inject-trim-trailing-blank-line-double-newline" { } "touch $out";

  prompt-inject-trim-trailing-blank-line-single-newline-is-noop =
    let
      out = promptInject.trimTrailingBlankLine "one\ntwo\n";
    in
    assert assertMsg (out == "one\ntwo\n")
      "trimTrailingBlankLine must be a no-op on text ending with a single newline, got: ${out}";
    pkgs.runCommand "prompt-inject-trim-trailing-blank-line-single-newline-is-noop" { } "touch $out";

  prompt-inject-inject-section-appends-when-absent =
    let
      out = promptInject.injectSection "# MARKER" "# MARKER\nblock body\n" "intro text\n";
    in
    assert assertMsg (out == "intro text\n\n# MARKER\nblock body\n")
      "injectSection must append the block, separated by a blank line, when the marker is absent, got: ${out}";
    pkgs.runCommand "prompt-inject-inject-section-appends-when-absent" { } "touch $out";

  prompt-inject-inject-section-idempotent-when-present =
    let
      promptText = "intro text\n\n# MARKER\nalready here\n";
      out = promptInject.injectSection "# MARKER" "# MARKER\nblock body\n" promptText;
    in
    assert assertMsg (out == promptText)
      "injectSection must leave promptText unchanged when it already contains the marker, got: ${out}";
    pkgs.runCommand "prompt-inject-inject-section-idempotent-when-present" { } "touch $out";
}
