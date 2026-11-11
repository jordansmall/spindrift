# Fixture Connectors for the Connector host's tests (ADR 0073), built as wasip1
# reactors. The source lives under nix/fixtures/, not connectors/, which is for
# shipped Connectors.
{ pkgs }:
let
  fs = pkgs.lib.fileset;
  # The fixture module replaces spindrift.dev/sdk with ../../../sdk, so both
  # trees keep their repo-relative layout.
  src = fs.toSource {
    root = ../.;
    fileset = fs.unions [
      ../sdk
      ./fixtures/connector-wasm
    ];
  };
  # The fixture Driver is built by the fixture Connector itself, the one source
  # of its wasm; it keeps its place here for the tests that read driver.wasm.
  driverWasm = import ./fixtures/connector/package.nix {
    inherit pkgs;
    core = import ../core;
  };
  versionSymbol = "spindrift.dev/sdk/connector.protocolVersion";
in
pkgs.runCommand "connector-wasm-fixtures" { nativeBuildInputs = [ pkgs.go ]; } ''
  export HOME=$TMPDIR GOCACHE=$TMPDIR/gocache
  export GOOS=wasip1 GOARCH=wasm CGO_ENABLED=0 GOFLAGS=-mod=mod GOPROXY=off
  cp -r ${src} src
  chmod -R +w src
  cd src/nix/fixtures/connector-wasm
  mkdir $out
  build() { go build -buildmode=c-shared -o "$out/$1" "''${@:2}"; }
  build current.wasm ./handshake
  build previous.wasm -ldflags=-X=${versionSymbol}=0.0.3 ./handshake
  build future.wasm -ldflags=-X=${versionSymbol}=0.2.0 ./handshake
  build probe.wasm ./probe
  build call.wasm ./call
  build seamtest-driver.wasm ./seamtest-driver
  cp ${driverWasm}/driver.wasm $out/driver.wasm
  for mode in trap loop memory oversize malformed invalid; do
    build hostile-$mode.wasm -ldflags=-X=main.mode=$mode ./hostile
  done
''
