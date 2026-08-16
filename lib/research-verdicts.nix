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
  inject = import ./prompt-inject.nix;

  # Escapes a literal string's regex metacharacters so builtins.split reads it
  # as a literal (same table as lib/prompt-inject.nix's private escapeRegex).
  escapeRegex = builtins.replaceStrings
    [ "\\" "^" "$" "." "|" "?" "*" "+" "(" ")" "[" "]" "{" "}" ]
    [ "\\\\" "\\^" "\\$" "\\." "\\|" "\\?" "\\*" "\\+" "\\(" "\\)" "\\[" "\\]" "\\{" "\\}" ];

  # True when `marker` appears at least once in `text`.
  present = marker: text: builtins.length (builtins.split (escapeRegex marker) text) > 1;

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

  verdictMarker = "# VERDICT";
  postMarker = "# POST THE VERDICT";

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
  # The built-in three-verdict set (ADR 0022), with descriptions byte-matching
  # templates/default/prompts/research-prompt.md's VERDICT bullets and labels
  # matching forge.ResearchVerdictLabels(). Used when RESEARCH_VERDICTS is
  # unset; rendering against it is short-circuited to a no-op below so the
  # default prompt stays byte-identical to the template on disk.
  defaultVerdicts = [
    {
      verdict = "recommend";
      label = "agent-research-recommend";
      description = "relevant, now enriched with real context; promote it.";
    }
    {
      verdict = "reject";
      label = "agent-research-reject";
      description = "false positive, not worth doing, or a duplicate.";
    }
    {
      verdict = "unclear";
      label = "agent-research-unclear";
      description = "relevance can't be determined without a human's answer.";
    }
  ];

  # Parses the RESEARCH_VERDICTS knob string into the ordered verdict list.
  # The empty string (the schema default) yields defaultVerdicts, so no custom
  # set means no change; any other value is parsed as JSON (a malformed value
  # fails the build loudly, mirroring the launcher's startup validation).
  parse = s: if s == "" then defaultVerdicts else validate (builtins.fromJSON s);

  # Renders the verdict contract of `promptText` from `verdicts`: the VERDICT
  # section's enumerated bullet list, the `recommend / reject / unclear`
  # enumeration, and the `status=<${RESEARCH_STATUS_ENUM}>` alternation of the
  # outcome line -- the OUTCOME grammar line's registry-generated placeholder
  # (issue #2504), not a hand-typed literal, so a custom verdict set fully
  # replaces the token rather than leaving it for the runtime substitution
  # pass (agent/entrypoint.sh's RESEARCH_STATUS_ENUM span / driver-exec
  # assemble-prompt) to fill in with the built-in default. Each rewrite is
  # guarded on the text it targets being present, so rendering a Consumer
  # prompt that lacks the default markers/tokens (or the default set itself —
  # see renderIfCustom) is a safe no-op.
  renderPrompt =
    promptText: verdicts:
    let
      tokens = map (v: v.verdict) verdicts;
      pipeJoined = concatSep "|" tokens;
      # The `# POST THE VERDICT` enumeration wraps each token in backticks
      # (`recommend` / `reject` / `unclear`); match that form.
      backtickEnum = concatSep " / " (map (t: "`" + t + "`") tokens);
      bullet = v: "- `" + v.verdict + "` — " + (v.description or "");
      bullets = concatSep "\n" (map bullet verdicts);
      newSection = verdictMarker + "\n\nRender exactly one of these verdicts:\n\n" + bullets + "\n\n";
      withSection =
        if present verdictMarker promptText && present postMarker promptText then
          let
            oldSection = inject.sliceBetween verdictMarker postMarker promptText;
          in
          builtins.replaceStrings [ oldSection ] [ newSection ] promptText
        else
          promptText;
    in
    builtins.replaceStrings
      [ "status=<\${RESEARCH_STATUS_ENUM}>" "`recommend` / `reject` / `unclear`" ]
      [ ("status=<" + pipeJoined + ">") backtickEnum ]
      withSection;

  # renderPrompt gated on the raw knob: the empty (default) knob returns the
  # prompt untouched, guaranteeing the default research prompt stays byte-
  # identical to the template (nix/checks/prompts.nix pins this); only a
  # configured custom set rewrites the contract.
  renderIfCustom =
    rawKnob: promptText:
    if rawKnob == "" then promptText else renderPrompt promptText (parse rawKnob);
}
