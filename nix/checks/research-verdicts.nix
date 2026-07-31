# Eval-level pins for lib/research-verdicts.nix (issue #2201): the build-time
# half of the configurable research verdict set. One assertion per behavior —
# default parsing, the byte-identical default-render no-op, custom-set contract
# rendering, and the marker-absent safety guard — ahead of nix/checks/prompts.nix's
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
  customRendered = rv.renderIfCustom customJSON template;
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

  # The default knob is a byte-identical no-op — the guarantee
  # nix/checks/prompts.nix's mkharness-prompt-research-outcome-default-unchanged
  # relies on at the built-prompt level.
  research-verdicts-render-default-is-noop =
    assert assertMsg (rv.renderIfCustom "" template == template)
      "renderIfCustom \"\" must leave the prompt byte-identical";
    pkgs.runCommand "research-verdicts-render-default-is-noop" { } "touch $out";

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
    pkgs.runCommand "research-verdicts-render-custom" { } "touch $out";

  # Rendering a prompt that lacks the VERDICT markers (a Consumer research
  # prompt carrying only its own preamble) must not throw — the section
  # rewrite is guarded, and the token rewrites are no-ops when absent.
  research-verdicts-render-markerless-is-safe =
    let
      preamble = "CONFIGURED-RESEARCH-PROMPT-MARKER\nResearch issue.\n";
      out = rv.renderIfCustom customJSON preamble;
    in
    assert assertMsg (out == preamble)
      "renderIfCustom on a markerless prompt with no default tokens must be a no-op";
    pkgs.runCommand "research-verdicts-render-markerless-is-safe" { } "touch $out";
}
