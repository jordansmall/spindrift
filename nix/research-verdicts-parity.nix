# Fixture for TestResearchVerdictRenderingMatchesNix (issues #4159, #2630):
# the three placeholder renderings lib/research-verdicts.nix's renderPrompt
# performs on research-prompt.md, per RESEARCH_VERDICTS value, which
# forge.VerdictLabels.RenderPrompt's output is diffed against. Each target is
# rendered on its own through the build's own `render`, so the expected bytes
# cannot drift from the nix function.
{ pkgs }:
let
  rv = import ../lib/research-verdicts.nix;

  rendered = verdicts: target: rv.render verdicts target;

  sets = [
    ""
    (builtins.toJSON [
      {
        verdict = "accept";
        label = "l-accept";
        description = "worth doing; the change is small and the files are named.";
      }
      {
        verdict = "defer";
        label = "l-defer";
        description = "relevant, but blocked on #123 — revisit after it lands.";
      }
      {
        verdict = "decline";
        label = "l-decline";
        description = "a false positive: the `guard` already covers this case.";
      }
    ])
  ];
in
pkgs.writeText "research-verdicts-parity.json" (
  builtins.toJSON (
    map (verdicts: {
      inherit verdicts;
      status = rendered verdicts "status=<\${RESEARCH_STATUS_ENUM}>";
      enum = rendered verdicts "`<RESEARCH_VERDICT_ENUM>`";
      bullets = rendered verdicts "<!-- RESEARCH_VERDICT_BULLETS -->";
    }) sets
  )
)
