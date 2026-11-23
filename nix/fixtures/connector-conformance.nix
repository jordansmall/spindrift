# Test-only backend Connectors named after no core backend row, so
# core.lib.connectorChecks takes its generic conformance path for them
# (lib/connector-checks.nix). Each reuses a wasm the fixture Connector builds
# and declares just the Routes that wasm calls; they live here, never under
# connectors/, which is for shipped Connectors.
{ core }:
let
  checkRoutes = (import ../../lib/connector-routes.nix).check;
  # The fixture wasm calls {Target.API}/repos/{repo}/..., and the scripted
  # backend serves under /api/v1.
  routes =
    name: methods:
    checkRoutes name (
      map (method: {
        inherit method;
        host = "fixture.example";
        path = "/api/v1/repos/{repo}";
      }) methods
    );
  # connectorChecks reads ${package}/${name}.wasm.
  entry = name: wasm: methods: {
    routes = routes name methods;
    package =
      pkgs:
      pkgs.runCommand "${name}-wasm" { } ''
        mkdir $out
        cp ${import ./connector/package.nix { inherit pkgs core; }}/${wasm}.wasm $out/${name}.wasm
      '';
  };
in
{
  conformance-tracker = entry "conformance-tracker" "tracker" [ "GET" ];
  # The wasm probes /dialect, which only the scripted answer serves, so the
  # check passes only if connectorChecks exports `script` in Scripted's shape.
  conformance-tracker-dialect = entry "conformance-tracker-dialect" "tracker-dialect" [ "GET" ] // {
    script = [
      {
        method = "GET";
        path = "/api/v1/repos/o/r/dialect";
        status = 200;
        body = "{}";
      }
    ];
  };
  conformance-tracker-writes = entry "conformance-tracker-writes" "tracker-writes" [
    "GET"
    "POST"
    "DELETE"
  ];
  conformance-forge = entry "conformance-forge" "forge" [
    "GET"
    "DELETE"
  ];
  conformance-forge-pr = entry "conformance-forge-pr" "forge-pr" [
    "GET"
    "POST"
    "PATCH"
    "DELETE"
  ];
}
