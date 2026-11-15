# Issue #7082: a Box runs with sandbox=false, so it sees the whole /nix/store
# while CI's sandboxed build sees only a derivation's closure (ADR 0008). A
# seam fixture that names a real store path it does not carry passes in the
# Box and breaks in CI, so every /nix/store path in the fixtures must be in
# their own closure or be the zero-hash sentinel
# (/nix/store/00000000000000000000000000000000-<name>), which is never realised.
{ pkgs, fixtures, ... }:
let
  inherit (fixtures.seamFixtures) sentinelHash storePathRegex;

  # Usage: seam-fixtures-store-path-scan <dir> <closure store-paths file>.
  # Store names are [0-9a-zA-Z+._?=-], so JSON quotes, commas and backslashes
  # end a match.
  scan = pkgs.writeShellApplication {
    name = "seam-fixtures-store-path-scan";
    runtimeInputs = [
      pkgs.coreutils
      pkgs.gnugrep
      pkgs.findutils
    ];
    text = ''
      dir=$1
      closure=$2
      offenders=$(
        {
          grep -rhoaE '${storePathRegex}' "$dir" || true
          # The renderer never rewrites symlink targets, so a link to an
          # uncarried path fails here rather than being sentinelled.
          find "$dir" -type l -printf '%l\n' | grep -oE '${storePathRegex}' || true
        } | sort -u \
          | { grep -v '^/nix/store/${sentinelHash}-' || true; } \
          | { grep -vxFf "$closure" || true; }
      )
      if [ -n "$offenders" ]; then
        echo "seam fixtures name store paths their closure does not carry:" >&2
        echo "$offenders" >&2
        exit 1
      fi
    '';
  };

  # A leaky fixture dir: a carried path, the sentinel, and (when leak) a real
  # store path whose context is discarded, so nothing depends on it.
  toyFixture =
    leak:
    pkgs.writeTextDir "run-input.json" ''
      {"carried": "${pkgs.coreutils}", "sentinel": "/nix/store/${sentinelHash}-image.tar"${pkgs.lib.optionalString leak ", \"leak\": \"${builtins.unsafeDiscardStringContext "${pkgs.hello}"}\""}}
    '';
  toyClosure =
    fixture:
    pkgs.closureInfo {
      rootPaths = [
        fixture
        pkgs.coreutils
      ];
    };
in
{
  seam-fixtures-names-no-uncarried-store-path =
    pkgs.runCommand "seam-fixtures-names-no-uncarried-store-path" { }
      ''
        ${scan}/bin/seam-fixtures-store-path-scan ${fixtures.seamFixtures} ${
          pkgs.closureInfo { rootPaths = [ fixtures.seamFixtures ]; }
        }/store-paths
        touch $out
      '';

  seam-fixtures-store-path-scan-detects-leak =
    let
      clean = toyFixture false;
      leaky = toyFixture true;
    in
    pkgs.runCommand "seam-fixtures-store-path-scan-detects-leak" { } ''
      ${scan}/bin/seam-fixtures-store-path-scan ${clean} ${toyClosure clean}/store-paths
      if ${scan}/bin/seam-fixtures-store-path-scan ${leaky} ${toyClosure leaky}/store-paths 2>err; then
        echo "scan passed a fixture naming an uncarried store path" >&2
        exit 1
      fi
      grep -qF '${builtins.unsafeDiscardStringContext "${pkgs.hello}"}' err
      touch $out
    '';
}
