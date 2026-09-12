# Drift parity between the code-review fallback fragment and the upstream
# `/code-review` skill (issue #3222, following the tdd pattern from #3219).
# See nix/checks/tdd-fragment-parity.nix for the full rationale comment; this
# file repeats only what differs.
{ pkgs, fixtures, ... }:
let
  inherit (pkgs.lib)
    assertMsg
    concatStringsSep
    hasInfix
    toLower
    ;

  rosterLib = import ../../lib/roster.nix { inherit (pkgs) lib; };

  skillRowByName =
    name:
    let
      matches = builtins.filter (r: r.name == name) fixtures.dogfoodSkills;
    in
    if matches == [ ] then
      throw "nix/checks/code-review-fragment-parity.nix: no dogfood skill named \"${name}\" (nix/dogfood-skills.nix row may have been renamed or dropped)"
    else
      builtins.head matches;

  normalize =
    text:
    let
      words = builtins.filter (w: builtins.isString w && w != "") (builtins.split "[[:space:]]+" text);
    in
    toLower (concatStringsSep " " words);

  skillText = normalize (skillRowByName "code-review").src;
  # Issue #3226 moved the four hunt dimensions out of the gated fallback
  # fragment and into review-prompt.md as unconditional inline text (they
  # must render whether or not the skill is baked), so the drift target
  # follows the prose to where it now lives.
  fallbackText = normalize (builtins.readFile ../../templates/default/prompts/review-prompt.md);
  anchorText = builtins.readFile ../../templates/default/prompts/fragments/code-review-baked.md;

  skillDesc = "the upstream code-review SKILL.md (pinned `matt-skills` flake input, read via nix/dogfood-skills.nix)";
  fallbackDesc = "templates/default/prompts/review-prompt.md";
  remedy = "either re-sync the fallback with the skill, or -- if the skill's discipline genuinely changed -- update this check's clause list to match.";

  # Kept to two clauses. The skill's Standards/Spec two-axis model and
  # review-prompt.md's four hunt dimensions (SPEC/CORRECTNESS/SECURITY/
  # STANDARDS & SMELLS) are genuinely different shapes -- spindrift's
  # CORRECTNESS and SECURITY dimensions have no counterpart in the skill at
  # all, by design (the skill only aggregates two sub-agent reports;
  # spindrift's single reviewer hunts more ground inline) -- so most of
  # review-prompt.md's prose has no shared vocabulary to pin against. These
  # two survive because they name the same finding class in both texts, not
  # just a shared topic word.
  sharedClauses = [
    {
      name = "scope-creep";
      # The Spec axis's own term for unrequested behaviour: the skill's Spec
      # sub-agent brief and the fallback's SPEC dimension both use this exact
      # phrase for the same finding. Losing it from either side means SPEC
      # would stop flagging unrequested changes as its own named category.
      clause = "scope creep";
    }
    {
      name = "code-smells";
      # The skill's Fowler smell baseline and the fallback's STANDARDS &
      # SMELLS dimension both name the discipline "code smells" verbatim.
      # Losing it from either side would mean the Standards axis stops
      # hunting smells as a distinct thing from documented-standard
      # violations.
      clause = "code smells";
    }
  ];

  clauseCheck = c: {
    name = "code-review-fragment-parity-clause-${c.name}";
    value =
      let
        needle = normalize c.clause;
      in
      assert assertMsg (hasInfix needle skillText)
        "code-review fallback drift: ${skillDesc} no longer states \"${c.clause}\", which ${fallbackDesc} restates -- ${remedy}";
      assert assertMsg (hasInfix needle fallbackText)
        "code-review fallback drift: ${fallbackDesc} no longer states \"${c.clause}\", which ${skillDesc} teaches -- ${remedy}";
      pkgs.runCommand "code-review-fragment-parity-clause-${c.name}" { } "touch $out";
  };

  # Phrases that belong only to review-prompt.md's always-inline dimension
  # prose, never to the gated baked arm.
  stepProseMarkers = [
    "hunt every dimension"
    "scope creep"
    "code smells"
    "standards & smells"
  ];
  normalizedAnchor = normalize anchorText;
  leakedMarkers = builtins.filter (m: hasInfix m normalizedAnchor) stepProseMarkers;
  anchorLines = builtins.filter (l: builtins.isString l && normalize l != "") (
    builtins.split "\n" anchorText
  );

  # Issue #3447: the upstream `/code-review` skill's own fan-out step
  # defaults both axis subagents to `general-purpose`, an unrostered,
  # ungoverned agent type -- spindrift's only lever over that default is
  # this baked anchor line. The anchor can't name the roster entry
  # literally, though: a Consumer roster that omits it (or the #392
  # `model = ""` opt-out) provisions no such agent, so the name is
  # substituted in-box from the run's own provisioned agents. So pin the
  # whole seam instead, still by extraction rather than by asserting a
  # literal appears somewhere on the line (which would keep passing even if
  # the line drifted back to also naming `general-purpose`): the anchor's
  # spawn clause must name the placeholder, the registry row must declare
  # that substitution variable, and the Go resolver's own constant must name
  # a roster entry.
  anchorLine = if anchorLines == [ ] then "" else builtins.head anchorLines;
  # Anchored on the "spawning ... as agent type `x`" phrase, not on "agent
  # type" alone: a bare match reads the last such clause on the line, so a
  # negated sentence appended after the override ("never agent type
  # `general-purpose`") would fail the check and "do not use agent type
  # `review-axis`" alone would pass it.
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
builtins.listToAttrs (map clauseCheck sharedClauses)
// {
  code-review-fragment-parity-baked-anchor-omits-step-prose =
    assert assertMsg (leakedMarkers == [ ])
      "templates/default/prompts/fragments/code-review-baked.md restates the unbaked arm's dimension prose (${concatStringsSep ", " leakedMarkers}) -- the baked arm must name the `/code-review` skill and stop, since the skill itself carries that discipline in-box; move any wording worth keeping into ${fallbackDesc}.";
    assert assertMsg (builtins.length anchorLines == 1)
      "templates/default/prompts/fragments/code-review-baked.md is ${toString (builtins.length anchorLines)} non-empty lines, want 1 -- the baked arm is an anchor line, not a paragraph; prose belongs in ${fallbackDesc}.";
    assert assertMsg (hasInfix "/code-review" anchorText)
      "templates/default/prompts/fragments/code-review-baked.md no longer names the `/code-review` skill -- the baked arm's entire job is to point at the baked skill, so with the name gone the prompt says nothing about review discipline at all.";
    pkgs.runCommand "code-review-fragment-parity-baked-anchor-omits-step-prose" { } "touch $out";

  # Issue #3447: the upstream skill's `general-purpose` fan-out default is
  # unrostered and ungoverned -- this pins the substitution seam that carries
  # the override end to end, so a future edit that drops or misspells any
  # link in it can't silently regress to that default.
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
