# Eval-level pins for lib/research-verdicts.nix (issue #2201), ahead of
# nix/checks/prompts.nix's integration coverage of the same rendering through
# a built harness prompt dir.
{ pkgs, ... }:
let
  rv = import ../../lib/research-verdicts.nix;
  inherit (pkgs.lib) assertMsg hasInfix;

  template = builtins.readFile ../../templates/default/prompts/research-prompt.md;
  templateSelfContained = builtins.readFile ../../templates/default/prompts/research-self-contained-prompt.md;
  customJSON = builtins.toJSON [
    {
      verdict = "approve";
      label = "agent-research-approve";
      description = "ship it.";
    }
    {
      verdict = "skip";
      label = "agent-research-skip";
      description = "drop it.";
    }
  ];
  customRendered = rv.render customJSON template;
  defaultRendered = rv.render "" template;
in
{
  research-verdicts-parse-empty-is-default =
    let
      out = rv.parse "";
    in
    assert assertMsg (out == rv.defaultVerdicts) "parse \"\" must return defaultVerdicts";
    pkgs.runCommand "research-verdicts-parse-empty-is-default" { } "touch $out";

  research-verdicts-parse-custom =
    let
      out = rv.parse customJSON;
    in
    assert assertMsg (
      map (v: v.verdict) out == [
        "approve"
        "skip"
      ]
    ) "parse must preserve verdict order from the JSON array";
    assert assertMsg (
      (builtins.elemAt out 0).label == "agent-research-approve"
    ) "parse must carry the mapped label";
    pkgs.runCommand "research-verdicts-parse-custom" { } "touch $out";

  # Every other check here reads research-prompt.md alone, so a typo'd or
  # removed marker in the self-contained sibling template would ship silently.
  # Pin both markers in both templates, ahead of any rendering.
  research-verdicts-templates-carry-both-markers =
    assert assertMsg (hasInfix rv.bulletsMarker template)
      "research-prompt.md must carry bulletsMarker";
    assert assertMsg (hasInfix rv.enumMarker template)
      "research-prompt.md must carry enumMarker";
    assert assertMsg (hasInfix rv.bulletsMarker templateSelfContained)
      "research-self-contained-prompt.md must carry bulletsMarker";
    assert assertMsg (hasInfix rv.enumMarker templateSelfContained)
      "research-self-contained-prompt.md must carry enumMarker";
    pkgs.runCommand "research-verdicts-templates-carry-both-markers" { } "touch $out";

  # The empty (default) knob renders through the same machinery as a custom
  # set (issue #2525), with no byte-identical-to-template no-op special case,
  # so the output must carry content synthesized from defaultVerdicts rather
  # than the raw template text.
  research-verdicts-render-default-renders-registry-content =
    assert assertMsg
      (hasInfix "- `recommend` — relevant, now enriched with real context; promote it." defaultRendered)
      "default render must emit the recommend bullet synthesized from defaultVerdicts";
    assert assertMsg
      (hasInfix "- `reject` — false positive, not worth doing, or a duplicate. Name the duplicate issue by number in your rationale; duplicate is a reason under `reject`, not a separate verdict." defaultRendered)
      "default render must emit the reject bullet synthesized from defaultVerdicts, full description included";
    assert assertMsg
      (hasInfix "- `unclear` — relevance can't be determined without a human's answer." defaultRendered)
      "default render must emit the unclear bullet synthesized from defaultVerdicts";
    assert assertMsg (hasInfix "status=<recommend|reject|unclear>" defaultRendered)
      "default render must emit the status alternation from defaultVerdicts";
    assert assertMsg (hasInfix "`recommend` / `reject` / `unclear`" defaultRendered)
      "default render must emit the backtick enumeration from defaultVerdicts";
    pkgs.runCommand "research-verdicts-render-default-renders-registry-content" { } "touch $out";

  # The checked-in template carries only the injection markers, no hand-typed
  # default text (issue #2525), so the negative assertions below confirm that
  # render consumed the markers, not just that default text never appeared.
  research-verdicts-render-custom =
    assert assertMsg (hasInfix "- `approve` — ship it." customRendered)
      "custom render must emit the configured verdict bullet";
    assert assertMsg (hasInfix "status=<approve|skip>" customRendered)
      "custom render must rewrite the status alternation";
    assert assertMsg (hasInfix "`approve` / `skip`" customRendered)
      "custom render must rewrite the backtick enumeration";
    assert assertMsg (
      !(hasInfix "status=<recommend|reject|unclear>" customRendered)
    ) "custom render must not leave the default status alternation";
    assert assertMsg (!(hasInfix "\${RESEARCH_STATUS_ENUM}" customRendered))
      "custom render must not leave the RESEARCH_STATUS_ENUM placeholder token unresolved (issue #2504)";
    assert assertMsg (
      !(hasInfix "<!-- RESEARCH_VERDICT_BULLETS -->" customRendered)
    ) "custom render must consume the bulletsMarker, not leave it unrendered";
    assert assertMsg (
      !(hasInfix "<RESEARCH_VERDICT_ENUM>" customRendered)
    ) "custom render must consume the enumMarker, not leave it unrendered";
    pkgs.runCommand "research-verdicts-render-custom" { } "touch $out";

  # Each of bulletsMarker/enumMarker is single-use, consumed by the first
  # render, so a second pass with the same knob finds no marker left to act on
  # and must be a byte-identical no-op.
  research-verdicts-render-is-idempotent =
    let
      defaultTwice = rv.render "" defaultRendered;
      customTwice = rv.render customJSON customRendered;
    in
    assert assertMsg (
      defaultTwice == defaultRendered
    ) "re-rendering the default-rendered prompt with the same (empty) knob must be a no-op";
    assert assertMsg (
      customTwice == customRendered
    ) "re-rendering the custom-rendered prompt with the same custom knob must be a no-op";
    pkgs.runCommand "research-verdicts-render-is-idempotent" { } "touch $out";

  # Regression pin (issue #2525 review): renderPrompt must never derive
  # correctness from the template bytes matching what defaultVerdicts renders
  # to. A byte-matching design would rewrite the enumeration and status
  # alternation but leave the reflowed bullets below in place, contradicting
  # each other; the two markers are all-or-nothing, so this design cannot.
  research-verdicts-render-reflowed-prose-is-not-load-bearing =
    let
      reflowed = ''
        # VERDICT

        Render exactly one of these verdicts:

        - `recommend` — relevant, now enriched with real
          context; promote it, wrapped across an extra line that
          does not match any registry description byte-for-byte.
        - `reject` — false positive, not worth doing, or a
          duplicate. Name the duplicate issue by number in your
          rationale; duplicate is a reason under `reject`, not a
          separate verdict.
        - `unclear` — relevance can't be determined without a
          human's answer.

        <!-- RESEARCH_VERDICT_BULLETS -->

        # POST THE VERDICT

        1. **Verdict** — `<RESEARCH_VERDICT_ENUM>`, plus a one-line rationale.

        status=<''${RESEARCH_STATUS_ENUM}>
      '';
      out = rv.render customJSON reflowed;
    in
    assert assertMsg (hasInfix "- `approve` — ship it." out)
      "reflowed decoy prose must not stop the custom bullet from being inserted";
    assert assertMsg (hasInfix "`approve` / `skip`" out)
      "reflowed decoy prose must not stop the enumeration from being rewritten";
    assert assertMsg (hasInfix "status=<approve|skip>" out)
      "reflowed decoy prose must not stop the status alternation from being rewritten";
    pkgs.runCommand "research-verdicts-render-reflowed-prose-is-not-load-bearing" { } "touch $out";

  # A Consumer research prompt may carry only its own preamble, so render must
  # not throw when the markers are absent: each token rewrite is a no-op.
  research-verdicts-render-markerless-is-safe =
    let
      preamble = "CONFIGURED-RESEARCH-PROMPT-MARKER\nResearch issue.\n";
      out = rv.render customJSON preamble;
    in
    assert assertMsg (
      out == preamble
    ) "render on a markerless prompt with no default tokens must be a no-op";
    pkgs.runCommand "research-verdicts-render-markerless-is-safe" { } "touch $out";

  # Render rewrites only the markers themselves, never a span of surrounding
  # text, so hand-typed prose next to a marker survives byte-for-byte.
  research-verdicts-render-does-not-clobber-surrounding-prose =
    let
      withDecoy = "# VERDICT\n\nSome custom lead-in sentence unrelated to any registry text.\n\n<!-- RESEARCH_VERDICT_BULLETS -->\n\n# POST THE VERDICT\n";
      out = rv.render customJSON withDecoy;
    in
    assert assertMsg (hasInfix "Some custom lead-in sentence unrelated to any registry text." out)
      "render must leave prose surrounding the marker untouched";
    pkgs.runCommand "research-verdicts-render-does-not-clobber-surrounding-prose" { } "touch $out";

  # Mirrors ParseResearchVerdicts's "must contain at least one entry".
  research-verdicts-parse-rejects-empty-array =
    let
      badJSON = builtins.toJSON [ ];
      result = builtins.tryEval (rv.parse badJSON);
    in
    assert assertMsg (!result.success) "parse must throw on an empty verdict array";
    pkgs.runCommand "research-verdicts-parse-rejects-empty-array" { } "touch $out";

  research-verdicts-parse-rejects-empty-verdict =
    let
      badJSON = builtins.toJSON [
        {
          verdict = "";
          label = "agent-research-approve";
          description = "ship it.";
        }
      ];
      result = builtins.tryEval (rv.parse badJSON);
    in
    assert assertMsg (!result.success) "parse must throw on an entry with an empty verdict";
    pkgs.runCommand "research-verdicts-parse-rejects-empty-verdict" { } "touch $out";

  research-verdicts-parse-rejects-empty-label =
    let
      badJSON = builtins.toJSON [
        {
          verdict = "approve";
          label = "";
          description = "ship it.";
        }
      ];
      result = builtins.tryEval (rv.parse badJSON);
    in
    assert assertMsg (!result.success) "parse must throw on an entry with an empty label";
    pkgs.runCommand "research-verdicts-parse-rejects-empty-label" { } "touch $out";

  research-verdicts-parse-rejects-whitespace-token =
    let
      badJSON = builtins.toJSON [
        {
          verdict = "ship it";
          label = "agent-research-approve";
          description = "ship it.";
        }
      ];
      result = builtins.tryEval (rv.parse badJSON);
    in
    assert assertMsg (!result.success) "parse must throw on a verdict token containing whitespace";
    pkgs.runCommand "research-verdicts-parse-rejects-whitespace-token" { } "touch $out";

  # "blocked" is the reserved crash/no-verdict escape hatch, so it must never
  # be a configurable verdict.
  research-verdicts-parse-rejects-reserved-blocked-token =
    let
      badJSON = builtins.toJSON [
        {
          verdict = "blocked";
          label = "agent-research-blocked";
          description = "reserved.";
        }
      ];
      result = builtins.tryEval (rv.parse badJSON);
    in
    assert assertMsg (!result.success) "parse must throw on the reserved \"blocked\" verdict token";
    pkgs.runCommand "research-verdicts-parse-rejects-reserved-blocked-token" { } "touch $out";

  research-verdicts-parse-rejects-duplicate-token =
    let
      badJSON = builtins.toJSON [
        {
          verdict = "approve";
          label = "agent-research-approve";
          description = "ship it.";
        }
        {
          verdict = "approve";
          label = "agent-research-approve-again";
          description = "ship it again.";
        }
      ];
      result = builtins.tryEval (rv.parse badJSON);
    in
    assert assertMsg (!result.success) "parse must throw on a duplicate verdict token";
    pkgs.runCommand "research-verdicts-parse-rejects-duplicate-token" { } "touch $out";
}
