# The dogfood's tuned leaf values, defined once for both flake.nix's
# `spindrift` module config and nix/fixtures.nix's direct mkHarness mirror, so
# the equivalence checks (flakemodule-equivalence, flakemodule-dogfood-drivers)
# compare two wiring paths rather than two hand-copied value sets (issue #459).
{ system, lib }:
let
  isLinux = builtins.match ".*-linux" system != null;
  rosterLib = import ../lib/roster.nix { inherit lib; };
in
{
  # `nix flake archive` warms flake inputs alongside the Go module cache so a
  # later in-box `nix flake check` doesn't hit the network cold (ADR 0008,
  # issue #470).
  prefetch = "go mod download || true\nnix flake archive || true";
  packages = p: [
    p.go
    p.nil
    p.bats
    p.shellcheck
    # `nix run .#gate` evaluates the inbox checks with it (issue #5862).
    p.nix-eval-jobs
  ];
  # Self-test mode (ADR 0018, issue #469): a writable store lets a Box working a
  # spindrift issue run real checks in-box (issue #470) instead of
  # round-tripping CI for nix feedback.
  nixStoreWritable = true;
  # Bake the rest of the nix/checks/ dependency closure so in-box checks don't
  # cold-substitute it. The packages above and the ones every mkHarness image
  # bakes already cover most checks; `nixfmt` and `mandoc` are the remaining
  # gap (issue #470).
  extraClosures = p: [
    p.nixfmt
    p.mandoc
  ];
  # Pinned by roster entry name (issues #2386, #2388, #2426), mirroring
  # lib/default-model-fixture.nix's dogfoodPins; scout inherits the schema
  # default (issues #2433, #2434, #2435). `filer`'s default is empty, which
  # would leave findings in PR bodies (#393). `reviewer` and `worker` run the
  # 5.5 models ahead of the schema defaults. The review pass runs at the
  # reviewer's roster effort, so no `reviewEffort` knob is set (issue #2512).
  roster = rosterLib.defaultRoster {
    models = {
      filer = "claude-haiku-5-5";
      reviewer = "claude-opus-5-5";
      worker = "claude-sonnet-5-5";
    };
  };
  # The opencode Driver entry (ADR 0064, issue #5054) proves Driver switching,
  # untuned. claude's values stay top-level because the fixtures mirror and
  # the pin checks read them there; dogfood-roster-names-only-pins reads the
  # first `models` attrset in this file, so this one must stay below claude's.
  # Ids are namespaced for github-copilot, opencode's supported Provider. The
  # `""` worker only states intent (an inherited bare default would be dropped
  # anyway); a namespaced worker model would provision it.
  opencode = {
    model = "github-copilot/claude-opus-4.5";
    roster = rosterLib.defaultRoster {
      models = {
        scout = "github-copilot/claude-haiku-4.5";
        filer = "github-copilot/claude-haiku-4.5";
        reviewer = "github-copilot/claude-opus-4.5";
        worker = "";
      };
    };
  };
  # The pi Driver entry runs on a self-hosted Provider (issue #6049): wadsworth
  # serves GLM-5.3-Flash, supplied through PI_MODELS_CONTENT. Scout and filer
  # use it too: the NPU's qwen3.5:4b overflowed its 32k context on #6003. Each
  # role pins only low or high effort (riding the model id as
  # `<model>:<level>`): GLM's chat template treats any other as max.
  # review-axis stays unpinned: it follows the reviewer's model and inherits
  # its effort from rosterDefaults.
  pi = {
    model = "wadsworth/glm-5.3-flash";
    roster = rosterLib.defaultRoster {
      byName = {
        scout = {
          model = "wadsworth/glm-5.3-flash";
          effort = "low";
        };
        filer = {
          model = "wadsworth/glm-5.3-flash";
          effort = "low";
        };
        reviewer = {
          model = "wadsworth/glm-5.3-flash";
          effort = "high";
        };
        worker = {
          model = "wadsworth/glm-5.3-flash";
          effort = "high";
        };
      };
    };
  };
  defaults = {
    repoSlug = "jordansmall/spindrift";
    model = "claude-opus-5-5";
    effort = "high";
    mergeMode = "immediate";
    # Behaviour-changing files merge by hand whatever their origin (ADR 0062,
    # issue #4949). Setting the knob replaces the shipped default, so it is
    # restated here; the shipped default itself stays unchanged.
    mergeGuardPaths = lib.concatStringsSep "," [
      (import ../lib/env-schema.nix).mergeGuardPaths.default
      "templates/**"
      "skills/**"
      # Matches no root directory today (prompt fragments sit under
      # templates/**); kept because ADR 0062 lists it.
      "fragments/**"
      "lib/chore-catalog.nix"
      "lib/env-schema.nix"
      # Row-owned knobs (ADR 0068) put part of the env schema on registry
      # rows, folded by lib/row-knobs.nix from the registries
      # lib/row-knob-registries.nix lists (issues #5634, #5686).
      "lib/backends/**"
      "lib/chores/**"
      "lib/runners/**"
      "lib/row-knobs.nix"
      "lib/row-knob-registries.nix"
      "flake.nix"
      # Holds the model/effort pins and this guard list itself; the fixture
      # mirrors the pins (issue #4974).
      "nix/dogfood-defaults.nix"
      "lib/default-model-fixture.nix"
      # Which Connectors dogfood shadows (issue #6797).
      "nix/dogfood-shadow.nix"
      # Which Connectors dogfood runs as primary (issue #6998).
      "nix/dogfood-primary.nix"
      # The accepted Route baseline a widened Connector table is reviewed against
      # (ADR 0073).
      "spindrift-connector-routes.json"
    ];
    autoFormat = true;
    autoLint = true;
    # Dogfood the host-mediated read-only path (ADR 0034, #1916-#1919): the Box
    # makes no forge or tracker writes, and the launcher relays branch, draft
    # PR, and comment writes host-side.
    boxForgeAndIssueAccess = "read-only";
    # On darwin and windows podman runs in a fixed-RAM VM and needs the cap
    # (issue #712); native Linux shares host RAM with the container, so no cap
    # is needed there (issue #2379).
    memoryLimit = if isLinux then "" else "5g";
    # Butler paused: an empty list drops the butler kind from a bare daemon.
    # Restore "bugs docs-drift" to resume; the caps below are inert until then.
    butlerChores = "";
    butlerMaxPromotionsPerDay = 5;
    # Let the host land up to five docs-drift patch PRs a day (ADR 0057), on
    # the default patch classes and paths; mergeMode = "immediate" merges
    # each once green.
    butlerMaxPatchesPerDay = 5;
    # Sweep more often than the 6h/8-sweep defaults; the findings cap rises
    # too, or two full sweeps leave less than one sweep's headroom and end
    # the butler's day early.
    butlerEvery = "2h";
    butlerMaxSweepsPerDay = 16;
    butlerMaxFindingsPerDay = 30;
    # A cold checks-inbox gate outruns claude's 10-minute stock Bash cap
    # (issue #4409) and opencode's 2-minute default (issue #5351); 30 minutes
    # lets it finish in one blocking call.
    driverBashTimeoutMs = 1800000;
    # A four-slot Daemon pool: at six, concurrent checks-inbox gates contend
    # for host CPU and roughly double each gate. The daemon reads MAX_PARALLEL
    # from the baked settings only, so harness.env cannot change it.
    maxParallel = 4;
  };
}
