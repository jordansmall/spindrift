# Research verdict vocabulary rendering (issue #2201): the build-time half of
# the configurable research verdict set. lib/env-schema.nix's RESEARCH_VERDICTS
# knob carries a JSON array of {verdict,label,description} objects (order
# preserved); the Go launcher parses the same knob at runtime
# (cmd/launcher/internal/forge/verdict.go's ParseResearchVerdicts) to validate
# the posted verdict and apply the mapped label, while this file renders the
# verdict contract into the research prompt so a custom set flows into the
# prompt, not only the launcher (ADR 0022 amendment).
#
# Pure builtins only (no `pkgs.lib`): keeps this file evaluable and unit-
# testable with a bare `nix eval`, without a locked nixpkgs (mirrors
# lib/prompt-inject.nix and lib/renderers.nix).
let
  # Joins `list` with `sep` (builtins-only concatStringsSep).
  concatSep =
    sep: list:
    if list == [ ] then
      ""
    else
      builtins.foldl' (a: b: a + sep + b) (builtins.head list) (builtins.tail list);

  # True when `s` contains any of the six ASCII whitespace characters Go's
  # strings.ContainsAny(r.Verdict, " \t\n\r\v\f") checks (space, tab, LF, CR,
  # vertical tab, form feed) -- POSIX [:space:] covers the same set.
  hasWhitespace = s: builtins.match ".*[[:space:]].*" s != null;

  # Mirrors verdict.go's blockedVerdict const: the reserved crash/no-verdict
  # escape-hatch status, never a configurable verdict token.
  blockedVerdict = "blocked";

  # Validates a parsed RESEARCH_VERDICTS array against the same rules the Go
  # launcher enforces at runtime (cmd/launcher/internal/forge/verdict.go's
  # ParseResearchVerdicts): the array must be non-empty; every entry must
  # carry a non-empty verdict and label; no verdict token may contain
  # whitespace; no verdict token may be the reserved "blocked" escape-hatch
  # status; and every verdict token must be unique. Throws on the first
  # violated rule (nix has no multi-error accumulation idiom here); returns
  # `verdicts` unchanged otherwise.
  validate =
    verdicts:
    if verdicts == [ ] then
      throw "parse RESEARCH_VERDICTS: must contain at least one entry"
    else
      let
        indices = builtins.genList (i: i) (builtins.length verdicts);
        # Checks entry `i`'s rules in Go's order, accumulating the verdict
        # tokens seen so far into `seen` (for the uniqueness check below).
        # builtins.foldl' forces each step's result to WHNF, which is enough
        # to force every `if`-chain condition here (each guard forces just
        # the field it inspects), so a violated rule throws even though
        # nothing downstream ever reads `seen`'s contents besides membership.
        checkEntry =
          seen: i:
          let
            raw = builtins.elemAt verdicts i;
            verdict = raw.verdict or "";
            label = raw.label or "";
          in
          if verdict == "" then
            throw "parse RESEARCH_VERDICTS: entry ${toString i}: verdict must not be empty"
          else if label == "" then
            throw "parse RESEARCH_VERDICTS: entry ${toString i} (verdict \"${verdict}\"): label must not be empty"
          else if hasWhitespace verdict then
            throw "parse RESEARCH_VERDICTS: entry ${toString i}: verdict \"${verdict}\" must not contain whitespace"
          else if verdict == blockedVerdict then
            throw "parse RESEARCH_VERDICTS: entry ${toString i}: verdict \"${verdict}\" is reserved for the crash/no-verdict escape hatch"
          else if builtins.elem verdict seen then
            throw "parse RESEARCH_VERDICTS: duplicate verdict token \"${verdict}\""
          else
            seen ++ [ verdict ];
      in
      builtins.seq (builtins.foldl' checkEntry [ ] indices) verdicts;
in
rec {
  # The built-in three-verdict set (ADR 0022), with labels matching
  # forge.ResearchVerdictLabels(). Used when RESEARCH_VERDICTS is unset;
  # render below always renders this set through the same machinery as a
  # custom set, so these descriptions are what actually reaches the
  # production prompt.
  defaultVerdicts = [
    {
      verdict = "recommend";
      label = "agent-research-recommend";
      description = "relevant, now enriched with real context; promote it.";
    }
    {
      verdict = "reject";
      label = "agent-research-reject";
      description = "false positive, not worth doing, or a duplicate. Name the duplicate issue by number in your rationale; duplicate is a reason under `reject`, not a separate verdict.";
    }
    {
      verdict = "unclear";
      label = "agent-research-unclear";
      description = "relevance can't be determined without a human's answer.";
    }
  ];

  # Parses the RESEARCH_VERDICTS knob string into the ordered verdict list.
  # The empty string (the schema default) yields defaultVerdicts; any other
  # value is parsed as JSON (a malformed value fails the build loudly,
  # mirroring the launcher's startup validation).
  parse = s: if s == "" then defaultVerdicts else validate (builtins.fromJSON s);

  # Renders the verdict contract of `promptText` from `verdicts`: the VERDICT
  # section's enumerated bullet list, the backtick-wrapped verdict
  # enumeration on the "Structure the verdict" line, and the
  # `status=<${RESEARCH_STATUS_ENUM}>` alternation of the outcome line (the
  # OUTCOME grammar line's registry-generated runtime placeholder, issue
  # #2504 -- unrelated to the two rewrites below).
  #
  # Each of the other two rewrites is a literal-text substitution: it
  # replaces the exact bytes `defaultVerdicts` itself renders to (the
  # checked-in template's own hand-typed content) with the exact bytes
  # `verdicts` renders to. `builtins.replaceStrings` is a safe no-op
  # wherever its target text isn't present, so this is safe against a
  # Consumer prompt that never carried the default content, or one that's
  # already been rewritten to a custom set (repeated calls are idempotent:
  # once the default text is gone, the same pattern no longer matches). And
  # because the match is exact bytes rather than a marker-bounded span, any
  # other prose sharing the VERDICT section (e.g. the self-contained
  # prompt's "Judge relevance..." sentence) is never touched. `render`
  # below always calls this, for both the default and a custom set --
  # rendering the default set against the checked-in template happens to be
  # byte-identical to the template, because the template's own hand-typed
  # content is exactly what `defaultVerdicts` renders to.
  renderPrompt =
    promptText: verdicts:
    let
      bullet = v: "- `" + v.verdict + "` — " + (v.description or "");
      bullets = vs: concatSep "\n" (map bullet vs);
      backtickEnum = vs: concatSep " / " (map (v: "`" + v.verdict + "`") vs);
      pipeJoined = concatSep "|" (map (v: v.verdict) verdicts);
    in
    builtins.replaceStrings
      [
        "status=<\${RESEARCH_STATUS_ENUM}>"
        (backtickEnum defaultVerdicts)
        (bullets defaultVerdicts)
      ]
      [
        ("status=<" + pipeJoined + ">")
        (backtickEnum verdicts)
        (bullets verdicts)
      ]
      promptText;

  # Parses the raw RESEARCH_VERDICTS knob and renders `promptText` against
  # the result -- one path for both the default (empty knob, defaultVerdicts)
  # and a configured custom set. There is no byte-identical-to-template
  # no-op special case in the dispatch itself: render always calls
  # renderPrompt. (Against the checked-in template, rendering the default
  # set happens to produce byte-identical output, because the template's
  # hand-typed content already equals what defaultVerdicts renders to.)
  render = rawKnob: promptText: renderPrompt promptText (parse rawKnob);
}
