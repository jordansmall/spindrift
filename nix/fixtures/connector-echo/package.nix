# Builds echo.wasm, the test-only Driver's host half, a wasip1 reactor. Laid
# out like nix/fixtures/connector/package.nix: go.mod replaces
# spindrift.dev/sdk with a relative path three levels up.
{ pkgs, core }:
let
  fs = pkgs.lib.fileset;
  src = fs.toSource {
    root = ./.;
    fileset = fs.fileFilter (f: f.hasExt "go" || f.name == "go.mod") ./.;
  };
in
pkgs.runCommand "connector-echo-wasm" { nativeBuildInputs = [ pkgs.go ]; } ''
  export HOME=$TMPDIR GOCACHE=$TMPDIR/gocache
  export GOOS=wasip1 GOARCH=wasm CGO_ENABLED=0 GOFLAGS=-mod=mod GOPROXY=off
  mkdir -p work/nix/fixtures/connector-echo
  cp -r ${core.lib.sdkSrc} work/sdk
  cp -r ${src}/. work/nix/fixtures/connector-echo
  chmod -R +w work
  cd work/nix/fixtures/connector-echo
  mkdir $out
  go build -buildmode=c-shared -o $out/echo.wasm .
''
