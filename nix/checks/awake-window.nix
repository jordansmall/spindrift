# Eval-level pins for lib/awake-window.nix (issue #3542), mirroring
# cmd/launcher/internal/daemon/awake.go's ParseWindow. tryEval exposes only
# success/failure, never the thrown message text, so these pins cannot also
# assert the message wording (see nix/checks/jira-status-mapping.nix's same
# caveat).
{ pkgs, ... }:
let
  aw = import ../../lib/awake-window.nix;
  inherit (pkgs.lib) assertMsg;
in
{
  awake-window-parse-empty-is-no-window =
    let
      out = aw.parse "";
    in
    assert assertMsg (out == null) "parse \"\" must return null (no window, always awake)";
    pkgs.runCommand "awake-window-parse-empty-is-no-window" { } "touch $out";

  awake-window-parse-valid-same-day =
    let
      out = aw.parse "06:00-22:00 Europe/London";
    in
    assert assertMsg (
      out == {
        start = 360;
        end = 1320;
        zone = "Europe/London";
      }
    ) "parse must decode a same-day window's start/end/zone";
    pkgs.runCommand "awake-window-parse-valid-same-day" { } "touch $out";

  awake-window-parse-valid-wraparound =
    let
      out = aw.parse "22:00-06:00 Europe/London";
    in
    assert assertMsg (
      out == {
        start = 1320;
        end = 360;
        zone = "Europe/London";
      }
    ) "parse must decode a wraparound (end < start) window's start/end/zone";
    pkgs.runCommand "awake-window-parse-valid-wraparound" { } "touch $out";

  awake-window-parse-rejects-malformed-span =
    let
      result = builtins.tryEval (aw.parse "22:00 06:00 Europe/London");
    in
    assert assertMsg (!result.success) "parse must throw on a span missing the HH:MM-HH:MM shape";
    pkgs.runCommand "awake-window-parse-rejects-malformed-span" { } "touch $out";

  awake-window-parse-rejects-hour-out-of-range =
    let
      result = builtins.tryEval (aw.parse "24:00-06:00 Europe/London");
    in
    assert assertMsg (!result.success) "parse must throw on an hour > 23";
    pkgs.runCommand "awake-window-parse-rejects-hour-out-of-range" { } "touch $out";

  awake-window-parse-rejects-minute-out-of-range =
    let
      result = builtins.tryEval (aw.parse "22:60-06:00 Europe/London");
    in
    assert assertMsg (!result.success) "parse must throw on a minute > 59";
    pkgs.runCommand "awake-window-parse-rejects-minute-out-of-range" { } "touch $out";

  awake-window-parse-rejects-equal-start-end =
    let
      result = builtins.tryEval (aw.parse "22:00-22:00 Europe/London");
    in
    assert assertMsg (!result.success) "parse must throw when start == end";
    pkgs.runCommand "awake-window-parse-rejects-equal-start-end" { } "touch $out";

  awake-window-parse-rejects-local-zone =
    let
      result = builtins.tryEval (aw.parse "22:00-06:00 Local");
    in
    assert assertMsg (!result.success) "parse must throw on the literal zone \"Local\"";
    pkgs.runCommand "awake-window-parse-rejects-local-zone" { } "touch $out";

  # One pin per whitespace byte the zone sub-pattern's [:space:] class
  # rejects beyond a literal space, since a regression to [^ ] would pass
  # every one of them through to the daemon.
  awake-window-parse-rejects-tab-in-zone =
    let
      result = builtins.tryEval (aw.parse "22:00-06:00 Europe/Lon\tdon");
    in
    assert assertMsg (!result.success) "parse must throw on a tab embedded in the zone token";
    pkgs.runCommand "awake-window-parse-rejects-tab-in-zone" { } "touch $out";

  awake-window-parse-rejects-newline-in-zone =
    let
      result = builtins.tryEval (aw.parse "22:00-06:00 Europe/Lon\ndon");
    in
    assert assertMsg (!result.success) "parse must throw on a newline embedded in the zone token";
    pkgs.runCommand "awake-window-parse-rejects-newline-in-zone" { } "touch $out";

  awake-window-parse-rejects-carriage-return-in-zone =
    let
      result = builtins.tryEval (aw.parse "22:00-06:00 Europe/Lon\rdon");
    in
    assert assertMsg (!result.success) "parse must throw on a carriage return embedded in the zone token";
    pkgs.runCommand "awake-window-parse-rejects-carriage-return-in-zone" { } "touch $out";
}
