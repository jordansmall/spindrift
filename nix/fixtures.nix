{
  pkgs,
  nixpkgs,
  system,
  flake-parts,
  revision ? "unknown",
}:
let
  # The launchers pin the real `gh` via runtimeInputs, which would shadow
  # a PATH-injected fake; so the bats-driven harnesses below overlay `gh`
  # with the recording fake, keeping the suite offline. podman/docker stay
  # unpinned host installs, so their fakes still resolve through PATH.
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

  # A plain harness whose launcher commands drive the bats suite: default
  # run knobs, a trivial toolchain, and the fake `gh` overlaid in.
  batsHarness = import ../lib/mkHarness.nix {
    inherit nixpkgs system;
    overlays = [ ghFakeOverlay ];
    packages = p: [ p.hello ];
  };

  # The dogfood as a direct call, mirroring the `spindrift = { ... }`
  # module config below. Kept so the equivalence check can prove the
  # module and direct paths yield byte-identical outputs.  Uses the
  # same revision as the dogfood module (passed in from flake.nix).
  harness = import ../lib/mkHarness.nix {
    inherit nixpkgs system revision;
    prefetch = "go mod download || true";
    packages = p: [
      p.go
      p.nil
    ];
    defaults = {
      mergeMode = "immediate";
    };
  };

  # The template's config as a direct call with revision = "unknown".
  # Used by template-fixture: the template module consumer has a stub self
  # with no shortRev, so its revision is "unknown"; this must match.
  # Does not include dogfood-only packages (e.g. nil).
  harnessNoRevision = import ../lib/mkHarness.nix {
    inherit nixpkgs system;
    prefetch = "go mod download || true";
    packages = p: [ p.go ];
  };

  # A minimal, non-Rust consumer, proving the engine bakes an arbitrary
  # `packages` set with no language-specific machinery. Kept off the
  # public outputs — the checks introspect it at eval time only.
  nonRustHarness = import ../lib/mkHarness.nix {
    inherit nixpkgs system;
    # Empty subagent tiers keep this a genuine no-model harness so the
    # agents-json-baked check can assert an empty AGENTS_JSON_TEMPLATE.
    defaults = {
      scoutModel = "";
      reviewModel = "";
    };
    packages = p: [ p.hello ];
  };

  # The lean/no-nix escape hatch: a Consumer that opts out of the
  # nix-in-box default for the smallest possible image. Eval-only.
  leanHarness = import ../lib/mkHarness.nix {
    inherit nixpkgs system;
    nixInBox = false;
    packages = p: [ p.hello ];
  };

  # Exercise the run knobs (#3): non-default baked `defaults` and a
  # docker `runtime`. Eval-only, consumed by the checks below.
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

  # The daemonless bubblewrap runner fixture (issue #54): exercises the
  # bwrap build/run path through the bats suite.
  bwrapHarness = import ../lib/mkHarness.nix {
    inherit nixpkgs system;
    overlays = [ ghFakeOverlay ];
    runtime = "bwrap";
    packages = p: [ p.hello ];
  };

  # A harness whose baked runtime is never on PATH, so `build`'s
  # container fallback is unavailable — used to exercise the
  # both-paths-impossible error (the host build is faked to fail too).
  noRuntimeHarness = import ../lib/mkHarness.nix {
    inherit nixpkgs system;
    overlays = [ ghFakeOverlay ];
    runtime = "no-such-runtime";
    packages = p: [ p.hello ];
  };

  # A Consumer-configured prompt (#4): proves the `prompt` argument is
  # what gets rendered to the store path and flows through to the agent.
  # The per-issue placeholders are escaped so they survive to run time.
  promptHarness = import ../lib/mkHarness.nix {
    inherit nixpkgs system;
    prompt = ''
      CONFIGURED-PROMPT-MARKER
      Implement issue #''${ISSUE_NUMBER}: ''${ISSUE_TITLE} on ''${BRANCH}
    '';
    packages = p: [ p.hello ];
  };

  # A Consumer-configured skill (#119): proves the `skills` argument bakes
  # the skill files into the image's skills path. Eval-only for the
  # skillsDir assertion; the image-layer check is Linux-gated.
  # Uses ghFakeOverlay so the run command drives the offline fake gh, not
  # the real gh pinned into runtimeInputs (same pattern as batsHarness).
  skillsHarness = import ../lib/mkHarness.nix {
    inherit nixpkgs system;
    overlays = [ ghFakeOverlay ];
    skills = [
      (pkgs.writeText "baked-skill.md" ''
        ---
        name: baked-skill
        description: A skill baked into the image at build time.
        ---
        BAKED-SKILL-MARKER
      '')
    ];
    packages = p: [ p.hello ];
  };

  # The bwrap variant of the skills harness: same baked skills but with the
  # daemonless bwrap runner so bats can verify the bind-mount path.
  skillsBwrapHarness = import ../lib/mkHarness.nix {
    inherit nixpkgs system;
    overlays = [ ghFakeOverlay ];
    runtime = "bwrap";
    skills = [
      (pkgs.writeText "baked-skill.md" ''
        ---
        name: baked-skill
        description: A skill baked into the image at build time.
        ---
        BAKED-SKILL-MARKER
      '')
    ];
    packages = p: [ p.hello ];
  };

  # A minimal flake-parts consumer fixture (#5), standing in for a
  # downstream flake. Evaluated in-repo (no separate lock / no network)
  # via a nested `mkFlake`; the checks compare its outputs to the
  # equivalent direct `mkHarness` call.
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

  # The `templates.default` starter, evaluated as a fixture (#6): call
  # its real `outputs` directly — no `nix flake init`, no network —
  # wiring `spindrift` to THIS checkout instead of the github input. The
  # full Linux image realize is verified out-of-band via the podman
  # builder; here we assert eval + the image store path resolving into
  # the launcher commands.
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
    ghFakeOverlay
    batsHarness
    harness
    nonRustHarness
    leanHarness
    customHarness
    dockerHarness
    bwrapHarness
    noRuntimeHarness
    promptHarness
    skillsHarness
    skillsBwrapHarness
    minimalDirect
    consumerPkgs
    consumerFormatter
    templatePkgs
    harnessNoRevision
    ;
}
