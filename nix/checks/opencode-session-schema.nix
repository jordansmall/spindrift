# Schema-drift guard for the opencode session store (issue #5379): Go tests
# seed a sqlite db from testdata/session-schema.sql, so that fixture must keep
# matching the `project` and `session` columns (name, type, notnull) the baked
# opencode actually creates. Bumping pkgs.opencode trips this until the
# fixture is regenerated.
{ pkgs, ... }:
let
  fixtureRel = "cmd/launcher/internal/driver/opencode/testdata/session-schema.sql";
  fixture = ../.. + "/${fixtureRel}";
in
{
  opencode-session-schema =
    pkgs.runCommand "opencode-session-schema"
      {
        nativeBuildInputs = [
          pkgs.sqlite
          pkgs.diffutils
          pkgs.coreutils
          pkgs.gnugrep
        ];
      }
      ''
        export HOME=$TMPDIR/home
        mkdir -p $HOME
        export OPENCODE_DB=$HOME/opencode.db
        ${pkgs.opencode}/bin/opencode session list >/dev/null

        sqlite3 $TMPDIR/fixture.db < ${fixture}

        cols="SELECT m.name, p.name, p.type, p.\"notnull\" FROM sqlite_master m, pragma_table_info(m.name) p WHERE m.type = 'table' AND m.name IN ('project','session') ORDER BY m.name, p.cid"
        sqlite3 $OPENCODE_DB "$cols" > $TMPDIR/real.txt
        sqlite3 $TMPDIR/fixture.db "$cols" > $TMPDIR/fixture.txt

        # An empty or session-less dump would let a store-path change pass vacuously.
        if ! grep -q '^session|' $TMPDIR/real.txt || ! grep -q '^project|' $TMPDIR/real.txt; then
          echo "opencode ${pkgs.opencode.version} created no project/session tables in its session store" >&2
          exit 1
        fi

        if ! diff -u $TMPDIR/fixture.txt $TMPDIR/real.txt; then
          echo "opencode ${pkgs.opencode.version} schema drifted from ${fixtureRel}; regenerate it from the repo root with:" >&2
          sed -n 's/^--   //p' ${fixture} >&2
          exit 1
        fi
        touch $out
      '';
}
