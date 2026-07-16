# Every agent workflow must run a rate-limit smoke test before the ~2m40s
# image build. The smoke test itself lives once in the shared agent-setup
# composite action (it needs the exported GH_TOKEN and must run ahead of the
# build), and each agent workflow inherits it by calling that action. This
# guard pins both halves of that invariant against the hand-maintained YAML so
# neither can silently regress: the `gh api rate_limit` preflight must exist in
# agent-setup, and every agent workflow must wire agent-setup in.
{ pkgs, ... }:
let
  inherit (pkgs.lib) assertMsg concatStringsSep filter hasInfix;
  setupSrc = builtins.readFile ../../.github/actions/agent-setup/action.yml;
  agentWorkflows = {
    "agent-dispatch.yml" = builtins.readFile ../../.github/workflows/agent-dispatch.yml;
    "agent-recover.yml" = builtins.readFile ../../.github/workflows/agent-recover.yml;
    "agent-research.yml" = builtins.readFile ../../.github/workflows/agent-research.yml;
  };
  setupHasSmoke = hasInfix "gh api rate_limit" setupSrc;
  missingWire = filter (
    name: !hasInfix "uses: ./.github/actions/agent-setup" agentWorkflows.${name}
  ) (builtins.attrNames agentWorkflows);
in
{
  agent-workflows-run-rate-limit-smoke =
    assert assertMsg setupHasSmoke
      "agent-setup/action.yml is missing the `gh api rate_limit` smoke test — the rate-limit preflight was removed or renamed.";
    assert assertMsg (
      missingWire == [ ]
    ) "agent workflow(s) no longer call ./.github/actions/agent-setup, so they skip the rate-limit smoke test: ${concatStringsSep ", " missingWire}";
    pkgs.runCommand "agent-workflows-run-rate-limit-smoke" { } "touch $out";
}
