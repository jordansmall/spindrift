# Issue #7420: core.lib.connectorChecks runs a Driver Connector named after no
# core Driver over the corpus it carries. The test-only `echo` fixture is that
# Connector; its connector-echo-* checks are the proof. The Driver Connector
# scaffold (templates/connector-driver) is the second: it must start green and
# fail on a drifted golden, so a generated Connector does too. It also holds
# `connector-scaffold-builds`, the placeholder-name wasm build of the tracker
# and forge templates, kept out of the docs-lane connector-scaffold checks.
{
  nixpkgs,
  system,
  pkgs,
  ...
}:
let
  core = import ../../core;
  checksOf =
    { name, src }:
    (core.lib.connectorChecks {
      inherit nixpkgs src name;
      systems = [ system ];
      connector = (import src).lib.connector { inherit core; };
    }).${system};
  echo = checksOf {
    name = "echo";
    src = ../fixtures/connector-echo;
  };
  template = checksOf {
    name = "connectorname";
    src = ../../templates/connector-driver;
  };

  # A corpus case whose golden disagrees must fail the conformance check and
  # name the case; this builds that failure and asserts on it.
  goldenDrift =
    conformance:
    conformance.overrideAttrs (o: {
      name = "${o.name}-golden-drift";
      buildCommand = ''
        cp -r ${o.SPINDRIFT_DRIVER_CONNECTOR_CORPUS} "$TMPDIR/corpus"
        chmod -R u+w "$TMPDIR/corpus"
        echo drift >> "$TMPDIR/corpus/basic/result.golden.txt"
        # Shadows the derivation attribute; works only because the inherited
        # script never re-exports it.
        export SPINDRIFT_DRIVER_CONNECTOR_CORPUS="$TMPDIR/corpus"
        # bash ignores set -e in a subshell used as an `if`/`&&` operand, hence the status capture.
        set +e
        (
          set -e
          ${o.buildCommand}
        ) >"$TMPDIR/drift.log" 2>&1
        status=$?
        set -e
        fail() {
          echo "golden-drift: $1" >&2
          cat "$TMPDIR/drift.log" >&2
          exit 1
        }
        [ "$status" -ne 0 ] || fail "the conformance run passed over a drifted golden"
        grep -qF -- '--- FAIL: TestConnectorCorpus/basic ' "$TMPDIR/drift.log" || fail "the failure does not name the case"
        grep -qF 'basic/result.golden.txt differs from the produced output' "$TMPDIR/drift.log" || fail "the failure does not name the golden"
        touch $out
      '';
    });
in
echo
// {
  connector-echo-conformance-golden-drift = goldenDrift echo.connector-echo-conformance;

  # Only the conformance checks: the template's go fmt/vet/test are the
  # Connector lane's, as for every Connector.
  connector-driver-template-conformance = template.connector-connectorname-conformance;
  connector-driver-template-golden-drift = goldenDrift template.connector-connectorname-conformance;

  # The tracker and forge scaffolds (templates/connector-tracker, -forge) are
  # data Connectors; a generated one must compile to its wasm. connector-scaffold
  # only evaluates them, being in the docs lane, so the build lives here.
  connector-scaffold-builds = pkgs.runCommand "connector-scaffold-builds" { } ''
    ${pkgs.lib.concatMapStringsSep "\n"
      (
        dir:
        let
          connector = (import dir).lib.connector { inherit core; };
        in
        "test -s ${connector.package pkgs}/${connector.data.name}.wasm"
      )
      [
        ../../templates/connector-tracker
        ../../templates/connector-forge
      ]
    }
    touch $out
  '';
}
