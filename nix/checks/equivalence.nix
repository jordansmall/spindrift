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
    rancherHarness
    bwrapHarness
    skillsHarness
    minimalDirect
    consumerPkgs
    templatePkgs
    harnessNoRevision
    ;
  # Shared by the mkharness-review-effort-* checks below (issue #2512): each
  # asserts against a differently-configured mkHarness call's own resolved
  # `.roster` output, but all three only ever need the reviewer entry's
  # effort out of it.
  reviewerEffortOf =
    roster: (builtins.head (builtins.filter (e: e.name == "reviewer") roster)).effort;
in
{
  # Pure-eval-style assertion: the image store path is substituted into the
  # generated Launcher input documents (ADR 0020 — the wrapper itself carries
  # no knob/artifact env or store paths beyond the document's own, passed via
  # a single --input flag) and the placeholder is gone.
  mkharness-substitution = pkgs.runCommand "mkharness-substitution" { } ''
    buildCmd=${harness.build}/bin/build
    runCmd=${harness.run}/bin/run
    buildDoc=${harness.buildInputDocumentFile}
    runDoc=${harness.runInputDocumentFile}

    grep -q -- '--input' "$buildCmd"
    grep -q -- '--input' "$runCmd"
    grep -q '${harness.imagePath}' "$buildDoc"
    grep -q '${harness.imagePath}' "$runDoc"
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
        # The module wrapper carries an --input document, and (since the two
        # wrapper store paths are byte-identical above) that document is the
        # direct call's own — which bakes the very same (Linux) image store path.
        grep -q -- '--input' "$moduleSpindrift/bin/spindrift"
        grep -q "$imagePath" ${harness.runInputDocumentFile}
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
        runDoc = minimalDirect.runInputDocumentFile;
      }
      ''
        [ "$fixtureSpindrift" = "$directSpindrift" ] \
          || { echo "spindrift mismatch: $fixtureSpindrift != $directSpindrift" >&2; exit 1; }
        # The fixture's image store path matches the direct call's, asserted
        # via the path baked into its --input document.
        grep -q -- '--input' "$fixtureSpindrift/bin/spindrift"
        grep -q "$imagePath" "$runDoc"
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
        runDoc = harnessNoRevision.runInputDocumentFile;
      }
      ''
        spindriftCmd="$templateSpindrift/bin/spindrift"

        ! grep -q '@imagePath@' "$spindriftCmd"
        grep -q -- '--input' "$spindriftCmd"
        grep -q "$imagePath" "$runDoc"
        case "$imagePath" in
          /nix/store/*spindrift*) : ;;
          *) echo "unexpected image path: $imagePath" >&2; exit 1 ;;
        esac

        # Same config as the template ⇒ identical launcher commands.
        [ "$templateSpindrift" = "$directSpindrift" ] \
          || { echo "spindrift mismatch: $templateSpindrift != $directSpindrift" >&2; exit 1; }
        touch $out
      '';

  # The configured `defaults` and `runtime` are baked into the generated
  # `run`/`build` Launcher input documents (ADR 0020; eval-only, no Linux
  # builder) — the wrapper command text itself carries no per-knob values
  # any more, only a single --input flag pointing at the document below.
  # This is the drift gate for the hand-written inputDocument Go struct
  # (ADR 0020, cmd/launcher/inputdoc.go): these greps hand-pick specific
  # keys, so a new flakeOption knob is not automatically asserted here.
  mkharness-defaults = pkgs.runCommand "mkharness-defaults" { } ''
    runDoc=${customHarness.runInputDocumentFile}
    ! grep -q -- '@label@' "$runDoc"
    grep -q '"LABEL":"custom-label"' "$runDoc"
    grep -q '"BASE_BRANCH":"develop"' "$runDoc"
    grep -q '"MAX_PARALLEL":"5"' "$runDoc"
    grep -q '"BRANCH_PREFIX":"bot/"' "$runDoc"
    grep -q '"IN_PROGRESS_LABEL":"custom-wip"' "$runDoc"
    grep -q '"FAILED_LABEL":"custom-broken"' "$runDoc"
    grep -q '"SCOUT_MODEL":"custom-scout"' "$runDoc"
    grep -q '"REVIEW_MODEL":"custom-reviewer"' "$runDoc"
    grep -q '"COMPLETE_LABEL":"custom-done"' "$runDoc"

    # Default COMPLETE_LABEL baked into a default harness.
    grep -q '"COMPLETE_LABEL":"agent-complete"' ${harness.runInputDocumentFile}

    # Default runtime is podman; the docker/rancher harnesses bake their own
    # runtime value verbatim (rancher is a knob value, not a binary name —
    # the nerdctl alias lives in the Go runner package, not here).
    grep -q '"RUNTIME":"podman"' ${harness.runInputDocumentFile}
    grep -q '"RUNTIME":"docker"' ${dockerHarness.runInputDocumentFile}
    grep -q '"RUNTIME":"rancher"' ${rancherHarness.runInputDocumentFile}

    # bwrap harness bakes bwrap runtime and agent store paths; no OCI store paths.
    grep -q '"RUNTIME":"bwrap"' ${bwrapHarness.runInputDocumentFile}
    grep -q '"AGENT_FILES":' ${bwrapHarness.runInputDocumentFile}
    grep -q '"AGENT_ENV":' ${bwrapHarness.runInputDocumentFile}
    # IMAGE_ARCHIVE is not baked as a store path (empty-default guard is fine).
    ! grep -q '"IMAGE_ARCHIVE":"/nix/store/' ${bwrapHarness.runInputDocumentFile}
    grep -q '"AGENT_FILES_DRV":' ${bwrapHarness.buildInputDocumentFile}
    grep -q '"AGENT_ENV_DRV":' ${bwrapHarness.buildInputDocumentFile}
    ! grep -q '"IMAGE_DRV":' ${bwrapHarness.buildInputDocumentFile}
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
  # /caveman, /tdd, /to-tickets, /commit, and /code-review. The skill-file
  # analogue of the nil/shellcheck baked-toolchain guards above (issue #486);
  # fails if the dogfood config stops baking any of them or reverts to the
  # flat layout.
  caveman-baked-in-dogfood = pkgs.runCommand "caveman-baked-in-dogfood" { } ''
    test -s ${harness.skillsDir}/caveman/SKILL.md
    test -s ${harness.skillsDir}/tdd/SKILL.md
    test -s ${harness.skillsDir}/to-tickets/SKILL.md
    test -s ${harness.skillsDir}/commit/SKILL.md
    test -s ${harness.skillsDir}/code-review/SKILL.md
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
              agents.models.scout = "scout-test";
              agents.models.review = "review-test";
              issues.labels.complete = "done-test";
              infra.image.packages = p: [ p.hello ];
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

  # The flakeModule must expose `agents.promptDir` as a real domain-tree
  # option (issue #2200 slice 2, following slice 1's `flakeOption = true;
  # nixSubPath = "promptDir";` on the schema entry). A consumer that sets
  # `agents.promptDir` gets byte-identical outputs to a direct mkHarness call
  # with the equivalent flat `spindriftPromptDir` default, and the value
  # bakes into the generated input document as SPINDRIFT_PROMPT_DIR.
  flakemodule-prompt-dir =
    let
      consumer2200 =
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
              agents.promptDir = "prompt-dir-test";
            };
          };
      direct2200 = import ../../lib/mkHarness.nix {
        inherit nixpkgs system;
        packages = p: [ p.hello ];
        defaults = {
          spindriftPromptDir = "prompt-dir-test";
        };
      };
      consumerPkgs2200 = consumer2200.packages.${system};
    in
    pkgs.runCommand "flakemodule-prompt-dir"
      {
        moduleSpindrift = consumerPkgs2200.spindrift;
        directSpindrift = direct2200.spindrift;
        doc = direct2200.runInputDocumentFile;
      }
      ''
        [ "$moduleSpindrift" = "$directSpindrift" ] \
          || { echo "spindrift mismatch: $moduleSpindrift != $directSpindrift" >&2; exit 1; }
        grep -q '"SPINDRIFT_PROMPT_DIR":"prompt-dir-test"' "$doc" \
          || { echo "SPINDRIFT_PROMPT_DIR=prompt-dir-test not baked in the input document" >&2; exit 1; }
        touch $out
      '';

  # ADR 0037 Pass 1 (issue #2179): the OLD settings.* / flat structural paths
  # must keep working (deprecation shims that forward via lib.warn) — a
  # consumer that sets a representative spread of knobs via old paths gets
  # byte-identical outputs to a direct mkHarness call with the equivalent
  # flat `defaults`. Deprecation warnings print during eval; that's expected.
  flakemodule-alias-parity =
    let
      consumer105b =
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
              settings = {
                branches = {
                  mergeMode = "immediate";
                };
                models = {
                  filerModel = "claude-haiku-4-5-20251001";
                };
                repository = {
                  boxForgeAndIssueAccess = "read-only";
                };
              };
              driver = "claude";
              # Deprecated flat nixpkgs path (issue #2179 Pass 2): exercises
              # the old-path fallback so structuralResolved.nixpkgs stays
              # symmetric with the other structural knobs.
              nixpkgs = nixpkgs;
              packages = p: [ p.hello ];
            };
          };
      direct105b = import ../../lib/mkHarness.nix {
        inherit nixpkgs system;
        defaults = {
          mergeMode = "immediate";
          filerModel = "claude-haiku-4-5-20251001";
          boxForgeAndIssueAccess = "read-only";
        };
        driver = "claude";
        packages = p: [ p.hello ];
      };
      consumerPkgs105b = consumer105b.packages.${system};
    in
    pkgs.runCommand "flakemodule-alias-parity"
      {
        moduleSpindrift = consumerPkgs105b.spindrift;
        directSpindrift = direct105b.spindrift;
      }
      ''
        [ "$moduleSpindrift" = "$directSpindrift" ] \
          || { echo "spindrift mismatch: $moduleSpindrift != $directSpindrift" >&2; exit 1; }
        touch $out
      '';

  # ADR 0037 (issue #2522): the 13 flat legacy shim options (oldFlatShims) must
  # be GENERATED from the same single hand-written structuralOptions
  # declaration as the domain-tree entries, not hand-copied — the shim's
  # description is a one-line auto-generated rename pointer (not the
  # domain-tree option's own doc text, which was the pre-#2522 verbatim
  # copy). Pins the generated shape via one representative knob (driver) so a
  # future hand-copy regression is caught at eval time.
  flakemodule-legacy-shim-description-generated =
    let
      eval =
        flake-parts.lib.evalFlakeModule
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
          };
      actual = (eval.options.perSystem.type.getSubOptions [ "perSystem" ]).spindrift.driver.description;
      expected = "perSystem.spindrift.driver is deprecated; use perSystem.spindrift.agents.driver.";
    in
    pkgs.runCommand "flakemodule-legacy-shim-description-generated" { } (
      if actual == expected then
        "touch $out"
      else
        throw "oldFlatShims.driver.description mismatch: got `${actual}`, want `${expected}`"
    );

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
      # settings.<section>.<knob> is grouped by section; mkHarness's `defaults`
      # is flat (schema attr name -> value, issue #353's own direct105 mirror
      # above does the same flattening by hand). Schema attr names are unique
      # across sections, so a plain right-biased merge is lossless.
      flattenSettings = cfg: pkgs.lib.foldl' (acc: section: acc // section) { } (pkgs.lib.attrValues cfg);
      mkRun =
        settingsCfg:
        let
          moduleSpindrift =
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
          # A direct mkHarness call with the equivalent flat `defaults`, so the
          # baked document is reachable (the flakeModule shim exposes only
          # `packages`/`apps`, not mkHarness's full attrset) — validated
          # against the module path by the derivation-equality assert below,
          # the same pattern flakemodule-schema-options above uses.
          direct = import ../../lib/mkHarness.nix {
            inherit nixpkgs system;
            packages = p: [ p.hello ];
            defaults = flattenSettings settingsCfg;
          };
        in
        {
          inherit moduleSpindrift;
          inherit (direct) spindrift runInputDocumentFile;
        };

      assertModuleMatchesDirect =
        run:
        assert assertMsg (
          run.moduleSpindrift == run.spindrift
        ) "spindrift mismatch: ${run.moduleSpindrift} != ${run.spindrift}";
        run;

      behaviorRun = assertModuleMatchesDirect (mkRun {
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
      });

      identityRun = assertModuleMatchesDirect (mkRun {
        repository = {
          repoSlug = "test-org/test-repo";
          gitUserName = "Test Bot";
          gitUserEmail = "bot@test.example";
        };
      });

      # REPO_SLUG without a consumer setting must bake an *empty* value so
      # runtime required-validation is not masked. The document renders
      # `"REPO_SLUG":""`; the grep matches that exact empty-string pair.
      defaultRun = assertModuleMatchesDirect (mkRun { });

      # ISSUE_NUMBER must not be settable via settings (per-run dispatch
      # override; keep-off list). Forcing .moduleSpindrift (not just the
      # lazily-returned mkRun attrset) is what actually reaches the
      # flake-parts module evaluation an unknown option throws from.
      badIssueNumber =
        builtins.tryEval
          (mkRun {
            issueDiscovery.issueNumber = "42";
          }).moduleSpindrift;
    in
    assert assertMsg (
      !badIssueNumber.success
    ) "ISSUE_NUMBER must not be settable via settings.issueDiscovery (keep-off list)";
    pkgs.runCommand "flakemodule-widen-operator-knobs"
      {
        behaviorDoc = behaviorRun.runInputDocumentFile;
        identityDoc = identityRun.runInputDocumentFile;
        defaultDoc = defaultRun.runInputDocumentFile;
      }
      ''
        grep -q '"MAX_FIX_ATTEMPTS":"5"' "$behaviorDoc" \
          || { echo "MAX_FIX_ATTEMPTS=5 not baked in the input document" >&2; exit 1; }
        grep -q '"MAX_REBASE_ATTEMPTS":"2"' "$behaviorDoc" \
          || { echo "MAX_REBASE_ATTEMPTS=2 not baked in the input document" >&2; exit 1; }
        grep -q '"HOLD_JITTER_SECS":"10"' "$behaviorDoc" \
          || { echo "HOLD_JITTER_SECS=10 not baked in the input document" >&2; exit 1; }
        grep -q '"TRANSIENT_BACKOFF_SECS":"60"' "$behaviorDoc" \
          || { echo "TRANSIENT_BACKOFF_SECS=60 not baked in the input document" >&2; exit 1; }
        grep -q '"TRANSIENT_RETRY_MAX":"5"' "$behaviorDoc" \
          || { echo "TRANSIENT_RETRY_MAX=5 not baked in the input document" >&2; exit 1; }
        grep -q '"MAX_JOBS":"2"' "$behaviorDoc" \
          || { echo "MAX_JOBS=2 not baked in the input document" >&2; exit 1; }
        grep -q '"MERGE_POLL_INTERVAL":"90"' "$behaviorDoc" \
          || { echo "MERGE_POLL_INTERVAL=90 not baked in the input document" >&2; exit 1; }
        grep -q '"MERGE_POLL_TIMEOUT":"3600"' "$behaviorDoc" \
          || { echo "MERGE_POLL_TIMEOUT=3600 not baked in the input document" >&2; exit 1; }
        grep -q '"REPO_SLUG":"test-org/test-repo"' "$identityDoc" \
          || { echo "REPO_SLUG=test-org/test-repo not baked in the input document" >&2; exit 1; }
        grep -q '"GIT_USER_NAME":"Test Bot"' "$identityDoc" \
          || { echo "GIT_USER_NAME='Test Bot' not baked in the input document" >&2; exit 1; }
        grep -q '"GIT_USER_EMAIL":"bot@test.example"' "$identityDoc" \
          || { echo "GIT_USER_EMAIL=bot@test.example not baked in the input document" >&2; exit 1; }
        grep -q '"REPO_SLUG":""' "$defaultDoc" \
          || { echo "REPO_SLUG must be empty in the document when not set; required validation must not be masked" >&2; exit 1; }
        touch $out
      '';

  # The `roster` knob (issue #264, lib/roster.nix) must be reachable through
  # the flakeModule Consumer surface, not only via a raw `mkHarness` call — a
  # Consumer that sets `perSystem.spindrift.roster` must get byte-identical
  # outputs to a direct `mkHarness` call with the same `roster`.
  flakemodule-roster =
    let
      testRoster = [
        {
          name = "scout";
          model = "claude-haiku-4-5-20251001";
          mode = "subagent";
          description = "Map relevant files, seams, and tests; return a structured brief";
          tools = [
            "Read"
            "Bash"
            "WebFetch"
            "WebSearch"
            "Glob"
            "Grep"
          ];
          promptFile = "scout-prompt.md";
          prompt = null;
        }
        {
          name = "auditor";
          model = "claude-opus-4-5-20251101";
          mode = "subagent";
          description = "Independently audit the diff for correctness before merge";
          tools = [
            "Read"
            "Bash"
            "WebFetch"
          ];
          promptFile = "auditor-prompt.md";
          prompt = null;
        }
      ];
      consumer106 =
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
              roster = testRoster;
            };
          };
      direct106 = import ../../lib/mkHarness.nix {
        inherit nixpkgs system;
        packages = p: [ p.hello ];
        roster = testRoster;
      };
      consumerPkgs106 = consumer106.packages.${system};
    in
    pkgs.runCommand "flakemodule-roster"
      {
        moduleSpindrift = consumerPkgs106.spindrift;
        directSpindrift = direct106.spindrift;
      }
      ''
        [ "$moduleSpindrift" = "$directSpindrift" ] \
          || { echo "spindrift mismatch: $moduleSpindrift != $directSpindrift" >&2; exit 1; }
        touch $out
      '';

  # ADR 0037 (issue #2522 slice 2): the structural-knob forwarding chain in
  # config.perSystem (structuralArgs) must forward a knob reached via its NEW
  # domain-tree path, not just the deprecated flat path flakemodule-alias-parity
  # exercises above. Picks `extraClosures` (infra.image.extraClosures) since no
  # other flakemodule-* check routes a structural knob through the new path.
  # Pins the forwarding chain's derivation from `structuralPlacements` against
  # a hand-written-chain regression.
  flakemodule-structural-domaintree-parity =
    let
      testExtraClosures = p: [ p.cowsay ];
      consumer107 =
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
              infra.image.extraClosures = testExtraClosures;
            };
          };
      direct107 = import ../../lib/mkHarness.nix {
        inherit nixpkgs system;
        packages = p: [ p.hello ];
        extraClosures = testExtraClosures;
      };
      consumerPkgs107 = consumer107.packages.${system};
    in
    pkgs.runCommand "flakemodule-structural-domaintree-parity"
      {
        moduleSpindrift = consumerPkgs107.spindrift;
        directSpindrift = direct107.spindrift;
      }
      ''
        [ "$moduleSpindrift" = "$directSpindrift" ] \
          || { echo "spindrift mismatch: $moduleSpindrift != $directSpindrift" >&2; exit 1; }
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

  # A schema knob that declares `choices` (issue #2519) must be exposed on
  # the flakeModule domain-tree surface as `types.nullOr (types.enum
  # entry.choices)`, not the type/int/bool/str inference `mkKnobOption` falls
  # back to for a choiceless knob — an out-of-enum value must throw at eval
  # time the same way `structuralPlacements.runtime` (a hand-written enum)
  # already does, naming the option path and the valid choices. Exercises
  # `mergeMode` (lib/env-schema.nix), whose domain-tree path is
  # `git.merge.policy` per its `nixSubPath`; a valid choice must still
  # evaluate cleanly so this pins acceptance, not just rejection.
  #
  # Forced only through `legacyPackages.mergePolicyProbe` — a plain read of
  # `config.spindrift.git.merge.policy` — never through `.packages.${system}`,
  # which would route through `mkHarness`'s own choices assert on
  # `documentSettings` (issue #2519's other guard, lib/mkHarness.nix) and
  # pass this check even with `mkKnobOption`'s enum branch deleted: mkHarness
  # throws naming the env var (`MERGE_MODE="bogus"`), not the option path,
  # so a failure sourced there wouldn't be pinning what this check claims to
  # pin. Reading only the option's resolved config value forces nothing but
  # the flakeModule option's own type check, so any throw here can only be
  # the enum type's — never mkHarness's — by construction.
  flakemodule-rejects-invalid-choice =
    let
      inherit (pkgs.lib) assertMsg;
      mkMergePolicyFlake =
        policy:
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
            perSystem =
              { config, ... }:
              {
                spindrift.git.merge.policy = policy;
                legacyPackages.mergePolicyProbe = config.spindrift.git.merge.policy;
              };
          }
        ).legacyPackages.${system}.mergePolicyProbe;
      badPolicy = builtins.tryEval (mkMergePolicyFlake "bogus");
      goodPolicy = builtins.tryEval (mkMergePolicyFlake "auto");
    in
    assert assertMsg (
      !badPolicy.success
    ) "flakeModule must throw on out-of-enum value 'bogus' for git.merge.policy (mergeMode's choices)";
    assert assertMsg (goodPolicy.success
    ) "flakeModule must accept an in-enum value ('auto') for git.merge.policy (mergeMode's choices)";
    pkgs.runCommand "flakemodule-rejects-invalid-choice" { } "touch $out";

  # The dogfood's tuned leaf values (mergeMode, autoFormat, autoLint, the
  # roster's `filer` model) must be defined exactly once, in
  # nix/dogfood-defaults.nix, and consumed by both flake.nix's `spindrift`
  # module config and fixtures.nix's direct mkHarness mirror — not
  # hand-restated at each site (issue #459). Commit faf8d2d is that
  # hand-restatement drifting once already. `prefetch` is not pinned here:
  # fixtures.nix's harnessNoRevision legitimately reuses the same command
  # string for the (out-of-scope, per issue #459) template mirror, so it
  # isn't a safe drift discriminant. The legacy `filerModel` knob the roster
  # superseded (issue #2388/#2435) gets the same hand-restatement guard even
  # though it's gone from nix/dogfood-defaults.nix on purpose: see
  # `leakOnlyLiterals` below, checked for leakage but never required present.
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
      defaultsSrc = builtins.readFile ../dogfood-defaults.nix;
      literals = [
        ''mergeMode = "immediate"''
        "autoFormat = true"
        "autoLint = true"
        ''filer = "claude-haiku-4-5-20251001"''
      ];
      # The legacy `filerModel` knob (superseded by the roster's `models.filer`
      # per issue #2388/#2435) is deliberately absent from
      # nix/dogfood-defaults.nix now -- it must not be asserted `missing`
      # there. But a hand-restatement of the old positional knob at either
      # consumption site (e.g. reverting to `filerModel = "..."` instead of
      # the roster) is exactly the kind of drift this check exists to catch,
      # so it stays tracked for `leaked` only.
      leakOnlyLiterals = [
        ''filerModel = "claude-haiku-4-5-20251001"''
      ];
      leaked = filter (l: hasInfix l flakeSrc || hasInfix l fixturesSrc) (literals ++ leakOnlyLiterals);
      missing = filter (l: !hasInfix l defaultsSrc) literals;
    in
    # A respelling in nix/dogfood-defaults.nix (e.g. reformatted quoting)
    # would make `leaked` vacuously empty without the value having moved
    # anywhere -- assert the tracked literal still lives where it's supposed
    # to, not just that it's absent from the two hand-restatement sites.
    assert assertMsg (missing == [ ])
      "dogfood leaf value(s) not found in nix/dogfood-defaults.nix -- literals list is stale, update it to match: ${concatStringsSep ", " missing}";
    assert assertMsg (leaked == [ ])
      "dogfood leaf value(s) hand-restated outside nix/dogfood-defaults.nix: ${concatStringsSep ", " leaked}";
    pkgs.runCommand "dogfood-leaf-values-single-source" { } "touch $out";

  # The dogfood's `memoryLimit` leaf must resolve differently per Consumer
  # host platform (issue #2379): native Linux runs the container directly on
  # host RAM, so no `--memory` cap is warranted there, while darwin runs
  # podman inside a fixed-RAM VM where the historical 5g cap (issue #712)
  # still applies. Same cross-system-at-pure-eval-time technique as
  # skills-content-form-drvpath-host-independent below — import
  # nix/dogfood-defaults.nix twice, differing only in `system`, and assert
  # on the resulting `defaults.memoryLimit` split.
  dogfood-memory-limit-platform-aware =
    let
      inherit (pkgs.lib) assertMsg;
      defaultsLinux = import ../dogfood-defaults.nix {
        system = "aarch64-linux";
        lib = pkgs.lib;
      };
      defaultsDarwin = import ../dogfood-defaults.nix {
        system = "aarch64-darwin";
        lib = pkgs.lib;
      };
    in
    assert assertMsg (defaultsLinux.defaults.memoryLimit == "")
      ''dogfood memoryLimit must be unset ("") on native Linux (issue #2379): got "${defaultsLinux.defaults.memoryLimit}"'';
    assert assertMsg (defaultsDarwin.defaults.memoryLimit == "5g")
      ''dogfood memoryLimit must stay "5g" on darwin/podman-in-VM (issue #2379): got "${defaultsDarwin.defaults.memoryLimit}"'';
    pkgs.runCommand "dogfood-memory-limit-platform-aware" { } "touch $out";

  # The dogfood Consumer config sources agent models/efforts from an explicit
  # roster (lib/roster.nix's defaultRoster) instead of the legacy `filerModel`
  # knob (issue #2388): `defaults` must no longer carry `filerModel` directly,
  # and the roster's `filer` entry must still carry the Filer's (#393) tuned
  # model as the roster's sole local pin. The dogfood config also carries no
  # local `reviewEffort` pin (issue #2512): the roster's `reviewer` entry's
  # effort flows straight to the orchestrator's code-owned review pass (issue
  # #2387) via the Handoff, so the `effortMismatches` assertion below --
  # comparing the *resolved* roster against `rosterHelper.rosterDefaults` per
  # agent, including `reviewer` -- checks that the dogfood roster's per-agent
  # efforts still match rosterDefaults. It does not independently check that
  # the dogfood config carries no local `reviewEffort` pin at all -- that
  # assertion was deleted along with the pin itself and nothing replaces it;
  # inert today only because flake.nix no longer wires
  # dogfood-defaults.nix's `reviewEffort` field into the flake options, so
  # there is nothing left to pin. scout, reviewer, and
  # worker are all left unmentioned in the roster's `models` (issue #2435) and
  # must resolve to their `lib/env-schema.nix` schema defaults as of this
  # writing -- claude-haiku-4-5-20251001, claude-opus-5, and claude-sonnet-5
  # respectively (issue #2434/#2433). Each assertion below is anchored to
  # that literal rather than re-derived from the schema: comparing against
  # e.g. `schema.reviewModel.default` would pass no matter what the schema
  # default drifted to, defeating the point of a regression guard (issue
  # #2435 AC2). Also pins the roster's fixed per-agent efforts (issue #2386),
  # read from rosterDefaults below rather than restated here.
  dogfood-roster-and-review-effort =
    let
      inherit (pkgs.lib)
        assertMsg
        filterAttrs
        listToAttrs
        mapAttrs
        nameValuePair
        ;
      defaults = import ../dogfood-defaults.nix {
        system = "aarch64-linux";
        lib = pkgs.lib;
      };
      rosterByName = listToAttrs (map (e: nameValuePair e.name e) defaults.roster);
      # Single source of truth for the per-agent effort literals below
      # (issue #2506): read from lib/roster-schema-defaults.nix instead of
      # restating them by hand. Deliberately does NOT extend to
      # expectedModels above -- see its own comment for why (issue #2435
      # AC2).
      rosterHelper = import ../../lib/roster-schema-defaults.nix { inherit (pkgs) lib; };
      # Per-agent rationale for the expected models below (folded out of the
      # individual asserts this table replaced): filer keeps its tuned model
      # claude-haiku-4-5-20251001 (issue #2388, was #393) as the roster's sole
      # local pin. reviewer, scout, and worker are all left unmentioned in the
      # roster's `models` and must resolve to their `lib/env-schema.nix`
      # schema defaults as of this writing -- reviewer to claude-opus-5, the
      # model the code-owned review pass binds to (issue #2435); scout to
      # claude-haiku-4-5-20251001 (issue #2435 AC2); worker to
      # claude-sonnet-5 (issue #2435 AC2).
      expectedModels = {
        filer = "claude-haiku-4-5-20251001";
        reviewer = "claude-opus-5";
        scout = "claude-haiku-4-5-20251001";
        worker = "claude-sonnet-5";
      };
      modelMismatches = filterAttrs (
        name: model: rosterByName.${name}.model or null != model
      ) expectedModels;
      expectedEfforts = mapAttrs (_: v: v.effort) rosterHelper.rosterDefaults;
      effortMismatches = filterAttrs (
        name: effort: rosterByName.${name}.effort or null != effort
      ) expectedEfforts;
    in
    assert assertMsg (!(defaults.defaults ? filerModel))
      "dogfood defaults must not carry the deprecated filerModel knob once a roster is set (issue #2388)";
    assert assertMsg (
      modelMismatches == { }
    ) "dogfood roster per-agent model mismatch(es): ${builtins.toJSON modelMismatches}";
    assert assertMsg (
      effortMismatches == { }
    ) "dogfood roster per-agent effort mismatch(es): ${builtins.toJSON effortMismatches}";
    pkgs.runCommand "dogfood-roster-and-review-effort" { } "touch $out";

  # AC1 (issue #2435): the dogfood's roster must name only the Filer -- scout,
  # reviewer, and worker inherit their schema defaults by staying unmentioned.
  # The assertions above only see the *resolved* roster, which can't tell a
  # genuinely-unmentioned agent from one re-pinned to the same value its
  # schema default already produces (e.g. re-adding `reviewer =
  # "claude-opus-5"` to `models` would still pass every assertion above).
  # Grep dogfood-defaults.nix's own `models` attrset source instead, so a
  # config-time regression toward re-pinning is caught even when it happens
  # to match the current schema default. The `${name}[[:space:]]*=` match
  # (rather than a whitespace-exact `${name} =`) tolerates a respelling like
  # `reviewer="claude-opus-5"` that would otherwise evade a plain substring
  # check. This is still a textual check, not a parser -- it can't tell code
  # from a comment, same inherent limitation as
  # dogfood-leaf-values-single-source above. `lib/roster.nix`'s
  # `defaultRoster` also accepts four legacy positional knobs
  # (scoutModel/reviewModel/filerModel/workerModel) that take the same
  # precedence as an entry in `models`, so a regression could re-pin an agent
  # through one of those instead of `models` -- scan the whole file's source
  # (not just the `models` block) for the three that would violate AC1;
  # `filerModel` is deliberately excluded since `filer` is the one agent this
  # roster is allowed to pin.
  dogfood-roster-names-only-filer =
    let
      inherit (pkgs.lib)
        assertMsg
        concatStringsSep
        filter
        hasInfix
        head
        splitString
        ;
      src = builtins.readFile ../dogfood-defaults.nix;
      afterModels = splitString "models = {" src;
      modelsBlock =
        if builtins.length afterModels < 2 then
          ""
        else
          head (splitString "};" (builtins.elemAt afterModels 1));
      named = filter (name: builtins.match ".*${name}[[:space:]]*=.*" modelsBlock != null) [
        "scout"
        "reviewer"
        "worker"
      ];
      legacyKnobsFound = filter (knob: hasInfix knob src) [
        "scoutModel ="
        "reviewModel ="
        "workerModel ="
      ];
    in
    assert assertMsg (modelsBlock != "")
      "dogfood-roster-names-only-filer couldn't find a `models = { ... }` block in nix/dogfood-defaults.nix -- check moved or renamed";
    assert assertMsg (named == [ ])
      "dogfood roster's models attrset must name only filer (issue #2435 AC1); found: ${concatStringsSep ", " named}";
    assert assertMsg (legacyKnobsFound == [ ])
      "dogfood-defaults.nix must not pass the legacy scoutModel/reviewModel/workerModel knobs to defaultRoster -- they take the same precedence as a `models` entry and would re-pin an agent that must stay unmentioned (issue #2435 AC1); found: ${concatStringsSep ", " legacyKnobsFound}";
    pkgs.runCommand "dogfood-roster-names-only-filer" { } "touch $out";

  # driverExecBin.src must not contain *_test.go — the image drvPath
  # must be invariant under host-side launcher test churn (issue #474).
  # A tight fileset is the invariant; adding a new import outside it fails
  # the build loudly (missing package) rather than silently expanding the src.
  driver-exec-src-excludes-tests = pkgs.runCommand "driver-exec-src-excludes-tests" { } ''
    test_files=$(find ${nonRustHarness.driverExecBin.src} -name '*_test.go')
    if [ -n "$test_files" ]; then
      echo "driverExecBin.src contains *_test.go files:" >&2
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

  # The function-of-pkgs `packages` knob (like `extraClosures`, issue #469)
  # receives the image's own (always-Linux) pkgs as its argument — a Consumer
  # flake writes `packages = p: [ p.hello ];` and gets the Linux `hello`
  # regardless of host. If a future refactor instead closed the function over
  # the Consumer's host pkgs (e.g. via `nixpkgs.legacyPackages.${system}`),
  # the built derivations — and therefore the image drvPath — would diverge
  # between a linux and darwin Consumer host, reintroducing the ADR
  # 0019/issue #597 freshness-probe bug this file's other host-independence
  # checks guard against (issue #2114). Same cross-system-at-pure-eval-time
  # technique as skills-content-form-drvpath-host-independent above.
  packages-function-form-drvpath-host-independent =
    let
      inherit (pkgs.lib) assertMsg;
      packages = p: [ p.hello ];
      harnessLinux = import ../../lib/mkHarness.nix {
        inherit nixpkgs packages;
        system = "aarch64-linux";
      };
      harnessDarwin = import ../../lib/mkHarness.nix {
        inherit nixpkgs packages;
        system = "aarch64-darwin";
      };
    in
    assert assertMsg (harnessLinux.image.drvPath == harnessDarwin.image.drvPath) ''
      agent-image drvPath depends on the Consumer's host system via the
      `packages` knob (issue #597 / #2114):
        aarch64-linux:  ${harnessLinux.image.drvPath}
        aarch64-darwin: ${harnessDarwin.image.drvPath}'';
    pkgs.runCommand "packages-function-form-drvpath-host-independent" { } "touch $out";

  # The function-of-pkgs `extraClosures` knob (issue #469) receives the
  # image's own (always-Linux) pkgs as its argument, same contract as the
  # `packages` knob above. If a future refactor instead closed the function
  # over the Consumer's host pkgs, the built closure -- and therefore the
  # image drvPath -- would diverge between a linux and darwin Consumer host,
  # reintroducing the ADR 0019/issue #597 freshness-probe bug this file's
  # other host-independence checks guard against (issue #2114). Same
  # cross-system-at-pure-eval-time technique as
  # packages-function-form-drvpath-host-independent above.
  extraclosures-function-form-drvpath-host-independent =
    let
      inherit (pkgs.lib) assertMsg;
      extraClosures = p: [ p.cowsay ];
      harnessLinux = import ../../lib/mkHarness.nix {
        inherit nixpkgs extraClosures;
        system = "aarch64-linux";
      };
      harnessDarwin = import ../../lib/mkHarness.nix {
        inherit nixpkgs extraClosures;
        system = "aarch64-darwin";
      };
    in
    assert assertMsg (harnessLinux.image.drvPath == harnessDarwin.image.drvPath) ''
      agent-image drvPath depends on the Consumer's host system via the
      `extraClosures` knob (issue #597 / #2114):
        aarch64-linux:  ${harnessLinux.image.drvPath}
        aarch64-darwin: ${harnessDarwin.image.drvPath}'';
    pkgs.runCommand "extraclosures-function-form-drvpath-host-independent" { } "touch $out";

  # The `skills` knob's path/derivation form (lib/image.nix:352-376) is copied
  # verbatim via `cp -r ${f}` rather than re-realized with the image's own
  # pkgs, unlike the `{ name; src; }` content form covered above. A plain
  # source path is content-addressed and host-independent, so pointing
  # `skills` at one must not tag the image drvPath with the consumer host
  # system either — but a derivation realized with the consumer's *host*
  # pkgs would (issue #597 / #2114); this locks in the path-form half of that
  # invariant. Same cross-system-at-pure-eval-time technique as
  # skills-content-form-drvpath-host-independent above.
  skills-path-form-drvpath-host-independent =
    let
      inherit (pkgs.lib) assertMsg;
      skills = [ ../fixtures/skill-path-form ];
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
      agent-image drvPath depends on the Consumer's host system via the
      `skills` knob's path form (issue #597 / #2114):
        aarch64-linux:  ${harnessLinux.image.drvPath}
        aarch64-darwin: ${harnessDarwin.image.drvPath}'';
    pkgs.runCommand "skills-path-form-drvpath-host-independent" { } "touch $out";

  # Negative counterpart to the three host-independence checks above: proof
  # that they have teeth. Each of those checks asserts `==` between a
  # linux-consumer and darwin-consumer image drvPath for a knob that
  # correctly receives the image's own (always-Linux) pkgs. Here we
  # deliberately reintroduce the regression those checks guard against — a
  # knob closing over a derivation realized with the *Consumer's host* pkgs
  # (`import nixpkgs { system = <consumer system>; }`) rather than the
  # image's own — and assert the drvPaths DO diverge between a linux and
  # darwin consumer `system`. A host-realized derivation is expected to tag
  # the image drvPath with the consumer host system (issue #597 / #2114): if
  # this ever stopped diverging, the positive `==` checks above would have
  # gone vacuous (always passing regardless of whether the regression they
  # name is present). Covers all three knobs (`packages`, `extraClosures`,
  # `skills`) with one small harness-building helper.
  consumer-knob-host-realized-derivation-tags-drvpath =
    let
      inherit (pkgs.lib) assertMsg;
      hostPkgs = s: import nixpkgs { system = s; };
      regHarness =
        { knob, sys }:
        import ../../lib/mkHarness.nix (
          {
            inherit nixpkgs;
            system = sys;
          }
          // knob sys
        );
      diverges =
        knob:
        (regHarness {
          inherit knob;
          sys = "aarch64-linux";
        }).image.drvPath != (regHarness {
          inherit knob;
          sys = "aarch64-darwin";
        }).image.drvPath;
      pkgsKnob = sys: {
        packages = _: [ (hostPkgs sys).hello ];
      };
      extraClosuresKnob = sys: {
        extraClosures = _: [ (hostPkgs sys).cowsay ];
      };
      skillsKnob = sys: {
        skills = [ (hostPkgs sys).emptyDirectory ];
      };
    in
    assert assertMsg (diverges pkgsKnob) ''
      regression sanity check failed: a `packages` knob closing over a
      *host*-realized derivation (issue #597 / #2114) was expected to tag the
      image drvPath with the consumer host system, but linux and darwin
      drvPaths matched anyway -- the positive host-independence check above
      would not catch this regression.'';
    assert assertMsg (diverges extraClosuresKnob) ''
      regression sanity check failed: an `extraClosures` knob closing over a
      *host*-realized derivation (issue #597 / #2114) was expected to tag the
      image drvPath with the consumer host system, but linux and darwin
      drvPaths matched anyway -- the positive host-independence check above
      would not catch this regression.'';
    assert assertMsg (diverges skillsKnob) ''
      regression sanity check failed: a `skills` knob closing over a
      *host*-realized derivation (issue #597 / #2114) was expected to tag the
      image drvPath with the consumer host system, but linux and darwin
      drvPaths matched anyway -- the positive host-independence check above
      would not catch this regression.'';
    pkgs.runCommand "consumer-knob-host-realized-derivation-tags-drvpath" { } "touch $out";

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

  # A custom roster entry (the AC4 path) omitting both `promptFile` and
  # `prompt` must degrade gracefully -- not throw a cryptic missing-attribute
  # eval error. mkHarness routes every roster (explicit or default) through
  # `rosterLib.normalizeRoster` (issue #2152 slice B) before anything reads
  # `promptFile`, so by the time agentsPromptFilesJson/customRosterPromptFiles
  # run their now-unconditional `e.promptFile` reads, every entry already
  # carries one -- normalizeRoster's own default-injection is pinned
  # separately in nix/checks/roster.nix. Forcing `.spindrift` to a string
  # realizes the whole image-input graph, including the roster-derived JSON
  # map, so a regression reintroducing a bare unnormalized `e.promptFile`/
  # `e.prompt` read would throw here.
  mkharness-roster-custom-entry-missing-prompt-fields =
    let
      minimalRoster = [
        {
          name = "auditor";
          model = "claude-sonnet-5";
          description = "";
          tools = [ ];
          mode = "subagent";
        }
      ];
      direct = import ../../lib/mkHarness.nix {
        inherit nixpkgs system;
        packages = p: [ p.hello ];
        roster = minimalRoster;
      };
      result = builtins.tryEval (builtins.toString direct.spindrift);
    in
    assert pkgs.lib.assertMsg result.success
      "mkHarness must not throw when a custom roster entry omits promptFile/prompt (issue #264 review finding)";
    pkgs.runCommand "mkharness-roster-custom-entry-missing-prompt-fields" { } "touch $out";

  # The other AC4 shape: a custom roster entry carrying an inline `prompt` but
  # NO `promptFile`. Unlike the missing-both case above (dropped by
  # customRosterPromptFiles' `prompt != null` filter, so its bake line never
  # runs), this entry survives the filter and IS baked -- exercising the one
  # code path customRosterPromptFiles exists to serve. `resolvedRoster` is
  # normalized before customRosterPromptFiles is derived from it (issue #2152
  # slice B), so the entry's `promptFile` is already "auditor-prompt.md" by
  # the time the bake `cp` reads it unconditionally; a regression that read
  # `e.promptFile` before normalization would throw a missing-attribute error
  # here (issue #264 review finding).
  mkharness-roster-custom-entry-inline-prompt-no-file =
    let
      inlineRoster = [
        {
          name = "auditor";
          model = "claude-sonnet-5";
          description = "";
          tools = [ ];
          mode = "subagent";
          prompt = "You are the auditor. Review the diff.";
        }
      ];
      direct = import ../../lib/mkHarness.nix {
        inherit nixpkgs system;
        packages = p: [ p.hello ];
        roster = inlineRoster;
      };
      result = builtins.tryEval (builtins.toString direct.spindrift);
    in
    assert pkgs.lib.assertMsg result.success
      "mkHarness must not throw when a custom roster entry sets an inline prompt but omits promptFile (issue #264 review finding)";
    pkgs.runCommand "mkharness-roster-custom-entry-inline-prompt-no-file" { } "touch $out";

  # Issue #2512 (blocking review finding): reviewEffort is the one legacy
  # knob documented (lib/roster.nix) to override the reviewer entry's effort
  # regardless of roster source -- unlike the four legacy model knobs, which
  # are explicit-roster-wins. mkHarness must apply it as a
  # post-normalize step on `finalRoster`, so it reaches an explicit
  # caller-supplied `roster` exactly the same way it reaches the
  # `defaultRoster` fallback path. Both branches below assert against
  # mkHarness's own exposed `.roster` output (lib/mkHarness.nix, issue
  # #2512), not a re-derivation of the override logic, so a regression that
  # only fixed one of the two roster sources would still fail here.
  mkharness-review-effort-overrides-default-roster =
    let
      direct = import ../../lib/mkHarness.nix {
        inherit nixpkgs system;
        packages = p: [ p.hello ];
        defaults.reviewEffort = "xhigh";
      };
    in
    assert pkgs.lib.assertMsg (reviewerEffortOf direct.roster == "xhigh")
      "mkHarness must apply a non-empty defaults.reviewEffort to the defaultRoster-resolved reviewer entry, got: ${builtins.toJSON (reviewerEffortOf direct.roster)}";
    pkgs.runCommand "mkharness-review-effort-overrides-default-roster" { } "touch $out";

  mkharness-review-effort-overrides-explicit-roster =
    let
      explicitRoster = [
        {
          name = "reviewer";
          model = "claude-opus-5";
          effort = "low";
          mode = "subagent";
          description = "";
          tools = [ ];
        }
      ];
      direct = import ../../lib/mkHarness.nix {
        inherit nixpkgs system;
        packages = p: [ p.hello ];
        roster = explicitRoster;
        defaults.reviewEffort = "xhigh";
      };
    in
    assert pkgs.lib.assertMsg (reviewerEffortOf direct.roster == "xhigh")
      "mkHarness must apply a non-empty defaults.reviewEffort to an explicit caller-supplied roster's reviewer entry, overriding its own \"low\" effort, got: ${builtins.toJSON (reviewerEffortOf direct.roster)}";
    pkgs.runCommand "mkharness-review-effort-overrides-explicit-roster" { } "touch $out";

  # The other half of the contract: an unset/empty reviewEffort must leave
  # the reviewer entry's effort untouched -- the override is opt-in, not a
  # blanket rewrite.
  mkharness-review-effort-empty-leaves-reviewer-effort-untouched =
    let
      direct = import ../../lib/mkHarness.nix {
        inherit nixpkgs system;
        packages = p: [ p.hello ];
      };
      rosterHelper = import ../../lib/roster-schema-defaults.nix { inherit (pkgs) lib; };
    in
    assert pkgs.lib.assertMsg
      (reviewerEffortOf direct.roster == rosterHelper.rosterDefaults.reviewer.effort)
      "mkHarness must leave the reviewer entry at its roster default effort (${rosterHelper.rosterDefaults.reviewer.effort}) when defaults.reviewEffort is unset, got: ${builtins.toJSON (reviewerEffortOf direct.roster)}";
    pkgs.runCommand "mkharness-review-effort-empty-leaves-reviewer-effort-untouched" { } "touch $out";
}
