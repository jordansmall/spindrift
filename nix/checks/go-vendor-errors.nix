# Issue #7284: the narrowed launcher vendor derivation runs `go mod vendor -e`,
# which exits 0 even when a module download fails, so go-vendor-errors.sh
# classifies its stderr instead.
{ pkgs, ... }:
let
  script = ./go-vendor-errors.sh;
  fixtures = ./go-vendor-errors-fixtures;
in
{
  go-vendor-errors =
    pkgs.runCommand "go-vendor-errors"
      {
        nativeBuildInputs = [
          pkgs.bash
          pkgs.shellcheck
        ];
      }
      ''
        shellcheck --shell=bash ${script}

        fail=0
        for name in expected expected-chain; do
          if ! bash ${script} ${fixtures}/$name.stderr >/dev/null 2>"$TMPDIR/out"; then
            echo "FAIL: $name.stderr should pass:"; cat "$TMPDIR/out"; fail=1
          fi
        done
        for name in download-failure download-failure-launcher-line mismatched-launcher-package; do
          if bash ${script} ${fixtures}/$name.stderr >/dev/null 2>"$TMPDIR/out"; then
            echo "FAIL: $name.stderr should fail"; fail=1
          elif ! grep -qF 'go mod vendor: unexpected stderr (likely a module download failure):' "$TMPDIR/out"; then
            echo "FAIL: $name.stderr failure lacks the header:"; cat "$TMPDIR/out"; fail=1
          elif [ "$name" != mismatched-launcher-package ] \
            && ! grep -qF 'github.com/tetratelabs/wazero@v1.11.0' "$TMPDIR/out"; then
            echo "FAIL: $name.stderr failure does not name the module:"; cat "$TMPDIR/out"; fail=1
          fi
        done
        [ "$fail" = 0 ]
        touch $out
      '';
}
