# Registry-ownership guard (issue #2349): a `fragment` or `var` identifier
# from lib/fragments.nix must never be hardcoded in the Go assemble-prompt
# module, so adding a fragment on an existing gate stays nix-only. `gate`
# names are exempt because gates.go mirrors agent/entrypoint.sh's gate
# computation, and `extraSubstVars` too, since SKILLS_FOUND is also a gate.
{
  pkgs,
  fixtures,
  config,
  ...
}:
let
  inherit (pkgs.lib) unique concatMapStringsSep;

  # nix/regen-goldens.nix's update-mode app reuses the same
  # fragmentsRegistryJsonFile and env parity-env.nix builds (issue #2951), so
  # regeneration runs in the environment this check verifies against.
  parity = import ../parity-env.nix { inherit pkgs fixtures; };
  inherit (parity) registry fragmentsRegistryJsonFile;

  protectedIdentifiers = unique (map (r: r.fragment) registry ++ map (r: r.var) registry);

  identifierListFile = pkgs.writeText "promptassembly-registry-identifiers.txt" (
    concatMapStringsSep "\n" (x: x) protectedIdentifiers
  );

  # Tests legitimately reference fragment/var literals when they check the
  # loader against testdata/registry.json, so the guard reads only non-test
  # source (issue #474's fileset shape).
  promptassemblyNonTestSrc = pkgs.lib.fileset.toSource {
    root = ../../cmd/launcher/internal/promptassembly;
    fileset = pkgs.lib.fileset.fileFilter (
      f: f.hasExt "go" && !pkgs.lib.hasSuffix "_test.go" f.name
    ) ../../cmd/launcher/internal/promptassembly;
  };

  testdataRegistryJson = ../../cmd/launcher/internal/promptassembly/testdata/registry.json;
in
{
  promptassembly-registry-ownership =
    pkgs.runCommand "promptassembly-registry-ownership"
      {
        nativeBuildInputs = [
          pkgs.gnugrep
          pkgs.gawk
        ];
      }
      ''
        # Blank out full-line `//` comments before grepping (preserving line
        # numbers), rather than dropping them: registry.go/env.go/gates.go
        # legitimately *describe* specific fragment/var names in doc comments
        # (e.g. registry.go's ExtraSubstVars doc citing skill-preamble.md/
        # ci-failure.md as the two rows that set it) without creating any
        # functional coupling a new fragment-on-an-existing-gate would need
        # to touch -- only a literal used by actual code (string comparison,
        # a switch case, and so on) is the guard's real target.
        found=0
        while IFS= read -r -d "" file; do
          if awk '/^[[:space:]]*\/\// { print ""; next } { print }' "$file" \
            | grep -n -F -f ${identifierListFile}; then
            echo "promptassembly-registry-ownership: hardcoded fragment/var identifier found in $file (above)" >&2
            found=1
          fi
        done < <(find ${promptassemblyNonTestSrc} ${../../cmd/launcher/driver-exec/assembleprompt_cmd.go} -name '*.go' -print0)

        if [ "$found" -ne 0 ]; then
          echo "promptassembly-registry-ownership: found lib/fragments.nix fragment/var identifiers hardcoded in non-test Go source -- a fragment on an existing gate must stay nix-only" >&2
          exit 1
        fi
        touch $out
      '';

  # Catches drift between lib/fragments.nix and the testdata/registry.json
  # fixture registry_test.go loads. The nix-rendered field names already match
  # registry.go's FragmentRow JSON tags, so the only step needed is `jq -S`,
  # which makes the diff order-independent.
  promptassembly-registry-drift =
    pkgs.runCommand "promptassembly-registry-drift"
      {
        nativeBuildInputs = [ pkgs.jq ];
      }
      ''
        jq -S . ${fragmentsRegistryJsonFile} > nix-registry.json
        jq -S . ${testdataRegistryJson} > testdata-registry.json
        if ! diff -u nix-registry.json testdata-registry.json; then
          echo "promptassembly-registry-drift: lib/fragments.nix and cmd/launcher/internal/promptassembly/testdata/registry.json have diverged -- regenerate the testdata fixture" >&2
          exit 1
        fi
        touch $out
      '';

  # Byte-parity harness (issue #2349, slice 6): runs the same env through both
  # agent/entrypoint.sh's bash phase_prompt_assembly and the Go `driver-exec
  # assemble-prompt` verb, and asserts they produce equivalent output for the
  # one Env cell promptassembly.Assemble covers. It reuses batsHarness's env
  # but never the OCI image: driverExecBin is a plain buildGoModule package.
  promptassembly-parity =
    pkgs.runCommand "promptassembly-parity"
      (
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
        }
        // parity.env
      )
      ''
        export HOME="$TMPDIR/home"
        mkdir -p "$HOME"
        cp -r ${../../tests} tests
        chmod -R +w tests
        for f in tests/fakes/*; do
          substituteInPlace "$f" \
            --replace '#!/usr/bin/env bash' "#!${pkgs.bash}/bin/bash"
        done
        export FAKES_DIR="$PWD/tests/fakes"
        bats --print-output-on-failure tests/prompt-assembly-parity.bats
        touch $out
      '';

  # Wiring guard (issue #2951): flake.nix's apps.regen-goldens must resolve to
  # the same derivation this check builds from nix/regen-goldens.nix, not a
  # lookalike a fixtures refactor swapped in. The build script references
  # regenGoldensApp's own output path so the app really gets built, including
  # writeShellApplication's shellcheck pass.
  regen-goldens-app-wiring =
    let
      inherit (pkgs.lib) assertMsg;
      regenGoldensApp = import ../regen-goldens.nix { inherit pkgs fixtures; };
      expectedProgram = "${regenGoldensApp}/bin/regen-goldens";
    in
    assert assertMsg (config.apps ? regen-goldens)
      "flake.nix must expose apps.regen-goldens at the top level (issue #2951) so `nix run .#regen-goldens` actually resolves, got top-level app names: ${builtins.toJSON (builtins.attrNames config.apps)}";
    assert assertMsg (config.apps.regen-goldens.type == "app")
      "flake.nix's top-level apps.regen-goldens must be a real app, got: ${builtins.toJSON config.apps.regen-goldens}";
    assert assertMsg (config.apps.regen-goldens.program == expectedProgram)
      "flake.nix's top-level apps.regen-goldens must be built from nix/regen-goldens.nix with the SAME pkgs/fixtures this check uses (issue #2951) -- otherwise `nix run .#regen-goldens` silently regenerates goldens against a foreign env: ${config.apps.regen-goldens.program} != ${expectedProgram}";
    pkgs.runCommand "regen-goldens-app-wiring" { } ''
      [ -x ${regenGoldensApp}/bin/regen-goldens ]
      touch $out
    '';
}
