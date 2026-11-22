# ISSUE_ROUTE and the Tracker scope (ADR 0075, issue #7331) pinned at eval
# time through the real lib/mkHarness.nix entry point: a route reaches the run
# input document, a kind the tracker row has not declared fails, a label route
# evaluates on local, github and forgejo, and a Tracker scope that differs from
# the repo slug on github/forgejo fails.
{
  pkgs,
  nixpkgs,
  system,
  flake-parts,
  ...
}:
let
  inherit (pkgs.lib) assertMsg;
  trackerScope = import ../../lib/tracker-scope.nix { inherit (pkgs) lib; };

  mkHarnessWith =
    defaults:
    import ../../lib/mkHarness.nix {
      inherit nixpkgs system defaults;
      packages = p: [ p.hello ];
    };
  # .drvPath walks mkHarness's assert chain without an image build.
  evaluates = defaults: (builtins.tryEval (mkHarnessWith defaults).spindrift.drvPath).success;

  localRouted = mkHarnessWith {
    codeForge = "local";
    issueTracker = "local";
    issueRoute = {
      label = "orders-api";
    };
  };
  localRoutedString = mkHarnessWith {
    codeForge = "local";
    issueTracker = "local";
    issueRoute = "label=orders-api";
  };

  # The same route set through the flake option, which types it as an attrset.
  viaFlake =
    (flake-parts.lib.mkFlake
      {
        inputs = {
          inherit nixpkgs;
          self.outPath = ../../.;
        };
      }
      {
        systems = [ system ];
        imports = [ ../../lib/flakeModule.nix ];
        perSystem.spindrift = {
          infra.image.packages = p: [ p.hello ];
          forge.backend = "local";
          issues.tracker = "local";
          issues.route = {
            label = "orders-api";
          };
        };
      }
    ).packages.${system}.spindrift;

  issueRoute = import ../../lib/issue-route.nix { inherit (pkgs) lib; };
  inherit (pkgs.lib) hasInfix;

  scopeCases = row: scope: {
    inherit row scope;
    repoSlug = "acme/orders";
  };
  scopeOk = args: (builtins.tryEval (trackerScope.sameRepoOk args)).success;
  githubRow = import ../../lib/backends/github.nix;
  forgejoRow = import ../../lib/backends/forgejo.nix;
  jiraRow = import ../../lib/backends/jira.nix;
in
{
  issue-route-reaches-the-document =
    pkgs.runCommand "issue-route-reaches-the-document"
      {
        nativeBuildInputs = [ pkgs.jq ];
        flakeWrapper = viaFlake;
      }
      ''
        for doc in ${localRouted.internals.runInputDocumentFile} ${localRoutedString.internals.runInputDocumentFile}; do
          jq -e '.settings.ISSUE_ROUTE == "label=orders-api"' "$doc"
        done
        doc=$(grep -o '/nix/store/[^ ]*launcher-run-input.json' "$flakeWrapper/bin/spindrift")
        jq -e '.settings.ISSUE_ROUTE == "label=orders-api"' "$doc"
        touch $out
      '';

  issue-route-unset-evaluates =
    assert assertMsg (evaluates {
      codeForge = "github";
      issueTracker = "github";
    }) "an unset ISSUE_ROUTE must leave a github tracker evaluating";
    assert assertMsg (evaluates {
      codeForge = "github";
      issueTracker = "github";
      issueRoute = { };
    }) "an empty route attrset is the unset route";
    pkgs.runCommand "issue-route-unset-evaluates" { } "touch $out";

  issue-route-label-evaluates =
    let
      labelRoute = tracker: {
        codeForge = tracker;
        issueTracker = tracker;
        issueRoute = {
          label = "orders-api";
        };
      };
    in
    assert assertMsg (evaluates (
      labelRoute "github"
    )) "github declares label, so a label route must evaluate";
    assert assertMsg (evaluates (
      labelRoute "forgejo"
    )) "forgejo declares label, so a label route must evaluate";
    assert assertMsg (evaluates (
      labelRoute "github" // { issueRoute = "label=orders-api"; }
    )) "the string form of a label route must evaluate on github";
    pkgs.runCommand "issue-route-label-evaluates" { } "touch $out";

  issue-route-rejects-undeclared-kind =
    let
      route = {
        codeForge = "github";
        issueTracker = "github";
        issueRoute = {
          component = "orders-api";
        };
      };
    in
    assert assertMsg (!evaluates route) "github declares only label, so a component route must fail";
    assert assertMsg (
      !evaluates (route // { issueRoute = "component=orders-api"; })
    ) "the string form must fail on an undeclared kind too";
    assert assertMsg (
      !evaluates (
        route
        // {
          issueTracker = "local";
          codeForge = "local";
          issueRoute = {
            component = "orders-api";
          };
        }
      )
    ) "local declares only label, so a component route must fail";
    # tryEval cannot read a throw's message, so the message is pinned through
    # the pure function mkHarness throws from.
    let
      msg = issueRoute.kindsError {
        tracker = "github";
        declared = [ ];
        route = {
          label = "orders-api";
        };
      };
    in
    assert assertMsg (
      msg != null
      && hasInfix "label" msg
      && hasInfix "ISSUE_TRACKER=github" msg
      && hasInfix "does not declare" msg
    ) "the undeclared-kind message must name the kind and tracker and say it does not declare";
    assert assertMsg (
      issueRoute.kindsError {
        tracker = "local";
        declared = [ "label" ];
        route = "label=orders-api";
      } == null
    ) "a declared kind must produce no error";
    pkgs.runCommand "issue-route-rejects-undeclared-kind" { } "touch $out";

  issue-route-jira-declares-label-and-component =
    let
      jira = route: {
        codeForge = "github";
        issueTracker = "jira";
        issueRoute = route;
      };
    in
    assert assertMsg (evaluates (jira {
      label = "orders-api";
    })) "jira declares label, so a label route must evaluate";
    assert assertMsg (evaluates (jira {
      component = "orders-api";
    })) "jira declares component, so a component route must evaluate";
    assert assertMsg (evaluates (
      jira "label=x,component=orders-api"
    )) "jira must accept both kinds in the string form";
    assert assertMsg (
      !evaluates (jira {
        assignee = "orders-api";
      })
    ) "jira declares no assignee kind, so that route must fail";
    pkgs.runCommand "issue-route-jira-declares-label-and-component" { } "touch $out";

  issue-route-rejects-malformed-string =
    let
      bad = s: {
        codeForge = "local";
        issueTracker = "local";
        issueRoute = s;
      };
    in
    assert assertMsg (!evaluates (bad "label")) "a pair without = must fail";
    assert assertMsg (!evaluates (bad "label=")) "an empty value must fail";
    assert assertMsg (!evaluates (bad "=orders-api")) "an empty kind must fail";
    assert assertMsg (!evaluates (bad "label=a,,label=b")) "an empty pair must fail";
    assert assertMsg (
      !evaluates (bad {
        label = "a,b";
      })
    ) "a comma in an attrset value must fail";
    assert assertMsg (
      !(builtins.tryEval (issueRoute.render { "a,b" = "c"; })).success
    ) "a comma in an attrset kind must fail";
    pkgs.runCommand "issue-route-rejects-malformed-string" { } "touch $out";

  tracker-scope-must-equal-repo-slug =
    assert assertMsg (scopeOk (
      scopeCases githubRow "acme/orders"
    )) "github scope equal to the repo slug must pass";
    assert assertMsg (scopeOk (
      scopeCases forgejoRow "acme/orders"
    )) "forgejo scope equal to the repo slug must pass";
    assert assertMsg (
      !scopeOk (scopeCases githubRow "acme/billing")
    ) "github scope differing from the repo slug must fail";
    assert assertMsg (
      !scopeOk (scopeCases forgejoRow "acme/billing")
    ) "forgejo scope differing from the repo slug must fail";
    assert assertMsg (scopeOk (
      scopeCases jiraRow "ORD"
    )) "a jira project-key scope is not the repo slug and must pass";
    # The accepting path runs the real entry point: its scope is derived, so
    # github evaluates and a jira project key becomes the scope.
    assert assertMsg (evaluates {
      codeForge = "github";
      issueTracker = "github";
      repoSlug = "acme/orders";
    }) "mkHarness must accept github with a repo slug";
    assert assertMsg (
      trackerScope.of {
        row = {
          trackerScopeKnob = "jiraProjectKey";
        };
        mergedDefaults = {
          repoSlug = "acme/orders";
          jiraProjectKey = "ORD";
        };
      } == "ORD"
    ) "a row naming trackerScopeKnob scopes by that knob";
    assert assertMsg (
      trackerScope.of {
        row = { };
        mergedDefaults = {
          repoSlug = "acme/orders";
        };
      } == "acme/orders"
    ) "a row without one scopes by the repo slug";
    assert assertMsg (
      jiraRow.trackerScopeKnob == "jiraProjectKey"
    ) "the jira row must name its project key as the Tracker scope";
    pkgs.runCommand "tracker-scope-must-equal-repo-slug" { } "touch $out";
}
