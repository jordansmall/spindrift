{
  pkgs,
  nixpkgs,
  system,
  flake-parts,
  caveman,
  matt-skills,
  jordan-skills,
  revision ? "unknown",
}:
let
  # Shared with flake.nix's `spindrift` module config so the two wiring paths
  # below can never drift (issue #459).
  dogfoodDefaults = import ./dogfood-defaults.nix {
    inherit system;
    lib = pkgs.lib;
  };

  rosterLib = import ../lib/roster.nix { inherit (pkgs) lib; };

  # Shared with flake.nix's `spindrift` module config the same way (issue #486).
  dogfoodSkills = import ./dogfood-skills.nix {
    inherit caveman matt-skills jordan-skills;
  };

  # The launchers pin the real `gh` via runtimeInputs, which shadows a
  # PATH-injected fake, so the bats harnesses below overlay `gh` with the
  # recording fake to keep the suite offline. podman/docker stay unpinned host
  # installs, so their fakes still resolve through PATH.
  ghFakeOverlay = _final: prev: {
    gh = prev.runCommand "fake-gh" { } ''
      mkdir -p $out/bin
      # The launcher execs this by path, so rewrite the fake's
      # `#!/usr/bin/env bash` to the store bash — a sandboxed Linux
      # build has no /usr/bin/env.
      substitute ${../tests/fakes/gh} $out/bin/gh \
        --replace '#!/usr/bin/env bash' "#!${prev.bash}/bin/bash"
      chmod +x $out/bin/gh
    '';
  };

  batsHarnessArgs = {
    inherit nixpkgs system;
    overlays = [ ghFakeOverlay ];
    packages = p: [ p.hello ];
  };

  batsHarness = import ../lib/mkHarness.nix batsHarnessArgs;

  # batsHarness as a multi-Driver flake renders it (ADR 0064, issue #5053):
  # the wrapper layers harness.<driver>.env over harness.env.
  multiDriverBatsHarness = import ../lib/mkHarness.nix (batsHarnessArgs // { multiDriver = true; });

  # Rendered artifacts for the Go seam tests (issue #4280).
  seamFixtures = import ./seam-fixtures.nix {
    inherit
      pkgs
      batsHarness
      dockerHarness
      bwrapHarness
      butlerHarness
      opencodeHarness
      ;
  };

  # The batsHarness document with a provisioned Filer and a Chore enabled,
  # the two things `spindrift butler` preflight needs from the document
  # (FILER_ENABLED, BUTLER_CHORES). Rendered for the Go seam smoke matrix.
  butlerHarness = import ../lib/mkHarness.nix {
    inherit nixpkgs system;
    overlays = [ ghFakeOverlay ];
    defaults = {
      filerModel = "seam-filer";
      butlerChores = "bugs";
    };
    packages = p: [ p.hello ];
  };

  # Bound once and shared verbatim by every dogfood mkHarness call below, so a
  # future nix/dogfood-defaults.nix key threaded into only one call site can't
  # turn the podman/bwrap A/B into a comparison of diverging configs instead of
  # a runtime-only one (issue #2672).
  dogfoodHarnessArgs = {
    inherit nixpkgs system revision;
    inherit (dogfoodDefaults)
      prefetch
      packages
      defaults
      nixStoreWritable
      extraClosures
      roster
      ;
    skills = dogfoodSkills;
    # flake.nix declares the opencode Driver beside claude, so the module
    # renders the default Harness with harness.<driver>.env layering on.
    # dogfoodBwrapHarness inherits it, so `.#dogfood-bwrap` layers too.
    multiDriver = true;
  };

  # The dogfood as a direct call, mirroring the `spindrift = { ... }` module
  # config below, so the equivalence check can prove the module and direct
  # paths yield byte-identical outputs.
  harness = import ../lib/mkHarness.nix dogfoodHarnessArgs;

  # The A/B twin of `harness` above (issue #2672): only `runtime`,
  # `daemonApp`, and `daemonSelfApp` differ, so the dogfood loop runs through
  # the daemonless bwrap runner without a second, hand-copied config
  # drifting from the podman-backed original. daemonApp points its daemon
  # back at `.#dogfood-bwrap` (issue #3538) so `nix run .#dogfood-bwrap-daemon`
  # drives a bwrap Box, not the podman one the schema default (`.#`) would
  # re-invoke. daemonSelfApp points at `.#dogfood-bwrap-daemon` (issue #3543):
  # flake.nix exposes this harness's `apps.daemon` under that top-level name,
  # so the daemon's own self-check must name the attribute it was actually
  # launched as, or it would evaluate the *default* (podman) harness's
  # daemon and see a permanent mismatch.
  dogfoodBwrapHarness = import ../lib/mkHarness.nix (
    dogfoodHarnessArgs
    // {
      runtime = bwrapRuntime;
      defaults = dogfoodHarnessArgs.defaults // {
        daemonApp = ".#dogfood-bwrap";
        daemonSelfApp = ".#dogfood-bwrap-daemon";
      };
    }
  );

  # The opencode Driver's Harness as flake.nix declares it and the module's
  # otherHarness renders it, so the equivalence check can pin its content, not
  # just that it differs from claude's. A non-default Driver never inherits
  # the default entry's Driver-valued env keys (effort, driverBashTimeoutMs);
  # flake.nix sets opencode's own bashTimeoutMs, so it is re-added below.
  # The list is hand-written on purpose, independent of driver-map.nix's
  # driverValued.env, so a new top-level Driver-valued default must join it.
  dogfoodOpencodeHarness = import ../lib/mkHarness.nix (
    dogfoodHarnessArgs
    // {
      driver = "opencode";
      roster = dogfoodDefaults.opencode.roster;
      imageAttrName = "agent-image-opencode";
      defaults =
        removeAttrs dogfoodHarnessArgs.defaults [
          "effort"
          "driverBashTimeoutMs"
        ]
        // {
          model = dogfoodDefaults.opencode.model;
          driverBashTimeoutMs = dogfoodDefaults.defaults.driverBashTimeoutMs;
          daemonApp = ".#opencode";
          daemonSelfApp = ".#opencode-daemon";
        };
    }
  );

  # The pi Driver's Harness, mirroring dogfoodOpencodeHarness. flake.nix sets
  # no bashTimeoutMs for pi (its row declares no timeout variable), so nothing
  # is re-added. The args are shared with dogfoodBwrapPiHarness, which differs
  # only in runner, imageAttrName and daemon app names (issue #6064).
  dogfoodPiHarnessArgs = dogfoodHarnessArgs // {
    driver = "pi";
    roster = dogfoodDefaults.pi.roster;
    imageAttrName = "agent-image-pi";
    defaults =
      removeAttrs dogfoodHarnessArgs.defaults [
        "effort"
        "driverBashTimeoutMs"
      ]
      // {
        model = dogfoodDefaults.pi.model;
        daemonApp = ".#pi";
        daemonSelfApp = ".#pi-daemon";
      };
  };
  dogfoodPiHarness = import ../lib/mkHarness.nix dogfoodPiHarnessArgs;

  # The pi Driver in a bwrap Box (issue #6064), the pi twin of
  # dogfoodBwrapHarness. A bwrap harness publishes its closure under
  # imageAttrName, so the inherited "agent-image-pi" would collide with the
  # podman pi image and FLAKE_IMAGE_ATTR would name the wrong closure.
  dogfoodBwrapPiHarness = import ../lib/mkHarness.nix (
    dogfoodPiHarnessArgs
    // {
      runtime = bwrapRuntime;
      imageAttrName = "agent-closure-pi";
      defaults = dogfoodPiHarnessArgs.defaults // {
        daemonApp = ".#dogfood-bwrap-pi";
        daemonSelfApp = ".#dogfood-bwrap-pi-daemon";
      };
    }
  );

  # Used by template-fixture: the template module consumer has a stub self with
  # no shortRev, so its revision is "unknown" and this call must match by
  # leaving `revision` unset.
  harnessNoRevision = import ../lib/mkHarness.nix {
    inherit nixpkgs system;
    prefetch = "go mod download || true";
    packages = p: [ p.go ];
  };

  # Proves the engine bakes an arbitrary `packages` set with no
  # language-specific machinery. Eval-only: the checks introspect it, nothing
  # builds it.
  nonRustHarness = import ../lib/mkHarness.nix {
    inherit nixpkgs system;
    # Empty tiers keep this a genuine no-model harness, so agents-json-baked
    # can assert an empty AGENTS_JSON_TEMPLATE. scoutModel, reviewModel and
    # workerModel each need an explicit "" because their schema defaults are
    # non-empty (issues #2433, #2054); filerModel already defaults to empty.
    defaults = {
      scoutModel = "";
      reviewModel = "";
      workerModel = "";
    };
    packages = p: [ p.hello ];
  };

  # A Consumer that opts out of the nix-in-box default for the smallest
  # possible image. Eval-only.
  leanHarness = import ../lib/mkHarness.nix {
    inherit nixpkgs system;
    nixInBox = false;
    packages = p: [ p.hello ];
  };

  # Self-test mode opted in (ADR 0018, issue #469): /nix/store becomes
  # agent-writable and mkHarness bakes the entrypoint's warning marker. Only
  # the Linux checks that inspect the built image realize it.
  nixStoreWritableHarness = import ../lib/mkHarness.nix {
    inherit nixpkgs system;
    nixStoreWritable = true;
    packages = p: [ p.hello ];
  };

  # Proves an arbitrary derivation, unrelated to `packages`, lands in the image
  # and store DB (issue #469). No other fixture bakes cowsay, so its presence
  # is unambiguous.
  extraClosuresHarness = import ../lib/mkHarness.nix {
    inherit nixpkgs system;
    extraClosures = p: [ p.cowsay ];
    packages = p: [ p.hello ];
  };

  # Proves mkHarness bakes each subagent into `--agents` independently rather
  # than as an all-or-nothing pair. Eval-only, consumed by agents-json-baked.
  # Every single-agent fixture below pins workerModel to empty because its
  # schema default is non-empty, so leaving it unset bakes a worker entry
  # alongside the one under test.
  scoutOnlyHarness = import ../lib/mkHarness.nix {
    inherit nixpkgs system;
    defaults = {
      scoutModel = "solo-scout";
      reviewModel = "";
      workerModel = "";
    };
    packages = p: [ p.hello ];
  };

  # The reviewer-only mirror of scoutOnlyHarness.
  reviewerOnlyHarness = import ../lib/mkHarness.nix {
    inherit nixpkgs system;
    defaults = {
      scoutModel = "";
      reviewModel = "solo-reviewer";
      workerModel = "";
    };
    packages = p: [ p.hello ];
  };

  # review-axis (issue #3447, ADR 0049) has no model knob of its own: it tracks
  # whatever the reviewer entry resolves to. This fixture pins the reviewer
  # through the recommended `byName` knob, leaving the deprecated positional
  # reviewModel unset. Eval-only, consumed by agents-json-baked.
  reviewAxisTracksReviewerHarness = import ../lib/mkHarness.nix {
    inherit nixpkgs system;
    defaults = {
      scoutModel = "";
      workerModel = "";
    };
    byName.reviewer.model = "pinned-reviewer";
    packages = p: [ p.hello ];
  };

  # The opt-out mirror of reviewAxisTracksReviewerHarness: the reviewer opts
  # out through the #392 "" sentinel, so review-axis must go with it.
  # scoutModel stays set to keep the baked template non-empty, otherwise the
  # absence assertions pass vacuously against an empty AGENTS_JSON_TEMPLATE.
  # Eval-only, consumed by agents-json-baked.
  reviewAxisOptOutHarness = import ../lib/mkHarness.nix {
    inherit nixpkgs system;
    defaults = {
      scoutModel = "axis-optout-scout";
      workerModel = "";
    };
    byName.reviewer.model = "";
    packages = p: [ p.hello ];
  };

  # The historical four entries, reviewer present and review-axis absent, so
  # the /code-review fan-out falls back to the Driver's ungoverned default
  # (issue #3447). Filters defaultRoster rather than hand-writing the entries,
  # so it keeps the real shape normalizeRoster contracts for. Eval-only,
  # consumed by the roster-explicit-roster-* warning checks.
  legacyFourEntryRosterHarness = import ../lib/mkHarness.nix {
    inherit nixpkgs system;
    roster = builtins.filter (e: e.name != "review-axis") (rosterLib.defaultRoster { });
    packages = p: [ p.hello ];
  };

  # Provider-namespaced ids for the opencode Harnesses that inherit schema
  # defaults, which are bare claude ids and fail eval under opencode (issue
  # #5355). workerModel is left to the inherited bare default on purpose: the
  # worker drop (issue #5331) is silent and many fixtures exercise it.
  opencodeDefaultModel = "anthropic/claude-sonnet-5";
  opencodeNamespacedDefaults = {
    model = opencodeDefaultModel;
    scoutModel = "anthropic/claude-haiku-5";
    reviewModel = "anthropic/claude-opus-5";
    filerModel = "anthropic/claude-haiku-5";
  };
  # An opencode Harness over the given `defaults`, for the eval-level checks
  # that probe the namespaced-model rule (issue #5355).
  opencodeHarnessWith =
    defaults:
    import ../lib/mkHarness.nix {
      inherit nixpkgs system defaults;
      driver = "opencode";
      packages = p: [ p.hello ];
    };

  # Explicit opencode roster holding only a worker, bare and namespaced
  # (issue #5331). Eval-only, consumed by the roster-opencode-*-worker checks.
  opencodeWorkerRosterHarness =
    model:
    import ../lib/mkHarness.nix {
      inherit nixpkgs system;
      driver = "opencode";
      defaults.model = opencodeDefaultModel;
      roster = [
        {
          name = "worker";
          inherit model;
          mode = "subagent";
          description = "fixture worker description";
          tools = [ ];
        }
      ];
      packages = p: [ p.hello ];
    };
  opencodeBareWorkerRosterHarness = opencodeWorkerRosterHarness "gpt-4o";
  opencodeNamespacedWorkerRosterHarness = opencodeWorkerRosterHarness "openai/gpt-4o";

  # The legacy `defaults.workerModel` knob set bare under opencode (issue
  # #5331); no roster, so the drop must still warn.
  opencodeLegacyBareWorkerHarness = import ../lib/mkHarness.nix {
    inherit nixpkgs system;
    driver = "opencode";
    defaults = opencodeNamespacedDefaults // {
      workerModel = "gpt-4o";
    };
    packages = p: [ p.hello ];
  };

  # Proves the opt-in, default-empty filer model composes on its own rather
  # than needing scout or reviewer set. Eval-only, consumed by
  # agents-json-baked.
  filerOnlyHarness = import ../lib/mkHarness.nix {
    inherit nixpkgs system;
    defaults = {
      scoutModel = "";
      reviewModel = "";
      filerModel = "solo-filer";
      workerModel = "";
    };
    packages = p: [ p.hello ];
  };

  # Proves the worker model composes on its own (issue #2054). Unlike filer,
  # WORKER_MODEL's schema default is non-empty, so scout/reviewer/filer are
  # pinned empty here to leave the worker the only baked entry. Eval-only,
  # consumed by agents-json-baked.
  workerOnlyHarness = import ../lib/mkHarness.nix {
    inherit nixpkgs system;
    defaults = {
      scoutModel = "";
      reviewModel = "";
      filerModel = "";
      workerModel = "solo-worker";
    };
    packages = p: [ p.hello ];
  };

  # Proves mkHarness bakes fj (forgejo-cli) into the image only for a
  # forgejo-backend harness (issue #1963), whichever axis names forgejo and
  # once when both do (issue #5768). Eval-only, consumed by the
  # forgejo-cli-baked-only-for-forgejo-backend image check.
  forgejoHarness = import ../lib/mkHarness.nix {
    inherit nixpkgs system;
    defaults = {
      issueTracker = "forgejo";
    };
    packages = p: [ p.hello ];
  };

  # forgejo on the code-forge axis alone; the tracker keeps its github
  # default.
  forgejoForgeHarness = import ../lib/mkHarness.nix {
    inherit nixpkgs system;
    defaults = {
      codeForge = "forgejo";
    };
    packages = p: [ p.hello ];
  };

  # forgejo on both axes, so the union must dedupe to a single fj.
  forgejoBothHarness = import ../lib/mkHarness.nix {
    inherit nixpkgs system;
    defaults = {
      issueTracker = "forgejo";
      codeForge = "forgejo";
    };
    packages = p: [ p.hello ];
  };

  # The opencode Driver's on-disk agent files (issue #262 slice 5, AC4).
  # filerModel stays empty so the filer file's omission is provable too.
  # Eval-only, consumed by the opencode-agent-files image check.
  opencodeHarness = import ../lib/mkHarness.nix {
    inherit nixpkgs system;
    driver = "opencode";
    defaults = {
      model = opencodeDefaultModel;
      scoutModel = "anthropic/claude-x";
      reviewModel = "anthropic/claude-y";
      filerModel = "";
      workerModel = "anthropic/claude-z";
    };
    packages = p: [ p.hello ];
  };

  # The pi Harness over arbitrary defaults, for the effort-on-model checks
  # (issue #5483): a bad `reviewEffort` is the reject fixture.
  piHarnessWith =
    defaults:
    import ../lib/mkHarness.nix {
      inherit nixpkgs system defaults;
      driver = "pi";
      packages = p: [ p.hello ];
    };

  # The pi Driver's Harness: the pi roster bakes, the
  # session-cache-mountpoint-baked-pi image check (issue #5481), and the
  # pi-hook-bridge image checks (issue #5482) share it.
  piHarness = import ../lib/mkHarness.nix {
    inherit nixpkgs system;
    driver = "pi";
    packages = p: [ p.hello ];
    # pi model ids are `<provider>/<model>`, like opencode's.
    defaults = opencodeNamespacedDefaults;
  };

  # The pi Harness with a Provider-namespaced worker too, so the bare-worker
  # gate (issue #5331) drops nothing and the full roster provisions (issue
  # #5483).
  piFullRosterHarness = import ../lib/mkHarness.nix {
    inherit nixpkgs system;
    driver = "pi";
    packages = p: [ p.hello ];
    defaults = opencodeNamespacedDefaults // {
      workerModel = "anthropic/claude-sonnet-5";
    };
  };

  # Exercises the run knobs (#3) with non-default baked `defaults`.
  customHarness = import ../lib/mkHarness.nix {
    inherit nixpkgs system;
    overlays = [ ghFakeOverlay ];
    defaults = {
      label = "custom-label";
      baseBranch = "develop";
      maxParallel = 5;
      branchPrefix = "bot/";
      inProgressLabel = "custom-wip";
      failedLabel = "custom-broken";
      scoutModel = "custom-scout";
      reviewModel = "custom-reviewer";
      completeLabel = "custom-done";
    };
    packages = p: [ p.hello ];
  };

  # One harness per runtime the runner rows list, so a new row is covered by
  # nix/checks/equivalence.nix without editing it.
  runners = import ../lib/runners/index.nix { };
  # The store-closure runner row and its runtime, read from the row so no check
  # names either.
  bwrapRow = import ../lib/runners/bwrap.nix;
  bwrapRuntime = builtins.head bwrapRow.runtimes;
  runtimeHarnessArgs =
    runtime:
    {
      inherit nixpkgs system runtime;
      overlays = [ ghFakeOverlay ];
      packages = p: [ p.hello ];
    }
    # nixInBox = false on a store-closure runner because `bwrap build`'s
    # EnsureReady (ADR 0042) reads the live /nix/var/nix/db/db.sqlite, which a
    # sandboxed `nix flake check` build cannot reach (issue #2664).
    // pkgs.lib.optionalAttrs ((runners.forRuntime runtime).artifact == "store-closure") {
      nixInBox = false;
    };
  runtimeHarnesses = pkgs.lib.genAttrs runners.runtimes (
    runtime: import ../lib/mkHarness.nix (runtimeHarnessArgs runtime)
  );

  # Named handles into runtimeHarnesses for the checks and seam fixtures that
  # test one runtime's own behaviour.
  # The docker seam golden (nix/seam-fixtures.nix) pins docker specifically.
  dockerHarness = runtimeHarnesses.docker;
  # Exposed as args so equivalence.nix can build a twin that overrides only
  # `prefetch`.
  bwrapHarnessArgs = runtimeHarnessArgs bwrapRuntime;
  bwrapHarness = runtimeHarnesses.${bwrapRuntime};

  # Proves the `prompt` argument is what gets rendered to the store path and
  # reaches the agent (#4). The per-issue placeholders are escaped so they
  # survive to run time.
  promptHarness = import ../lib/mkHarness.nix {
    inherit nixpkgs system;
    prompt = ''
      CONFIGURED-PROMPT-MARKER
      Implement issue #''${ISSUE_NUMBER}: ''${ISSUE_TITLE} on ''${BRANCH}
    '';
    packages = p: [ p.hello ];
  };

  # Proves a fix-prompt carrying only a fix-specific preamble, with no
  # COMMS/CHECK/outcome-contract markers, still gets all three shared blocks
  # injected (issue #455).
  fixPromptHarness = import ../lib/mkHarness.nix {
    inherit nixpkgs system;
    fixPrompt = ''
      CONFIGURED-FIX-PROMPT-MARKER
      Fix issue #''${ISSUE_NUMBER}: ''${ISSUE_TITLE} on ''${BRANCH}
    '';
    packages = p: [ p.hello ];
  };

  # Proves a research prompt carrying only a research-specific preamble, with
  # no "# POST THE VERDICT" marker, still gets the research outcome-contract
  # block injected (issue #640).
  researchPromptHarness = import ../lib/mkHarness.nix {
    inherit nixpkgs system;
    researchPrompt = ''
      CONFIGURED-RESEARCH-PROMPT-MARKER
      Research issue #''${ISSUE_NUMBER}: ''${ISSUE_TITLE}
    '';
    packages = p: [ p.hello ];
  };

  # Proves a custom RESEARCH_VERDICTS set reaches the baked research prompt's
  # verdict contract, not only the launcher (issue #2201).
  # nix/checks/prompts.nix greps the rendered research-prompt.md.
  researchVerdictsHarness = import ../lib/mkHarness.nix {
    inherit nixpkgs system;
    defaults = {
      researchVerdicts = builtins.toJSON [
        {
          verdict = "approve";
          label = "agent-research-approve";
          description = "relevant and worth doing; promote it.";
        }
        {
          verdict = "decline";
          label = "agent-research-decline";
          description = "not worth doing.";
        }
      ];
    };
    packages = p: [ p.hello ];
  };

  # Proves the `skills` argument bakes skill files into the image's skills path
  # (#119). A { name; src; } content entry (issue #597) rather than a pre-built
  # pkgs.writeText derivation, so the fixture carries no host-tagged skill into
  # either harness below.
  bakedSkillFixture = {
    name = "baked-skill";
    src = ''
      ---
      name: baked-skill
      description: A skill baked into the image at build time.
      ---
      BAKED-SKILL-MARKER
    '';
  };

  skillsHarness = import ../lib/mkHarness.nix {
    inherit nixpkgs system;
    overlays = [ ghFakeOverlay ];
    skills = [ bakedSkillFixture ];
    packages = p: [ p.hello ];
  };

  # No Consumer `skills` at all, proving the harness-owned skill (issue #2489)
  # bakes into the image regardless of Consumer config.
  noSkillsHarness = import ../lib/mkHarness.nix {
    inherit nixpkgs system;
    overlays = [ ghFakeOverlay ];
    packages = p: [ p.hello ];
  };

  # A minimal flake-parts consumer (#5), evaluated in-repo through a nested
  # `mkFlake` so it needs no separate lock and no network. The checks compare
  # its outputs to the equivalent direct `mkHarness` call.
  minimalDirect = import ../lib/mkHarness.nix {
    inherit nixpkgs system;
    packages = p: [ p.hello ];
  };
  moduleConsumer =
    flake-parts.lib.mkFlake
      {
        inputs = {
          inherit nixpkgs;
          self = {
            outPath = ../.;
          };
        };
      }
      {
        systems = [ system ];
        imports = [ ../lib/flakeModule.nix ];
        perSystem.spindrift.packages = p: [ p.hello ];
      };
  consumerPkgs = moduleConsumer.packages.${system};
  consumerFormatter = moduleConsumer.formatter.${system};

  # The `templates.default` starter (#6): call its real `outputs` directly, no
  # `nix flake init` and no network, wiring `spindrift` to this checkout
  # instead of the github input. The podman builder verifies the full Linux
  # image realize out of band; here the checks assert eval and the image store
  # path resolving into the launcher commands.
  templateOutputs = (import ../templates/default/flake.nix).outputs {
    inherit nixpkgs flake-parts;
    self = {
      outPath = ../templates/default;
    };
    spindrift = {
      flakeModules.default = ../lib/flakeModule.nix;
      lib.mkHarness = import ../lib/mkHarness.nix;
    };
  };
  templatePkgs = templateOutputs.packages.${system};
in
{
  inherit
    dogfoodSkills
    ghFakeOverlay
    batsHarness
    multiDriverBatsHarness
    seamFixtures
    harness
    dogfoodBwrapHarness
    dogfoodOpencodeHarness
    dogfoodPiHarness
    dogfoodBwrapPiHarness
    nonRustHarness
    leanHarness
    nixStoreWritableHarness
    extraClosuresHarness
    scoutOnlyHarness
    reviewerOnlyHarness
    reviewAxisTracksReviewerHarness
    reviewAxisOptOutHarness
    legacyFourEntryRosterHarness
    opencodeDefaultModel
    opencodeNamespacedDefaults
    opencodeHarnessWith
    opencodeBareWorkerRosterHarness
    opencodeNamespacedWorkerRosterHarness
    opencodeLegacyBareWorkerHarness
    opencodeWorkerRosterHarness
    filerOnlyHarness
    workerOnlyHarness
    forgejoHarness
    forgejoForgeHarness
    forgejoBothHarness
    opencodeHarness
    piHarness
    piHarnessWith
    piFullRosterHarness
    customHarness
    runtimeHarnesses
    dockerHarness
    bwrapRow
    bwrapRuntime
    bwrapHarnessArgs
    bwrapHarness
    promptHarness
    fixPromptHarness
    researchPromptHarness
    researchVerdictsHarness
    skillsHarness
    noSkillsHarness
    minimalDirect
    consumerPkgs
    consumerFormatter
    templatePkgs
    harnessNoRevision
    ;
}
