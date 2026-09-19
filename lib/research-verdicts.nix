# Build-time half of the configurable research verdict set (issue #2201,
# amending ADR 0022). The Go launcher parses the same RESEARCH_VERDICTS knob at
# runtime (cmd/launcher/internal/forge/verdict.go); this file renders the same
# contract into the prompt, so a custom set reaches it too. Pure builtins only
# (no `pkgs.lib`) so a bare `nix eval` can unit-test this file.
let
  # Matches the same six characters Go's strings.ContainsAny(r.Verdict,
  # " \t\n\r\v\f") checks, since POSIX [:space:] covers the same set.
  hasWhitespace = s: builtins.match ".*[[:space:]].*" s != null;

  # Mirrors verdict.go's blockedVerdict const: reserved for the crash/no-verdict
  # escape hatch, never a configurable verdict token.
  blockedVerdict = "blocked";

  # Enforces the same rules the Go launcher applies at runtime
  # (cmd/launcher/internal/forge/verdict.go's ParseResearchVerdicts). Throws on
  # the first violated rule, because nix has no multi-error accumulation here.
  validate =
    verdicts:
    if verdicts == [ ] then
      throw "parse RESEARCH_VERDICTS: must contain at least one entry"
    else
      let
        indices = builtins.genList (i: i) (builtins.length verdicts);
        # Checks entry `i`'s rules in Go's order, so the same bad input reports
        # the same error. builtins.foldl' forces each step to WHNF, which forces
        # every guard in the if-chain, so a violated rule throws even though
        # nothing downstream reads `seen` beyond the membership test.
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
  # The built-in set used when RESEARCH_VERDICTS is unset (ADR 0022), with
  # labels matching forge.ResearchVerdictLabels(). render puts it through the
  # same machinery as a custom set, so these descriptions reach the prompt.
  defaultVerdicts = [
    {
      verdict = "recommend";
      label = "agent-research-recommend";
      description = "relevant, now enriched with real context; promote it.";
    }
    {
      verdict = "reject";
      label = "agent-research-reject";
      # Concatenated literals only to keep the source near this repo's ~76-col
      # wrap. The concatenation adds no newline, so the rendered bullet stays a
      # single unbroken line.
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

  # The empty string is the schema default and yields defaultVerdicts. Any
  # other value is JSON, and a malformed one fails the build loudly, mirroring
  # the launcher's startup validation.
  parse = s: if s == "" then defaultVerdicts else validate (builtins.fromJSON s);

  # Injection markers the checked-in templates carry in place of hand-typed
  # verdict content (issue #2525). Keying each rewrite to a marker rather than
  # to the bytes defaultVerdicts renders means a reworded default can never
  # leave one site rewritten and the other stale. Templates must carry both or
  # neither; a lone one no-ops. nix/checks/research-verdicts.nix checks both.
  bulletsMarker = "<!-- RESEARCH_VERDICT_BULLETS -->";
  enumMarker = "`<RESEARCH_VERDICT_ENUM>`";

  # Three independent rewrites: the two markers above, plus the outcome line's
  # status alternation, a registry-generated runtime placeholder (issue #2504)
  # unrelated to them. Each targets a single-purpose marker, not a span between
  # headings, so other prose in the VERDICT section is untouched, and
  # `builtins.replaceStrings` no-ops where its target is absent.
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

  # One path for both the default (empty knob) and a configured custom set:
  # there is no byte-identical-to-template no-op special case.
  render = rawKnob: promptText: renderPrompt promptText (parse rawKnob);
}
