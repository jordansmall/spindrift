# Both control-plane workflow sets must reach the image build through the shared
# agent-setup composite and claim the issue first, and the YAML is hand
# maintained, so this guard pins both against silent regression (issue #1967).
# The GitHub set never sets `forge`, which is how it keeps agent-setup's
# `gh api rate_limit` smoke test and `gh issue edit` claim.
#
# agent-research-close.yml is deliberately absent from both sets: it claims no
# issue and builds no image, so it has nothing to reach agent-setup for. Its
# label guard is pinned in nix/checks/dispatch-labels.nix instead.
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

  setupHasSmoke = hasInfix "gh api rate_limit" setupSrc;
  setupForgeRoutes = hasInfix "inputs.forge != 'forgejo'" setupSrc;

  swapHitsForgejoRest = hasInfix "/api/v1/repos" swapSrc;

  wiresSetup = src: hasInfix "uses: ./.github/actions/agent-setup" src;

  githubMissingWire = filter (name: !wiresSetup githubWorkflows.${name}) (
    builtins.attrNames githubWorkflows
  );
  # A Codeberg runner has no `gh` and no api.github.com. A workflow that dropped
  # `forge: forgejo` would fall back onto the gh steps and fail on 401 before the
  # build; one that dropped the forgejo-label-swap claim would never take the issue.
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
