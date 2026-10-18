# Regenerator for the opencode session-store fixture (issue #5492). Separate
# from nix/regen.nix because the fixture is a snapshot of an upstream
# binary's output, not a render of a Nix value: folding it into `.#regen`
# would make every env-schema regen fetch and run opencode. It copies the same
# lib/opencode-session-schema.nix output that nix/checks/opencode-session-schema.nix
# compares against, so the app and its drift guard share one renderer.
{ pkgs }:
let
  schema = import ../lib/opencode-session-schema.nix { inherit pkgs; };
in
pkgs.writeShellApplication {
  name = "regen-opencode-schema";
  runtimeInputs = [
    pkgs.git
    pkgs.coreutils
  ];
  text = ''
    root="$(git rev-parse --show-toplevel)"
    if [ ! -f "$root/lib/env-schema.nix" ]; then
      echo "regen-opencode-schema: $root doesn't look like the spindrift repo (no lib/env-schema.nix); refusing to write" >&2
      exit 1
    fi

    dest="$root/${schema.fixtureRel}"
    # The store file is read-only; install sets the mode the repo file carries.
    install -m 644 ${schema} "$dest"
    echo "regen-opencode-schema: wrote $dest"
  '';
}
