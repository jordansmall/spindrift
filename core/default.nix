# Plain-Nix core entry point (ADR 0073): the root batteries flake plain-imports
# this and core/flake.nix wraps it. Reads ../lib in place.
{
  lib.mkHarness = import ../lib/mkHarness.nix;

  # The SDK's Go source (ADR 0073): a compiled Connector builds against it
  # through `core`, never a relative path out of its own directory.
  lib.sdkSrc = ../sdk;

  # Roster helpers (issue #2560). normalizeRosterResult stays internal
  # test-support machinery, so it is deliberately not exported here.
  lib.rosterLib =
    args:
    let
      roster = import ../lib/roster.nix args;
    in
    {
      inherit (roster) normalizeRoster dropOptedOut defaultRoster;
    };

  # The flake-parts shim, for Consumer flakes that want the declarative
  # options (ADR 0001).
  flakeModules.default = ../lib/flakeModule.nix;
}
