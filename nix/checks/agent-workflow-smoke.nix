# Both control-plane workflow sets must reach the image build through the shared
# agent-setup composite and claim the issue first, and the YAML is hand
# maintained, so this guard pins both against silent regression (issue #1967).
# The GitHub set never sets `forge`, which is how it keeps agent-setup's
# `gh api rate_limit` smoke test and `gh issue edit` claim.
#
# agent-research-close.yml is deliberately absent from both sets: it claims no
# issue and builds no image, so it has nothing to reach agent-setup for. Its
# label guard is pinned in nix/checks/dispatch-labels.nix instead.
#
# It also pins both agent workflow sets to `.spindrift/logs/`, where the launcher
# writes (HostLogDirFor): a repo-root `logs/` is always empty (#3934).
#
# The Forgejo `comment: >-` copies of the recover-park and dispatch-blocked
# comments are hand-maintained against the GitHub `--body` originals and have
# drifted before (issue #4010), so their normalized text is pinned equal too.
{ pkgs, ... }:
let
  inherit (pkgs.lib)
    assertMsg
    concatStringsSep
    filter
    findFirst
    hasInfix
    replaceStrings
    sublist
    trim
    ;
  inherit (pkgs.lib.lists) findFirstIndex;
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

  # Every agent workflow uploads its run logs. Any "logs/" left once the correct
  # dir is stripped out is a stale path, however it is quoted or prefixed.
  allWorkflows = githubWorkflows // forgejoWorkflows;
  badLogsDir = filter (
    name:
    let
      src = allWorkflows.${name};
    in
    !(
      hasInfix "path: .spindrift/logs/" src
      && !hasInfix "logs/" (replaceStrings [ ".spindrift/logs/" ] [ "" ] src)
    )
  ) (builtins.attrNames allWorkflows);
  # The strip above cannot see a `.spindrift/log/` typo in the marker path, so
  # pin the blocked-release step's test and read positively.
  badBlockedMarker = filter (
    name:
    let
      src = allWorkflows.${name};
    in
    !(
      hasInfix "-f .spindrift/logs/blocked.txt" src
      && hasInfix "cat .spindrift/logs/blocked.txt" src
    )
  ) [
    "agent-dispatch.yml"
    "forgejo/agent-dispatch.yml"
  ];

  # Forgejo folds a `comment: >-` block, so line breaks collapse to spaces;
  # fold GitHub's copy the same way before comparing. Same normalizer as
  # lib/renderers.nix's `oneLine`, which that file does not export.
  collapseWs =
    s:
    concatStringsSep " " (filter (x: x != "") (filter builtins.isString (builtins.split "[ \t\n]+" s)));

  singleCapture =
    pattern: what: file: src:
    let
      matches = filter builtins.isList (builtins.split pattern src);
    in
    assert assertMsg (
      builtins.length matches == 1
    ) "expected exactly one ${what} in ${file}, found ${toString (builtins.length matches)}";
    builtins.head (builtins.head matches);

  # `[^"\\]`/`[^\n]*`, not `.*` (newline semantics differ across regex libs); the
  # `\n` must be a "" escape — a `''`-quoted string passes a literal `\` and `n`.
  # The `\\.` alternative keeps an escaped `\"` from ending the payload early.
  ghBody = singleCapture "--body \"(([^\"\\\\]|\\\\.)*)\"" "`--body \"…\"` payload";
  ghRecoverReason = singleCapture "RECOVER_REASON: ([^\n]*)" "`RECOVER_REASON:` env line";

  # GitHub shell-escapes backticks and double quotes as \` and \" inside a
  # `--body "…"` double-quoted string; undo both before comparing prose.
  unescapeShell = replaceStrings [ "\\`" "\\\"" ] [ "`" "\"" ];

  # Approximates YAML `>-` folding: join the block's lines with a single space,
  # trimming each. As in YAML, a blank line does not end the block; only the
  # first non-blank line shallower than the block's first non-blank line does.
  # Unlike YAML, a blank line folds to a space rather than a newline, so a
  # Forgejo-only paragraph break does not register as drift.
  fjComment =
    what: src:
    let
      splitOnMarker = builtins.split "comment: >-\n" src;
      markerMatches = filter builtins.isList splitOnMarker;
      rest =
        assert assertMsg (
          builtins.length markerMatches == 1
        ) "expected exactly one `comment: >-` in ${what}";
        builtins.elemAt splitOnMarker 2;
      lines = filter builtins.isString (builtins.split "\n" rest);
      indent = builtins.head (builtins.match "( *).*" (findFirst (l: trim l != "") "" lines));
      hasIndent = l: builtins.substring 0 (builtins.stringLength indent) l == indent;
      endIdx = findFirstIndex (l: trim l != "" && !hasIndent l) (builtins.length lines) lines;
    in
    concatStringsSep " " (map trim (sublist 0 endIdx lines));

  # Each row names the comment, the shared basename both forges' workflow file
  # is keyed under, and the substring substitutions (zero or more) needed to
  # line up the GitHub `--body` prose with the Forgejo `comment: >-` prose.
  # The recover row reads its `to` from the GitHub env line, so the fallback
  # literal is pinned too; the dispatch row's `blockers` has no GitHub-side
  # expression to read, so its Forgejo expression is written out here.
  commentRows = [
    {
      name = "recover park comment";
      file = "agent-recover.yml";
      subst = [
        {
          from = "$RECOVER_REASON";
          to = ghRecoverReason "agent-recover.yml" githubWorkflows."agent-recover.yml";
        }
      ];
    }
    {
      name = "dispatch blocked-release comment";
      file = "agent-dispatch.yml";
      subst = [
        {
          from = "\${blockers}";
          to = "\${{ steps.blocked.outputs.blockers }}";
        }
      ];
    }
  ];

  commentPairs = map (row: {
    inherit (row) name file;
    ghNorm = collapseWs (
      replaceStrings (map (s: s.from) row.subst) (map (s: s.to) row.subst) (unescapeShell (ghBody row.file githubWorkflows.${row.file}))
    );
    fjNorm = collapseWs (fjComment "forgejo/${row.file}" forgejoWorkflows."forgejo/${row.file}");
  }) commentRows;
  commentMismatches = filter (p: p.ghNorm != p.fjNorm) commentPairs;
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

  agent-workflows-log-dir =
    assert assertMsg (
      badLogsDir == [ ]
    ) "agent workflow(s) do not upload `.spindrift/logs/` or still reference a stale `logs/` dir — the launcher writes to `.spindrift/logs/` (dispatch.HostLogDirFor, issue #3934): ${concatStringsSep ", " badLogsDir}";
    assert assertMsg (
      badBlockedMarker == [ ]
    ) "agent dispatch workflow(s) no longer test and read `.spindrift/logs/blocked.txt` — a dependency-blocked issue would never be released (issue #3934): ${concatStringsSep ", " badBlockedMarker}";
    pkgs.runCommand "agent-workflows-log-dir" { } "touch $out";

  agent-workflows-comment-parity =
    assert assertMsg (commentMismatches == [ ]) (
      concatStringsSep "; " (
        map (
          p:
          "${p.name} drifted between .github/workflows/${p.file} and .forgejo/workflows/${p.file} (issue #4010): github=\"${p.ghNorm}\" forgejo=\"${p.fjNorm}\""
        ) commentMismatches
      )
    );
    pkgs.runCommand "agent-workflows-comment-parity" { } "touch $out";
}
