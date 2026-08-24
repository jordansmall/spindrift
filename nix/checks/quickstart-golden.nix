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
# green. Plain `import` of source text throughout — no
# import-from-derivation, no network, no binary execution during eval.
{
  pkgs,
  nixpkgs,
  system,
  flake-parts,
  ...
}:
let
  inherit (pkgs.lib) assertMsg;

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
  spindriftInput = {
    flakeModules.default = ../../lib/flakeModule.nix;
  };
  evalGolden =
    goldenPath:
    (import goldenPath).outputs {
      inherit nixpkgs flake-parts;
      self = {
        outPath = ../../.;
        inputs = {
          inherit nixpkgs flake-parts;
          spindrift = spindriftInput;
        };
      };
      spindrift = spindriftInput;
    };

  # Shared body for both per-golden "evaluates" checks below: forces
  # .packages.${system}.spindrift the same way flakemodule-schema-options
  # (nix/checks/equivalence.nix) forces the module-derived consumer's
  # packages output, proving the module actually ran (not just that
  # flake-parts.lib.mkFlake accepted the shape) — and also forces
  # .devShells.${system}.default, the golden's own perSystem.devShells.default
  # block, which is no part of the spindrift module and would otherwise
  # never be evaluated by this check.
  mkEvaluatesCheck =
    name: goldenPath:
    let
      outputs = evalGolden goldenPath;
      spindrift = outputs.packages.${system}.spindrift;
      devShell = outputs.devShells.${system}.default;
    in
    pkgs.runCommand name { inherit spindrift devShell; } ''
      : "$spindrift"
      : "$devShell"
      touch $out
    '';

  githubGolden = ../../cmd/launcher/quickstart/testdata/golden/github/flake.nix;
  forgejoGolden = ../../cmd/launcher/quickstart/testdata/golden/forgejo/flake.nix;
  brokenGolden = ../../cmd/launcher/quickstart/testdata/golden/broken/flake.nix;
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
}
