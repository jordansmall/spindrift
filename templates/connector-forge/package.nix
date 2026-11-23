# Builds connectorname.wasm, a wasip1 reactor. The SDK source comes from `core`,
# not a path out of this directory (the escape guard): go.mod's replace names
# the SDK on GitHub, which GOPROXY=off cannot fetch, so the build repoints it.
{ pkgs, core }:
let
  fs = pkgs.lib.fileset;
  # Only the Go module, so a Nix-only edit does not rebuild the wasm.
  src = fs.toSource {
    root = ./.;
    fileset = fs.fileFilter (f: f.hasExt "go" || f.name == "go.mod") ./.;
  };
in
pkgs.runCommand "connector-connectorname-wasm" { nativeBuildInputs = [ pkgs.go ]; } ''
  export HOME=$TMPDIR GOCACHE=$TMPDIR/gocache
  export GOOS=wasip1 GOARCH=wasm CGO_ENABLED=0 GOFLAGS=-mod=mod GOPROXY=off
  cp -r ${src} connectorname
  chmod -R +w connectorname
  cd connectorname
  go mod edit -replace spindrift.dev/sdk=${core.lib.sdkSrc}
  mkdir $out
  go build -buildmode=c-shared -o $out/connectorname.wasm .
''
