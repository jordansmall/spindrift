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
      # Wrapped across concatenated string literals to stay near this
      # repo's ~76-col prose wrap (see the templates this replaced) --
      # the concatenation introduces no newline, so the rendered bullet
      # (renderPrompt's `bullet` joins verdict + description onto one
      # line) stays a single unbroken line, unaffected by the source wrap.
      description =
        "false positive, not worth doing, or a duplicate. Name the "
        + "duplicate issue by number in your rationale; duplicate is a "
        + "reason under `reject`, not a separate verdict.";
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

  # Injection markers the checked-in templates carry in place of hand-typed
  # verdict content (issue #2525): `bulletsMarker` sits alone where the
  # VERDICT section's enumerated bullet list belongs, and `enumMarker` sits
  # inside the "Structure the verdict" line's backtick enumeration. Each is
  # single-use -- renderPrompt replaces it with content synthesized from
  # `verdicts`, consuming the marker in the process, so re-rendering an
  # already-rendered prompt finds no marker left and is a safe no-op via
  # `builtins.replaceStrings`' no-match behavior. Neither rewrite depends on
  # the template's on-disk bytes matching what `defaultVerdicts` itself
  # renders to, so a reworded or reflowed default bullet can never leave the
  # enumeration/status line rewritten while the bullet list itself stays
  # stale (or vice versa).
  #
  # The two markers being always both present (nothing rendered yet) or both
  # absent (already rendered), never one of each, is an invariant the
  # checked-in templates are expected to maintain -- `renderPrompt` and
  # `builtins.replaceStrings` enforce nothing of the kind for arbitrary
  # `promptText`; either marker missing while the other is present just
  # silently no-ops that one rewrite. nix/checks/research-verdicts.nix's
  # research-verdicts-templates-carry-both-markers check gives partial eval-
  # time coverage of this invariant for research-prompt.md and
  # research-self-contained-prompt.md specifically, not a general guarantee.
  bulletsMarker = "<!-- RESEARCH_VERDICT_BULLETS -->";
  enumMarker = "`<RESEARCH_VERDICT_ENUM>`";

  # Renders the verdict contract of `promptText` from `verdicts`: the VERDICT
  # section's enumerated bullet list (bulletsMarker), the backtick-wrapped
  # verdict enumeration on the "Structure the verdict" line (enumMarker), and
  # the `status=<${RESEARCH_STATUS_ENUM}>` alternation of the outcome line
  # (the OUTCOME grammar line's registry-generated runtime placeholder, issue
  # #2504 -- unrelated to the two marker rewrites). `builtins.replaceStrings`
  # is a safe no-op wherever its target text isn't present, so this is safe
  # against a Consumer prompt that never carried the markers at all. Because
  # each rewrite targets a single-purpose marker rather than a span between
  # section headings, any other prose sharing the VERDICT section (e.g. the
  # self-contained prompt's "Judge relevance..." sentence) is never touched.
  # `render` below always calls this, for both the default and a custom set
  # -- there is no byte-identical-to-template no-op special case.
  renderPrompt =
    promptText: verdicts:
    let
      bullet = v: "- `" + v.verdict + "` — " + (v.description or "");
      bullets = builtins.concatStringsSep "\n" (map bullet verdicts);
      backtickEnum = builtins.concatStringsSep " / " (map (v: "`" + v.verdict + "`") verdicts);
      pipeJoined = builtins.concatStringsSep "|" (map (v: v.verdict) verdicts);
    in
    builtins.replaceStrings
      [
        "status=<\${RESEARCH_STATUS_ENUM}>"
        enumMarker
        bulletsMarker
      ]
      [
        ("status=<" + pipeJoined + ">")
        backtickEnum
        bullets
      ]
      promptText;

  # Parses the raw RESEARCH_VERDICTS knob and renders `promptText` against
  # the result -- one path for both the default (empty knob, defaultVerdicts)
  # and a configured custom set. There is no byte-identical-to-template
  # no-op special case in the dispatch itself: render always calls
  # renderPrompt.
  render = rawKnob: promptText: renderPrompt promptText (parse rawKnob);
}
