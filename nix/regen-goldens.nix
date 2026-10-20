# Update-mode regenerator for the Go-test goldens (issue #2951): prompt
# assembly, `spindrift stats`, the per-Driver Pass-log corpus, the Digest
# corpora (the digestkit's own and each Chore's), and the Ecosystem
# corpora. It runs the Go golden tests
# (cmd/launcher/internal/promptassembly/golden_integration_test.go,
# cmd/launcher/stats_test.go, cmd/launcher/stats_by_test.go,
# cmd/launcher/stats_reverts_test.go, cmd/launcher/stats_churn_test.go,
# cmd/launcher/internal/driver/<driver>/corpus_test.go,
# cmd/launcher/internal/ecosystem/referencedocs_test.go,
# cmd/launcher/internal/ecosystem/corpus_test.go,
# cmd/launcher/internal/ecosystemtest/ecosystemtest_test.go,
# cmd/launcher/internal/digestkit/digestkit_test.go,
# cmd/launcher/internal/<chore>/digest_corpus_test.go) with
# UPDATE_GOLDENS=1, which flips their compare helpers from fail to
# overwrite. Separate from nix/regen.nix's regen verb because these
# goldens are the output of running the test, not a pure render of a
# Nix value.
{ pkgs, fixtures }:
let
  buildConstants = import ../lib/build-constants.nix;
  # Same derivation as nix/checks/default.nix's launcherGoModules (identical
  # attrs), so a regen run reuses the vendor tree the checks already built.
  launcherGoModules =
    (pkgs.buildGoModule {
      pname = "spindrift-launcher-modules";
      version = "0";
      src = ../cmd/launcher;
      vendorHash = buildConstants.launcherVendorHash;
    }).goModules;
in
pkgs.writeShellApplication {
  name = "regen-goldens";
  runtimeInputs = [
    pkgs.go
    pkgs.git
    pkgs.coreutils
  ];
  text = ''
    root="$(git rev-parse --show-toplevel)"
    if [ ! -f "$root/lib/env-schema.nix" ]; then
      echo "regen-goldens: $root doesn't look like the spindrift repo (no lib/env-schema.nix); refusing to write" >&2
      exit 1
    fi

    scratch="$(mktemp -d)"
    trap 'chmod -R +w "$scratch"; rm -rf "$scratch"' EXIT
    export TMPDIR="$scratch"

    # The vendor tree has to sit inside the module, and the invoking checkout
    # has none, so run the test from a copy of the module. The tests read
    # ../../tests, ../../templates, and ../../docs relative to the module, so
    # those three are symlinks back into the checkout. So are the module's own
    # testdata/golden, each Driver's corpus
    # (internal/driver/<driver>/testdata/corpus), each Digest corpus
    # (internal/*/testdata/digest, plus the digestkit's own testdata/corpus),
    # and both Ecosystem corpora (internal/ecosystem{,test}/testdata/corpus),
    # so goldens written through them land in the repo.
    mkdir -p "$scratch/src/cmd"
    cp -r "$root/cmd/launcher" "$scratch/src/cmd/launcher"
    chmod -R +w "$scratch/src"
    cp -r ${launcherGoModules} "$scratch/src/cmd/launcher/vendor"
    ln -s "$root/tests" "$scratch/src/tests"
    ln -s "$root/templates" "$scratch/src/templates"
    ln -s "$root/docs" "$scratch/src/docs"
    rm -rf "$scratch/src/cmd/launcher/testdata/golden"
    ln -s "$root/cmd/launcher/testdata/golden" "$scratch/src/cmd/launcher/testdata/golden"
    # The Driver list is whatever carries a corpus, so a new Driver is picked
    # up without touching this file.
    driver_pkgs=()
    for corpus in "$root"/cmd/launcher/internal/driver/*/testdata/corpus; do
      [ -d "$corpus" ] || continue
      rel="''${corpus#"$root"/cmd/launcher/}"
      rm -rf "$scratch/src/cmd/launcher/$rel"
      ln -s "$corpus" "$scratch/src/cmd/launcher/$rel"
      driver_pkgs+=("./''${rel%/testdata/corpus}")
    done
    if [ "''${#driver_pkgs[@]}" -eq 0 ]; then
      echo "regen-goldens: no Driver corpus found under cmd/launcher/internal/driver/*/testdata/corpus" >&2
      exit 1
    fi

    # Likewise for the Digest corpora: any package with a testdata/digest
    # directory is a Chore's corpus, so a new records Chore is picked up
    # without touching this file.
    digest_pkgs=()
    for corpus in "$root"/cmd/launcher/internal/*/testdata/digest; do
      [ -d "$corpus" ] || continue
      rel="''${corpus#"$root"/cmd/launcher/}"
      rm -rf "$scratch/src/cmd/launcher/$rel"
      ln -s "$corpus" "$scratch/src/cmd/launcher/$rel"
      digest_pkgs+=("./''${rel%/testdata/digest}")
    done
    if [ "''${#digest_pkgs[@]}" -eq 0 ]; then
      echo "regen-goldens: no Digest corpus found under cmd/launcher/internal/*/testdata/digest" >&2
      exit 1
    fi
    kit_corpus=cmd/launcher/internal/digestkit/testdata/corpus
    rm -rf "$scratch/src/$kit_corpus"
    ln -s "$root/$kit_corpus" "$scratch/src/$kit_corpus"
    # Symlinked, not copied: ecosystemtest.Run also deletes goldens for stages a
    # row skips, and a copy would lose those deletions with the scratch tree.
    for eco_corpus in \
      cmd/launcher/internal/ecosystem/testdata/corpus \
      cmd/launcher/internal/ecosystemtest/testdata/corpus; do
      if [ ! -d "$root/$eco_corpus" ]; then
        echo "regen-goldens: no Ecosystem corpus found at $eco_corpus" >&2
        exit 1
      fi
      rm -rf "$scratch/src/$eco_corpus"
      ln -s "$root/$eco_corpus" "$scratch/src/$eco_corpus"
    done

    ${import ./checks/go-check-env.nix}
    export SPINDRIFT_SEAM_FIXTURES_DIR=${fixtures.seamFixtures}
    export UPDATE_GOLDENS=1

    cd "$scratch/src/cmd/launcher"
    go test -count=1 -tags integration -run '^TestPromptAssemblyGoldens$' ./internal/promptassembly
    # Selects by name: a new statsGolden caller must end in Golden or start
    # with By, or update mode silently skips it (issue #4861). The By prefix
    # also matches TestStats_ByDimensionsDocumented, which reads ../../docs,
    # hence the docs link above (issue #5248).
    go test -count=1 -run '^TestStats_(.*Golden|By.*)$' .
    go test -count=1 -run '^TestCorpus$' "''${driver_pkgs[@]}"
    go test -count=1 -run '^TestDigestCorpus$' "''${digest_pkgs[@]}"
    go test -count=1 -run '^TestRunReplaysCorpusIntoGoldens$' ./internal/digestkit
    go test -count=1 -run '^TestCorpus$' ./internal/ecosystem ./internal/ecosystemtest
    # Rewrites the generated spans in docs/reference.md (the docs link above).
    go test -count=1 -run '^TestReferenceDocs_GeneratedSpans$' ./internal/ecosystem
  '';
}
