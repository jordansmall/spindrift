# Fixture for the nix-to-Go parity test in tests/prompt-assembly-parity.bats
# (issues #4159, #2630): the three renderings lib/research-verdicts.nix's
# renderPrompt performs on research-prompt.md, per RESEARCH_VERDICTS value,
# for forge.VerdictLabels.RenderPrompt's output at prompt assembly to be
# diffed against. Each target is rendered on its own, through the same `render`
# the build uses, so the expected bytes cannot drift from the nix function.
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
