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

  batsHarness = import ../lib/mkHarness.nix {
    inherit nixpkgs system;
    overlays = [ ghFakeOverlay ];
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
  };

  # The dogfood as a direct call, mirroring the `spindrift = { ... }` module
  # config below, so the equivalence check can prove the module and direct
  # paths yield byte-identical outputs.
  harness = import ../lib/mkHarness.nix dogfoodHarnessArgs;

  # The A/B twin of `harness` above (issue #2672): only `runtime` and
  # `daemonApp` differ, so the dogfood loop runs through the daemonless bwrap
  # runner without a second, hand-copied config drifting from the
  # podman-backed original. daemonApp points its daemon back at
  # `.#dogfood-bwrap` (issue #3538) so `nix run .#dogfood-bwrap-daemon`
  # drives a bwrap Box, not the podman one the schema default (`.#`) would
  # re-invoke.
  dogfoodBwrapHarness = import ../lib/mkHarness.nix (
    dogfoodHarnessArgs
    // {
      runtime = "bwrap";
      defaults = dogfoodHarnessArgs.defaults // {
        daemonApp = ".#dogfood-bwrap";
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
  # forgejo-backend harness (issue #1963). Eval-only, consumed by the
  # forgejo-cli-baked-only-for-forgejo-backend image check.
  forgejoHarness = import ../lib/mkHarness.nix {
    inherit nixpkgs system;
    defaults = {
      issueTracker = "forgejo";
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
      scoutModel = "anthropic/claude-x";
      reviewModel = "anthropic/claude-y";
      filerModel = "";
      workerModel = "anthropic/claude-z";
    };
    packages = p: [ p.hello ];
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

  dockerHarness = import ../lib/mkHarness.nix {
    inherit nixpkgs system;
    overlays = [ ghFakeOverlay ];
    runtime = "docker";
    packages = p: [ p.hello ];
  };

  # Proves the "rancher" runtime knob bakes as its own OCI-family value even
  # though it invokes nerdctl, not a "rancher" binary (issue #1274).
  rancherHarness = import ../lib/mkHarness.nix {
    inherit nixpkgs system;
    overlays = [ ghFakeOverlay ];
    runtime = "rancher";
    packages = p: [ p.hello ];
  };

  # The daemonless bubblewrap runner fixture (issue #54). nixInBox = false
  # because `bwrap build`'s EnsureReady (ADR 0042) reads the live
  # /nix/var/nix/db/db.sqlite, which a sandboxed `nix flake check` build cannot
  # reach, breaking every BWRAP_BUILD_CMD bats test (issue #2664). Named as a
  # set so equivalence.nix can build a twin that overrides only `prefetch`.
  bwrapHarnessArgs = {
    inherit nixpkgs system;
    overlays = [ ghFakeOverlay ];
    runtime = "bwrap";
    nixInBox = false;
    packages = p: [ p.hello ];
  };
  bwrapHarness = import ../lib/mkHarness.nix bwrapHarnessArgs;

  # The baked runtime is never on PATH, so `build`'s container fallback is
  # unavailable. Exercises the both-paths-impossible error, with the host build
  # faked to fail too.
  noRuntimeHarness = import ../lib/mkHarness.nix {
    inherit nixpkgs system;
    overlays = [ ghFakeOverlay ];
    runtime = "no-such-runtime";
    packages = p: [ p.hello ];
  };

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

  # The bwrap variant of skillsHarness, so bats can verify the bind-mount path.
  skillsBwrapHarness = import ../lib/mkHarness.nix {
    inherit nixpkgs system;
    overlays = [ ghFakeOverlay ];
    runtime = "bwrap";
    skills = [ bakedSkillFixture ];
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
    harness
    dogfoodBwrapHarness
    nonRustHarness
    leanHarness
    nixStoreWritableHarness
    extraClosuresHarness
    scoutOnlyHarness
    reviewerOnlyHarness
    reviewAxisTracksReviewerHarness
    reviewAxisOptOutHarness
    legacyFourEntryRosterHarness
    filerOnlyHarness
    workerOnlyHarness
    forgejoHarness
    opencodeHarness
    customHarness
    dockerHarness
    rancherHarness
    bwrapHarnessArgs
    bwrapHarness
    noRuntimeHarness
    promptHarness
    fixPromptHarness
    researchPromptHarness
    researchVerdictsHarness
    skillsHarness
    noSkillsHarness
    skillsBwrapHarness
    minimalDirect
    consumerPkgs
    consumerFormatter
    templatePkgs
    harnessNoRevision
    ;
}
