# Pins every hand-written spelling of the gh-token-refresher intervals
# against lib/gh-token-intervals.nix (issue #2893): the `sleep_secs=<n>`
# shell literals in .github/actions/gh-token-refresher/action.yml, and any
# Go-side declaration of the same two intervals under cmd/launcher.
{ pkgs, ... }:
let
  inherit (pkgs.lib)
    assertMsg
    concatStringsSep
    mod
    replaceStrings
    splitString
    ;
  intervals = import ../../lib/gh-token-intervals.nix;
  actionSrc = builtins.readFile ../../.github/actions/gh-token-refresher/action.yml;

  # The Go sites spell their durations as `N * time.Minute`, so the registry's
  # seconds must survive the conversion exactly. Integer division alone would
  # floor refreshSeconds = 2730 to 45, pinning action.yml at 2730s while the Go
  # guard still accepted 2700s, a 30-second disagreement passing this check.
  wholeMinutes =
    attr: seconds:
    assert assertMsg (mod seconds 60 == 0)
      "lib/gh-token-intervals.nix's ${attr} = ${toString seconds} is not a whole number of minutes, so it cannot be pinned against the Go-side `N * time.Minute` spelling; pick a multiple of 60";
    seconds / 60;

  refreshMinutes = toString (wholeMinutes "refreshSeconds" intervals.refreshSeconds);
  backoffMinutes = toString (wholeMinutes "failureBackoffSeconds" intervals.failureBackoffSeconds);

  # Extracts every `sleep_secs=<digits>` literal, in file order. Span-scanned
  # off the marker rather than per line, because the loop's three sites sit on
  # three different lines. It reads only the digit run right after the `=`:
  # scanning further would pick digits out of a trailing comment. A site whose
  # value is not a bare literal yields a sentinel that fails the comparison.
  extractSleepSecs =
    src:
    let
      literalAfterMarker =
        segment:
        let
          m = builtins.match "([0-9]+).*" (builtins.head (splitString "\n" segment));
        in
        if m == null then "<non-literal>" else builtins.head m;
    in
    map literalAfterMarker (builtins.tail (splitString "sleep_secs=" src));

  # Compares the ordered, counted list, not merely that each value appears
  # somewhere, which would miss one of the two refreshSeconds sites drifting
  # alone. Factored out so the -regression siblings below can exercise this
  # path against doctored source without touching the real files.
  assertSleepSecsPinned =
    src:
    let
      expected = [
        (toString intervals.refreshSeconds)
        (toString intervals.refreshSeconds)
        (toString intervals.failureBackoffSeconds)
      ];
      found = extractSleepSecs src;
    in
    assert assertMsg (found == expected)
      "gh-token-refresher/action.yml's sleep_secs=<n> literals drifted from lib/gh-token-intervals.nix: found [${concatStringsSep ", " found}], want [${concatStringsSep ", " expected}] (refreshSeconds/refreshSeconds/failureBackoffSeconds, in file order). If you legitimately added or removed a sleep_secs= site, update the expected list in nix/checks/gh-token-intervals.nix to match the new file order";
    found;

  wantEnv = {
    WANT_REFRESH_MINUTES = refreshMinutes;
    WANT_BACKOFF_MINUTES = backoffMinutes;
  };

  # Scans at build time, not eval time: an eval-time walk of the launcher tree
  # taxes every flake eval for a guard that matches nothing on main today
  # (issue #2893). The grep takes any assignment (`=[^=]` excludes `==`), then
  # checks the `N * time.Minute` form, so an unexpected spelling fails instead
  # of quietly passing. It skips _test.go, where the tests override the var.
  scanScript = pkgs.writeShellScript "gh-token-intervals-go-guard-scan" ''
    set -euo pipefail

    dir="$1"
    status=0

    check_symbol() {
      local symbol="$1" want="$2" assignments m file lineno line found
      assignments=$(grep -rnE "\b$symbol\b[[:space:]]*:?=[^=]" "$dir" --include='*.go' --exclude='*_test.go' || true)
      if [ -z "$assignments" ]; then
        echo "gh-token-intervals-go-guard: $symbol not declared anywhere under $dir — vacuous pass (the Go-side constants land with issue #2867)"
        return 0
      fi
      while IFS= read -r m; do
        file=$(printf '%s\n' "$m" | cut -d: -f1)
        lineno=$(printf '%s\n' "$m" | cut -d: -f2)
        line=$(printf '%s\n' "$m" | cut -d: -f3-)
        if ! printf '%s\n' "$line" | grep -qE "\b$symbol\b[[:space:]]*=[[:space:]]*[0-9]+[[:space:]]*\*[[:space:]]*time\.Minute"; then
          echo "gh-token-intervals-go-guard: $file:$lineno assigns $symbol in a form this guard cannot compare against lib/gh-token-intervals.nix (want \`$symbol = N * time.Minute\`), found: $line" >&2
          status=1
          continue
        fi
        found=$(printf '%s\n' "$line" | grep -oE "=[[:space:]]*[0-9]+[[:space:]]*\*[[:space:]]*time\.Minute" | grep -oE '[0-9]+')
        if [ "$found" != "$want" ]; then
          echo "gh-token-intervals-go-guard: $file:$lineno declares $symbol = $found * time.Minute, want $want * time.Minute (lib/gh-token-intervals.nix)" >&2
          status=1
        fi
      done <<< "$assignments"
    }

    check_symbol ghAppRefreshInterval "$WANT_REFRESH_MINUTES"
    check_symbol remintFailureBackoff "$WANT_BACKOFF_MINUTES"

    exit "$status"
  '';
in
{
  # builtins.seq forces assertSleepSecsPinned's own assert, so the expected
  # [refresh refresh backoff] list is not re-spelled a second time here.
  gh-token-intervals-pinned-in-action = builtins.seq (assertSleepSecsPinned actionSrc) (
    pkgs.runCommand "gh-token-intervals-pinned-in-action" { } "touch $out"
  );

  # Each fixture targets a way this check could pass vacuously: drift in only
  # the second sleep_secs=refreshSeconds site, which an exists-anywhere
  # comparison would miss, and a site whose value stops being a bare literal.
  # Anchors derive from `intervals` so a legitimate registry bump does not make
  # these fixtures fail for an unrelated reason.
  gh-token-intervals-pinned-in-action-regression =
    let
      resetDriftedSrc =
        replaceStrings
          [ "sleep_secs=${toString intervals.refreshSeconds}\n            else" ]
          [ "sleep_secs=${toString (intervals.refreshSeconds + 1)}\n            else" ]
          actionSrc;
      nonLiteralSrc =
        replaceStrings
          [ "sleep_secs=${toString intervals.failureBackoffSeconds}" ]
          [ ''sleep_secs="$backoff" # ${toString intervals.failureBackoffSeconds}'' ]
          actionSrc;
      fixtures = [
        {
          name = "the success-path sleep_secs=${toString intervals.refreshSeconds} reset drifted to ${
            toString (intervals.refreshSeconds + 1)
          }";
          doctoredSrc = resetDriftedSrc;
          anchor = "the success-path reset line";
        }
        {
          name = "the failure-path sleep_secs literal replaced by a shell variable with the old value left in a trailing comment";
          doctoredSrc = nonLiteralSrc;
          anchor = "the failure-path backoff line";
        }
      ];
      # Guard the fixtures themselves: if action.yml's surrounding text reflows
      # so a replaceStrings match stops firing, the doctored source would equal
      # actionSrc and the tryEval below would pass against undoctored input.
      check =
        f:
        assert assertMsg (f.doctoredSrc != actionSrc)
          "gh-token-intervals-pinned-in-action-regression: a replaceStrings fixture found no match in action.yml — update its anchor text to match ${f.anchor}";
        assert assertMsg (!(builtins.tryEval (assertSleepSecsPinned f.doctoredSrc)).success)
          "gh-token-intervals-pinned-in-action-regression: expected assertSleepSecsPinned to reject a synthetic action.yml with ${f.name}, but it evaluated successfully";
        true;
    in
    assert builtins.all check fixtures;
    pkgs.runCommand "gh-token-intervals-pinned-in-action-regression" { } "touch $out";

  # Proves wholeMinutes rejects a registry value that is not a whole number of
  # minutes instead of flooring it, which is the only thing keeping the
  # seconds-based action.yml pin and the minutes-based Go pin in agreement.
  gh-token-intervals-whole-minutes-regression =
    let
      truncating = builtins.tryEval (wholeMinutes "refreshSeconds" 2730);
    in
    assert assertMsg (!truncating.success)
      "gh-token-intervals-whole-minutes-regression: expected wholeMinutes to reject 2730 seconds (45.5 minutes), but it evaluated to ${toString truncating.value}";
    assert assertMsg (wholeMinutes "refreshSeconds" 2700 == 45)
      "gh-token-intervals-whole-minutes-regression: expected wholeMinutes to convert 2700 seconds to 45 minutes";
    pkgs.runCommand "gh-token-intervals-whole-minutes-regression" { } "touch $out";

  # The real-tree run passes today because neither symbol exists on main. It
  # starts enforcing the moment either constant lands with issue #2867.
  gh-token-intervals-go-guard = pkgs.runCommand "gh-token-intervals-go-guard" wantEnv ''
    ${scanScript} ${../../cmd/launcher}
    touch $out
  '';

  # Exercises scanScript against synthetic fixtures, because the real tree
  # matches neither symbol today. The unrelated fixture pins that bootstrap.go's
  # ghTokenRefreshInterval, a different concept, never counts as a match.
  gh-token-intervals-go-guard-regression =
    pkgs.runCommand "gh-token-intervals-go-guard-regression" wantEnv
      ''
        mkdir -p drifted unrecognised shortdecl correct unrelated

        cat > drifted/ghapptoken.go <<'EOF'
        package ghapptoken

        import "time"

        const ghAppRefreshInterval = 46 * time.Minute

        var remintFailureBackoff = 6 * time.Minute
        EOF

        cat > unrecognised/ghapptoken.go <<'EOF'
        package ghapptoken

        import "time"

        const ghAppRefreshInterval = 2700 * time.Second
        EOF

        cat > shortdecl/ghapptoken.go <<'EOF'
        package ghapptoken

        import "time"

        func newBackoff() time.Duration {
            remintFailureBackoff := 5 * time.Minute
            return remintFailureBackoff
        }
        EOF

        cat > correct/bootstrap.go <<'EOF'
        package bootstrap

        import "time"

        const ghAppRefreshInterval = 45 * time.Minute
        EOF

        cat > correct/ghapptoken.go <<'EOF'
        package ghapptoken

        import "time"

        var (
            remintFailureBackoff = 5 * time.Minute
        )
        EOF

        cat > correct/ghapptoken_test.go <<'EOF'
        package ghapptoken

        import "time"

        func init() { remintFailureBackoff = 1 * time.Minute }
        EOF

        cat > unrelated/bootstrap.go <<'EOF'
        package bootstrap

        import "time"

        const ghTokenRefreshInterval = 60 * time.Second
        EOF

        if ${scanScript} drifted; then
          echo "gh-token-intervals-go-guard-regression: expected the scan to reject the drifted fixture (ghAppRefreshInterval=46m, remintFailureBackoff=6m), but it exited 0" >&2
          exit 1
        fi

        if ${scanScript} unrecognised; then
          echo "gh-token-intervals-go-guard-regression: expected the scan to reject the unrecognised-form fixture (ghAppRefreshInterval = 2700 * time.Second), but it exited 0" >&2
          exit 1
        fi

        if ${scanScript} shortdecl; then
          echo "gh-token-intervals-go-guard-regression: expected the scan to reject the short-declaration fixture (remintFailureBackoff := 5 * time.Minute), but it exited 0" >&2
          exit 1
        fi

        ${scanScript} correct

        unrelated_out=$(${scanScript} unrelated)
        for symbol in ghAppRefreshInterval remintFailureBackoff; do
          if ! grep -q "$symbol not declared anywhere under unrelated" <<< "$unrelated_out"; then
            echo "gh-token-intervals-go-guard-regression: expected $symbol to be reported not-declared over the unrelated fixture (its ghTokenRefreshInterval must never register as a false 'found'), but scan output was: $unrelated_out" >&2
            exit 1
          fi
        done

        touch $out
      '';
}
