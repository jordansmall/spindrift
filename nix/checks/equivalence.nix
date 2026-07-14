# mkHarness output-substitution and flake-module equivalence: the launcher
# commands mkHarness renders, the flakeModule shim's byte-identical parity
# with a direct mkHarness call, and the schema-derived settings surface.
{
  pkgs,
  config,
  fixtures,
  nixpkgs,
  system,
  flake-parts,
  ...
}:
let
  inherit (fixtures)
    harness
    nonRustHarness
    leanHarness
    customHarness
    dockerHarness
    bwrapHarness
    skillsHarness
    minimalDirect
    consumerPkgs
    templatePkgs
    harnessNoRevision
    ;
in
{
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
  # Uses `packages.spindrift` (the CLI); `packages.{build,run}` were
  # removed from the flake surface (issue #613).
  flakemodule-equivalence =
    pkgs.runCommand "flakemodule-equivalence"
      {
        moduleSpindrift = config.packages.spindrift;
        directSpindrift = harness.spindrift;
        imagePath = harness.imagePath;
      }
      ''
        [ "$moduleSpindrift" = "$directSpindrift" ] \
          || { echo "spindrift mismatch: $moduleSpindrift != $directSpindrift" >&2; exit 1; }
        # The module bakes the very same (Linux) image store path.
        grep -q "$imagePath" "$moduleSpindrift/bin/spindrift"
        touch $out
      '';

  # A minimal flake-parts consumer that imports the shim evaluates and
  # yields the same outputs as the equivalent direct call (#5).
  flakemodule-fixture =
    pkgs.runCommand "flakemodule-fixture"
      {
        fixtureSpindrift = consumerPkgs.spindrift;
        directSpindrift = minimalDirect.spindrift;
        imagePath = minimalDirect.imagePath;
      }
      ''
        [ "$fixtureSpindrift" = "$directSpindrift" ] \
          || { echo "spindrift mismatch: $fixtureSpindrift != $directSpindrift" >&2; exit 1; }
        # The fixture's image store path matches the direct call's,
        # asserted via the path baked into its `spindrift` command.
        grep -q "$imagePath" "$fixtureSpindrift/bin/spindrift"
        touch $out
      '';

  # The `templates.default` starter (#6): its `spindrift` command must have
  # the Linux image store path substituted in, and — since its config
  # mirrors the dogfood's — be byte-identical to the direct call. Eval-only;
  # the Linux realize is done on the podman builder against an instantiated
  # copy.
  template-fixture =
    pkgs.runCommand "template-fixture"
      {
        templateSpindrift = templatePkgs.spindrift;
        directSpindrift = harnessNoRevision.spindrift;
        imagePath = harnessNoRevision.imagePath;
      }
      ''
        spindriftCmd="$templateSpindrift/bin/spindrift"

        ! grep -q '@imagePath@' "$spindriftCmd"
        grep -q "$imagePath" "$spindriftCmd"
        case "$imagePath" in
          /nix/store/*spindrift*) : ;;
          *) echo "unexpected image path: $imagePath" >&2; exit 1 ;;
        esac

        # Same config as the template ⇒ identical launcher commands.
        [ "$templateSpindrift" = "$directSpindrift" ] \
          || { echo "spindrift mismatch: $templateSpindrift != $directSpindrift" >&2; exit 1; }
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
        import ../../lib/mkHarness.nix {
          inherit nixpkgs system;
          defaults = {
            typoLabel = "oops";
          };
        }
      );
    in
    assert assertMsg (!result.success) "mkHarness must throw on unknown defaults key 'typoLabel'";
    pkgs.runCommand "mkharness-rejects-unknown-key" { } "touch $out";

  # The configured `skills` are rendered to a store-path skills directory.
  # Eval/native only (the skills dir is a host store path built by hostPkgs;
  # the image-layer check is below, Linux-gated).
  mkharness-skills = pkgs.runCommand "mkharness-skills" { } ''
    grep -q 'BAKED-SKILL-MARKER' \
      ${skillsHarness.skillsDir}/baked-skill/SKILL.md
    touch $out
  '';

  # The engine must carry nothing language-specific: a Go/Node/Python
  # consumer inherits no Rust machinery (ADR 0003).
  engine-language-agnostic =
    pkgs.runCommand "engine-language-agnostic" { engine = ../../lib/mkHarness.nix; }
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

  # bats and shellcheck are baked into the dogfood toolchain so an agent
  # editing shell files can lint/test them in-box, the shell-file analogue
  # of the nil diagnostics guidance above (issue #471).
  bats-baked-in-dogfood =
    let
      inherit (pkgs.lib) assertMsg any hasInfix;
      names = map (p: p.name or "") harness.agentEnv.paths;
      hasBats = any (n: hasInfix "bats-" n) names;
    in
    assert assertMsg hasBats "expected bats to be baked into the dogfood toolchain";
    pkgs.runCommand "bats-baked-in-dogfood" { } "touch $out";

  shellcheck-baked-in-dogfood =
    let
      inherit (pkgs.lib) assertMsg any hasInfix;
      names = map (p: p.name or "") harness.agentEnv.paths;
      hasShellcheck = any (n: hasInfix "shellcheck-" n) names;
    in
    assert assertMsg hasShellcheck "expected shellcheck to be baked into the dogfood toolchain";
    pkgs.runCommand "shellcheck-baked-in-dogfood" { } "touch $out";

  # The dogfood skills (nix/dogfood-skills.nix) are each baked into the image
  # as a <name>/SKILL.md directory — the layout Claude Code actually discovers
  # (a flat <name>.md is ignored) — so the in-box skill preamble advertises
  # /caveman, /tdd, /to-tickets, and /commit. The skill-file analogue of the
  # nil/shellcheck baked-toolchain guards above (issue #486); fails if the
  # dogfood config stops baking any of them or reverts to the flat layout.
  caveman-baked-in-dogfood = pkgs.runCommand "caveman-baked-in-dogfood" { } ''
    test -s ${harness.skillsDir}/caveman/SKILL.md
    test -s ${harness.skillsDir}/tdd/SKILL.md
    test -s ${harness.skillsDir}/to-tickets/SKILL.md
    test -s ${harness.skillsDir}/commit/SKILL.md
    touch $out
  '';

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
                outPath = ../../.;
              };
            };
          }
          {
            systems = [ system ];
            imports = [ ../../lib/flakeModule.nix ];
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
      direct105 = import ../../lib/mkHarness.nix {
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
        moduleSpindrift = consumerPkgs105.spindrift;
        directSpindrift = direct105.spindrift;
      }
      ''
        [ "$moduleSpindrift" = "$directSpindrift" ] \
          || { echo "spindrift mismatch: $moduleSpindrift != $directSpindrift" >&2; exit 1; }
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
                outPath = ../../.;
              };
            };
          }
          {
            systems = [ system ];
            imports = [ ../../lib/flakeModule.nix ];
            perSystem.spindrift = {
              packages = p: [ p.hello ];
              settings = settingsCfg;
            };
          }
        ).packages.${system}.spindrift;

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
        grep -q 'MAX_FIX_ATTEMPTS:-5' "$behaviorRun/bin/spindrift" \
          || { echo "MAX_FIX_ATTEMPTS:-5 not baked in spindrift cmd" >&2; exit 1; }
        grep -q 'MAX_REBASE_ATTEMPTS:-2' "$behaviorRun/bin/spindrift" \
          || { echo "MAX_REBASE_ATTEMPTS:-2 not baked in spindrift cmd" >&2; exit 1; }
        grep -q 'HOLD_JITTER_SECS:-10' "$behaviorRun/bin/spindrift" \
          || { echo "HOLD_JITTER_SECS:-10 not baked in spindrift cmd" >&2; exit 1; }
        grep -q 'TRANSIENT_BACKOFF_SECS:-60' "$behaviorRun/bin/spindrift" \
          || { echo "TRANSIENT_BACKOFF_SECS:-60 not baked in spindrift cmd" >&2; exit 1; }
        grep -q 'TRANSIENT_RETRY_MAX:-5' "$behaviorRun/bin/spindrift" \
          || { echo "TRANSIENT_RETRY_MAX:-5 not baked in spindrift cmd" >&2; exit 1; }
        grep -q 'MAX_JOBS:-2' "$behaviorRun/bin/spindrift" \
          || { echo "MAX_JOBS:-2 not baked in spindrift cmd" >&2; exit 1; }
        grep -q 'MERGE_POLL_INTERVAL:-90' "$behaviorRun/bin/spindrift" \
          || { echo "MERGE_POLL_INTERVAL:-90 not baked in spindrift cmd" >&2; exit 1; }
        grep -q 'MERGE_POLL_TIMEOUT:-3600' "$behaviorRun/bin/spindrift" \
          || { echo "MERGE_POLL_TIMEOUT:-3600 not baked in spindrift cmd" >&2; exit 1; }
        grep -q 'REPO_SLUG:-test-org/test-repo' "$identityRun/bin/spindrift" \
          || { echo "REPO_SLUG:-test-org/test-repo not baked in spindrift cmd" >&2; exit 1; }
        grep -q 'GIT_USER_NAME:-Test Bot' "$identityRun/bin/spindrift" \
          || { echo "GIT_USER_NAME:-Test Bot not baked in spindrift cmd" >&2; exit 1; }
        grep -q 'GIT_USER_EMAIL:-bot@test.example' "$identityRun/bin/spindrift" \
          || { echo "GIT_USER_EMAIL:-bot@test.example not baked in spindrift cmd" >&2; exit 1; }
        grep -q 'REPO_SLUG:-}"' "$defaultRun/bin/spindrift" \
          || { echo "REPO_SLUG must have empty baked default (REPO_SLUG:-}) when not set; required validation must not be masked" >&2; exit 1; }
        touch $out
      '';

  # Unknown section or knob keys in `settings` must throw at eval time; the
  # NixOS module system rejects undeclared option names.  We force evaluation
  # down to `.packages.${system}.spindrift` so the module config is actually
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
                outPath = ../../.;
              };
            };
          }
          {
            systems = [ system ];
            imports = [ ../../lib/flakeModule.nix ];
            perSystem.spindrift = {
              packages = p: [ p.hello ];
            }
            // cfg;
          }
        ).packages.${system}.spindrift;
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

  # The dogfood's tuned leaf values (mergeMode, autoFormat, autoLint,
  # filerModel) must be defined exactly once, in nix/dogfood-defaults.nix,
  # and consumed by both flake.nix's `spindrift` module config and
  # fixtures.nix's direct mkHarness mirror — not hand-restated at each site
  # (issue #459). Commit faf8d2d is that hand-restatement drifting once
  # already. `prefetch` is not pinned here: fixtures.nix's
  # harnessNoRevision legitimately reuses the same command string for the
  # (out-of-scope, per issue #459) template mirror, so it isn't a safe
  # drift discriminant.
  dogfood-leaf-values-single-source =
    let
      inherit (pkgs.lib)
        assertMsg
        concatStringsSep
        filter
        hasInfix
        ;
      flakeSrc = builtins.readFile ../../flake.nix;
      fixturesSrc = builtins.readFile ../../nix/fixtures.nix;
      literals = [
        ''mergeMode = "immediate"''
        "autoFormat = true"
        "autoLint = true"
        ''filerModel = "claude-haiku-4-5-20251001"''
      ];
      leaked = filter (l: hasInfix l flakeSrc || hasInfix l fixturesSrc) literals;
    in
    assert assertMsg (leaked == [ ])
      "dogfood leaf value(s) hand-restated outside nix/dogfood-defaults.nix: ${concatStringsSep ", " leaked}";
    pkgs.runCommand "dogfood-leaf-values-single-source" { } "touch $out";

  # heartbeatFilterBin.src must not contain *_test.go — the image drvPath
  # must be invariant under host-side launcher test churn (issue #474).
  # A tight fileset is the invariant; adding a new import outside it fails
  # the build loudly (missing package) rather than silently expanding the src.
  heartbeat-filter-src-excludes-tests = pkgs.runCommand "heartbeat-filter-src-excludes-tests" { } ''
    test_files=$(find ${nonRustHarness.heartbeatFilterBin.src} -name '*_test.go')
    if [ -n "$test_files" ]; then
      echo "heartbeatFilterBin.src contains *_test.go files:" >&2
      echo "$test_files" >&2
      echo "Tighten the fileset in lib/mkHarness.nix (issue #474)" >&2
      exit 1
    fi
    touch $out
  '';

  # The agent-image drvPath must be a pure function of flake content, not the
  # Consumer's host system (issue #597). ADR 0019's freshness probe evaluates
  # `.#packages.<linuxSystem>.agent-image.drvPath` fresh and compares it
  # against the launcher's baked IMAGE_DRV. On Linux the two hosts coincide,
  # so a host-tagged drvPath still matches by accident; on a macOS Consumer
  # they never can, so the probe reports "rebuild needed" forever and
  # continuous dispatch loops rebuilding an already-current image instead of
  # claiming work (issue #598) — this check locks in the invariant so a
  # future baked input (a new skill, prompt, or tool built with the
  # Consumer's host pkgs) can't silently reintroduce that regression.
  # Reproduces the darwin-vs-linux divergence at pure eval time — no darwin
  # builder is needed to read a foreign-system derivation's drvPath — by
  # baking the *same* { name; src; } skill entry through mkHarness calls that
  # differ only in `system`. Before the fix a pre-built host derivation in
  # `skills` would tag the whole image graph with the host's system; the
  # content form never constructs a derivation outside the image's own
  # (always-Linux) pkgs, so the two must coincide.
  skills-content-form-drvpath-host-independent =
    let
      inherit (pkgs.lib) assertMsg;
      skills = [
        {
          name = "cross-system-skill.md";
          src = "cross-system marker content";
        }
      ];
      harnessLinux = import ../../lib/mkHarness.nix {
        inherit nixpkgs skills;
        system = "aarch64-linux";
      };
      harnessDarwin = import ../../lib/mkHarness.nix {
        inherit nixpkgs skills;
        system = "aarch64-darwin";
      };
    in
    assert assertMsg (harnessLinux.image.drvPath == harnessDarwin.image.drvPath) ''
      agent-image drvPath depends on the Consumer's host system (issue #597):
        aarch64-linux:  ${harnessLinux.image.drvPath}
        aarch64-darwin: ${harnessDarwin.image.drvPath}'';
    pkgs.runCommand "skills-content-form-drvpath-host-independent" { } "touch $out";

  # The `run`/`build` app-style aliases promised gone in v0.2.0 (MIGRATING.md)
  # must stay gone from the flake-output surface: a Consumer invoking
  # `nix run .#run` or `nix run .#build` should get an unknown-output error,
  # not a forwarding alias (issue #613). Guards against silent
  # reintroduction, the nix-output analogue of TestEngageAliasRemoved.
  run-build-aliases-removed =
    let
      inherit (pkgs.lib) assertMsg;
    in
    assert assertMsg (!(harness.apps ? build)) "apps.build must not exist (removed, issue #613)";
    assert assertMsg (!(harness.apps ? run)) "apps.run must not exist (removed, issue #613)";
    assert assertMsg (
      !(harness.packages ? build)
    ) "packages.build must not exist (removed, issue #613)";
    assert assertMsg (!(harness.packages ? run)) "packages.run must not exist (removed, issue #613)";
    pkgs.runCommand "run-build-aliases-removed" { } "touch $out";
}
