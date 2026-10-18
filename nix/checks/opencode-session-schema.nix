# Schema-drift guard for the opencode session store (issue #5379): Go tests
# seed a sqlite db from testdata/session-schema.sql, so that fixture must keep
# matching the `project` and `session` columns (name, type, notnull) the baked
# opencode actually creates. Bumping pkgs.opencode trips this until the
# fixture is regenerated. Columns, not bytes, are compared so a whitespace-only
# upstream reformat does not fail CI; the rendering itself is shared with the
# regen app (issue #5492).
{ pkgs, config, ... }:
let
  rendered = import ../../lib/opencode-session-schema.nix { inherit pkgs; };
  fixture = ../.. + "/${rendered.fixtureRel}";
  mkAppWiringCheck = import ../../lib/app-wiring-check.nix { inherit pkgs; };
in
{
  opencode-session-schema =
    pkgs.runCommand "opencode-session-schema"
      {
        nativeBuildInputs = [
          pkgs.sqlite
          pkgs.diffutils
        ];
      }
      ''
        sqlite3 $TMPDIR/real.db < ${rendered}
        sqlite3 $TMPDIR/fixture.db < ${fixture}

        cols="SELECT m.name, p.name, p.type, p.\"notnull\" FROM sqlite_master m, pragma_table_info(m.name) p WHERE m.type = 'table' AND m.name IN ('project','session') ORDER BY m.name, p.cid"
        sqlite3 $TMPDIR/real.db "$cols" > $TMPDIR/real.txt
        sqlite3 $TMPDIR/fixture.db "$cols" > $TMPDIR/fixture.txt

        if ! diff -u $TMPDIR/fixture.txt $TMPDIR/real.txt; then
          echo "opencode ${pkgs.opencode.version} schema drifted from ${rendered.fixtureRel}; regenerate it with: nix run .#regen-opencode-schema" >&2
          exit 1
        fi
        touch $out
      '';

  # Wiring guard (issue #5492): flake.nix's apps.regen-opencode-schema must
  # resolve to the derivation this check builds from nix/regen-opencode-schema.nix.
  regen-opencode-schema-app-wiring = mkAppWiringCheck {
    name = "regen-opencode-schema";
    apps = config.apps;
    package = import ../regen-opencode-schema.nix { inherit pkgs; };
    exposedReason = "flake.nix must expose apps.regen-opencode-schema at the top level (issue #5492) so `nix run .#regen-opencode-schema` actually resolves";
    sameEnvReason = "flake.nix's top-level apps.regen-opencode-schema must be built from nix/regen-opencode-schema.nix with the SAME pkgs this check uses (issue #5492) -- otherwise `nix run .#regen-opencode-schema` silently regenerates the fixture against a foreign opencode";
  };
}
