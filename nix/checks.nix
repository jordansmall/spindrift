{ pkgs, config, fixtures, nixpkgs, system, flake-parts }:
let
  inherit (fixtures)
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
    templatePkgs
    ;
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
        # Not read by bats directly, but forces Nix to realise
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
  # call. Eval-only; the Linux realise is done on the podman builder
  # against an instantiated copy.
  template-fixture =
    pkgs.runCommand "template-fixture"
      {
        templateBuild = templatePkgs.build;
        templateRun = templatePkgs.run;
        directBuild = harness.build;
        directRun = harness.run;
        imagePath = harness.imagePath;
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

        # Same config as the dogfood ⇒ identical launcher commands.
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
      result = builtins.tryEval (import ../lib/mkHarness.nix {
        inherit nixpkgs system;
        defaults = { typoLabel = "oops"; };
      });
    in
    assert assertMsg (!result.success)
      "mkHarness must throw on unknown defaults key 'typoLabel'";
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
    assert assertMsg (baked "hello-")
      "expected the hello package baked into the env";
    # engine plumbing is still layered on, language-agnostically
    assert assertMsg (baked "git-")
      "expected git plumbing layered into the env";
    pkgs.runCommand "packages-baked" { } "touch $out";

  # Nix is the first-class default: every box ships the nix CLI unless
  # the Consumer opts into the lean escape hatch (nixInBox = false).
  nix-baked-by-default =
    let
      inherit (pkgs.lib) assertMsg any hasInfix;
      names = map (p: p.name or "") nonRustHarness.agentEnv.paths;
      hasNix = any (n: hasInfix "nix-" n || n == "nix") names;
    in
    assert assertMsg hasNix
      "expected the nix CLI to be baked into the default box";
    pkgs.runCommand "nix-baked-by-default" { } "touch $out";

  # The lean/no-nix escape hatch must not include the nix CLI.
  lean-escape-hatch =
    let
      inherit (pkgs.lib) assertMsg any hasInfix;
      names = map (p: p.name or "") leanHarness.agentEnv.paths;
      hasNix = any (n: hasInfix "nix-" n || n == "nix") names;
    in
    assert assertMsg (!hasNix)
      "lean harness (nixInBox = false) must not bake in the nix CLI";
    pkgs.runCommand "lean-escape-hatch" { } "touch $out";

  # The flakeModule must expose scoutModel, reviewModel, and completeLabel
  # as declarative options (today's drift class, issue #105). A consumer
  # that sets those three gets byte-identical outputs to a direct mkHarness
  # call with the same defaults.
  flakemodule-schema-options =
    let
      consumer105 = flake-parts.lib.mkFlake
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
            defaults = {
              scoutModel = "scout-test";
              reviewModel = "review-test";
              completeLabel = "done-test";
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

  # harness.env.example must match the content generated from env-schema.nix.
  # Fails when a new schema knob is added but the committed file is not
  # regenerated (golden-file drift; resolves issue #109).
  harness-env-example =
    let
      schema = import ../lib/env-schema.nix;
      inherit (pkgs.lib)
        attrValues
        concatStrings
        filterAttrs
        mapAttrsToList
        ;
      # Render one entry: required/secret → uncommented; optional → commented.
      renderEntry =
        _key: entry:
        let
          active = (entry.required or false) || (entry.secret or false);
          value =
            if (entry.required or false) && !(entry.secret or false) then
              entry.placeholder or ""
            else
              toString (entry.default or "");
          prefix = if active then "" else "# ";
        in
        "# ${entry.doc}\n${prefix}${entry.env}=${value}\n\n";
      generated = pkgs.writeText "harness.env.example.generated" (
        "# Copy to harness.env (gitignored) and fill in — or export these in your shell.\n\n"
        + concatStrings (mapAttrsToList renderEntry schema)
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
        "DEV_SHELL_PROBE_TIMEOUT"
      ];
      # Forward: every schema name (that Go reads directly) must appear as a
      # string literal in main.go.
      missingFromGo = filter (name: !pkgs.lib.hasInfix ''"${name}"'' mainGoSrc) (subtractLists boxEnvOnly schemaEnvNames);
      # Reverse: extract names from os.Getenv/getenv calls in main.go.
      parts = builtins.split ''(os\.Getenv|getenv)\("([A-Z_][A-Z0-9_]*)"\)'' mainGoSrc;
      goEnvNames = map (m: builtins.elemAt m 1) (filter builtins.isList parts);
      extraInGo = subtractLists (schemaEnvNames ++ nixBaked) goEnvNames;
    in
    assert pkgs.lib.assertMsg (missingFromGo == [ ]) "schema knobs absent from main.go: ${concatStringsSep ", " missingFromGo}";
    assert pkgs.lib.assertMsg (extraInGo == [ ]) "main.go reads env vars absent from schema: ${concatStringsSep ", " extraInGo}";
    pkgs.runCommand "launcher-env-coverage" { } "touch $out";

  # cmd/launcher/flagtable_gen.go must match the content generated from
  # env-schema.nix by mkHarness.nix renderFlagTableGo.  Fails when a new
  # schema knob is added but the committed generated file is not regenerated.
  launcher-flag-table =
    let
      schema = import ../lib/env-schema.nix;
      inherit (pkgs.lib)
        concatStrings
        filterAttrs
        mapAttrsToList
        replaceStrings
        toLower
        ;
      nonSecretSchema = filterAttrs (_: e: !(e.secret or false)) schema;
      secretSchema = filterAttrs (_: e: (e.secret or false)) schema;
      toKebab = env: toLower (replaceStrings [ "_" ] [ "-" ] env);
      flagKind = e: if builtins.isInt (e.default or null) then "int" else "string";
      flagDflt = e: if e ? default then (if builtins.isInt e.default then builtins.toString e.default else e.default) else "";
      flagAlias = e: if e ? alias then ", alias: \"${e.alias}\"" else "";
      rows = concatStrings (
        mapAttrsToList (_: e:
          "\t{env: \"${e.env}\", flag: \"${toKebab e.env}\"${flagAlias e}, kind: \"${flagKind e}\", doc: \"${e.doc}\", dflt: \"${flagDflt e}\"},\n"
        ) nonSecretSchema
      );
      secretRows = concatStrings (
        mapAttrsToList (_: e:
          "\t{env: \"${e.env}\", doc: \"${e.doc}\", fileFlag: \"${toKebab e.env}-file\"},\n"
        ) secretSchema
      );
      generated = pkgs.writeText "flagtable_gen.go.generated" (
        "// Code generated by mkHarness.nix from lib/env-schema.nix. DO NOT EDIT.\n"
        + "package main\n"
        + "\n"
        + "// schemaFlags is the flag table derived from lib/env-schema.nix.\n"
        + "// Secret knobs are excluded from schemaFlags; see secretKnobs below.\n"
        + "// Run `nix flake check` after editing lib/env-schema.nix to regenerate.\n"
        + "var schemaFlags = []flagEntry{\n"
        + rows
        + "}\n"
        + "\n"
        + "// secretKnobs lists secret knobs that have no value flag.\n"
        + "// Callers must supply these via the environment or via --<fileFlag> path flag.\n"
        + "var secretKnobs = []secretKnob{\n"
        + secretRows
        + "}\n"
      );
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

  # gofmt -l must exit cleanly — any output means unformatted files.
  launcher-go-fmt = pkgs.runCommand "launcher-go-fmt"
    { nativeBuildInputs = [ pkgs.go ]; }
    ''
      unformatted=$(gofmt -l ${../cmd/launcher})
      if [ -n "$unformatted" ]; then
        echo "gofmt violations:" >&2
        echo "$unformatted" >&2
        exit 1
      fi
      touch $out
    '';

  # go vet catches suspicious constructs at analysis time.
  launcher-go-vet = pkgs.runCommand "launcher-go-vet"
    { nativeBuildInputs = [ pkgs.go ]; }
    ''
      cp -r ${../cmd/launcher} src
      chmod -R +w src
      export GOPROXY=off
      export GONOSUMCHECK='*'
      export GOMODCACHE="$TMPDIR/gomodcache"
      export GOCACHE="$TMPDIR/gocache"
      cd src
      go vet ./...
      touch $out
    '';

  # go test must stay green: unit tests catch config-parsing bugs
  # before they reach the binary (see issue #112, 9494fc1-class).
  launcher-go-test = pkgs.runCommand "launcher-go-test"
    { nativeBuildInputs = [ pkgs.go ]; }
    ''
      cp -r ${../cmd/launcher} src
      chmod -R +w src
      export GOPROXY=off
      export GONOSUMCHECK='*'
      export GOMODCACHE="$TMPDIR/gomodcache"
      export GOCACHE="$TMPDIR/gocache"
      cd src
      go test ./...
      touch $out
    '';

  # Cross-build: launcher must compile for linux and darwin. Native
  # (x86_64-linux on CI) plus explicit darwin cross-targets.
  # CGO_ENABLED=0 makes pure-Go cross-compilation work without
  # a C cross-toolchain.
  launcher-cross-build = pkgs.runCommand "launcher-cross-build"
    { nativeBuildInputs = [ pkgs.go ]; }
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
}
// pkgs.lib.optionalAttrs pkgs.stdenv.isLinux {
  # The baked entrypoint must carry a store-path shebang, not the
  # source's `#!/usr/bin/env bash` — the Box has no /usr/bin/env. Guards
  # against baking the raw source instead of the writeShellApplication
  # output. Realises the agent-files layer, so it is gated to a Linux
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

  # AGENTS_JSON_TEMPLATE baked into the entrypoint by nix (ADR 0007):
  # when both models are configured it contains the JSON produced by
  # builtins.toJSON; when either is unset it is the empty string.
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

    touch $out
  '';

  # The Box must run unprivileged: Claude Code refuses
  # --dangerously-skip-permissions under root. Assert the image config
  # runs as the non-root `agent` user. Realises the image, so it is
  # Linux-gated like the shebang check.
  box-runs-as-non-root =
    pkgs.runCommand "box-runs-as-non-root" { nativeBuildInputs = [ pkgs.jq ]; } ''
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
  # /nix/store mount (which a macOS podman VM cannot provide). Realises
  # the agent-files layer, so it is Linux-gated like the shebang check.
  prompt-baked-into-image = pkgs.runCommand "prompt-baked-into-image" { } ''
    grep -q 'CONFIGURED-PROMPT-MARKER' \
      ${promptHarness.agentFiles}/agent/prompts/issue-prompt.md
    grep -q 'git rebase' \
      ${promptHarness.agentFiles}/agent/prompts/conflict-resolve-prompt.md
    touch $out
  '';

  # Skills configured at build time must land in the agent-files layer at
  # /home/agent/.claude/skills so the Box is self-contained. Realises the
  # agent-files layer; Linux-gated like the other image checks.
  skills-baked-into-image = pkgs.runCommand "skills-baked-into-image" { } ''
    grep -q 'BAKED-SKILL-MARKER' \
      ${skillsHarness.agentFiles}/home/agent/.claude/skills/baked-skill.md
    touch $out
  '';

  # The nix.conf and store DB must be present in the image so
  # `nix flake check` reuses the baked closure instead of re-substituting.
  # Realises the default image; Linux-gated like the other image checks.
  nix-conf-in-image =
    pkgs.runCommand "nix-conf-in-image" { nativeBuildInputs = [ pkgs.jq ]; } ''
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
}
