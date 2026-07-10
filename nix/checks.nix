{
  pkgs,
  config,
  fixtures,
  nixpkgs,
  system,
  flake-parts,
}:
let
  inherit (fixtures)
    batsHarness
    harness
    nonRustHarness
    leanHarness
    scoutOnlyHarness
    reviewerOnlyHarness
    filerOnlyHarness
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
  renderers = import ../lib/renderers.nix;
in
{
  shellcheck =
    pkgs.runCommand "shellcheck"
      {
        nativeBuildInputs = [ pkgs.shellcheck ];
      }
      ''
        # The launcher scripts are body fragments (they reference the
        # nix-rendered preamble), so they are shellcheck'd by
        # writeShellApplication at build time, not standalone here.
        shellcheck --shell=bash \
          ${../dogfood.sh} \
          ${../agent/entrypoint.sh} \
          ${../agent/format-transcript.sh} \
          ${../tests/fakes/runtime} \
          ${../tests/fakes/gh} \
          ${../tests/fakes/claude} \
          ${../tests/fakes/nix} \
          ${../tests/fakes/spindrift-heartbeat-filter} \
          ${../tests/helper.bash}
        touch $out
      '';

  # The bash layers under bats, driven entirely through fakes — no real
  # container, network, or LLM.
  bats =
    pkgs.runCommand "bats"
      {
        nativeBuildInputs = [
          pkgs.bats
          pkgs.bash
          pkgs.git
          pkgs.gettext
          pkgs.coreutils
          pkgs.gnugrep
          pkgs.gnused
          pkgs.jq
        ];
        # The launcher commands under test overlay `gh` with the fake
        # (batsHarness/customHarness/dockerHarness), since the real `gh`
        # is pinned into their runtimeInputs PATH and would otherwise
        # shadow a PATH-injected fake.
        RUN_CMD = "${batsHarness.run}/bin/run";
        SPINDRIFT_CMD = "${batsHarness.spindrift}/bin/spindrift";
        BUILD_CMD = "${batsHarness.build}/bin/build";
        BUILD_NO_RUNTIME_CMD = "${noRuntimeHarness.build}/bin/build";
        CUSTOM_RUN_CMD = "${customHarness.run}/bin/run";
        DOCKER_RUN_CMD = "${dockerHarness.run}/bin/run";
        BWRAP_RUN_CMD = "${bwrapHarness.run}/bin/run";
        BWRAP_BUILD_CMD = "${bwrapHarness.build}/bin/build";
        IMAGE_PATH = batsHarness.imagePath;
        ENTRYPOINT = ../agent/entrypoint.sh;
        FORMAT_TRANSCRIPT_SCRIPT = ../agent/format-transcript.sh;
        DOGFOOD_SH = ../dogfood.sh;
        PROMPTS_DIR = ../templates/default/prompts;
        # The baked default prompt dir the `run` command mounts, and a
        # Consumer-configured one whose rendered content flows through
        # to the stubbed agent (#4).
        PROMPT_PATH = batsHarness.promptDir;
        PROMPT_HARNESS_DIR = promptHarness.promptDir;
        # Harnesses with baked skills for skills-precedence tests.
        SKILLS_RUN_CMD = "${skillsHarness.run}/bin/run";
        SKILLS_BWRAP_RUN_CMD = "${skillsBwrapHarness.run}/bin/run";
        # Not read by bats directly, but forces Nix to realize
        # skillsBwrapHarness.agentFiles so its store path exists on disk when
        # the bwrap adapter calls os.Stat on the baked-skills subdirectory.
        # The run command embeds the path via unsafeDiscardStringContext, which
        # drops the Nix dependency — without this attr the path is absent.
        SKILLS_AGENT_FILES = skillsBwrapHarness.agentFiles;
      }
      ''
        export HOME="$TMPDIR/home"
        mkdir -p "$HOME"
        cp -r ${../tests} tests
        chmod -R +w tests
        # The fakes ship a `#!/usr/bin/env bash` shebang, which the
        # host's launchers exec by path. A sandboxed Linux build has no
        # /usr/bin/env, so rewrite them to the store bash before use.
        for f in tests/fakes/*; do
          substituteInPlace "$f" \
            --replace '#!/usr/bin/env bash' "#!${pkgs.bash}/bin/bash"
        done
        export FAKES_DIR="$PWD/tests/fakes"
        bats --print-output-on-failure tests/
        touch $out
      '';

  # Pure-eval-style assertion: the image store path is substituted into
  # the generated commands and the placeholder is gone.
  mkharness-substitution = pkgs.runCommand "mkharness-substitution" { } ''
    buildCmd=${harness.build}/bin/build
    runCmd=${harness.run}/bin/run

    grep -q '${harness.imagePath}' "$buildCmd"
    grep -q '${harness.imagePath}' "$runCmd"
    ! grep -q '@imagePath@' "$buildCmd"
    ! grep -q '@imagePath@' "$runCmd"

    case '${harness.imagePath}' in
      /nix/store/*spindrift*) : ;;
      *) echo "unexpected image path: ${harness.imagePath}" >&2; exit 1 ;;
    esac
    touch $out
  '';

  # The declarative shim must produce byte-identical outputs to a
  # direct `mkHarness` call with the same inputs (#5). Compare store
  # paths at eval time — no Linux builder needed, since the launcher
  # commands are native and the image path is baked into them as text.
  flakemodule-equivalence =
    pkgs.runCommand "flakemodule-equivalence"
      {
        moduleBuild = config.packages.build;
        directBuild = harness.build;
        moduleRun = config.packages.run;
        directRun = harness.run;
        imagePath = harness.imagePath;
      }
      ''
        [ "$moduleBuild" = "$directBuild" ] \
          || { echo "build mismatch: $moduleBuild != $directBuild" >&2; exit 1; }
        [ "$moduleRun" = "$directRun" ] \
          || { echo "run mismatch: $moduleRun != $directRun" >&2; exit 1; }
        # The module bakes the very same (Linux) image store path.
        grep -q "$imagePath" "$moduleRun/bin/run"
        touch $out
      '';

  # A minimal flake-parts consumer that imports the shim evaluates and
  # yields the same outputs as the equivalent direct call (#5).
  flakemodule-fixture =
    pkgs.runCommand "flakemodule-fixture"
      {
        fixtureBuild = consumerPkgs.build;
        directBuild = minimalDirect.build;
        fixtureRun = consumerPkgs.run;
        directRun = minimalDirect.run;
        imagePath = minimalDirect.imagePath;
      }
      ''
        [ "$fixtureBuild" = "$directBuild" ] \
          || { echo "build mismatch: $fixtureBuild != $directBuild" >&2; exit 1; }
        [ "$fixtureRun" = "$directRun" ] \
          || { echo "run mismatch: $fixtureRun != $directRun" >&2; exit 1; }
        # The fixture's image store path matches the direct call's,
        # asserted via the path baked into its `run` command.
        grep -q "$imagePath" "$fixtureRun/bin/run"
        touch $out
      '';

  # The `templates.default` starter (#6): its `build`/`run` commands
  # must have the Linux image store path substituted in, and — since
  # its config mirrors the dogfood's — be byte-identical to the direct
  # call. Eval-only; the Linux realize is done on the podman builder
  # against an instantiated copy.
  template-fixture =
    pkgs.runCommand "template-fixture"
      {
        templateBuild = templatePkgs.build;
        templateRun = templatePkgs.run;
        directBuild = harnessNoRevision.build;
        directRun = harnessNoRevision.run;
        imagePath = harnessNoRevision.imagePath;
      }
      ''
        buildCmd="$templateBuild/bin/build"
        runCmd="$templateRun/bin/run"

        ! grep -q '@imagePath@' "$buildCmd"
        ! grep -q '@imagePath@' "$runCmd"
        grep -q "$imagePath" "$buildCmd"
        grep -q "$imagePath" "$runCmd"
        case "$imagePath" in
          /nix/store/*spindrift*) : ;;
          *) echo "unexpected image path: $imagePath" >&2; exit 1 ;;
        esac

        # Same config as the template ⇒ identical launcher commands.
        [ "$templateBuild" = "$directBuild" ] \
          || { echo "build mismatch: $templateBuild != $directBuild" >&2; exit 1; }
        [ "$templateRun" = "$directRun" ] \
          || { echo "run mismatch: $templateRun != $directRun" >&2; exit 1; }
        touch $out
      '';

  # The configured `defaults` and `runtime` are baked into the
  # generated `run` command text (eval-only; no Linux builder).
  mkharness-defaults = pkgs.runCommand "mkharness-defaults" { } ''
    runCmd=${customHarness.run}/bin/run
    ! grep -q -- '@label@' "$runCmd"
    grep -q 'LABEL:-custom-label' "$runCmd"
    grep -q 'BASE_BRANCH:-develop' "$runCmd"
    grep -q 'MAX_PARALLEL:-5' "$runCmd"
    grep -q 'BRANCH_PREFIX:-bot/' "$runCmd"
    grep -q 'IN_PROGRESS_LABEL:-custom-wip' "$runCmd"
    grep -q 'FAILED_LABEL:-custom-broken' "$runCmd"
    grep -q 'SCOUT_MODEL:-custom-scout' "$runCmd"
    grep -q 'REVIEW_MODEL:-custom-reviewer' "$runCmd"
    grep -q 'COMPLETE_LABEL:-custom-done' "$runCmd"

    # Default COMPLETE_LABEL baked into a default harness.
    grep -q 'COMPLETE_LABEL:-agent-complete' ${harness.run}/bin/run

    # Default runtime is podman; the docker harness bakes docker.
    grep -q 'RUNTIME="podman"' ${harness.run}/bin/run
    grep -q 'RUNTIME="docker"' ${dockerHarness.run}/bin/run

    # bwrap harness bakes bwrap runtime and agent store paths; no OCI store paths.
    grep -q 'RUNTIME="bwrap"' ${bwrapHarness.run}/bin/run
    grep -q 'AGENT_FILES=' ${bwrapHarness.run}/bin/run
    grep -q 'AGENT_ENV=' ${bwrapHarness.run}/bin/run
    # IMAGE_ARCHIVE is not baked as a store path (empty-default guard is fine).
    ! grep -q 'IMAGE_ARCHIVE="/nix/store/' ${bwrapHarness.run}/bin/run
    grep -q 'AGENT_FILES_DRV=' ${bwrapHarness.build}/bin/build
    grep -q 'AGENT_ENV_DRV=' ${bwrapHarness.build}/bin/build
    ! grep -q 'IMAGE_DRV=' ${bwrapHarness.build}/bin/build
    touch $out
  '';

  # An unknown key in `defaults` must throw at eval time so typos
  # hard-error instead of being silently ignored (issue #97).
  mkharness-rejects-unknown-key =
    let
      inherit (pkgs.lib) assertMsg;
      result = builtins.tryEval (
        import ../lib/mkHarness.nix {
          inherit nixpkgs system;
          defaults = {
            typoLabel = "oops";
          };
        }
      );
    in
    assert assertMsg (!result.success) "mkHarness must throw on unknown defaults key 'typoLabel'";
    pkgs.runCommand "mkharness-rejects-unknown-key" { } "touch $out";

  # The configured `prompt` is rendered to a store-path directory and,
  # by default, baked into the image (see agentFiles) rather than
  # mounted — `run` only bind-mounts a dir under the
  # SPINDRIFT_PROMPT_DIR override. Eval/native only (the rendered
  # prompt dir is a host store path; the image bake is checked
  # Linux-side by prompt-baked-into-image below).
  # The conditional prompt mount is handled by the Go launcher binary,
  # so the bats suite verifies runtime behaviour rather than grepping
  # the wrapper's source.
  mkharness-prompt = pkgs.runCommand "mkharness-prompt" { } ''
    # The Consumer's prompt text is what lands in the rendered file.
    grep -q 'CONFIGURED-PROMPT-MARKER' \
      ${promptHarness.promptDir}/issue-prompt.md
    touch $out
  '';

  # A Consumer `prompt` that drops the SPINDRIFT_OUTCOME contract must still
  # ship an agent that emits the outcome line, so the launcher can learn the
  # PR (issue #419) — the harness appends the canonical contract exactly once.
  mkharness-prompt-outcome-injected = pkgs.runCommand "mkharness-prompt-outcome-injected" { } ''
    count=$(grep -c '# LAND THE CHANGE' ${promptHarness.promptDir}/issue-prompt.md)
    [ "$count" -eq 1 ] || {
      echo "expected the outcome contract injected exactly once, got $count" >&2
      exit 1
    }
    touch $out
  '';

  # The default prompt already contains the contract, so injection must be a
  # no-op: no duplication (issue #419).
  mkharness-prompt-outcome-not-duplicated = pkgs.runCommand "mkharness-prompt-outcome-not-duplicated" { } ''
    count=$(grep -c '# LAND THE CHANGE' ${batsHarness.promptDir}/issue-prompt.md)
    [ "$count" -eq 1 ] || {
      echo "expected the default prompt's outcome contract to stay single, got $count" >&2
      exit 1
    }
    touch $out
  '';

  # The default box's rendered prompt must be byte-identical to the template
  # on disk — injection must not touch a prompt that already has the
  # contract (issue #419).
  mkharness-prompt-outcome-default-unchanged =
    pkgs.runCommand "mkharness-prompt-outcome-default-unchanged" { } ''
      diff ${../templates/default/prompts/issue-prompt.md} ${batsHarness.promptDir}/issue-prompt.md
      touch $out
    '';

  # The block injected into a prompt lacking the contract must be
  # byte-identical to the default prompt's own contract section — both are
  # sliced from the same marker in the same source file, so they cannot
  # drift apart (issue #419).
  mkharness-prompt-outcome-no-drift = pkgs.runCommand "mkharness-prompt-outcome-no-drift" { } ''
    awk '/# LAND THE CHANGE/{f=1} f' ${promptHarness.promptDir}/issue-prompt.md > injected-contract.txt
    diff ${batsHarness.outcomeContractFile} injected-contract.txt
    touch $out
  '';

  # The configured `skills` are rendered to a store-path skills directory.
  # Eval/native only (the skills dir is a host store path built by hostPkgs;
  # the image-layer check is below, Linux-gated).
  mkharness-skills = pkgs.runCommand "mkharness-skills" { } ''
    grep -q 'BAKED-SKILL-MARKER' \
      ${skillsHarness.skillsDir}/baked-skill.md
    touch $out
  '';

  # The engine must carry nothing language-specific: a Go/Node/Python
  # consumer inherits no Rust machinery (ADR 0003).
  engine-language-agnostic =
    pkgs.runCommand "engine-language-agnostic" { engine = ../lib/mkHarness.nix; }
      ''
        if grep -Eni 'rust|cargo' "$engine"; then
          echo "lib/mkHarness.nix must not reference rust/cargo symbols" >&2
          exit 1
        fi
        touch $out
      '';

  # A non-Rust `packages` set is baked into the image on top of the
  # harness plumbing. Asserted by matching the (Linux) env's `paths`
  # names in nix — pure eval, so it needs no Linux builder and no
  # sandboxed read of the env derivation.
  packages-baked =
    let
      inherit (pkgs.lib) assertMsg any hasInfix;
      names = map (p: p.name or "") nonRustHarness.agentEnv.paths;
      baked = frag: any (n: hasInfix frag n) names;
    in
    assert assertMsg (baked "hello-") "expected the hello package baked into the env";
    # engine plumbing is still layered on, language-agnostically
    assert assertMsg (baked "git-") "expected git plumbing layered into the env";
    pkgs.runCommand "packages-baked" { } "touch $out";

  # Nix is the first-class default: every box ships the nix CLI unless
  # the Consumer opts into the lean escape hatch (nixInBox = false).
  nix-baked-by-default =
    let
      inherit (pkgs.lib) assertMsg any hasInfix;
      names = map (p: p.name or "") nonRustHarness.agentEnv.paths;
      hasNix = any (n: hasInfix "nix-" n || n == "nix") names;
    in
    assert assertMsg hasNix "expected the nix CLI to be baked into the default box";
    pkgs.runCommand "nix-baked-by-default" { } "touch $out";

  # nil is baked into the dogfood toolchain for fast, store-free Nix
  # structural checks (syntax, duplicate keys, unused bindings) as uid 1000
  # where nix flake check is unavailable.
  nil-baked-in-dogfood =
    let
      inherit (pkgs.lib) assertMsg any hasInfix;
      names = map (p: p.name or "") harness.agentEnv.paths;
      hasNil = any (n: hasInfix "nil-" n || n == "nil") names;
    in
    assert assertMsg hasNil "expected nil to be baked into the dogfood toolchain";
    pkgs.runCommand "nil-baked-in-dogfood" { } "touch $out";

  # The lean/no-nix escape hatch must not include the nix CLI.
  lean-escape-hatch =
    let
      inherit (pkgs.lib) assertMsg any hasInfix;
      names = map (p: p.name or "") leanHarness.agentEnv.paths;
      hasNix = any (n: hasInfix "nix-" n || n == "nix") names;
    in
    assert assertMsg (!hasNix) "lean harness (nixInBox = false) must not bake in the nix CLI";
    pkgs.runCommand "lean-escape-hatch" { } "touch $out";

  # The flakeModule must expose grouped settings.<section>.<knob> options
  # derived from env-schema.nix (issue #352). A consumer that sets knobs
  # under settings.* gets byte-identical outputs to a direct mkHarness call
  # with the equivalent flat defaults.
  flakemodule-schema-options =
    let
      consumer105 =
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
            perSystem.spindrift = {
              packages = p: [ p.hello ];
              settings = {
                models = {
                  scoutModel = "scout-test";
                  reviewModel = "review-test";
                };
                lifecycleLabels = {
                  completeLabel = "done-test";
                };
              };
            };
          };
      direct105 = import ../lib/mkHarness.nix {
        inherit nixpkgs system;
        packages = p: [ p.hello ];
        defaults = {
          scoutModel = "scout-test";
          reviewModel = "review-test";
          completeLabel = "done-test";
        };
      };
      consumerPkgs105 = consumer105.packages.${system};
    in
    pkgs.runCommand "flakemodule-schema-options"
      {
        moduleBuild = consumerPkgs105.build;
        directBuild = direct105.build;
        moduleRun = consumerPkgs105.run;
        directRun = direct105.run;
      }
      ''
        [ "$moduleBuild" = "$directBuild" ] \
          || { echo "build mismatch: $moduleBuild != $directBuild" >&2; exit 1; }
        [ "$moduleRun" = "$directRun" ] \
          || { echo "run mismatch: $moduleRun != $directRun" >&2; exit 1; }
        touch $out
      '';

  # Promoted operator-tunable knobs (issue #353): the 13 newly consumer-tunable
  # knobs appear under their correct settings section and bake the expected
  # ${VAR:-<baked>} default into the generated run command.  Covers at least one
  # behavior knob (selfHealing.maxFixAttempts) and the identity knob
  # (repository.repoSlug).  Also confirms that REPO_SLUG bakes an *empty*
  # default when unset so runtime required-validation is not masked, and that
  # ISSUE_NUMBER remains absent from the flake surface (keep-off list).
  flakemodule-widen-operator-knobs =
    let
      inherit (pkgs.lib) assertMsg;
      mkRun =
        settingsCfg:
        (flake-parts.lib.mkFlake
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
            perSystem.spindrift = {
              packages = p: [ p.hello ];
              settings = settingsCfg;
            };
          }
        ).packages.${system}.run;

      behaviorRun = mkRun {
        selfHealing = {
          maxFixAttempts = 5;
          maxRebaseAttempts = 2;
          holdJitterSecs = 10;
          transientBackoffSecs = 60;
          transientRetryMax = 5;
        };
        concurrency = {
          maxJobs = 2;
          depsPollSecs = 60;
          depsWaitSecs = 3600;
        };
        branches = {
          mergePollInterval = 90;
          mergePollTimeout = 3600;
        };
      };

      identityRun = mkRun {
        repository = {
          repoSlug = "test-org/test-repo";
          gitUserName = "Test Bot";
          gitUserEmail = "bot@test.example";
        };
      };

      # REPO_SLUG without a consumer setting must bake an *empty* default so
      # runtime required-validation is not masked.  renderDefaultsPreamble
      # emits `REPO_SLUG="${REPO_SLUG:-}"` (value = ""); the grep matches
      # `REPO_SLUG:-}"` (i.e. `:-` immediately followed by `}"`).
      defaultRun = mkRun { };

      # ISSUE_NUMBER must not be settable via settings (per-run dispatch
      # override; keep-off list).
      badIssueNumber = builtins.tryEval (mkRun {
        issueDiscovery.issueNumber = "42";
      });
    in
    assert assertMsg (
      !badIssueNumber.success
    ) "ISSUE_NUMBER must not be settable via settings.issueDiscovery (keep-off list)";
    pkgs.runCommand "flakemodule-widen-operator-knobs"
      {
        inherit behaviorRun identityRun defaultRun;
      }
      ''
        grep -q 'MAX_FIX_ATTEMPTS:-5' "$behaviorRun/bin/run" \
          || { echo "MAX_FIX_ATTEMPTS:-5 not baked in run cmd" >&2; exit 1; }
        grep -q 'MAX_REBASE_ATTEMPTS:-2' "$behaviorRun/bin/run" \
          || { echo "MAX_REBASE_ATTEMPTS:-2 not baked in run cmd" >&2; exit 1; }
        grep -q 'HOLD_JITTER_SECS:-10' "$behaviorRun/bin/run" \
          || { echo "HOLD_JITTER_SECS:-10 not baked in run cmd" >&2; exit 1; }
        grep -q 'TRANSIENT_BACKOFF_SECS:-60' "$behaviorRun/bin/run" \
          || { echo "TRANSIENT_BACKOFF_SECS:-60 not baked in run cmd" >&2; exit 1; }
        grep -q 'TRANSIENT_RETRY_MAX:-5' "$behaviorRun/bin/run" \
          || { echo "TRANSIENT_RETRY_MAX:-5 not baked in run cmd" >&2; exit 1; }
        grep -q 'MAX_JOBS:-2' "$behaviorRun/bin/run" \
          || { echo "MAX_JOBS:-2 not baked in run cmd" >&2; exit 1; }
        grep -q 'DEPS_POLL_SECS:-60' "$behaviorRun/bin/run" \
          || { echo "DEPS_POLL_SECS:-60 not baked in run cmd" >&2; exit 1; }
        grep -q 'DEPS_WAIT_SECS:-3600' "$behaviorRun/bin/run" \
          || { echo "DEPS_WAIT_SECS:-3600 not baked in run cmd" >&2; exit 1; }
        grep -q 'MERGE_POLL_INTERVAL:-90' "$behaviorRun/bin/run" \
          || { echo "MERGE_POLL_INTERVAL:-90 not baked in run cmd" >&2; exit 1; }
        grep -q 'MERGE_POLL_TIMEOUT:-3600' "$behaviorRun/bin/run" \
          || { echo "MERGE_POLL_TIMEOUT:-3600 not baked in run cmd" >&2; exit 1; }
        grep -q 'REPO_SLUG:-test-org/test-repo' "$identityRun/bin/run" \
          || { echo "REPO_SLUG:-test-org/test-repo not baked in run cmd" >&2; exit 1; }
        grep -q 'GIT_USER_NAME:-Test Bot' "$identityRun/bin/run" \
          || { echo "GIT_USER_NAME:-Test Bot not baked in run cmd" >&2; exit 1; }
        grep -q 'GIT_USER_EMAIL:-bot@test.example' "$identityRun/bin/run" \
          || { echo "GIT_USER_EMAIL:-bot@test.example not baked in run cmd" >&2; exit 1; }
        grep -q 'REPO_SLUG:-}"' "$defaultRun/bin/run" \
          || { echo "REPO_SLUG must have empty baked default (REPO_SLUG:-}) when not set; required validation must not be masked" >&2; exit 1; }
        touch $out
      '';

  # Unknown section or knob keys in `settings` must throw at eval time; the
  # NixOS module system rejects undeclared option names.  We force evaluation
  # down to `.packages.${system}.run` so the module config is actually
  # evaluated (flake-parts evaluates perSystem configs lazily on attribute
  # access).
  flakemodule-rejects-unknown-settings =
    let
      inherit (pkgs.lib) assertMsg;
      mkBadFlake =
        cfg:
        (flake-parts.lib.mkFlake
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
            perSystem.spindrift = {
              packages = p: [ p.hello ];
            }
            // cfg;
          }
        ).packages.${system}.run;
      badSection = builtins.tryEval (mkBadFlake {
        settings.typoSection.label = "oops";
      });
      badKnob = builtins.tryEval (mkBadFlake {
        settings.branches.typoKnob = "oops";
      });
    in
    assert assertMsg (
      !badSection.success
    ) "flakeModule must throw on unknown settings section 'typoSection'";
    assert assertMsg (
      !badKnob.success
    ) "flakeModule must throw on unknown knob 'typoKnob' in settings.branches";
    pkgs.runCommand "flakemodule-rejects-unknown-settings" { } "touch $out";

  # harness.env.example must match the content generated from env-schema.nix.
  # Fails when a new schema knob is added but the committed file is not
  # regenerated (golden-file drift; resolves issue #109). Shares its renderer
  # with `nix run .#regen` (nix/regen.nix) via lib/renderers.nix — the guard
  # and the regenerator cannot drift from each other (issue #402).
  harness-env-example =
    let
      schema = import ../lib/env-schema.nix;
      generated = pkgs.writeText "harness.env.example.generated" (
        renderers.renderHarnessEnvExample schema
      );
    in
    pkgs.runCommand "harness-env-example"
      {
        inherit generated;
        committed = ../templates/default/harness.env.example;
      }
      ''
        diff "$generated" "$committed" \
          || { echo "templates/default/harness.env.example is out of sync with lib/env-schema.nix — regenerate it" >&2; exit 1; }
        touch $out
      '';

  # Every env-var string literal in cmd/launcher/main.go must have a
  # matching entry in lib/env-schema.nix, and vice-versa (presence-only;
  # value-level pinning would be refactor-brittle).  A set of known
  # nix-baked vars is excluded from the main.go side.
  launcher-env-coverage =
    let
      schema = import ../lib/env-schema.nix;
      inherit (pkgs.lib)
        attrValues
        concatStringsSep
        filter
        subtractLists
        ;
      mainGoSrc = builtins.readFile ../cmd/launcher/main.go;
      # Env var names that main.go reads but that are nix-generated
      # (not user-facing knobs): excluded from the schema-coverage check.
      nixBaked = [
        "IMAGE_ARCHIVE"
        "IMAGE_TAG"
        "IMAGE_DRV"
        "NIX_BUILDER_IMAGE"
        "NIX_VOLUME"
        "FLAKE_IMAGE_ATTR"
        "AGENT_FILES"
        "AGENT_ENV"
        "AGENT_FILES_DRV"
        "AGENT_ENV_DRV"
        "BAKED_PREFETCH"
        "RUNTIME"
        "DRIVER"
        "IMAGE"
        "BOX_ENV_VARS"
      ];
      schemaEnvNames = map (e: e.env) (attrValues schema);
      # Schema knobs forwarded to containers via BOX_ENV_VARS only — the Go
      # binary never reads them directly, so they need no os.Getenv call.
      boxEnvOnly = [
        "MODEL"
        "SCOUT_MODEL"
        "REVIEW_MODEL"
        "FILER_MODEL"
        "DEV_SHELL_NAME"
        "DEV_SHELL_PROBE_TIMEOUT"
      ];
      # Forward: every schema name (that Go reads directly) must appear as a
      # string literal in main.go.
      missingFromGo = filter (name: !pkgs.lib.hasInfix ''"${name}"'' mainGoSrc) (
        subtractLists boxEnvOnly schemaEnvNames
      );
      # Reverse: extract names from os.Getenv/getenv calls in main.go.
      parts = builtins.split ''(os\.Getenv|getenv)\("([A-Z_][A-Z0-9_]*)"\)'' mainGoSrc;
      goEnvNames = map (m: builtins.elemAt m 1) (filter builtins.isList parts);
      extraInGo = subtractLists (schemaEnvNames ++ nixBaked) goEnvNames;
    in
    assert pkgs.lib.assertMsg (
      missingFromGo == [ ]
    ) "schema knobs absent from main.go: ${concatStringsSep ", " missingFromGo}";
    assert pkgs.lib.assertMsg (
      extraInGo == [ ]
    ) "main.go reads env vars absent from schema: ${concatStringsSep ", " extraInGo}";
    pkgs.runCommand "launcher-env-coverage" { } "touch $out";

  # cmd/launcher/flagtable_gen.go must match the content generated from
  # env-schema.nix by mkHarness.nix renderFlagTableGo.  Fails when a new
  # schema knob is added but the committed generated file is not regenerated.
  # Shares its renderer with `nix run .#regen` via lib/renderers.nix.
  launcher-flag-table =
    let
      schema = import ../lib/env-schema.nix;
      generated = pkgs.writeText "flagtable_gen.go.generated" (renderers.renderFlagTableGo schema);
    in
    pkgs.runCommand "launcher-flag-table"
      {
        inherit generated;
        committed = ../cmd/launcher/flagtable_gen.go;
      }
      ''
        diff "$generated" "$committed" \
          || { echo "cmd/launcher/flagtable_gen.go is out of sync with lib/env-schema.nix — regenerate it" >&2; exit 1; }
        touch $out
      '';

  # docs/flake-options.md must match the reference generated from env-schema.nix.
  # Fails when a flakeOption knob is added or removed but the committed file is
  # not regenerated (same treatment as harness.env.example and flagtable_gen.go).
  # Shares its renderer with `nix run .#regen` via lib/renderers.nix.
  flake-options-doc =
    let
      schema = import ../lib/env-schema.nix;
      generated = pkgs.writeText "flake-options.md.generated" (renderers.renderFlakeOptionsDoc schema);
    in
    pkgs.runCommand "flake-options-doc"
      {
        inherit generated;
        committed = ../docs/flake-options.md;
      }
      ''
        diff "$generated" "$committed" \
          || { echo "docs/flake-options.md is out of sync with lib/env-schema.nix — regenerate it" >&2; exit 1; }
        touch $out
      '';

  # templates/default/flake.nix settings example block must cover every section
  # and every knob in the schema-derived flakeOption surface.  Fails when a new
  # section or knob is added to env-schema.nix but the template is not updated.
  template-settings-example =
    let
      schema = import ../lib/env-schema.nix;
      inherit (pkgs.lib)
        attrNames
        concatStringsSep
        filter
        filterAttrs
        foldl'
        hasInfix
        ;
      flakeOptionEntries = filterAttrs (_: e: e.flakeOption or false) schema;
      # Map sectionAttr -> [knobName] for all flakeOption knobs. groupToAttr
      # (must match lib/flakeModule.nix) comes from lib/renderers.nix — the
      # same mapping flake-options-doc renders from.
      sectionKnobs = foldl' (
        acc: knobName:
        let
          entry = flakeOptionEntries.${knobName};
          sectionAttr = renderers.groupToAttr.${entry.group} or null;
        in
        if sectionAttr == null then
          acc
        else
          acc
          // {
            ${sectionAttr} = (acc.${sectionAttr} or [ ]) ++ [ knobName ];
          }
      ) { } (attrNames flakeOptionEntries);
      templateSrc = builtins.readFile ../templates/default/flake.nix;
      missingSections = filter (s: !(hasInfix s templateSrc)) (attrNames sectionKnobs);
      missingKnobs = pkgs.lib.concatLists (
        pkgs.lib.mapAttrsToList (_section: knobs: filter (k: !(hasInfix k templateSrc)) knobs) sectionKnobs
      );
    in
    assert pkgs.lib.assertMsg (missingSections == [ ])
      "templates/default/flake.nix settings example is missing sections: ${concatStringsSep ", " missingSections}";
    assert pkgs.lib.assertMsg (missingKnobs == [ ])
      "templates/default/flake.nix settings example is missing knobs: ${concatStringsSep ", " missingKnobs}";
    pkgs.runCommand "template-settings-example" { } "touch $out";

  # The generated man page must render (mandoc parses it) and totally cover the
  # schema: every SH section, every OPTIONS group, every non-secret flag, and
  # every secret env var. A new knob with no man-page presence fails here.
  launcher-manpage =
    let
      schema = import ../lib/env-schema.nix;
      inherit (pkgs.lib)
        filter
        attrValues
        concatMapStrings
        replaceStrings
        toLower
        unique
        ;
      toKebab = env: toLower (replaceStrings [ "_" ] [ "-" ] env);
      # Roff renders the flag as \-\- with every hyphen escaped; match that form.
      roffFlag = e: "\\-\\-" + replaceStrings [ "-" ] [ "\\-" ] (toKebab e.env);
      nonSecret = filter (e: !(e.secret or false)) (attrValues schema);
      secretEntries = filter (e: e.secret or false) (attrValues schema);
      groups = unique (map (e: e.group) nonSecret);
      groupChecks = concatMapStrings (g: "need -F '.SS ${g}'\n") groups;
      flagChecks = concatMapStrings (e: "need -F '${roffFlag e}'\n") nonSecret;
      secretChecks = concatMapStrings (e: "need -F '${e.env}'\n") secretEntries;
    in
    pkgs.runCommand "launcher-manpage"
      {
        nativeBuildInputs = [ pkgs.mandoc ];
        man = "${harness.manpage}/share/man/man1/spindrift.1";
      }
      ''
        need() { grep -q "$@" "$man" || { echo "man page missing: $*" >&2; exit 1; }; }
        # Renders without a fatal parse error.
        mandoc -man -Tascii "$man" >/dev/null
        for s in NAME SYNOPSIS DESCRIPTION SUBCOMMANDS OPTIONS ENVIRONMENT FILES EXAMPLES; do
          grep -Eq "^\.SH \"?$s" "$man" || { echo "man page missing .SH $s" >&2; exit 1; }
        done
        ${groupChecks}
        ${flagChecks}
        ${secretChecks}
        touch $out
      '';

  # The changelog contract: .release-please-config.json must declare an
  # explicit changelog-sections map (never rely on release-please's
  # implicit defaults, which hide `security` and every non-feat/fix type
  # spindrift uses), and every rendered heading must be documented in
  # VERSIONING.md. Pure eval — reads both files, no builder needed.
  release-please-changelog =
    let
      inherit (pkgs.lib) assertMsg concatMapStringsSep hasInfix;
      # Source of truth for the section map. Order here is the order the
      # headings render in CHANGELOG.md. Nothing is hidden (see VERSIONING.md).
      sections = [
        {
          type = "feat";
          section = "Features";
        }
        {
          type = "fix";
          section = "Bug Fixes";
        }
        {
          type = "perf";
          section = "Performance Improvements";
        }
        {
          type = "security";
          section = "Security";
        }
        {
          type = "revert";
          section = "Reverts";
        }
        {
          type = "docs";
          section = "Documentation";
        }
        {
          type = "refactor";
          section = "Code Refactoring";
        }
        {
          type = "test";
          section = "Tests";
        }
        {
          type = "build";
          section = "Build System";
        }
        {
          type = "ci";
          section = "Continuous Integration";
        }
        {
          type = "chore";
          section = "Miscellaneous Chores";
        }
        {
          type = "style";
          section = "Styles";
        }
        {
          type = "deps";
          section = "Dependencies";
        }
      ];
      cfg = builtins.fromJSON (builtins.readFile ../.release-please-config.json);
      versioningDoc = builtins.readFile ../VERSIONING.md;
      missingFromDoc = builtins.filter (s: !hasInfix s.section versioningDoc) sections;
    in
    assert assertMsg (
      cfg ? "changelog-sections"
    ) ".release-please-config.json must declare changelog-sections (canonical map in nix/checks.nix)";
    assert assertMsg (cfg."changelog-sections" == sections)
      "changelog-sections in .release-please-config.json drifted from the canonical map in nix/checks.nix";
    assert assertMsg (missingFromDoc == [ ])
      "VERSIONING.md is missing changelog headings: ${
        concatMapStringsSep ", " (s: s.section) missingFromDoc
      }";
    pkgs.runCommand "release-please-changelog" { } "touch $out";

  # gofmt -l must exit cleanly — any output means unformatted files.
  launcher-go-fmt = pkgs.runCommand "launcher-go-fmt" { nativeBuildInputs = [ pkgs.go ]; } ''
    unformatted=$(gofmt -l ${../cmd/launcher})
    if [ -n "$unformatted" ]; then
      echo "gofmt violations:" >&2
      echo "$unformatted" >&2
      exit 1
    fi
    touch $out
  '';

  # nixfmt --check must exit cleanly — any output means unformatted files.
  nix-fmt = pkgs.runCommand "nix-fmt" { nativeBuildInputs = [ pkgs.nixfmt ]; } ''
    nixfmt --check \
      ${../flake.nix} \
      ${../lib/env-schema.nix} \
      ${../lib/flakeModule.nix} \
      ${../lib/mkHarness.nix} \
      ${../nix/checks.nix} \
      ${../nix/fixtures.nix} \
      ${../templates/default/flake.nix}
    touch $out
  '';

  # go vet catches suspicious constructs at analysis time.
  # CGO_ENABLED=0 avoids needing a C toolchain: the jira forge adapter
  # imports net/http, which otherwise pulls runtime/cgo into the build
  # and fails with "gcc not found" (matches launcher-cross-build, which
  # already builds the real binary this way).
  launcher-go-vet = pkgs.runCommand "launcher-go-vet" { nativeBuildInputs = [ pkgs.go ]; } ''
    cp -r ${../cmd/launcher} src
    chmod -R +w src
    export GOPROXY=off
    export GONOSUMCHECK='*'
    export GOMODCACHE="$TMPDIR/gomodcache"
    export GOCACHE="$TMPDIR/gocache"
    export CGO_ENABLED=0
    cd src
    go vet ./...
    touch $out
  '';

  # go test must stay green: unit tests catch config-parsing bugs
  # before they reach the binary (see issue #112, 9494fc1-class).
  # forge's tests shell out to git (TestGitForcePush_CapturesStderr), so
  # git must be on PATH in the sandbox alongside go. CGO_ENABLED=0 for
  # the same reason as launcher-go-vet above.
  launcher-go-test =
    pkgs.runCommand "launcher-go-test"
      {
        nativeBuildInputs = [
          pkgs.go
          pkgs.git
        ];
      }
      ''
        cp -r ${../cmd/launcher} src
        chmod -R +w src
        export GOPROXY=off
        export GONOSUMCHECK='*'
        export GOMODCACHE="$TMPDIR/gomodcache"
        export GOCACHE="$TMPDIR/gocache"
        export CGO_ENABLED=0
        cd src
        go test ./...
        touch $out
      '';

  # Cross-build: launcher must compile for linux and darwin. Native
  # (x86_64-linux on CI) plus explicit darwin cross-targets.
  # CGO_ENABLED=0 makes pure-Go cross-compilation work without
  # a C cross-toolchain.
  launcher-cross-build =
    pkgs.runCommand "launcher-cross-build" { nativeBuildInputs = [ pkgs.go ]; }
      ''
        cp -r ${../cmd/launcher} src
        chmod -R +w src
        export GOPROXY=off
        export GONOSUMCHECK='*'
        export GOMODCACHE="$TMPDIR/gomodcache"
        export GOCACHE="$TMPDIR/gocache"
        export CGO_ENABLED=0
        cd src
        go build -o "$TMPDIR/launcher-linux" .
        GOOS=darwin GOARCH=amd64 go build -o "$TMPDIR/launcher-darwin-amd64" .
        GOOS=darwin GOARCH=arm64 go build -o "$TMPDIR/launcher-darwin-arm64" .
        touch $out
      '';

  # formatter output must be the same store path as the pinned pkgs.nixfmt
  # used by the nix-fmt check — no drift between "how it's checked" and
  # "how it's fixed".
  formatter-is-nixfmt = pkgs.runCommand "formatter-is-nixfmt" { } ''
    test "${config.formatter}" = "${pkgs.nixfmt}"
    touch $out
  '';

  # flakeModule consumers receive the same formatter via perSystem.
  module-consumer-formatter-is-nixfmt = pkgs.runCommand "module-consumer-formatter-is-nixfmt" { } ''
    test "${consumerFormatter}" = "${pkgs.nixfmt}"
    touch $out
  '';
}
// pkgs.lib.optionalAttrs pkgs.stdenv.isLinux {
  # The baked entrypoint must carry a store-path shebang, not the
  # source's `#!/usr/bin/env bash` — the Box has no /usr/bin/env. Guards
  # against baking the raw source instead of the writeShellApplication
  # output. Realizes the agent-files layer, so it is gated to a Linux
  # builder and omitted from `nix flake check` on darwin.
  entrypoint-shebang = pkgs.runCommand "entrypoint-shebang" { } ''
    shebang=$(head -1 ${nonRustHarness.agentFiles}/agent/entrypoint.sh)
    case "$shebang" in
      '#!'/nix/store/*bash*) : ;;
      *) echo "entrypoint shebang is not a store bash: $shebang" >&2
         exit 1 ;;
    esac
    touch $out
  '';

  # AGENTS_JSON_TEMPLATE baked into the entrypoint by nix (ADR 0007): each
  # subagent is composed independently by its own model knob (issue #392), so
  # the template carries whichever of scout/reviewer have a model configured,
  # and is the empty string only when neither does.
  agents-json-baked = pkgs.runCommand "agents-json-baked" { } ''
    ep=${customHarness.agentFiles}/agent/entrypoint.sh

    # The custom harness bakes both models — template must contain them.
    grep -q 'custom-scout' "$ep" \
      || { echo "scout model not found in baked entrypoint" >&2; exit 1; }
    grep -q 'custom-reviewer' "$ep" \
      || { echo "reviewer model not found in baked entrypoint" >&2; exit 1; }
    grep -q 'AGENTS_JSON_TEMPLATE=' "$ep" \
      || { echo "AGENTS_JSON_TEMPLATE assignment missing from entrypoint" >&2; exit 1; }

    # Default harness bakes no models → template must not contain JSON content.
    ! grep -q 'AGENTS_JSON_TEMPLATE=.*{' ${nonRustHarness.agentFiles}/agent/entrypoint.sh \
      || { echo "AGENTS_JSON_TEMPLATE is non-empty for no-model harness" >&2; exit 1; }

    # A scout-only harness bakes the scout entry alone — no reviewer key at all.
    scout_line=$(grep '^AGENTS_JSON_TEMPLATE=' ${scoutOnlyHarness.agentFiles}/agent/entrypoint.sh)
    grep -q 'solo-scout' <<<"$scout_line" \
      || { echo "scout-only harness missing scout model in baked template" >&2; exit 1; }
    ! grep -q '"reviewer"' <<<"$scout_line" \
      || { echo "scout-only harness unexpectedly bakes a reviewer entry" >&2; exit 1; }

    # The reviewer-only mirror.
    reviewer_line=$(grep '^AGENTS_JSON_TEMPLATE=' ${reviewerOnlyHarness.agentFiles}/agent/entrypoint.sh)
    grep -q 'solo-reviewer' <<<"$reviewer_line" \
      || { echo "reviewer-only harness missing reviewer model in baked template" >&2; exit 1; }
    ! grep -q '"scout"' <<<"$reviewer_line" \
      || { echo "reviewer-only harness unexpectedly bakes a scout entry" >&2; exit 1; }

    # The filer-only mirror (opt-in, default empty — issue #393): composed
    # independently like scout/reviewer, no scout/reviewer keys alongside it.
    filer_line=$(grep '^AGENTS_JSON_TEMPLATE=' ${filerOnlyHarness.agentFiles}/agent/entrypoint.sh)
    grep -q 'solo-filer' <<<"$filer_line" \
      || { echo "filer-only harness missing filer model in baked template" >&2; exit 1; }
    ! grep -q '"scout"' <<<"$filer_line" \
      || { echo "filer-only harness unexpectedly bakes a scout entry" >&2; exit 1; }
    ! grep -q '"reviewer"' <<<"$filer_line" \
      || { echo "filer-only harness unexpectedly bakes a reviewer entry" >&2; exit 1; }

    touch $out
  '';

  # The Box must run unprivileged: Claude Code refuses
  # --dangerously-skip-permissions under root. Assert the image config
  # runs as the non-root `agent` user. Realizes the image, so it is
  # Linux-gated like the shebang check.
  box-runs-as-non-root =
    pkgs.runCommand "box-runs-as-non-root" { nativeBuildInputs = [ pkgs.jq ]; }
      ''
        mkdir img && tar -xf ${nonRustHarness.image} -C img
        cfg=$(jq -r '.[0].Config' img/manifest.json)
        user=$(jq -r '.config.User // ""' "img/$cfg")
        echo "image config User = '$user'"
        [ "$user" = "agent" ] || {
          echo "expected the Box to run as non-root 'agent', got '$user'" >&2
          exit 1
        }
        touch $out
      '';

  # The rendered prompt must be baked into the agent-files layer at
  # /agent/prompts, so the Box is self-contained and needs no host
  # /nix/store mount (which a macOS podman VM cannot provide). Realizes
  # the agent-files layer, so it is Linux-gated like the shebang check.
  prompt-baked-into-image = pkgs.runCommand "prompt-baked-into-image" { } ''
    grep -q 'CONFIGURED-PROMPT-MARKER' \
      ${promptHarness.agentFiles}/agent/prompts/issue-prompt.md
    grep -q 'git rebase' \
      ${promptHarness.agentFiles}/agent/prompts/conflict-resolve-prompt.md
    touch $out
  '';

  # Skills configured at build time must land in the agent-files layer at
  # /home/agent/.claude/skills so the Box is self-contained. Realizes the
  # agent-files layer; Linux-gated like the other image checks.
  skills-baked-into-image = pkgs.runCommand "skills-baked-into-image" { } ''
    grep -q 'BAKED-SKILL-MARKER' \
      ${skillsHarness.agentFiles}/home/agent/.claude/skills/baked-skill.md
    touch $out
  '';

  # The nix.conf and store DB must be present in the image so
  # `nix flake check` reuses the baked closure instead of re-substituting.
  # Realizes the default image; Linux-gated like the other image checks.
  nix-conf-in-image = pkgs.runCommand "nix-conf-in-image" { nativeBuildInputs = [ pkgs.jq ]; } ''
    # Extract the image ONCE (like box-runs-as-non-root), then read
    # only the top "customisation" layer where extraCommands writes
    # nix.conf. Reading the compressed image more than once exhausts
    # the runner's disk burst credits and wedges CI for minutes;
    # re-reading all ~98 extracted layers is just as slow.
    mkdir img && tar -xf ${nonRustHarness.image} -C img
    layer="$(jq -r '.[0].Layers[-1]' img/manifest.json)"
    # The customisation layer is packed with `tar -cf layer.tar .`, so
    # members carry a leading `./`; match and extract the real name.
    member="$(tar -tf "img/$layer" \
      | grep -E '^(\./)?etc/nix/nix\.conf$' | head -1 || true)"
    [ -n "$member" ] || {
      echo "etc/nix/nix.conf not in the image's top (customisation) layer" >&2
      exit 1
    }
    tar -xOf "img/$layer" "$member" > nix.conf
    grep -q 'experimental-features = nix-command flakes' nix.conf || {
      echo "nix.conf is missing experimental-features" >&2
      exit 1
    }
    grep -q 'sandbox = false' nix.conf || {
      echo "nix.conf is missing sandbox = false" >&2
      exit 1
    }
    touch $out
  '';

  # nix/var must be owned by uid 1000 so the non-root agent can lock the
  # SQLite store DB inside the unprivileged container (issue #356).
  # fakeRootCommands records ownership in the tar headers; --numeric-owner
  # surfaces the raw uid so the check does not depend on /etc/passwd names.
  nix-var-owned-by-agent =
    pkgs.runCommand "nix-var-owned-by-agent" { nativeBuildInputs = [ pkgs.jq ]; }
      ''
        mkdir img && tar -xf ${nonRustHarness.image} -C img
        layer="$(jq -r '.[0].Layers[-1]' img/manifest.json)"
        uid=$(tar --numeric-owner -tvf "img/$layer" \
          | awk '/nix\/var\/nix\/db\/?$/ { split($2,a,"/"); print a[1]; exit }' \
          || true)
        [ "$uid" = "1000" ] || {
          echo "nix/var/nix/db is not owned by uid 1000 (got: '$uid')" >&2
          exit 1
        }
        touch $out
      '';
}
