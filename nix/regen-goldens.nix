# Update-mode regenerator for the Go-test goldens (issue #2951): prompt
# assembly and `spindrift stats`. It runs the Go golden tests
# (cmd/launcher/internal/promptassembly/golden_integration_test.go,
# cmd/launcher/stats_test.go, cmd/launcher/stats_by_test.go) with
# UPDATE_GOLDENS=1, which flips their compare helpers from fail to
# overwrite. Separate from nix/regen.nix's regen verb because these goldens
# are the output of running the test, not a pure render of a Nix value.
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
    # has none, so run the test from a copy of the module. The test finds the
    # goldens and the raw prompt templates at ../../tests and ../../templates
    # relative to the module, so those two are symlinks back into the checkout,
    # as is the module's own testdata/golden: goldens written through them land
    # in the repo.
    mkdir -p "$scratch/src/cmd"
    cp -r "$root/cmd/launcher" "$scratch/src/cmd/launcher"
    chmod -R +w "$scratch/src"
    cp -r ${launcherGoModules} "$scratch/src/cmd/launcher/vendor"
    ln -s "$root/tests" "$scratch/src/tests"
    ln -s "$root/templates" "$scratch/src/templates"
    rm -rf "$scratch/src/cmd/launcher/testdata/golden"
    ln -s "$root/cmd/launcher/testdata/golden" "$scratch/src/cmd/launcher/testdata/golden"

    ${import ./checks/go-check-env.nix}
    export SPINDRIFT_SEAM_FIXTURES_DIR=${fixtures.seamFixtures}
    export UPDATE_GOLDENS=1

    cd "$scratch/src/cmd/launcher"
    go test -count=1 -tags integration -run '^TestPromptAssemblyGoldens$' ./internal/promptassembly
    # Selects by name: a new statsGolden caller must end in Golden or start
    # with By, or update mode silently skips it (issue #4861).
    go test -count=1 -run '^TestStats_(.*Golden|By.*)$' .
  '';
}
