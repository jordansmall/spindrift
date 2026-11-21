# A Connector's conformance suite, built from core so a Connector's own
# subflake can run it (`nix flake check` in connectors/<name>). It is the only
# place a Connector's checks run: root `checks` carry none, and CI's `connector`
# lane runs it via `nix run .#connector-dev -- <name> flake check`. It covers
# the Connector's Go fmt, wasip1 vet and unit tests, plus the launcher's
# conformance test for its Driver or backend run against the Connector's wasm.
# It runs `go test ./internal/{driver,forge}/<name>/...` inside core's launcher
# (a Driver with no native package, lib/drivers/default.nix's connectorHosted,
# runs the driverrows tests alone, over the corpus beside the Connector),
# so it works only for a Connector named after a core Driver or backend package.
{
  nixpkgs,
  systems,
  name,
  # The Connector's own directory.
  src,
  # The subflake's `entry.lib.connector { inherit core; }`.
  connector,
}:
let
  forSystem =
    system:
    let
      pkgs = nixpkgs.legacyPackages.${system};
      fs = pkgs.lib.fileset;
      goCheckEnv = import ../nix/checks/go-check-env.nix;
      isBackend = connector ? routes;

      # go.mod replaces the SDK with ../../sdk, so both trees keep that layout.
      # The Connector's own tests read testdata beside them (jira's goldens,
      # claude's corpus and dogfood logs); a Connector without one has none.
      connectorSrc = fs.toSource {
        root = src;
        fileset = fs.unions [
          (fs.fileFilter (f: f.hasExt "go" || f.name == "go.mod") src)
          (fs.maybeMissing (src + "/testdata"))
        ];
      };
      mk =
        suffix: script:
        pkgs.runCommand "connector-${name}-go-${suffix}" { nativeBuildInputs = [ pkgs.go ]; } ''
          mkdir -p src/connectors
          cp -r ${../sdk} src/sdk
          cp -r ${connectorSrc} src/connectors/${name}
          chmod -R +w src
          ${goCheckEnv}
          export GOFLAGS=-mod=mod
          cd src/connectors/${name}
          ${script}
          touch $out
        '';

      launcherGoModules =
        (pkgs.buildGoModule {
          pname = "spindrift-launcher-modules";
          version = "0";
          src = ../cmd/launcher;
          vendorHash = (import ./build-constants.nix).launcherVendorHash;
        }).goModules;
      row =
        pkgs.lib.findFirst (r: r.name == name)
          (throw "core.lib.connectorChecks: no core backend row named ${name}")
          (import ./backends/default.nix);
      # The conformance tests skip, not fail, when their env var is unset, so the
      # setup below always exports it.
      setup =
        if isBackend then
          ''
            export SPINDRIFT_CONNECTOR_FIXTURES_DIR=${(import ../nix/connector-wasm.nix { inherit pkgs; })}
            export SPINDRIFT_BACKEND_CONNECTOR_WASM=${connector.package pkgs}/${name}.wasm
            export SPINDRIFT_BACKEND_CONNECTOR_ROUTES=${pkgs.lib.escapeShellArg (builtins.toJSON connector.routes)}
          ''
          + pkgs.lib.concatMapStrings (f: ''
            mkdir -p ../../${builtins.dirOf f}
            cp ${../. + "/${f}"} ../../${f}
          '') (row.goTestRepoFiles or [ ])
        else
          ''
            export SPINDRIFT_DRIVER_CONNECTOR_WASM=${connector.package pkgs}/${name}.wasm
            export SPINDRIFT_DRIVER_CONNECTOR_NAME=${name}
          ''
          # fixturebuild.ClaudeConnectorWasm reads this claude-named variable; a
          # second connectorHosted Driver must not register claude's under its wasm.
          + pkgs.lib.optionalString (connectorHosted && name == "claude") ''
            export SPINDRIFT_CLAUDE_CONNECTOR_WASM=${connector.package pkgs}/${name}.wasm
          ''
          + pkgs.lib.optionalString connectorHosted ''
            mkdir -p ../../connectors/${name}
            cp -r ${src}/testdata ../../connectors/${name}/testdata
          '';
      # A Driver with no native package has this Connector alone as its host
      # half: driverrows' TestCorpus registers the claude wasm and replays the
      # corpus beside the Connector, in place of the native-vs-Connector
      # comparison a Driver with a native Strategy gets.
      connectorHosted =
        !isBackend
        && pkgs.lib.elem name (import ./drivers/default.nix { inherit (pkgs) lib; }).connectorHosted;
      testPkg = if isBackend then "forge" else "driver";
      # driverrows' primary-row corpus test runs the Connector; the Driver's own
      # tests cannot, as they may not import the registry root.
      testPkgs =
        pkgs.lib.optionalString (!connectorHosted) "./internal/${testPkg}/${name}/..."
        + pkgs.lib.optionalString (!isBackend) " ./internal/driver/driverrows/...";
      # The tests that run the Connector: TestCorpus for a hosted Driver,
      # TestPrimaryRowConnectorPassesCorpus (and the Driver's own
      # TestConnectorResolveExitMatchesNative, if it has one) for one with a
      # native Strategy. The path stays `../cmd/launcher + "..."`: a deeper
      # literal would make ci-connector-contract-covers-reads walk that tree.
      requiredPasses =
        if connectorHosted then
          [ "TestCorpus" ]
        else if !isBackend then
          [ "TestPrimaryRowConnectorPassesCorpus" ]
          ++ pkgs.lib.optional (builtins.pathExists (
            ../cmd/launcher + "/internal/driver/${name}/connector_test.go"
          )) "TestConnectorResolveExitMatchesNative"
        else
          [ ];
    in
    {
      "connector-${name}-go-fmt" = mk "fmt" ''
        unformatted=$(gofmt -l .)
        if [ -n "$unformatted" ]; then
          echo "gofmt violations:" >&2
          echo "$unformatted" >&2
          exit 1
        fi
      '';
      "connector-${name}-go-vet" = mk "vet" "GOOS=wasip1 GOARCH=wasm go vet ./...";
      "connector-${name}-go-test" = mk "test" "go test ./...";
      "connector-${name}-conformance" =
        pkgs.runCommand "connector-${name}-conformance"
          {
            nativeBuildInputs = [
              pkgs.go
              pkgs.git
            ];
          }
          ''
            mkdir -p src/cmd
            cp -r ${../cmd/launcher} src/cmd/launcher
            chmod -R +w src
            cp -r ${launcherGoModules} src/cmd/launcher/vendor
            ${goCheckEnv}
            mkdir -p "$TMPDIR/fakebin"
            cat > "$TMPDIR/fakebin/bwrap" <<'EOF'
            #!/bin/sh
            echo "fakebin/bwrap: stub for LookPath only, tests never invoke it for real" >&2
            exit 1
            EOF
            chmod +x "$TMPDIR/fakebin/bwrap"
            export PATH="$TMPDIR/fakebin:$PATH"
            cd src/cmd/launcher
            ${setup}
            ${
              if requiredPasses == [ ] then
                "go test ${testPkgs}"
              else
                ''
                  # go test exits 0 when the test skips, so require each PASS line.
                  log=$(go test -v ${testPkgs} 2>&1) || { printf '%s\n' "$log"; exit 1; }
                  printf '%s\n' "$log"
                  ${pkgs.lib.concatMapStrings (t: ''
                    grep -q '^--- PASS: ${t} ' <<<"$log" || {
                      echo "connector-${name}-conformance: ${t} did not pass (skipped or never ran)" >&2
                      exit 1
                    }
                  '') requiredPasses}
                ''
            }
            touch $out
          '';
    };
in
nixpkgs.lib.genAttrs systems forSystem
