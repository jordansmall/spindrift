# Every agent workflow must reach the ~2m40s image build through the shared
# agent-setup composite, and every run must be preceded by an issue claim — but
# the two control-plane template sets get there on different rails, and this
# guard pins each rail against the hand-maintained YAML so neither can silently
# regress (issue #1967).
#
# GitHub (.github/workflows): agent-setup runs a `gh api rate_limit` smoke test
# and a `gh issue edit` claim, both against api.github.com. Those steps are
# gated on `inputs.forge != 'forgejo'`, so this set — which never sets `forge` —
# keeps them.
#
# Forgejo (.forgejo/workflows): `gh` and api.github.com have no analog on a
# Codeberg runner, so each workflow passes `forge: forgejo` (skipping the smoke
# and gh-claim steps) and claims the issue itself through the forgejo-label-swap
# composite, which drives the Forgejo REST API. A forgejo workflow that dropped
# `forge: forgejo` would fall back onto the gh-shaped steps and fail on 401
# before the build; one that dropped the claim would never take ownership.
{ pkgs, ... }:
let
  inherit (pkgs.lib)
    assertMsg
    concatStringsSep
    filter
    hasInfix
    ;
  setupSrc = builtins.readFile ../../.github/actions/agent-setup/action.yml;
  swapSrc = builtins.readFile ../../.github/actions/forgejo-label-swap/label-swap.sh;
  githubWorkflows = {
    "agent-dispatch.yml" = builtins.readFile ../../.github/workflows/agent-dispatch.yml;
    "agent-recover.yml" = builtins.readFile ../../.github/workflows/agent-recover.yml;
    "agent-research.yml" = builtins.readFile ../../.github/workflows/agent-research.yml;
  };
  forgejoWorkflows = {
    "forgejo/agent-dispatch.yml" = builtins.readFile ../../.forgejo/workflows/agent-dispatch.yml;
    "forgejo/agent-recover.yml" = builtins.readFile ../../.forgejo/workflows/agent-recover.yml;
    "forgejo/agent-research.yml" = builtins.readFile ../../.forgejo/workflows/agent-research.yml;
  };

  # agent-setup keeps the GitHub smoke test and forge-routes it (plus the claim)
  # so the forgejo set can opt out.
  setupHasSmoke = hasInfix "gh api rate_limit" setupSrc;
  setupForgeRoutes = hasInfix "inputs.forge != 'forgejo'" setupSrc;

  # forgejo-label-swap must exist and drive the Forgejo REST label API.
  swapHitsForgejoRest = hasInfix "/api/v1/repos" swapSrc;

  wiresSetup = src: hasInfix "uses: ./.github/actions/agent-setup" src;

  githubMissingWire = filter (name: !wiresSetup githubWorkflows.${name}) (
    builtins.attrNames githubWorkflows
  );
  # A forgejo workflow must wire agent-setup, select the forgejo forge, and claim
  # via forgejo-label-swap.
  forgejoBroken = filter (
    name:
    let
      src = forgejoWorkflows.${name};
    in
    !(
      wiresSetup src
      && hasInfix "forge: forgejo" src
      && hasInfix "uses: ./.github/actions/forgejo-label-swap" src
    )
  ) (builtins.attrNames forgejoWorkflows);
in
{
  agent-workflows-control-plane-wiring =
    assert assertMsg setupHasSmoke
      "agent-setup/action.yml is missing the `gh api rate_limit` smoke test — the rate-limit preflight was removed or renamed.";
    assert assertMsg setupForgeRoutes
      "agent-setup/action.yml no longer forge-routes on `inputs.forge != 'forgejo'` — the GitHub-only smoke/claim steps are no longer skippable, so the forgejo templates cannot reuse this action.";
    assert assertMsg swapHitsForgejoRest
      "forgejo-label-swap/label-swap.sh no longer drives the Forgejo REST label API (`/api/v1/repos`) — the forgejo claim/undo path is broken.";
    assert assertMsg (
      githubMissingWire == [ ]
    ) "github agent workflow(s) no longer call ./.github/actions/agent-setup, so they skip the rate-limit smoke test: ${concatStringsSep ", " githubMissingWire}";
    assert assertMsg (
      forgejoBroken == [ ]
    ) "forgejo agent workflow(s) do not reach the build via agent-setup with `forge: forgejo` and a forgejo-label-swap claim — they would fall back onto the gh-shaped smoke/claim and fail on api.github.com: ${concatStringsSep ", " forgejoBroken}";
    pkgs.runCommand "agent-workflows-control-plane-wiring" { } "touch $out";
}
