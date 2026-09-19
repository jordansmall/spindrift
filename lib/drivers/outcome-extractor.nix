# Every Driver's outcomeExtractFnBody and outcomeExtractNearMissFnBody share
# this one pipeline (issue #2977); a Driver supplies only the jq selector its
# own event stream needs. The SPINDRIFT_OUTCOME token comes from
# prompt-contract.nix's markerChannels registry, not a second hardcoded
# literal.

# Pure builtins only, so this stays evaluable without a locked nixpkgs.
let
  promptContract = import ../prompt-contract.nix;
  outcomeToken =
    (builtins.head (builtins.filter (r: r.id == "outcome") promptContract.markerChannels)).token;
in
{
  # Renders one Driver's outcomeExtractFnBody ("match") or
  # outcomeExtractNearMissFnBody ("near-miss") shell function body. "match"
  # requires both landing= and status=, the two fields outcome.Parse needs, and
  # normalizes a colon delimiter back to the canonical space (issue #2012).

  # "near-miss" requires only the token and leaves the colon alone, because the
  # recovery nudge quotes that line back to the agent verbatim (issue #1900).

  # Both variants strip markdown wrapping per line before testing the token
  # anchor, so the launcher's grep and outcome.Parse see the line bare
  # (issue #1611).
  mkOutcomeExtractor =
    { jqSelector, variant }:
    if variant == "match" then
      ''
        # The backtick below is a literal char in a single-quoted sed script, not
        # an unexpanded command substitution.
        # shellcheck disable=SC2016
        jq -r '${jqSelector}' "$1" 2>/dev/null \
          | sed -E 's/^[[:space:]]*(\*\*|`)?//; s/(\*\*|`)?[[:space:]]*$//' \
          | grep -E '^${outcomeToken}[: ]' \
          | sed -E 's/^${outcomeToken}:[[:space:]]*/${outcomeToken} /' \
          | grep -E '(^| )landing=' \
          | grep -E '(^| )status=' \
          | tail -1 || true
      ''
    else if variant == "near-miss" then
      ''
        # The backtick below is a literal char in a single-quoted sed script, not
        # an unexpanded command substitution.
        # shellcheck disable=SC2016
        jq -r '${jqSelector}' "$1" 2>/dev/null \
          | sed -E 's/^[[:space:]]*(\*\*|`)?//; s/(\*\*|`)?[[:space:]]*$//' \
          | grep -E '^${outcomeToken}[: ]' \
          | grep -vE '(^| )landing=.*(^| )status=|(^| )status=.*(^| )landing=' \
          | tail -1 || true
      ''
    else
      throw "outcome-extractor.mkOutcomeExtractor: unknown variant '${variant}' -- expected \"match\" or \"near-miss\"";
}
