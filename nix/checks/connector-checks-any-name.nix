# Issue #7420: core.lib.connectorChecks runs a Driver Connector named after no
# core Driver over the corpus it carries. The test-only `echo` fixture is that
# Connector; its connector-echo-* checks are the proof.
{ nixpkgs, system, ... }:
let
  core = import ../../core;
  src = ../fixtures/connector-echo;
  echo =
    (core.lib.connectorChecks {
      inherit nixpkgs src;
      systems = [ system ];
      name = "echo";
      connector = (import src).lib.connector { inherit core; };
    }).${system};
in
echo
// {
  # A corpus case whose golden disagrees must fail the conformance check and
  # name the case; this builds that failure and asserts on it.
  connector-echo-conformance-golden-drift = echo.connector-echo-conformance.overrideAttrs (o: {
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
}
