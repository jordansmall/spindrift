# Eval-level pins for lib/research-verdicts.nix (issue #2201): the build-time
# half of the configurable research verdict set. One assertion per behavior —
# default parsing, the default-set render going through the same rendering
# machinery as a custom set (issue #2525), custom-set contract rendering, and
# the marker-absent safety guard — ahead of nix/checks/prompts.nix's
# integration coverage of the same rendering through a built harness prompt dir.
{ pkgs, ... }:
let
  rv = import ../../lib/research-verdicts.nix;
  inherit (pkgs.lib) assertMsg hasInfix;

  template = builtins.readFile ../../templates/default/prompts/research-prompt.md;
  templateSelfContained = builtins.readFile ../../templates/default/prompts/research-self-contained-prompt.md;
  customJSON = builtins.toJSON [
    {
      verdict = "approve";
      label = "agent-research-approve";
      description = "ship it.";
    }
    {
      verdict = "skip";
      label = "agent-research-skip";
      description = "drop it.";
    }
  ];
  customRendered = rv.render customJSON template;
  defaultRendered = rv.render "" template;
in
{
  # The empty knob yields the built-in three, in order.
  research-verdicts-parse-empty-is-default =
    let
      out = rv.parse "";
    in
    assert assertMsg (out == rv.defaultVerdicts) "parse \"\" must return defaultVerdicts";
    assert assertMsg (
      map (v: v.verdict) out == [
        "recommend"
        "reject"
        "unclear"
      ]
    ) "defaultVerdicts must be recommend/reject/unclear in order";
    pkgs.runCommand "research-verdicts-parse-empty-is-default" { } "touch $out";

  # A custom JSON array parses order-preserving into the same shape.
  research-verdicts-parse-custom =
    let
      out = rv.parse customJSON;
    in
    assert assertMsg (
      map (v: v.verdict) out == [
        "approve"
        "skip"
      ]
    ) "parse must preserve verdict order from the JSON array";
    assert assertMsg (
      (builtins.elemAt out 0).label == "agent-research-approve"
    ) "parse must carry the mapped label";
    pkgs.runCommand "research-verdicts-parse-custom" { } "touch $out";

  # lib/research-verdicts.nix's own comment documents bulletsMarker/enumMarker
  # as always both present or both absent in the checked-in templates -- this
  # file otherwise only ever reads research-prompt.md raw (the `template`
  # binding above), so a typo'd or removed marker in the self-contained
  # sibling template would ship silently, uncaught at eval time. Pins both
  # markers present in both template files directly, ahead of any rendering.
  research-verdicts-templates-carry-both-markers =
    assert assertMsg (hasInfix rv.bulletsMarker template)
      "research-prompt.md must carry bulletsMarker";
    assert assertMsg (hasInfix rv.enumMarker template)
      "research-prompt.md must carry enumMarker";
    assert assertMsg (hasInfix rv.bulletsMarker templateSelfContained)
      "research-self-contained-prompt.md must carry bulletsMarker";
    assert assertMsg (hasInfix rv.enumMarker templateSelfContained)
      "research-self-contained-prompt.md must carry enumMarker";
    pkgs.runCommand "research-verdicts-templates-carry-both-markers" { } "touch $out";

  # The empty (default) knob renders through the same machinery as a custom
  # set (issue #2525) -- no byte-identical-to-template no-op special case.
  # The rendered output must carry the bullets synthesized from
  # defaultVerdicts (not the raw hand-typed template text), the status
  # alternation, and the backtick enumeration.
  research-verdicts-render-default-renders-registry-content =
    assert assertMsg
      (hasInfix "- `recommend` — relevant, now enriched with real context; promote it." defaultRendered)
      "default render must emit the recommend bullet synthesized from defaultVerdicts";
    assert assertMsg
      (hasInfix "- `reject` — false positive, not worth doing, or a duplicate. Name the duplicate issue by number in your rationale; duplicate is a reason under `reject`, not a separate verdict." defaultRendered)
      "default render must emit the reject bullet synthesized from defaultVerdicts, full description included";
    assert assertMsg
      (hasInfix "- `unclear` — relevance can't be determined without a human's answer." defaultRendered)
      "default render must emit the unclear bullet synthesized from defaultVerdicts";
    assert assertMsg (hasInfix "status=<recommend|reject|unclear>" defaultRendered)
      "default render must emit the status alternation from defaultVerdicts";
    assert assertMsg (hasInfix "`recommend` / `reject` / `unclear`" defaultRendered)
      "default render must emit the backtick enumeration from defaultVerdicts";
    pkgs.runCommand "research-verdicts-render-default-renders-registry-content" { } "touch $out";

  # A custom set rewrites the VERDICT bullets, the enumeration, and the
  # status alternation -- asserted against the real checked-in template,
  # which (issue #2525) carries only the bulletsMarker/enumMarker injection
  # markers, no hand-typed default text, so the negative assertions below
  # confirm the markers were actually consumed rather than merely that
  # default text happens not to appear.
  research-verdicts-render-custom =
    assert assertMsg (hasInfix "- `approve` — ship it." customRendered)
      "custom render must emit the configured verdict bullet";
    assert assertMsg (hasInfix "status=<approve|skip>" customRendered)
      "custom render must rewrite the status alternation";
    assert assertMsg (hasInfix "`approve` / `skip`" customRendered)
      "custom render must rewrite the backtick enumeration";
    assert assertMsg (
      !(hasInfix "status=<recommend|reject|unclear>" customRendered)
    ) "custom render must not leave the default status alternation";
    assert assertMsg (!(hasInfix "\${RESEARCH_STATUS_ENUM}" customRendered))
      "custom render must not leave the RESEARCH_STATUS_ENUM placeholder token unresolved (issue #2504)";
    assert assertMsg (
      !(hasInfix "<!-- RESEARCH_VERDICT_BULLETS -->" customRendered)
    ) "custom render must consume the bulletsMarker, not leave it unrendered";
    assert assertMsg (
      !(hasInfix "<RESEARCH_VERDICT_ENUM>" customRendered)
    ) "custom render must consume the enumMarker, not leave it unrendered";
    pkgs.runCommand "research-verdicts-render-custom" { } "touch $out";

  # Rendering must be a fixpoint: a second render with the same knob is a
  # byte-identical no-op, for both the default and a custom set. Each of
  # bulletsMarker/enumMarker is single-use -- consumed by the first render --
  # so a second pass finds no marker left to act on.
  research-verdicts-render-is-idempotent =
    let
      defaultTwice = rv.render "" defaultRendered;
      customTwice = rv.render customJSON customRendered;
    in
    assert assertMsg (
      defaultTwice == defaultRendered
    ) "re-rendering the default-rendered prompt with the same (empty) knob must be a no-op";
    assert assertMsg (
      customTwice == customRendered
    ) "re-rendering the custom-rendered prompt with the same custom knob must be a no-op";
    pkgs.runCommand "research-verdicts-render-is-idempotent" { } "touch $out";

  # Regression pin (issue #2525 review): renderPrompt must never derive
  # correctness from the template's on-disk bytes matching what
  # `defaultVerdicts` itself renders to. Build a synthetic prompt whose
  # surrounding VERDICT prose is reflowed/reworded relative to any registry
  # description (an 80-col-wrapped paragraph, not the registry's own
  # single-line text) but which still carries both markers, then render it
  # against a custom set. A byte-matching design can rewrite the
  # enumeration/status alternation (which don't depend on the reflowed
  # prose) while silently leaving the old bullet text in place because it no
  # longer matches the search key -- producing a self-contradictory prompt.
  # The marker-based design can't: bulletsMarker/enumMarker are either both
  # present (both get replaced) or both already consumed, so the three
  # rewritten facts can never disagree with each other.
  research-verdicts-render-reflowed-prose-is-not-load-bearing =
    let
      reflowed = ''
        # VERDICT

        Render exactly one of these verdicts:

        - `recommend` — relevant, now enriched with real
          context; promote it, wrapped across an extra line that
          does not match any registry description byte-for-byte.
        - `reject` — false positive, not worth doing, or a
          duplicate. Name the duplicate issue by number in your
          rationale; duplicate is a reason under `reject`, not a
          separate verdict.
        - `unclear` — relevance can't be determined without a
          human's answer.

        <!-- RESEARCH_VERDICT_BULLETS -->

        # POST THE VERDICT

        1. **Verdict** — `<RESEARCH_VERDICT_ENUM>`, plus a one-line rationale.

        status=<''${RESEARCH_STATUS_ENUM}>
      '';
      out = rv.render customJSON reflowed;
    in
    assert assertMsg (hasInfix "- `approve` — ship it." out)
      "reflowed decoy prose must not stop the custom bullet from being inserted";
    assert assertMsg (hasInfix "`approve` / `skip`" out)
      "reflowed decoy prose must not stop the enumeration from being rewritten";
    assert assertMsg (hasInfix "status=<approve|skip>" out)
      "reflowed decoy prose must not stop the status alternation from being rewritten";
    pkgs.runCommand "research-verdicts-render-reflowed-prose-is-not-load-bearing" { } "touch $out";

  # Rendering a prompt that lacks the VERDICT markers (a Consumer research
  # prompt carrying only its own preamble) must not throw — each token
  # rewrite is a no-op when its marker/target text is absent.
  research-verdicts-render-markerless-is-safe =
    let
      preamble = "CONFIGURED-RESEARCH-PROMPT-MARKER\nResearch issue.\n";
      out = rv.render customJSON preamble;
    in
    assert assertMsg (
      out == preamble
    ) "render on a markerless prompt with no default tokens must be a no-op";
    pkgs.runCommand "research-verdicts-render-markerless-is-safe" { } "touch $out";

  # A prompt that carries both markers plus separately hand-typed decoy
  # bullets (the reflowed-prose check above) must still leave that decoy
  # prose byte-for-byte untouched -- only the markers themselves are
  # rewritten, never a span of surrounding text.
  research-verdicts-render-does-not-clobber-surrounding-prose =
    let
      withDecoy = "# VERDICT\n\nSome custom lead-in sentence unrelated to any registry text.\n\n<!-- RESEARCH_VERDICT_BULLETS -->\n\n# POST THE VERDICT\n";
      out = rv.render customJSON withDecoy;
    in
    assert assertMsg (hasInfix "Some custom lead-in sentence unrelated to any registry text." out)
      "render must leave prose surrounding the marker untouched";
    pkgs.runCommand "research-verdicts-render-does-not-clobber-surrounding-prose" { } "touch $out";

  # A custom RESEARCH_VERDICTS array with zero entries must be rejected
  # (mirrors ParseResearchVerdicts's "must contain at least one entry").
  research-verdicts-parse-rejects-empty-array =
    let
      badJSON = builtins.toJSON [ ];
      result = builtins.tryEval (rv.parse badJSON);
    in
    assert assertMsg (!result.success) "parse must throw on an empty verdict array";
    pkgs.runCommand "research-verdicts-parse-rejects-empty-array" { } "touch $out";

  # An entry with an empty verdict token must be rejected.
  research-verdicts-parse-rejects-empty-verdict =
    let
      badJSON = builtins.toJSON [
        {
          verdict = "";
          label = "agent-research-approve";
          description = "ship it.";
        }
      ];
      result = builtins.tryEval (rv.parse badJSON);
    in
    assert assertMsg (!result.success) "parse must throw on an entry with an empty verdict";
    pkgs.runCommand "research-verdicts-parse-rejects-empty-verdict" { } "touch $out";

  # An entry with an empty label must be rejected.
  research-verdicts-parse-rejects-empty-label =
    let
      badJSON = builtins.toJSON [
        {
          verdict = "approve";
          label = "";
          description = "ship it.";
        }
      ];
      result = builtins.tryEval (rv.parse badJSON);
    in
    assert assertMsg (!result.success) "parse must throw on an entry with an empty label";
    pkgs.runCommand "research-verdicts-parse-rejects-empty-label" { } "touch $out";

  # A verdict token containing whitespace must be rejected.
  research-verdicts-parse-rejects-whitespace-token =
    let
      badJSON = builtins.toJSON [
        {
          verdict = "ship it";
          label = "agent-research-approve";
          description = "ship it.";
        }
      ];
      result = builtins.tryEval (rv.parse badJSON);
    in
    assert assertMsg (!result.success) "parse must throw on a verdict token containing whitespace";
    pkgs.runCommand "research-verdicts-parse-rejects-whitespace-token" { } "touch $out";

  # The reserved "blocked" crash/no-verdict escape-hatch token must never be
  # a configurable verdict.
  research-verdicts-parse-rejects-reserved-blocked-token =
    let
      badJSON = builtins.toJSON [
        {
          verdict = "blocked";
          label = "agent-research-blocked";
          description = "reserved.";
        }
      ];
      result = builtins.tryEval (rv.parse badJSON);
    in
    assert assertMsg (!result.success) "parse must throw on the reserved \"blocked\" verdict token";
    pkgs.runCommand "research-verdicts-parse-rejects-reserved-blocked-token" { } "touch $out";

  # Two entries sharing the same verdict token must be rejected.
  research-verdicts-parse-rejects-duplicate-token =
    let
      badJSON = builtins.toJSON [
        {
          verdict = "approve";
          label = "agent-research-approve";
          description = "ship it.";
        }
        {
          verdict = "approve";
          label = "agent-research-approve-again";
          description = "ship it again.";
        }
      ];
      result = builtins.tryEval (rv.parse badJSON);
    in
    assert assertMsg (!result.success) "parse must throw on a duplicate verdict token";
    pkgs.runCommand "research-verdicts-parse-rejects-duplicate-token" { } "touch $out";
}
