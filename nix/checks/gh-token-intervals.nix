# Pins the hand-written `sleep_secs=<n>` shell literals in
# .github/actions/gh-token-refresher/action.yml against
# lib/gh-token-intervals.nix (issue #2893).
{ pkgs, ... }:
let
  inherit (pkgs.lib)
    assertMsg
    concatStringsSep
    replaceStrings
    splitString
    ;
  intervals = import ../../lib/gh-token-intervals.nix;
  actionSrc = builtins.readFile ../../.github/actions/gh-token-refresher/action.yml;

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
  # alone. Factored out so the -regression check below can exercise this
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
}
