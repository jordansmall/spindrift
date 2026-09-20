# Drift parity between the code-review fallback fragment and the upstream
# `/code-review` skill (issue #3222, same pattern as #3219). See
# nix/checks/mk-fragment-parity.nix for the full rationale; this file
# repeats only what differs.
{ pkgs, mkFragmentParity, ... }:
let
  inherit (pkgs.lib) assertMsg concatStringsSep;

  rosterLib = import ../../lib/roster.nix { inherit (pkgs) lib; };

  skillDesc = "the upstream code-review SKILL.md (pinned `matt-skills` flake input, read via nix/dogfood-skills.nix)";
  fallbackDesc = "templates/default/prompts/review-prompt.md";
  anchorDesc = "templates/default/prompts/fragments/code-review-baked.md";
  # Issue #3226 moved the four hunt dimensions into review-prompt.md as
  # unconditional inline text, since they must render whether or not the
  # skill is baked. The drift target follows the prose there.
  fallbackText = builtins.readFile ../../templates/default/prompts/review-prompt.md;
  anchorText = builtins.readFile ../../templates/default/prompts/fragments/code-review-baked.md;

  # Only two clauses. The skill's Standards/Spec two-axis model and
  # review-prompt.md's four hunt dimensions are different shapes by design:
  # CORRECTNESS and SECURITY have no counterpart in the skill, so most of
  # review-prompt.md's prose shares no vocabulary to pin against. These two
  # name the same finding class in both texts, not just a shared topic word.
  sharedClauses = [
    {
      name = "scope-creep";
      # Both texts use this exact phrase for the same finding. Losing it
      # from either side means SPEC stops flagging unrequested changes as
      # its own named category.
      clause = "scope creep";
    }
    {
      name = "code-smells";
      # Both texts name the discipline "code smells" verbatim. Losing it
      # from either side means the Standards axis stops hunting smells as
      # distinct from documented-standard violations.
      clause = "code smells";
    }
  ];

  # Phrases that belong only to review-prompt.md's always-inline dimension
  # prose, never to the gated baked arm.
  stepProseMarkers = [
    "hunt every dimension"
    "scope creep"
    "code smells"
    "standards & smells"
  ];

  parity = mkFragmentParity {
    skillName = "code-review";
    sourceFile = "nix/checks/code-review-fragment-parity.nix";
    inherit
      skillDesc
      fallbackText
      fallbackDesc
      anchorText
      anchorDesc
      sharedClauses
      stepProseMarkers
      ;
    proseKind = "dimension prose";
    carriedKind = "discipline";
    discipline = "review discipline";
  };

  anchorLine = if parity.anchorLines == [ ] then "" else builtins.head parity.anchorLines;
  # Anchored on the "spawning ... as agent type `x`" phrase, not on "agent
  # type" alone: a bare match reads the last such clause on the line, so an
  # appended negation ("never agent type `general-purpose`") would fail the
  # check and "do not use agent type `review-axis`" alone would pass it.
  anchorAgentTypeMatch = builtins.match ".*spawning[^`]*as agent type `([^`]+)`.*" anchorLine;
  anchorAgentType = if anchorAgentTypeMatch == null then null else builtins.head anchorAgentTypeMatch;
  substVar = "REVIEW_FANOUT_AGENT";
  anchorPlaceholder = "\${" + substVar + "}";
  fragmentRows = import ../../lib/fragments.nix;
  codeReviewBakedRows = builtins.filter (r: r.fragment == "code-review-baked.md") fragmentRows;
  codeReviewBakedExtras = builtins.concatMap (r: r.extraSubstVars or [ ]) codeReviewBakedRows;
  assembleGoLines = builtins.filter builtins.isString (
    builtins.split "\n" (builtins.readFile ../../cmd/launcher/internal/promptassembly/assemble.go)
  );
  goAgentMatches = builtins.filter (m: m != null) (
    map (l: builtins.match "const reviewFanoutAgent = \"([a-zA-Z0-9_-]+)\"" l) assembleGoLines
  );
  goAgentType = if goAgentMatches == [ ] then null else builtins.head (builtins.head goAgentMatches);
  rosterNames = map (e: e.name) (rosterLib.defaultRoster { });
in
parity.checks
// {
  # Issue #3447: pins the substitution seam end to end, so an edit that
  # drops or misspells any link in it cannot silently regress to the skill's
  # ungoverned `general-purpose` default.
  code-review-fragment-parity-baked-anchor-agent-type-is-rostered =
    assert assertMsg (anchorAgentType != null)
      "templates/default/prompts/fragments/code-review-baked.md names no agent type (expected a `spawning ... as agent type \\`<name>\\`` clause) -- without an explicit override the /code-review skill's fan-out silently falls back to its own `general-purpose` default, which defaultRoster does not govern.";
    assert assertMsg (anchorAgentType == anchorPlaceholder)
      "templates/default/prompts/fragments/code-review-baked.md spawns the axis subagents as agent type \"${anchorAgentType}\", want the \"${anchorPlaceholder}\" placeholder -- a baked literal orders an agent type a Consumer roster that omits the entry never provisions, and the Agent call then falls back to the ungoverned `general-purpose` default.";
    assert assertMsg (builtins.elem substVar codeReviewBakedExtras)
      "lib/fragments.nix's code-review-baked.md row does not declare extraSubstVars = [ \"${substVar}\" ] -- without it the placeholder is not on the substitution allowlist and the anchor renders a literal \"${anchorPlaceholder}\" into the reviewer's prompt.";
    assert assertMsg (goAgentType != null)
      "cmd/launcher/internal/promptassembly/assemble.go declares no `const reviewFanoutAgent = \"<name>\"` -- that constant is what resolves ${anchorPlaceholder} in-box, so with it gone the anchor has no governed agent type to name.";
    assert assertMsg (builtins.elem goAgentType rosterNames)
      "cmd/launcher/internal/promptassembly/assemble.go's reviewFanoutAgent is \"${goAgentType}\", which is not among defaultRoster's names (${concatStringsSep ", " rosterNames}) -- the fan-out's agent type must name a roster entry, not an unrostered agent type.";
    pkgs.runCommand "code-review-fragment-parity-baked-anchor-agent-type-is-rostered" { } "touch $out";
}
