# Shared pipeline shape behind every Driver's outcomeExtractFnBody /
# outcomeExtractNearMissFnBody (issue #2977): claude.nix and opencode.nix
# used to hand-write four byte-for-byte-identical pipelines between them,
# differing only in the jq selector each Driver's event stream needs. This
# file is the one place that pipeline shape (and the SPINDRIFT_OUTCOME token
# itself, sourced from prompt-contract.nix's markerChannels registry -- the
# "outcome" row -- instead of a second hardcoded literal) is written down.
#
# Pure builtins only (no `pkgs.lib`), matching prompt-contract.nix's own
# style, so this stays evaluable without a locked nixpkgs.
let
  promptContract = import ../prompt-contract.nix;
  outcomeToken =
    (builtins.head (builtins.filter (r: r.id == "outcome") promptContract.markerChannels)).token;
in
{
  # Renders one Driver's outcomeExtractFnBody ("match") or
  # outcomeExtractNearMissFnBody ("near-miss") shell function body.
  #
  #   jqSelector -- the Driver-specific jq filter (e.g. claude's
  #                 `select(.type == "result") | .result // empty` vs
  #                 opencode's `select(.type == "text") | .part.text //
  #                 empty`) picking the Driver's own result/text event out of
  #                 its stream-json/NDJSON log.
  #   variant    -- "match": requires both landing= and status= (outcome.Parse's
  #                 own two required fields) and normalizes a colon delimiter
  #                 back to the canonical space (issue #2012) before anything
  #                 downstream sees the line.
  #               -- "near-miss": requires the leading SPINDRIFT_OUTCOME token
  #                 but NOT both landing=/status=, and deliberately does NOT
  #                 normalize the colon delimiter -- the recovery nudge quotes
  #                 this line back to the agent verbatim, so it must read as
  #                 the agent actually typed it (issue #1900).
  #
  # Both variants strip markdown wrapping (backticks/bold, issue #1611) per
  # line before the token anchor is tested, so the launcher's grep and
  # outcome.Parse both see the line bare.
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
