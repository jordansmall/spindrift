# Fixture for the nix-to-Go parity test in tests/prompt-assembly-parity.bats
# (issue #4159): the `status=<...>` alternation lib/research-verdicts.nix's
# renderPrompt writes into research-prompt.md, per RESEARCH_VERDICTS value, for
# the Go assemble-prompt rendering of ${RESEARCH_STATUS_ENUM} to be diffed against.
{ pkgs }:
let
  rv = import ../lib/research-verdicts.nix;
  template = builtins.readFile ../templates/default/prompts/research-prompt.md;

  statusToken =
    verdicts:
    let
      line = builtins.head (
        builtins.filter (l: builtins.isString l && builtins.match "SPINDRIFT_OUTCOME .*status=<.*" l != null) (
          builtins.split "\n" (rv.render verdicts template)
        )
      );
    in
    builtins.head (builtins.match ".*(status=<[^>]*>).*" line);

  sets = [
    ""
    (builtins.toJSON [
      {
        verdict = "accept";
        label = "l-accept";
        description = "a";
      }
      {
        verdict = "decline";
        label = "l-decline";
        description = "d";
      }
    ])
  ];
in
pkgs.writeText "research-status-parity.json" (
  builtins.toJSON (
    map (verdicts: {
      inherit verdicts;
      status = statusToken verdicts;
    }) sets
  )
)
