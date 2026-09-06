# The one root for the gh-token-refresher timing constants (issue #2893):
# .github/actions/gh-token-refresher/action.yml's background loop hand-writes
# these as bare `sleep_secs=<n>` shell literals with no other guard tying
# them back to anything, so nix/checks/gh-token-intervals.nix can pin the
# action.yml literals against this registry instead of trusting the shell
# script to never drift from the doc comments beside it.
#
# A plain attrset (no `{ lib }:` wrapper), mirroring lib/labels.nix and
# lib/research-verdicts.nix, so it stays importable with zero arguments.
{
  # 45m: comfortably inside the ~1h GitHub App installation-token lifetime
  # the refresher loop exists to stay ahead of (issue #1027) — long enough
  # to avoid hammering the mint endpoint, short enough to leave slack if a
  # mint attempt is slow.
  refreshSeconds = 2700;

  # 5m: retries a transient mint failure (network blip, transient API error)
  # without waiting out the full refreshSeconds cycle — the gap between a
  # failure and the next attempt must stay well inside the same ~1h token
  # lifetime it's protecting.
  failureBackoffSeconds = 300;
}
