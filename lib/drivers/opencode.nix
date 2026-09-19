# The opencode Driver: pure data only (ADR 0009, issue #624). ./default.nix
# validates this entry's shape and renders it into what lib/mkHarness.nix bakes
# into the image. The bats harness sources the registry's rendered preamble,
# byte-identical to the baked one, before exec-ing the entrypoint, so the suite
# exercises the same bytes (issue #433).
{ lib }:
let
  outcomeExtractor = import ./outcome-extractor.nix;
  jqSelector = ''select(.type == "text") | .part.text // empty'';
in
{
  name = "opencode";

  package = pkgs: pkgs.opencode;

  bin = "opencode";

  # Only the flags shared by every invocation. driver-exec assembles the rest
  # of opencode's argv (positional prompt, -m, no --agents) around them.
  flagsCommon = "run --format json --auto";

  # opencode reads .claude/skills/ directly (ADR 0009); no opencode-specific
  # skills directory exists or is wired.
  skillsDirRelative = ".claude/skills";

  # agent/entrypoint.sh's file-rewrite loop (issue #2153) rewrites each baked
  # agent file's body at runtime through this path, so it must stay in lockstep
  # with the path agentFilesTemplate below bakes into or the loop silently
  # no-ops. Optional, not in default.nix's requiredAttrs: a Driver whose
  # subagents are not on-disk files (claude) has nothing to rewrite.
  agentFilesDirRelative = ".config/opencode/agents";

  # sessionCacheDirRelative is deliberately omitted: opencode wires no resumable
  # session state, so the launcher creates no per-issue cache and the runner
  # adapters add no mount for it.

  # envCommon is deliberately omitted: opencode has no env vars to export into
  # the child process environment.

  # opencode's stream has no single terminal `result` envelope, unlike claude's
  # stream-json, so every `type:"text"` event's incremental `.part.text` is
  # scanned for the outcome line. The rest of the pipeline is shared with every
  # Driver's "match" body so both produce the same launcher-side line shape; see
  # outcome-extractor.nix's mkOutcomeExtractor for the rationale (issue #2261).
  outcomeExtractFnBody = outcomeExtractor.mkOutcomeExtractor {
    inherit jqSelector;
    variant = "match";
  };

  # The complement of outcomeExtractFnBody above over the same event stream
  # (issue #1900): see outcome-extractor.nix's mkOutcomeExtractor for why this
  # variant leaves the colon delimiter alone and does not require both landing=
  # and status=.
  outcomeExtractNearMissFnBody = outcomeExtractor.mkOutcomeExtractor {
    inherit jqSelector;
    variant = "near-miss";
  };

  # The shared prefix of the two bodies above, with no classification and no
  # `tail -1` on top. The driver-exec marker gate (issue #2978) scans this raw
  # text in Go (cmd/launcher/internal/outcome/outcome.go) instead of trusting a
  # bash-side classification for the SPINDRIFT_OUTCOME nudge decision. Called as
  # `_driver_extract_result_text "$stream_log"`.
  resultTextExtractFnBody = ''
    # The backtick below is a literal char in a single-quoted sed script, not
    # an unexpanded command substitution.
    # shellcheck disable=SC2016
    jq -r '${jqSelector}' "$1" 2>/dev/null \
      | sed -E 's/^[[:space:]]*(\*\*|`)?//; s/(\*\*|`)?[[:space:]]*$//' || true
  '';

  sessionFlagsFnBody = ''
    # opencode wires no session resume; this is a defined no-op so the
    # registry's _driver_session_flags function body is always present.
    :
  '';

  # opencode composes subagents from the on-disk files agentFilesTemplate below
  # bakes, not a CLI flag, so this always returns "". It keeps claude.nix's
  # roster argument (issue #264) only to give mkHarness.nix one call site.
  agentsJsonTemplate = { roster }: "";

  # opencode discovers subagents by scanning these files, and an entry's effort
  # becomes opencode's reasoningEffort key (issue #2242). rosterLib.dropOptedOut
  # drops empty-model entries upstream, so e.model needs no fallback and reaches
  # driver-exec verbatim. Every frontmatter scalar is JSON-encoded (issue #2152
  # slice C) so a newline or colon cannot inject a second YAML key.
  agentFilesTemplate =
    { roster }:
    lib.listToAttrs (
      map (e: {
        name = ".config/opencode/agents/${e.name}.md";
        value = ''
          ---
          description: ${builtins.toJSON (e.description or "")}
          mode: ${builtins.toJSON (e.mode or "subagent")}
          model: ${builtins.toJSON e.model}
          ${lib.optionalString (
            (e.effort or "") != ""
          ) "reasoningEffort: ${builtins.toJSON e.effort}\n"}---
          ${e.description or ""}
        '';
      }) roster
    );

  # opencode's CLI argv shape (ADR 0009, issue #2534). It has no --agents
  # equivalent, so agentsFlag is deliberately omitted: the nullable slot's
  # absent case.
  argvShape = {
    promptStyle = "positional";
    modelFlag = "-m";
    modelOmitEmpty = true;
    effortFlag = "--variant";
    order = [
      "driverFlags"
      "model"
      "effort"
      "session"
      "prompt"
    ];
  };
}
