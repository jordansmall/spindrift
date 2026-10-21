# Rejects a quoted runner family or runtime name in any *.nix outside the runner
# rows (issue #5689, ADR 0069). The rows in lib/runners/ are the one home for
# these literals; everything else must read them from the registry, so adding a
# runner stays a one-row change. The name set is derived from the rows at eval
# time: each row's `name` plus its `runtimes`.
# Known limits: line-based; only a double-quoted exact name is matched, so a
# name inside a longer string ("docker-compose") or an indented-string prose
# mention without quotes is unseen; a full-line `#` comment is skipped but a
# trailing `#` comment after code is not; a name built by interpolation is unseen.
# Exemptions are whole-file or directory-prefix, so a literal added anywhere in
# an exempt path passes unseen: keep an exempt file down to the one site that
# needs the names (issue #5804). The golden directory stays a prefix exemption
# because the Go golden tests in cmd/launcher/quickstart/quickstart_test.go pin
# its files byte-for-byte, so a new literal there fails those tests, not this
# one.
{ pkgs, ... }:
let
  rows = import ../../lib/runners;
  names = pkgs.lib.unique (pkgs.lib.concatMap (r: [ r.name ] ++ r.runtimes) rows);

  # Repo-relative path prefix -> why that tree may name a runner. lib/runners/
  # (the rows) is structurally out of scope and is not listed here.
  exemptions = {
    "nix/checks/runners-unique-runtime.nix" = ''
      the runners-unique-runtime check pins the rows' derived runtime union
      and runtime-to-family lookup by value, so it must spell the names it
      expects'';
    "cmd/launcher/quickstart/testdata/golden/" = ''
      golden quickstart output: Consumer flakes (generated, plus a hand-made
      broken fixture) that choose their own runtime, which is data the
      Consumer owns, not repo logic'';
  };
  checkedExemptions = pkgs.lib.mapAttrs (
    path: reason:
    assert pkgs.lib.assertMsg (
      builtins.isString reason && reason != ""
    ) "runner-names-guard: exemption for ${path} needs a non-empty reason";
    reason
  ) exemptions;
  exemptPrefixes = builtins.attrNames checkedExemptions;

  pattern = ''"(${pkgs.lib.concatStringsSep "|" names})"'';
  probe = builtins.head (builtins.elemAt rows 0).runtimes;
  # The fixture is a file under a directory-prefix exemption, never a file entry.
  exemptProbe = builtins.head (builtins.filter (pkgs.lib.hasSuffix "/") exemptPrefixes);

  nixSrc = pkgs.lib.fileset.toSource {
    root = ../..;
    fileset = pkgs.lib.fileset.fileFilter (f: f.hasExt "nix") ../..;
  };

  scanner = pkgs.writeText "runner-names-scan.awk" ''
    BEGIN { n = split(pfx, a, " "); for (i = 1; i <= n; i++) prefixes[a[i]] = 1 }
    FNR == 1 { skip = 0; for (p in prefixes) if (index(FILENAME, p) == 1) skip = 1 }
    skip { next }
    /^[ \t]*#/ { next }
    $0 ~ re { printf "%s:%d: %s\n", FILENAME, FNR, $0; hits++ }
    END { exit (hits > 0) }
  '';
  # deepSeq forces the per-exemption reason assertions, which attrNames would not.
  prefixList = builtins.deepSeq checkedExemptions (
    pkgs.lib.concatStringsSep " " ([ "lib/runners/" ] ++ exemptPrefixes)
  );
in
{
  runner-names-guard = pkgs.runCommand "runner-names-guard" { } ''
    # Paths are scanned relative to the tree root so prefixes match.
    scan() {
      (
        cd "$1"
        find . -name '*.nix' | sed 's|^\./||' | sort \
          | xargs awk -v re='${pattern}' -v pfx='${prefixList}' -f ${scanner}
      )
    }

    # Fixtures are built with printf from the derived names so this file holds
    # no runner literal itself, and live outside the scanned tree.
    exempt=${exemptProbe}x.nix
    mkdir -p neg/nix neg/lib/runners "neg/$(dirname "$exempt")" pos/nix pos/lib/runners "pos/$(dirname "$exempt")"
    printf '%s\n' 'x = "${probe}";' > neg/nix/bad.nix
    printf '%s\n' 'x = "${probe}";' | tee neg/lib/runners/row.nix neg/"$exempt" pos/lib/runners/row.nix pos/"$exempt" > /dev/null
    printf '%s\n' '# x = "${probe}";' 'y = 1;' | tee neg/nix/comment.nix pos/nix/comment.nix > /dev/null

    if scan neg > neg.out; then echo "negative control accepted" >&2; exit 1; fi
    [ "$(wc -l < neg.out)" = 1 ] || { echo "negative control: expected one hit" >&2; cat neg.out >&2; exit 1; }
    grep -q '^nix/bad.nix:1: ' neg.out || { echo "negative control not named" >&2; cat neg.out >&2; exit 1; }
    scan pos || { echo "positive fixture rejected" >&2; exit 1; }

    scan ${nixSrc} > real.out || {
      echo "runner family/runtime name literal outside lib/runners/ (read it from the registry, or add an entry with a reason string to the exemptions attrset in nix/checks/runner-names-guard.nix):" >&2
      cat real.out >&2
      exit 1
    }
    touch $out
  '';
}
