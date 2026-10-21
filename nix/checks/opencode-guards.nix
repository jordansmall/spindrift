# Behaviour test for the opencode guard plugin (issue #5346): it must run the
# baked claude Bash hooks, so credentials are scrubbed, environ reads,
# credential-file bash and read calls, and backgrounding are refused with the
# hooks' own messages, a broken hook or jq fails closed, and a long Bash result
# is bounded to a tail by the baked summary hook (issue #5358). A bash timeout
# above the Driver's bashTimeoutEnv value is capped at it, a shorter or unset
# one kept (issue #5381). Runs under opencode's own runtime (BUN_BE_BUN=1) so
# no separate JS engine is needed.
{ pkgs, ... }:
let
  driverRegistry = import ../../lib/drivers/default.nix { inherit (pkgs) lib; };
  # The test sets the cap under the registry's name, so a plugin reading any
  # other name fails the clamp case instead of quietly never capping.
  capEnv = builtins.head driverRegistry.entries.opencode.bashTimeoutEnv;
in
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
        hooks=${import ../../lib/agent-hooks.nix pkgs}
        mkdir -p $TMPDIR/garbage $TMPDIR/silent
        for f in $hooks/*.sh; do
          s=$(basename "$f")
          printf '#!%s\necho not-json\n' "$(command -v bash)" > $TMPDIR/garbage/$s
          chmod +x $TMPDIR/garbage/$s
          printf '#!%s\nexit 0\n' "$(command -v bash)" > $TMPDIR/silent/$s
          chmod +x $TMPDIR/silent/$s
        done

        # The real hooks, except a tee that exits 0 printing nothing.
        cp -r $hooks $TMPDIR/silent-tee
        chmod -R u+w $TMPDIR/silent-tee
        cp -f $TMPDIR/silent/bash-output-tee.sh $TMPDIR/silent-tee/bash-output-tee.sh

        stage() {
          sed "s|^const HOOKS_DIR = .*|const HOOKS_DIR = \"$2\";|" ${../../agent/opencode-guards.js} > "$1"
          grep -qF "const HOOKS_DIR = \"$2\";" "$1"
        }
        stage $TMPDIR/guards.js $hooks
        stage $TMPDIR/guards-missing.js $TMPDIR/no-such-dir
        stage $TMPDIR/guards-garbage.js $TMPDIR/garbage
        stage $TMPDIR/guards-silent.js $TMPDIR/silent
        stage $TMPDIR/guards-silent-tee.js $TMPDIR/silent-tee

        sed 's|tools: \["bash"\], rewrites: false|tools: ["bash", "nosuchtool"], rewrites: false|' $TMPDIR/guards.js > $TMPDIR/guards-unmapped.js
        grep -qF '"nosuchtool"' $TMPDIR/guards-unmapped.js

        export HOME=$TMPDIR/home
        mkdir -p $HOME
        export HOOKS_DIR=$hooks
        export BASH_OUTPUT_TEE_DIR=$TMPDIR/tee-logs
        export BASH_OUTPUT_SUMMARY_TAIL_BYTES=64
        export PLUGIN=$TMPDIR/guards.js
        export PLUGIN_MISSING=$TMPDIR/guards-missing.js
        export PLUGIN_GARBAGE=$TMPDIR/guards-garbage.js
        export PLUGIN_SILENT=$TMPDIR/guards-silent.js
        export PLUGIN_SILENT_TEE=$TMPDIR/guards-silent-tee.js
        export PLUGIN_UNMAPPED=$TMPDIR/guards-unmapped.js
        export BASH_TIMEOUT_CAP_ENV=${capEnv}
        export CREDENTIAL_PATHS='${builtins.toJSON (import ../../lib/credential-paths.nix)}'
        BUN_BE_BUN=1 ${pkgs.opencode}/bin/opencode ${../../tests/opencode-guards.test.mjs}
        touch $out
      '';
}
