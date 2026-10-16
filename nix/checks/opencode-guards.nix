# Behaviour test for the opencode guard plugin (issue #5346): it must run the
# baked claude Bash hooks, so credentials are scrubbed, environ reads and
# backgrounding are refused with the hooks' own messages, and a broken hook
# fails closed. Runs under opencode's own runtime (BUN_BE_BUN=1) so no
# separate JS engine is needed.
{ pkgs, ... }:
{
  opencode-guards-test =
    pkgs.runCommand "opencode-guards-test"
      {
        nativeBuildInputs = [
          pkgs.jq
          pkgs.bash
          pkgs.coreutils
          pkgs.gnugrep
          pkgs.gnused
        ];
      }
      ''
        hooks=$TMPDIR/hooks
        mkdir -p $hooks $TMPDIR/garbage $TMPDIR/silent
        cp ${../../agent/reject-background-bash.sh} $hooks/reject-background-bash.sh
        cp ${../../agent/env-credential-scrub.sh} $hooks/env-credential-scrub.sh
        cp ${../../agent/credential-vars} $hooks/credential-vars
        chmod +x $hooks/*.sh
        patchShebangs $hooks
        for s in reject-background-bash.sh env-credential-scrub.sh; do
          printf '#!%s\necho not-json\n' "$(command -v bash)" > $TMPDIR/garbage/$s
          chmod +x $TMPDIR/garbage/$s
          printf '#!%s\nexit 0\n' "$(command -v bash)" > $TMPDIR/silent/$s
          chmod +x $TMPDIR/silent/$s
        done

        stage() {
          sed "s|^const HOOKS_DIR = .*|const HOOKS_DIR = \"$2\";|" ${../../agent/opencode-guards.js} > "$1"
          grep -qF "const HOOKS_DIR = \"$2\";" "$1"
        }
        stage $TMPDIR/guards.js $hooks
        stage $TMPDIR/guards-missing.js $TMPDIR/no-such-dir
        stage $TMPDIR/guards-garbage.js $TMPDIR/garbage
        stage $TMPDIR/guards-silent.js $TMPDIR/silent

        export HOME=$TMPDIR/home
        mkdir -p $HOME
        export HOOKS_DIR=$hooks
        export PLUGIN=$TMPDIR/guards.js
        export PLUGIN_MISSING=$TMPDIR/guards-missing.js
        export PLUGIN_GARBAGE=$TMPDIR/guards-garbage.js
        export PLUGIN_SILENT=$TMPDIR/guards-silent.js
        BUN_BE_BUN=1 ${pkgs.opencode}/bin/opencode ${../../tests/opencode-guards.test.mjs}
        touch $out
      '';
}
