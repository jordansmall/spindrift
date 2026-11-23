# A Connector's conformance suite, built from core so its own subflake can run
# it (`nix flake check` in connectors/<name>); root `checks` carry none bar
# the test-only fixtures' (nix/checks/connector-checks-any-name.nix and
# nix/checks/connector-checks-generic.nix), and CI's `connector` lane runs it
# via `nix run .#connector-dev -- <name> flake check`. It covers the
# Connector's Go fmt, wasip1 vet and unit tests, plus core's
# `go test ./internal/{driver,forge}/<name>/...` against the Connector's wasm
# (a connectorHosted Driver, with no native package, runs the driverrows tests
# alone). A Driver named after no core Driver has no such package or corpus
# in core: the kit's RunConnector runs over the Connector's own
# testdata/corpus instead. A backend Connector (one with `routes`) named after
# no core backend row has no per-backend tests either; it runs the
# connectorconformance kit against its wasm and declared Routes instead, for
# whichever tracker and forge capabilities it registers.
{
  nixpkgs,
  systems,
  name,
  # The Connector's own directory.
  src,
  # The subflake's `entry.lib.connector { inherit core; }`. A backend
  # Connector adds `routes`; on the generic path its optional `script` (a list
  # of {method, path, status, body}) is answered ahead of the scripted
  # backend's own model.
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
      drivers = import ./drivers/default.nix { inherit (pkgs) lib; };
      coreDriver = pkgs.lib.hasAttr name drivers.entries;

      # Both trees sit at src/sdk and src/connectors/<name>; mk points go.mod's
      # SDK replace at ../../sdk, since a Connector outside connectors/ (the
      # fixtures) replaces it with a different relative path.
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
          go mod edit -replace spindrift.dev/sdk=../../sdk
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
      row = pkgs.lib.findFirst (r: r.name == name) null (import ./backends/default.nix);
      # A backend Connector no core row names has no per-backend tests to run.
      generic = isBackend && row == null;
      # The conformance tests skip, not fail, when their env var is unset, so the
      # setup below always exports it.
      setup =
        if generic then
          ''
            export SPINDRIFT_BACKEND_CONNECTOR_WASM=${connector.package pkgs}/${name}.wasm
            export SPINDRIFT_BACKEND_CONNECTOR_ROUTES=${pkgs.lib.escapeShellArg (builtins.toJSON connector.routes)}
          ''
          + pkgs.lib.optionalString (connector ? script) ''
            export SPINDRIFT_BACKEND_CONNECTOR_SCRIPT=${pkgs.lib.escapeShellArg (builtins.toJSON connector.script)}
          ''
        else if isBackend then
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
          ''
          # Every Driver with a native package gets the Connector's source here;
          # pi's connector_source_parity_test.go reads it.
          + pkgs.lib.optionalString (coreDriver && !connectorHosted) ''
            mkdir -p ../../connectors/${name}
            cp -r ${connectorSrc}/. ../../connectors/${name}
          '';
      # A Driver with no native package has this Connector alone as its host
      # half: driverrows' TestCorpus registers the claude wasm and replays the
      # corpus beside the Connector, in place of the native-vs-Connector
      # comparison a Driver with a native Strategy gets.
      connectorHosted = !isBackend && pkgs.lib.elem name drivers.connectorHosted;
      connectorOnlyDriver = !isBackend && !coreDriver;
      testPkg = if isBackend then "forge" else "driver";
      # driverrows' primary-row corpus test runs the Connector; the Driver's own
      # tests cannot, as they may not import the registry root. A Driver named
      # after no core Driver has no row there
      # (TestPrimaryRowConnectorPassesCorpus fails for it), so the kit's
      # package, home of TestConnectorCorpus, stands in.
      testPkgs =
        if connectorOnlyDriver then
          "./internal/driver/driverkit/conformance/..."
        else if generic then
          "./internal/connectorconformance -run '^TestConnectorConformance$'"
        else
          pkgs.lib.optionalString (!connectorHosted) "./internal/${testPkg}/${name}/..."
          + pkgs.lib.optionalString (!isBackend) " ./internal/driver/driverrows/...";
      # The tests that run the Connector: TestCorpus for a hosted Driver,
      # TestPrimaryRowConnectorPassesCorpus (and the Driver's own
      # TestConnectorResolveExitMatchesNative, if it has one) for one with a
      # native Strategy. The path stays `../cmd/launcher + "..."`: a deeper
      # literal would make ci-connector-contract-covers-reads walk that tree.
      requiredPasses =
        if connectorOnlyDriver then
          [ "TestConnectorCorpus" ]
        else if connectorHosted then
          [ "TestCorpus" ]
        else if generic then
          [ "TestConnectorConformance" ]
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
          (
            {
              nativeBuildInputs = [
                pkgs.go
                pkgs.git
              ];
            }
            # An attribute, not a script export, so a check can overrideAttrs it
            # to point at a corpus with a drifted golden.
            // pkgs.lib.optionalAttrs connectorOnlyDriver {
              SPINDRIFT_DRIVER_CONNECTOR_CORPUS = "${connectorSrc}/testdata/corpus";
            }
          )
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
