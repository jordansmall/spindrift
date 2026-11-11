# Issue #6792: scans the launcher module's dependencies (wazero included)
# against the Go vulnerability database. Why this is an app and not a
# sandboxed check: docs/reference.md#pinned-wazero. `go` is a runtimeInput
# because govulncheck loads packages through it.
{ pkgs }:
pkgs.writeShellApplication {
  name = "govulncheck";
  runtimeInputs = [
    pkgs.git
    pkgs.go
    pkgs.govulncheck
  ];
  text = ''
    root=$(git rev-parse --show-toplevel) || {
      echo "govulncheck: run this from inside the spindrift checkout" >&2
      exit 2
    }
    cd "$root/cmd/launcher"
    exec govulncheck "$@" ./...
  '';
}
