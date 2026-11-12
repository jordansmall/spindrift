# Builds driver.wasm (the fixture Driver's host half) and network.wasm (the
# fixture network Connector, ./network), wasip1 reactors. The SDK
# source comes from `core`, not a path out of this directory (the escape
# guard); both trees are copied to their repo-relative layout because go.mod
# replaces spindrift.dev/sdk with a relative path three levels up.
{ pkgs, core }:
let
  fs = pkgs.lib.fileset;
  # Only the Go module, so a Nix-only edit does not rebuild the wasm.
  src = fs.toSource {
    root = ./.;
    fileset = fs.fileFilter (f: f.hasExt "go" || f.name == "go.mod") ./.;
  };
in
pkgs.runCommand "connector-fixture-wasm" { nativeBuildInputs = [ pkgs.go ]; } ''
  export HOME=$TMPDIR GOCACHE=$TMPDIR/gocache
  export GOOS=wasip1 GOARCH=wasm CGO_ENABLED=0 GOFLAGS=-mod=mod GOPROXY=off
  mkdir -p work/nix/fixtures/connector
  cp -r ${core.lib.sdkSrc} work/sdk
  cp -r ${src}/. work/nix/fixtures/connector
  chmod -R +w work
  cd work/nix/fixtures/connector
  mkdir $out
  go build -buildmode=c-shared -o $out/driver.wasm .
  go build -buildmode=c-shared -o $out/network.wasm ./network
''
