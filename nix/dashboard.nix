# The read-only Dashboard (ADR 0060), built standalone: its own Go module with
# no dependencies, so there is no vendorHash to keep in sync. src is scoped to
# ../dashboard so a launcher commit never moves this derivation and a Dashboard
# commit never moves the Daemon's store path (lib/mkHarness.nix daemonSrc is
# rooted at cmd/launcher).
{ pkgs }:
let
  unwrapped = pkgs.buildGoModule {
    pname = "spindrift-dashboard-unwrapped";
    version = "0";
    src = ../dashboard;
    vendorHash = null;
    # The dashboard-go-test check (nix/checks/go.nix) already runs go test over
    # the same source, with git on PATH.
    doCheck = false;
    meta.license = pkgs.lib.licenses.mit;
  };
in
# The binary execs git at runtime (rev-parse), so put it on PATH rather than
# trusting the caller's environment.
pkgs.runCommand "spindrift-dashboard"
  {
    nativeBuildInputs = [ pkgs.makeWrapper ];
    meta.mainProgram = "dashboard";
  }
  ''
    mkdir -p $out/bin
    makeWrapper ${unwrapped}/bin/dashboard $out/bin/dashboard \
      --prefix PATH : ${pkgs.lib.makeBinPath [ pkgs.git ]}
  ''
