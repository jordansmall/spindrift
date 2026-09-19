# Golden-scaffold eval guards (issue #2565, nix half). The Go golden tests pin
# the wizard's rendered bytes, not whether they evaluate, so this module imports
# each golden's own `outputs` function and wires its `spindrift` input to the
# in-repo lib/flakeModule.nix. It never evaluates a golden's `inputs` block, so
# a corrupt `inputs.nixpkgs.url` passes here but breaks a real `nix develop`.
{
  pkgs,
  nixpkgs,
  system,
  flake-parts,
  ...
}:
let
  inherit (pkgs.lib) assertMsg;

  # Single root for the runtime enum's valid values (ADR 0027), shared with
  # nix/checks/schema-drift.nix's runtime-values-gen check and the runner
  # module's generated value set. The check below uses the first entry as a
  # valid, in-enum replacement for the broken golden's out-of-enum value.
  runtimeValues = import ../../lib/runtime-values.nix;

  # Evaluates a golden flake.nix's own `outputs` function against stubbed
  # inputs, with `spindrift.flakeModules.default` wired to the local
  # lib/flakeModule.nix rather than a fetched copy of this repo. The
  # `self.inputs` stub is required because flake-parts computes a perSystem's
  # `inputs'` from it, and forcing `devShells.default` forces that computation.
  mkSpindriftInput = mod: {
    flakeModules.default = mod;
  };
  evalGoldenWith =
    spindriftInputArg: goldenPath:
    (import goldenPath).outputs {
      inherit nixpkgs flake-parts;
      self = {
        outPath = ../../.;
        inputs = {
          inherit nixpkgs flake-parts;
          spindrift = spindriftInputArg;
        };
      };
      spindrift = spindriftInputArg;
    };
  spindriftInput = mkSpindriftInput ../../lib/flakeModule.nix;
  evalGolden = evalGoldenWith spindriftInput;

  # Simulates a schema rename for the two checks below. An option can't be
  # removed by module composition, so the only way to make the real
  # `infra.runtime` path disappear is a patched copy of lib/flakeModule.nix
  # itself. Every needle is guarded by an assertMsg, since a real rename of
  # structuralOptions.runtime would otherwise make these checks vacuous.
  renamedRuntimeOption = "runtimeSchemaDrift";

  # lib/flakeModule.nix asserts that structuralOptions and structuralPlacements
  # share the same key set, so the rename below must move both the mkOption
  # entry and this placement's key and path segment together. builtins.toFile
  # writes content-addressed text at eval time with no derivation build, so the
  # patched copies here are not import-from-derivation.
  patchedStructuralPaths =
    let
      text = builtins.readFile ../../lib/structural-paths.nix;
      needle = "  runtime = [\n    \"infra\"\n    \"runtime\"\n  ];\n";
    in
    assert assertMsg (pkgs.lib.hasInfix needle text)
      "lib/structural-paths.nix no longer contains the expected runtime placement block — quickstart-golden-github-with-renamed-runtime-option-{fails,evaluates}'s patch needle is stale";
    builtins.toFile "structural-paths-runtime-schema-drift.nix" (
      builtins.replaceStrings
        [ needle ]
        [
          "  ${renamedRuntimeOption} = [\n    \"infra\"\n    \"${renamedRuntimeOption}\"\n  ];\n"
        ]
        text
    );

  # builtins.toFile writes at the store root, so every `import ./X.nix` in the
  # patched text must become an absolute store path first. The specific
  # structural-paths needle is tried before the generic one, per replaceStrings'
  # in-order-per-position matching. The generic rewrite points at the repo root,
  # not lib/, because lib/mkHarness.nix reads relative `../templates/...` paths.
  patchedFlakeModule =
    let
      text = builtins.readFile ../../lib/flakeModule.nix;
      optionNeedle = "runtime = mkOption {";
      structuralPathsNeedle = "import ./structural-paths.nix";
      relativeImportNeedle = "import ./";
      # structuralArgs forwards each structural knob to mkHarness under its own
      # flatName, and the unpatched mkHarness still only accepts `runtime`, so
      # the renamed knob needs a translation back to that engine-side name.
      structuralArgsNeedle = "\${flatName} = structuralResolved.\${flatName};";
    in
    assert assertMsg (pkgs.lib.hasInfix optionNeedle text)
      "lib/flakeModule.nix no longer contains the expected `${optionNeedle}` declaration — quickstart-golden-github-with-renamed-runtime-option-{fails,evaluates}'s patch needle is stale";
    assert assertMsg (pkgs.lib.hasInfix structuralPathsNeedle text)
      "lib/flakeModule.nix no longer contains `${structuralPathsNeedle}` — quickstart-golden-github-with-renamed-runtime-option-{fails,evaluates}'s patch needle is stale";
    assert assertMsg (pkgs.lib.hasInfix relativeImportNeedle text)
      "lib/flakeModule.nix no longer contains any `${relativeImportNeedle}` import — quickstart-golden-github-with-renamed-runtime-option-{fails,evaluates}'s patch needle is stale";
    assert assertMsg (pkgs.lib.hasInfix structuralArgsNeedle text)
      "lib/flakeModule.nix no longer contains the expected structuralArgs forwarding line `${structuralArgsNeedle}` — quickstart-golden-github-with-renamed-runtime-option-{fails,evaluates}'s patch needle is stale";
    builtins.toFile "flakeModule-runtime-schema-drift.nix" (
      builtins.replaceStrings
        [
          optionNeedle
          structuralPathsNeedle
          relativeImportNeedle
          structuralArgsNeedle
        ]
        [
          "${renamedRuntimeOption} = mkOption {"
          "import ${patchedStructuralPaths}"
          "import ${../..}/lib/"
          "\${if flatName == \"${renamedRuntimeOption}\" then \"runtime\" else flatName} = structuralResolved.\${flatName};"
        ]
        text
    );
  patchedSpindriftInput = mkSpindriftInput patchedFlakeModule;

  # Forces the packages output to prove the module actually ran, not just that
  # flake-parts.lib.mkFlake accepted the shape, and forces the golden's own
  # devShells.default block, which is no part of the spindrift module and would
  # otherwise never be evaluated here.
  mkEvaluatesCheckWith =
    evalFn: name: goldenPath:
    let
      outputs = evalFn goldenPath;
      spindrift = outputs.packages.${system}.spindrift;
      devShell = outputs.devShells.${system}.default;
    in
    pkgs.runCommand name { inherit spindrift devShell; } ''
      : "$spindrift"
      : "$devShell"
      touch $out
    '';
  mkEvaluatesCheck = mkEvaluatesCheckWith evalGolden;

  githubGolden = ../../cmd/launcher/quickstart/testdata/golden/github/flake.nix;
  forgejoGolden = ../../cmd/launcher/quickstart/testdata/golden/forgejo/flake.nix;
  brokenGolden = ../../cmd/launcher/quickstart/testdata/golden/broken/flake.nix;

  # Copy of the github golden with its infra.runtime line renamed to match
  # patchedFlakeModule's renamed option path, for the positive control below.
  githubGoldenRenamedRuntime =
    let
      text = builtins.readFile githubGolden;
      needle = ''infra.runtime = "podman";'';
    in
    assert assertMsg (pkgs.lib.hasInfix needle text)
      "cmd/launcher/quickstart/testdata/golden/github/flake.nix no longer contains the expected ${needle} — quickstart-golden-github-with-renamed-runtime-option-evaluates' replaceStrings needle is stale";
    builtins.toFile "golden-github-renamed-runtime.nix" (
      builtins.replaceStrings
        [ needle ]
        [
          ''infra.${renamedRuntimeOption} = "podman";''
        ]
        text
    );
in
{
  quickstart-golden-github-evaluates = mkEvaluatesCheck "quickstart-golden-github-evaluates" githubGolden;

  quickstart-golden-forgejo-evaluates = mkEvaluatesCheck "quickstart-golden-forgejo-evaluates" forgejoGolden;

  # The deliberately-broken golden sets infra.runtime to a value outside the
  # runtime enum and must fail to evaluate. That proves this check module can
  # detect a real schema regression instead of rubber-stamping anything that
  # parses as valid Nix syntax.
  quickstart-golden-broken-fails =
    let
      result = builtins.tryEval (evalGolden brokenGolden).packages.${system}.spindrift;
    in
    assert assertMsg (!result.success)
      "cmd/launcher/quickstart/testdata/golden/broken/flake.nix must fail to evaluate against the real spindrift flake module (infra.runtime is set to an out-of-enum value) — it evaluated successfully instead";
    pkgs.runCommand "quickstart-golden-broken-fails" { } "touch $out";

  # AC2 of issue #2735: builtins.tryEval sees only success or failure, never the
  # thrown message, so quickstart-golden-broken-fails alone cannot tell whether
  # the failure came from the `infra.runtime` key or its out-of-enum value.
  # Swapping only the value proves it was the value. This pair does not detect a
  # schema rename, where the key disappears; the two checks below cover that.
  quickstart-golden-broken-with-valid-runtime-evaluates =
    let
      brokenText = builtins.readFile brokenGolden;
      runtimeNeedle = ''infra.runtime = "not-a-real-runtime";'';
      goldenValidRuntime = builtins.toFile "golden-broken-valid-runtime.nix" (
        assert assertMsg (pkgs.lib.hasInfix runtimeNeedle brokenText)
          "cmd/launcher/quickstart/testdata/golden/broken/flake.nix no longer contains the expected out-of-enum infra.runtime line ${runtimeNeedle} — quickstart-golden-broken-with-valid-runtime-evaluates' replaceStrings needle is stale";
        builtins.replaceStrings
          [ runtimeNeedle ]
          [
            ''infra.runtime = "${builtins.head runtimeValues}";''
          ]
          brokenText
      );
    in
    mkEvaluatesCheck "quickstart-golden-broken-with-valid-runtime-evaluates" goldenValidRuntime;

  # Simulates a schema rename, as distinct from the value drift
  # quickstart-golden-broken-fails covers: the unmodified github golden,
  # evaluated against patchedFlakeModule, must fail with an unknown option.
  quickstart-golden-github-with-renamed-runtime-option-fails =
    let
      result =
        builtins.tryEval
          (evalGoldenWith patchedSpindriftInput githubGolden).packages.${system}.spindrift;
    in
    assert assertMsg (!result.success)
      "cmd/launcher/quickstart/testdata/golden/github/flake.nix must fail to evaluate against a spindrift flake module with infra.runtime renamed to infra.${renamedRuntimeOption} — it evaluated successfully instead";
    pkgs.runCommand "quickstart-golden-github-with-renamed-runtime-option-fails" { } "touch $out";

  # Positive control for the check above: without it, that failure proves only
  # that patchedFlakeModule broke something, not that the old infra.runtime path
  # is gone. githubGoldenRenamedRuntime sets the option at its new path.
  quickstart-golden-github-with-renamed-runtime-option-evaluates =
    mkEvaluatesCheckWith (evalGoldenWith patchedSpindriftInput)
      "quickstart-golden-github-with-renamed-runtime-option-evaluates"
      githubGoldenRenamedRuntime;
}
