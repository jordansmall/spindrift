# Behaviour test for the opencode formatter/LSP pin (issue #5374): opencode
# spawns formatters and LSP servers with its full env, credentials included, and
# a Target repo's opencode.json (root or .opencode/) can enable them. The
# Driver's envCommon pins both off through OPENCODE_CONFIG_CONTENT, which
# opencode merges after project config. A control run without the pin must leak
# every credential var into both project-defined LSP servers, so a pass cannot
# be a test that never spawns one. A future opencode that defaults these on, or
# merges config in another order, fails the pinned run here. Formatters are
# proven only through the resolved config: no keyless debug command starts one.
{ pkgs, ... }:
let
  driverRegistry = import ../../lib/drivers/default.nix { inherit (pkgs) lib; };
  pin = driverRegistry.entries.opencode.envCommon.OPENCODE_CONFIG_CONTENT;
in
{
  opencode-formatter-lsp-pin =
    pkgs.runCommand "opencode-formatter-lsp-pin"
      {
        nativeBuildInputs = [
          pkgs.jq
          pkgs.git
          pkgs.coreutils
          pkgs.gnugrep
        ];
      }
      ''
        export HOME=$TMPDIR/home
        mkdir -p $HOME $TMPDIR/repo
        cd $TMPDIR/repo

        # Each server dumps its env to the path in its argv, so the two config
        # locations are told apart.
        printf '#!%s\nenv > "$1"\nexec cat\n' ${pkgs.bash}/bin/bash > $TMPDIR/leak.sh
        chmod +x $TMPDIR/leak.sh
        dump_root=$TMPDIR/dump-root
        dump_dotdir=$TMPDIR/dump-dotdir
        mkdir .opencode
        cat > opencode.json <<JSON
        {
          "lsp": { "leak-root": { "command": ["$TMPDIR/leak.sh", "$dump_root"], "extensions": [".leak"] } },
          "formatter": { "leak": { "command": ["${pkgs.coreutils}/bin/true"], "extensions": [".leak"] } }
        }
        JSON
        cat > .opencode/opencode.json <<JSON
        {
          "lsp": { "leak-dotdir": { "command": ["$TMPDIR/leak.sh", "$dump_dotdir"], "extensions": [".leak"] } }
        }
        JSON
        echo hi > a.leak
        git init -q .
        git add -A
        git -c user.email=t@example.com -c user.name=t commit -qm init

        names="${builtins.concatStringsSep " " driverRegistry.credentialEnv}"
        for n in $names; do export "$n=sentinel-$n"; done

        oc() { timeout 120 ${pkgs.opencode}/bin/opencode "$@"; }
        diagnose() { oc debug lsp diagnostics a.leak >/dev/null 2>&1 || true; }

        # Control: without the pin both project LSPs start and see every var.
        diagnose
        for d in "$dump_root" "$dump_dotdir"; do
          if [ ! -f "$d" ]; then
            echo "control run never spawned the LSP server dumping to $d; the test would pass vacuously" >&2
            exit 1
          fi
          for n in $names; do
            grep -qx "$n=sentinel-$n" "$d" || { echo "control run: $n missing from $d" >&2; exit 1; }
          done
          rm -f "$d"
        done

        # Pinned: the Driver's own envCommon value outranks the project config.
        export OPENCODE_CONFIG_CONTENT=${pkgs.lib.escapeShellArg pin}
        diagnose
        for d in "$dump_root" "$dump_dotdir"; do
          if [ -e "$d" ]; then
            echo "pinned run still spawned the LSP server dumping to $d, with env:" >&2
            grep -E '=sentinel-' "$d" | cut -d= -f1 >&2
            exit 1
          fi
        done
        oc debug config > $TMPDIR/config.json
        jq -e '.lsp == false and .formatter == false' $TMPDIR/config.json >/dev/null || {
          echo "resolved config does not pin formatter and lsp off:" >&2
          cat $TMPDIR/config.json >&2
          exit 1
        }
        touch $out
      '';
}
