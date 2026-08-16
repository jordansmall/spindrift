# Eval-level pins for lib/research-verdicts.nix (issue #2201): the build-time
# half of the configurable research verdict set. One assertion per behavior —
# default parsing, the default-set render going through the same rendering
# machinery as a custom set (issue #2525), custom-set contract rendering, and
# the marker-absent safety guard — ahead of nix/checks/prompts.nix's
# integration coverage of the same rendering through a built harness prompt dir.
{ pkgs, ... }:
let
  rv = import ../../lib/research-verdicts.nix;
  inherit (pkgs.lib) assertMsg hasInfix;

  template = builtins.readFile ../../templates/default/prompts/research-prompt.md;
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
  # The empty knob yields the built-in three, in order.
  research-verdicts-parse-empty-is-default =
    let
      out = rv.parse "";
    in
    assert assertMsg (out == rv.defaultVerdicts)
      "parse \"\" must return defaultVerdicts";
    assert assertMsg (map (v: v.verdict) out == [ "recommend" "reject" "unclear" ])
      "defaultVerdicts must be recommend/reject/unclear in order";
    pkgs.runCommand "research-verdicts-parse-empty-is-default" { } "touch $out";

  # A custom JSON array parses order-preserving into the same shape.
  research-verdicts-parse-custom =
    let
      out = rv.parse customJSON;
    in
    assert assertMsg (map (v: v.verdict) out == [ "approve" "skip" ])
      "parse must preserve verdict order from the JSON array";
    assert assertMsg ((builtins.elemAt out 0).label == "agent-research-approve")
      "parse must carry the mapped label";
    pkgs.runCommand "research-verdicts-parse-custom" { } "touch $out";

  # The empty (default) knob renders through the same machinery as a custom
  # set (issue #2525) -- no byte-identical-to-template no-op special case.
  # The rendered output must carry the bullets synthesized from
  # defaultVerdicts (not the raw hand-typed template text), the status
  # alternation, and the backtick enumeration.
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

  # A custom set rewrites the VERDICT bullets, the enumeration, and the
  # status alternation, and drops every default token from the contract.
  research-verdicts-render-custom =
    assert assertMsg (hasInfix "- `approve` — ship it." customRendered)
      "custom render must emit the configured verdict bullet";
    assert assertMsg (hasInfix "status=<approve|skip>" customRendered)
      "custom render must rewrite the status alternation";
    assert assertMsg (hasInfix "`approve` / `skip`" customRendered)
      "custom render must rewrite the backtick enumeration";
    assert assertMsg (!(hasInfix "status=<recommend|reject|unclear>" customRendered))
      "custom render must not leave the default status alternation";
    assert assertMsg (!(hasInfix "\${RESEARCH_STATUS_ENUM}" customRendered))
      "custom render must not leave the RESEARCH_STATUS_ENUM placeholder token unresolved (issue #2504)";
    pkgs.runCommand "research-verdicts-render-custom" { } "touch $out";

  # Rendering a prompt that lacks the VERDICT markers (a Consumer research
  # prompt carrying only its own preamble) must not throw — the section
  # rewrite is guarded, and the token rewrites are no-ops when absent.
  research-verdicts-render-markerless-is-safe =
    let
      preamble = "CONFIGURED-RESEARCH-PROMPT-MARKER\nResearch issue.\n";
      out = rv.render customJSON preamble;
    in
    assert assertMsg (out == preamble)
      "render on a markerless prompt with no default tokens must be a no-op";
    pkgs.runCommand "research-verdicts-render-markerless-is-safe" { } "touch $out";

  # A custom RESEARCH_VERDICTS array with zero entries must be rejected
  # (mirrors ParseResearchVerdicts's "must contain at least one entry").
  research-verdicts-parse-rejects-empty-array =
    let
      badJSON = builtins.toJSON [ ];
      result = builtins.tryEval (rv.parse badJSON);
    in
    assert assertMsg (!result.success)
      "parse must throw on an empty verdict array";
    pkgs.runCommand "research-verdicts-parse-rejects-empty-array" { } "touch $out";

  # An entry with an empty verdict token must be rejected.
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
    assert assertMsg (!result.success)
      "parse must throw on an entry with an empty verdict";
    pkgs.runCommand "research-verdicts-parse-rejects-empty-verdict" { } "touch $out";

  # An entry with an empty label must be rejected.
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
    assert assertMsg (!result.success)
      "parse must throw on an entry with an empty label";
    pkgs.runCommand "research-verdicts-parse-rejects-empty-label" { } "touch $out";

  # A verdict token containing whitespace must be rejected.
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
    assert assertMsg (!result.success)
      "parse must throw on a verdict token containing whitespace";
    pkgs.runCommand "research-verdicts-parse-rejects-whitespace-token" { } "touch $out";

  # The reserved "blocked" crash/no-verdict escape-hatch token must never be
  # a configurable verdict.
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
    assert assertMsg (!result.success)
      "parse must throw on the reserved \"blocked\" verdict token";
    pkgs.runCommand "research-verdicts-parse-rejects-reserved-blocked-token" { } "touch $out";

  # Two entries sharing the same verdict token must be rejected.
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
    assert assertMsg (!result.success)
      "parse must throw on a duplicate verdict token";
    pkgs.runCommand "research-verdicts-parse-rejects-duplicate-token" { } "touch $out";
}
