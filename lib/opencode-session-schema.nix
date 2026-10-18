# The one renderer of the opencode session-store fixture (issue #5492): the
# `opencode-session-schema` check compares against this output and the
# `regen-opencode-schema` app copies it over the committed fixture, so the two
# cannot disagree about how it is produced. $out is the complete fixture text.
{ pkgs }:
pkgs.runCommand "opencode-session-schema.sql"
  {
    passthru.fixtureRel = "cmd/launcher/internal/driver/opencode/testdata/session-schema.sql";
    nativeBuildInputs = [
      pkgs.sqlite
      pkgs.coreutils
      pkgs.gnugrep
    ];
  }
  ''
    export HOME=$TMPDIR/home
    mkdir -p $HOME
    export OPENCODE_DB=$HOME/opencode.db
    ${pkgs.opencode}/bin/opencode session list >/dev/null

    sqlite3 $OPENCODE_DB "SELECT sql || ';' FROM sqlite_master WHERE name IN ('project','session') ORDER BY name" > $TMPDIR/ddl.sql

    # An empty or session-less dump would let a store-path change pass vacuously.
    if ! grep -q '^CREATE TABLE `session`' $TMPDIR/ddl.sql || ! grep -q '^CREATE TABLE `project`' $TMPDIR/ddl.sql; then
      echo "opencode ${pkgs.opencode.version} created no project/session tables in its session store" >&2
      exit 1
    fi

    cat > $out <<'HEADER'
    -- `project` and `session` tables, last regenerated from opencode
    -- ${pkgs.opencode.version} (pkgs.opencode in the flake's locked nixpkgs), verbatim
    -- from sqlite_master, each with a `;` appended. Rendered by
    -- lib/opencode-session-schema.nix; do not edit by hand. The nix check
    -- `opencode-session-schema` fails when pkgs.opencode's project/session
    -- columns drift from this file. Regenerate it from the repo root with
    -- `nix run .#regen-opencode-schema`.
    --
    HEADER
    cat $TMPDIR/ddl.sql >> $out
  ''
