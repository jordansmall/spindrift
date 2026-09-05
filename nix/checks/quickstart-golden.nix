# Golden-scaffold eval guards (issue #2565, nix half): the Quickstart
# wizard's committed golden Consumer flake.nix files
# (cmd/launcher/quickstart/testdata/golden/{github,forgejo}/flake.nix) are
# byte-pinned against the wizard's renderer by a Go test
# (TestRender_Github_Golden et al.), but that only proves the renderer's
# output hasn't drifted — it says nothing about whether the committed text
# evaluates against the real, in-repo spindrift flake module. This module
# closes that gap: it imports each golden's own `outputs` function directly
# (no re-typing its option lines into a fresh perSystem.spindrift block here
# — that would only prove a hand-copy evaluates, not the committed file
# itself), wires its `spindrift` flake input to this repo's own
# lib/flakeModule.nix (the same module flake.nix:68's
# `flake.flakeModules.default` exposes), and forces both the module-derived
# .packages.${system}.spindrift output and the golden's own,
# module-independent .devShells.${system}.default block — the first thing a
# Quickstart user runs is `nix develop`, so a golden whose devShells block
# silently fails to evaluate is exactly the regression this check exists to
# catch — so a real schema regression (a renamed option, a tightened enum, a
# devShells block referencing a dropped package, ...) that the Go
# byte-equality test can't see fails here instead. What this check does NOT
# cover: the golden's own `inputs = { ... }` block (e.g.
# `inputs.nixpkgs.url = ...`) is never evaluated at all — `evalGolden` below
# calls the golden's `.outputs` function directly and supplies its own
# stubbed `nixpkgs`/`flake-parts`/`self` arguments rather than letting Nix
# re-resolve the golden's `inputs` block (a plain `import` never does that,
# only real flake resolution does), so a corrupt `inputs.nixpkgs.url` would
# pass this check but break a real `nix develop`/`nix flake lock`. A sibling
# golden (testdata/golden/broken/flake.nix) is deliberately corrupted
# (infra.runtime set to an out-of-enum value) and asserted to fail eval,
# proving this harness can actually detect breakage and isn't vacuously
# green. A separate pair of checks simulates a schema rename instead of a
# bad value — the key itself disappearing, which no amount of editing the
# golden's fixture text can exercise. Plain `import` of source text
# throughout — no import-from-derivation, no network, no binary execution
# during eval.
{
  pkgs,
  nixpkgs,
  system,
  flake-parts,
  ...
}:
let
  inherit (pkgs.lib) assertMsg;

  # Single root for the runtime enum's valid values (ADR 0027) — shared with
  # nix/checks/schema-drift.nix's runtime-values-gen check and the runner
  # module's generated value set. "podman" (the first entry) is used below as
  # a valid, in-enum replacement for the broken golden's out-of-enum value.
  runtimeValues = import ../../lib/runtime-values.nix;

  # Evaluates a golden flake.nix's own `outputs` function against stubbed
  # inputs: the repo's locked nixpkgs/flake-parts (threaded through from
  # nix/checks/default.nix's `common`, the same locked inputs every other
  # check module in this directory uses), `self.outPath` pointed at this
  # repo's own root (mirroring the `consumer`/`consumerN` pattern in
  # equivalence.nix), and `spindrift.flakeModules.default` wired to the real,
  # local lib/flakeModule.nix rather than a fetched copy of this repo.
  # `self.inputs` is set to mirror the same stubbed inputs: a real flake
  # evaluation populates it automatically, and forcing the golden's own
  # `devShells.default` block specifically is what requires the stub —
  # flake-parts' perSystem machinery (modules/perSystem.nix's
  # `_module.args.inputs'`) reads `self.inputs` when computing `inputs'` for
  # that perSystem's module evaluation, and `devShells.default` here is
  # itself defined inside the golden's `perSystem` function, so forcing it
  # forces that computation. Forcing only the module-derived
  # `packages.spindrift` output did not hit that path: the pre-existing
  # commit ea093123's version of this file forced just
  # `packages.${system}.spindrift`, had no `self.inputs` stub at all, and
  # still built green — it's specifically forcing `devShells.default` that
  # requires this stub, not forcing perSystem outputs in general.
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

  # Simulates a schema rename (e.g. infra.runtime moving to a new name) for
  # the two checks below. An option can't be removed by module composition,
  # so the only way to make the real `infra.runtime` path disappear is a
  # patched copy of lib/flakeModule.nix itself. Every needle here is guarded
  # by an assertMsg naming the file and needle, since a real rename of
  # structuralOptions.runtime would otherwise silently make these checks
  # vacuous instead of failing loudly.
  renamedRuntimeOption = "runtimeSchemaDrift";

  # lib/flakeModule.nix:304-309 asserts structuralOptions and
  # structuralPlacements (this file) share the same key set, so the rename
  # below must move both the mkOption entry and this placement's key/path
  # segment together.
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

  # builtins.toFile writes at the store root, so every `import ./X.nix`
  # inside the patched text must become an absolute store path first:
  # `import ./structural-paths.nix` is rewritten to the patched copy above
  # (a specific needle, tried before the generic one below per
  # builtins.replaceStrings' in-order-per-position matching), and the
  # remaining `import ./*` imports are rewritten via `${../..}/lib/` —
  # the whole repo root, not just lib/, since lib/mkHarness.nix itself
  # escapes lib/ with its own relative `../templates/...` reads; copying
  # lib/ alone into the store strands those at a nonexistent store sibling.
  patchedFlakeModule =
    let
      text = builtins.readFile ../../lib/flakeModule.nix;
      optionNeedle = "runtime = mkOption {";
      structuralPathsNeedle = "import ./structural-paths.nix";
      relativeImportNeedle = "import ./";
      # config.perSystem's structuralArgs forwards each structural knob to
      # mkHarness under its own flatName, since today flatName is also
      # mkHarness's own argument name for every structural knob. mkHarness
      # (unpatched — it has no rename of its own) still only accepts
      # `runtime`, so the forwarding step needs a translation back to that
      # fixed engine-side name for the renamed knob specifically, mirroring
      # what a real rename's forwarding code would have to do anyway.
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

  # Shared body for both per-golden "evaluates" checks below: forces
  # .packages.${system}.spindrift the same way flakemodule-schema-options
  # (nix/checks/equivalence.nix) forces the module-derived consumer's
  # packages output, proving the module actually ran (not just that
  # flake-parts.lib.mkFlake accepted the shape) — and also forces
  # .devShells.${system}.default, the golden's own perSystem.devShells.default
  # block, which is no part of the spindrift module and would otherwise
  # never be evaluated by this check.
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
  # patchedFlakeModule's renamed option/path, for
  # quickstart-golden-github-with-renamed-runtime-option-evaluates below.
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
  # cmd/launcher/quickstart/testdata/golden/github/flake.nix (the github
  # tracker branch golden) must evaluate cleanly against the real spindrift
  # flake module.
  quickstart-golden-github-evaluates = mkEvaluatesCheck "quickstart-golden-github-evaluates" githubGolden;

  # Same as above for the forgejo tracker branch golden
  # (testdata/golden/forgejo/flake.nix), which additionally sets
  # forge.backend and issues.forgejo.baseURL for a self-hosted forge.
  quickstart-golden-forgejo-evaluates = mkEvaluatesCheck "quickstart-golden-forgejo-evaluates" forgejoGolden;

  # Guard (mirrors flakemodule-rejects-unknown-settings and
  # mkharness-rejects-unknown-key, both in nix/checks/equivalence.nix): the
  # deliberately-broken golden (testdata/golden/broken/flake.nix, whose
  # comment names the exact corruption: infra.runtime = "not-a-real-runtime",
  # a value outside the runtime enum) must fail to evaluate against the real
  # spindrift flake module. Proves this check module can actually detect a
  # real schema regression, not just rubber-stamp anything that parses as
  # valid Nix syntax.
  quickstart-golden-broken-fails =
    let
      result = builtins.tryEval (evalGolden brokenGolden).packages.${system}.spindrift;
    in
    assert assertMsg (!result.success)
      "cmd/launcher/quickstart/testdata/golden/broken/flake.nix must fail to evaluate against the real spindrift flake module (infra.runtime is set to an out-of-enum value) — it evaluated successfully instead";
    pkgs.runCommand "quickstart-golden-broken-fails" { } "touch $out";

  # AC2 of issue #2735: quickstart-golden-broken-fails above proves the
  # broken golden fails, but builtins.tryEval only sees success/failure,
  # never the thrown message text (same limitation documented at
  # jira-status-mapping.nix's *-rejects-unknown-key check and drivers.nix's
  # drivers-assert-shape-missing-attribute-throws), so that check alone
  # can't rule out the failure being sourced in the `infra.runtime` key
  # itself rather than its out-of-enum value. This sibling check pins
  # acceptance, not just rejection, mirroring flakemodule-rejects-invalid-choice
  # (nix/checks/equivalence.nix:1099, "a valid choice must still evaluate
  # cleanly"): it swaps only the broken golden's out-of-enum runtime value
  # for a valid one (lib/runtime-values.nix), leaving the `infra.runtime`
  # key itself untouched, so its success plus quickstart-golden-broken-fails'
  # failure on the unmodified text proves that failure is sourced in the
  # value, not the key — for this fixture's specific corruption only. This
  # pair proves value-vs-key attribution and nothing more: it does NOT
  # itself detect a schema rename, where the key disappears rather than
  # holding a bad value. That failure mode, and the needle asserts that turn
  # a real infra.runtime rename into a named error instead of an opaque
  # "option does not exist", are covered separately below by
  # quickstart-golden-github-with-renamed-runtime-option-{fails,evaluates}.
  # builtins.toFile writes content-addressed text at eval time with no
  # derivation build (nix/checks/schema-drift.nix documents the same
  # pattern), so this isn't import-from-derivation.
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

  # Simulates a schema rename (infra.runtime renamed out from under a
  # Consumer that still sets the old path) as distinct from the value drift
  # quickstart-golden-broken-fails covers: the unmodified github golden,
  # evaluated against patchedFlakeModule (whose runtime option no longer
  # exists under its old name), must fail — the "unknown option" failure
  # mode.
  quickstart-golden-github-with-renamed-runtime-option-fails =
    let
      result =
        builtins.tryEval
          (evalGoldenWith patchedSpindriftInput githubGolden).packages.${system}.spindrift;
    in
    assert assertMsg (!result.success)
      "cmd/launcher/quickstart/testdata/golden/github/flake.nix must fail to evaluate against a spindrift flake module with infra.runtime renamed to infra.${renamedRuntimeOption} — it evaluated successfully instead";
    pkgs.runCommand "quickstart-golden-github-with-renamed-runtime-option-fails" { } "touch $out";

  # Positive control for the check above: without it, that failure proves
  # only that patchedFlakeModule broke *something*, not specifically that
  # the old infra.runtime path is gone. githubGoldenRenamedRuntime sets the
  # option at its new path, so this must evaluate cleanly against the same
  # patchedFlakeModule.
  quickstart-golden-github-with-renamed-runtime-option-evaluates =
    mkEvaluatesCheckWith (evalGoldenWith patchedSpindriftInput)
      "quickstart-golden-github-with-renamed-runtime-option-evaluates"
      githubGoldenRenamedRuntime;
}
