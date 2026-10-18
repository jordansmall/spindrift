# Rejects a standalone `! cmd` shell command in any *.nix under nix/checks/,
# subdirectories included (issues #5102, #5549).
# bash errexit ignores a negated pipeline, so a bare `! grep -q X f` inside a
# runCommand can never fail the build; write `! cmd || { ...; exit 1; }`, or
# make it the tail of a function body's `&&`/`||` list so it sets the return.
# Known limits: line-based; a `!` after `;` on one line is unseen; a `||` in a
# quoted pattern or trailing `#` comment hides a bare line; any `!` line after a
# trailing `&&`/`||` passes, even at top level where errexit still skips it; a
# comment between `a &&` and `! b`, or a `! a |` pipe continuation, is rejected.
{ pkgs, ... }:
let
  scanner = pkgs.writeText "bare-bang-scan.awk" ''
    function flush() {
      if (cmd == "") return
      t = cmd
      sub(/^[ \t]+/, "", t)
      if (t ~ /^![ \t]/ && index(t, "||") == 0 && prev !~ /(&&|[|][|])$/) {
        printf "%s:%d: bare `! cmd` without `||` fallback: %s\n", file, start, t
        hits++
      }
      prev = t
      sub(/[ \t]+$/, "", prev)
      cmd = ""
    }
    FNR == 1 { flush(); prev = "" }
    {
      if (cmd == "") { start = FNR; file = FILENAME }
      line = $0
      cont = sub(/\\[ \t]*$/, "", line)
      cmd = cmd line
      if (!cont) flush()
    }
    END { flush(); exit (hits > 0) }
  '';
in
{
  bare-bang-guard = pkgs.runCommand "bare-bang-guard" { } ''
    scan() { awk -f ${scanner} "$@"; }

    # Fixtures are built with printf so this file holds no `!`-led line itself,
    # and live outside the scanned tree.
    printf '%s\n' 'echo a' '! grep -q X f' 'echo b' > neg.sh
    printf '%s\n' \
      '! grep -q X f \' '  || { echo bad >&2; exit 1; }' \
      'grep -q X f &&' '  ! grep -q Y f' \
      'grep -q X f ||' '  ! grep -q Y f' \
      'x=1' > pos.sh

    if scan neg.sh > neg.out; then echo "negative control accepted" >&2; exit 1; fi
    grep -qx 'neg.sh:2: .*' neg.out || { echo "negative control not named" >&2; cat neg.out >&2; exit 1; }
    [ "$(wc -l < neg.out)" = 1 ] || { echo "negative control: expected one hit" >&2; cat neg.out >&2; exit 1; }
    scan pos.sh || { echo "positive fixture rejected" >&2; exit 1; }

    scan $(find ${./.} -name '*.nix' | sort) || { echo "standalone '!' without '||' (errexit ignores it)" >&2; exit 1; }
    touch $out
  '';
}
