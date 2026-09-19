# The one root for the gh-token-refresher timing constants (issue #2893).
# .github/actions/gh-token-refresher/action.yml hand-writes them as bare
# `sleep_secs=<n>` shell literals, so nix/checks/gh-token-intervals.nix pins
# those literals against this registry. Keep it a plain attrset with no
# `{ lib }:` wrapper so it imports with zero arguments.
{
  # The ~1h GitHub App installation-token lifetime caps this (issue #1027);
  # 45m leaves slack for a slow mint without hammering the mint endpoint.
  refreshSeconds = 2700;

  # 5m retries a transient mint failure without waiting out a full
  # refreshSeconds cycle, and stays well inside that same ~1h lifetime.
  failureBackoffSeconds = 300;
}
