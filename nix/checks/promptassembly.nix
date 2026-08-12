# Registry-ownership guard (issue #2349): a fragment/var identifier from
# lib/fragments.nix's Conditional fragment registry must never appear
# hardcoded as a literal in the Go module driving the assemble-prompt verb
# (cmd/launcher/internal/promptassembly + driver-exec/assembleprompt_cmd.go)
# outside its own tests -- adding a fragment on an existing gate must stay
# nix-only, never require a matching Go edit. Paired with a drift check
# pinning the checked-in testdata/registry.json fixture those Go tests load
# against lib/fragments.nix itself, so the two representations can't
# silently diverge.
#
# Scope is deliberately narrow: only each row's `fragment` (the .md basename)
# and `var` (the bash/template variable name) fields are protected.
# `gate` names are legitimately hardcoded in gates.go -- they originate from
# agent/entrypoint.sh's own gate-computation logic, not the fragment
# registry, and Gates (gates.go) is the Go mirror of that computation, not a
# registry reader. `extraSubstVars` entries are excluded too: SKILLS_FOUND is
# simultaneously a `gate` name for its own row, so grepping it as a "var"
# would false-positive against gates.go's legitimate SKILLS_FOUND gate
# hardcoding.
{ pkgs, fixtures, ... }:
let
  inherit (pkgs.lib) unique concatMapStringsSep;
  inherit (fixtures) batsHarness;

  # lib/fragments.nix is a bare list of attrsets, pure builtins (no pkgs.lib
  # needed to evaluate it) -- see its own header comment.
  registry = import ../../lib/fragments.nix;

  protectedIdentifiers = unique (map (r: r.fragment) registry ++ map (r: r.var) registry);

  identifierListFile = pkgs.writeText "promptassembly-registry-identifiers.txt" (
    concatMapStringsSep "\n" (x: x) protectedIdentifiers
  );

  # Non-test Go source under cmd/launcher/internal/promptassembly, filtered
  # the same way lib/mkHarness.nix's driverExecBin fileset filters every
  # package it bakes in (issue #474 shape): *_test.go excluded, since tests
  # legitimately reference known fragment/var literals when checking the
  # loader against testdata/registry.json.
  promptassemblyNonTestSrc = pkgs.lib.fileset.toSource {
    root = ../../cmd/launcher/internal/promptassembly;
    fileset = pkgs.lib.fileset.fileFilter (
      f: f.hasExt "go" && !pkgs.lib.hasSuffix "_test.go" f.name
    ) ../../cmd/launcher/internal/promptassembly;
  };

  fragmentsRegistryJsonFile = pkgs.writeText "fragments-registry.json" (builtins.toJSON registry);

  promptContractRegistryJsonFile = pkgs.writeText "prompt-contract-registry.json" (
    builtins.toJSON (import ../../lib/prompt-contract.nix).validateMarkers
  );

  # lib/prompt-contract.nix's forbiddenMarkers list, rendered the same way as
  # promptContractRegistryJsonFile above (issue #2464): entrypoint.sh's
  # phase_prompt_assembly now unconditionally passes
  # `--forbidden-markers-registry` too, so this lightweight check needs its
  # own JSON file alongside promptContractRegistryJsonFile.
  forbiddenMarkersRegistryJsonFile = pkgs.writeText "forbidden-markers-registry.json" (
    builtins.toJSON (import ../../lib/prompt-contract.nix).forbiddenMarkers
  );

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

  # Cheap insurance the checked-in testdata/registry.json fixture
  # registry_test.go loads doesn't silently diverge from lib/fragments.nix
  # -- the nix-rendered JSON's field names (gate/fragment/var/extraSubstVars)
  # already match registry.go's FragmentRow JSON tags literally, so no
  # remapping is needed; canonicalize both sides through `jq -S` before
  # diffing to make the comparison order-independent.
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

  # Byte-parity harness (issue #2349, slice 6): runs the SAME env through
  # both agent/entrypoint.sh's real bash phase_prompt_assembly and the new
  # Go `driver-exec assemble-prompt` verb, and asserts they produce
  # equivalent output for the one Env cell promptassembly.Assemble covers.
  # Same lightweight/non-image-dependent shape as bats.nix's
  # "bats-prompt-contract-parity" (no batsHarness.run/build/spindrift/
  # imagePath reference -- driverExecBin is a plain `buildGoModule` package,
  # not the OCI image, see equivalence.nix's driver-exec-src-excludes-tests
  # for the same driverExecBin-as-package precedent) -- reuses batsHarness
  # rather than introducing a new harness instance, since it needs no
  # non-default run knobs of its own.
  promptassembly-parity =
    pkgs.runCommand "promptassembly-parity"
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
        ENTRYPOINT = ../../agent/entrypoint.sh;
        PROMPTS_DIR = ../../templates/default/prompts;
        OUTCOME_CONTRACT_FILE = batsHarness.outcomeContractFile;
        COMMS_CONTRACT_FILE = batsHarness.commsContractFile;
        CHECK_CONTRACT_FILE = batsHarness.checkContractFile;
        RESEARCH_OUTCOME_CONTRACT_FILE = batsHarness.researchOutcomeContractFile;
        DRIVER_PREAMBLE_FILE = batsHarness.driverPreambleFile;
        FRAGMENT_REGISTRY_FILE = batsHarness.fragmentRegistryFile;
        CONTRACT_REGISTRY_FILE = batsHarness.contractRegistryFile;
        DRIVER_EXEC_BIN = "${batsHarness.driverExecBin}/bin/driver-exec";
        # Reuses the same nix-rendered lib/fragments.nix JSON the drift
        # check above already built -- no second render of the registry.
        PROMPTASSEMBLY_REGISTRY_FILE = fragmentsRegistryJsonFile;
        PROMPT_CONTRACT_REGISTRY_FILE = promptContractRegistryJsonFile;
        FORBIDDEN_MARKERS_REGISTRY_FILE = forbiddenMarkersRegistryJsonFile;
      }
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
}
