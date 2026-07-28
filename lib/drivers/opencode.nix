# The opencode Driver: pure data only (ADR 0009, issue #624) -- the registry
# (./default.nix) validates this entry's shape and renders it into the data
# lib/mkHarness.nix bakes into the image: the opencode package, the
# entrypoint's DRIVER_* preamble, and the --agents JSON (here always ""; see
# agentsJsonTemplate below). The bats harness sources
# mkHarness.driverPreambleFile (the registry's rendered preamble,
# byte-identical to what the image bakes in) before exec-ing the entrypoint,
# so the suite exercises the exact same bytes (issue #433).
{ lib }:
{
  name = "opencode";

  # In-box package providing the `opencode` binary.
  package = pkgs: pkgs.opencode;

  # Binary name agent/entrypoint.sh invokes.
  bin = "opencode";

  # Flags common to every opencode invocation in agent/entrypoint.sh: the
  # `run` subcommand, JSON output, and auto-approve. opencode's own argv
  # shape -- a positional prompt, `-m provider/model`, and no `--agents`
  # equivalent -- is assembled around these flags by driver-exec in a later
  # slice; this attr covers only what's common across every invocation.
  flagsCommon = "run --format json --auto";

  # Directory opencode scans for skill files, relative to $HOME. opencode
  # reads .claude/skills/ directly (ADR 0009) -- no opencode-specific skills
  # directory (e.g. .config/opencode/skills) exists or is wired.
  skillsDirRelative = ".claude/skills";

  # sessionCacheDirRelative is deliberately omitted: opencode wires no
  # resumable session state, so the launcher creates no per-issue cache and
  # the runner adapters add no mount for it (see lib/drivers/default.nix's
  # requiredAttrs comment and lib/preambles.nix's renderDriverMountPreamble).

  # envCommon is deliberately omitted: opencode has no env vars to export
  # into the child process environment (contrast claude.nix's envCommon).

  # Shell function body extracting the SPINDRIFT_OUTCOME line from opencode's
  # `run --format json` NDJSON stream. Unlike claude's stream-json, opencode's
  # stream has no single terminal `result` envelope -- instead every
  # `type:"text"` event carries incremental `.part.text`, so every such
  # event's text is scanned for the outcome line. The per-line markdown-strip,
  # colon/space delimiter normalization, and required-field greps mirror
  # claude.nix's outcomeExtractFnBody exactly (see that file's comment for the
  # full rationale) so both Drivers produce the same launcher-side outcome
  # line shape from whichever event stream they emit.
  outcomeExtractFnBody = ''
    # The backtick below is a literal char in a single-quoted sed script, not
    # an unexpanded command substitution.
    # shellcheck disable=SC2016
    jq -r 'select(.type == "text") | .part.text // empty' "$1" 2>/dev/null \
      | sed -E 's/^[[:space:]]*(\*\*|`)?//; s/(\*\*|`)?[[:space:]]*$//' \
      | grep -E '^SPINDRIFT_OUTCOME[: ]' \
      | sed -E 's/^SPINDRIFT_OUTCOME:[[:space:]]*/SPINDRIFT_OUTCOME /' \
      | grep -E '(^| )landing=' \
      | grep -E '(^| )status=' \
      | tail -1 || true
  '';

  # Shell function body extracting a *near-miss* SPINDRIFT_OUTCOME line from
  # opencode's NDJSON text events (issue #1900). Mirrors claude.nix's
  # outcomeExtractNearMissFnBody exactly (see that file's comment for the full
  # rationale) over opencode's `type:"text"`/`.part.text` event stream, the
  # same way this file's outcomeExtractFnBody mirrors its claude.nix sibling.
  outcomeExtractNearMissFnBody = ''
    # The backtick below is a literal char in a single-quoted sed script, not
    # an unexpanded command substitution.
    # shellcheck disable=SC2016
    jq -r 'select(.type == "text") | .part.text // empty' "$1" 2>/dev/null \
      | sed -E 's/^[[:space:]]*(\*\*|`)?//; s/(\*\*|`)?[[:space:]]*$//' \
      | grep -E '^SPINDRIFT_OUTCOME[: ]' \
      | grep -vE '(^| )landing=.*(^| )status=|(^| )status=.*(^| )landing=' \
      | tail -1 || true
  '';

  # Shell function body computing opencode-specific session pin/resume flags.
  # opencode wires no session resume (contrast claude.nix's deterministic
  # --session-id/--resume pinning), so this is a defined no-op body -- the
  # registry's _driver_session_flags function is always present, it just does
  # nothing for this Driver.
  sessionFlagsFnBody = ''
    # opencode wires no session resume; this is a defined no-op so the
    # registry's _driver_session_flags function body is always present.
    :
  '';

  # opencode composes subagents from on-disk agents/*.md files
  # (agentFilesTemplate below), not a CLI flag like claude's --agents JSON, so
  # this template correctly declines that mechanism: it accepts the same
  # model-knob args as claude.nix's template (for a uniform call site in
  # mkHarness.nix) but always returns "", meaning no --agents-equivalent flag
  # is ever rendered.
  agentsJsonTemplate =
    {
      scoutModel,
      reviewModel,
      filerModel,
      workerModel,
    }:
    "";

  # opencode has no --agents JSON flag; it discovers subagents by scanning
  # HOME-relative markdown files under .config/opencode/agents/, each with a
  # YAML frontmatter block (description/mode/model) plus a body that seeds
  # the subagent's system prompt. Composed independently per agent, mirroring
  # claude.nix's agentsJsonTemplate: each file is baked only when its model
  # knob is non-empty (lib.optionalAttrs), so an empty model omits that
  # agent's file entirely rather than baking a modelless stub. The
  # description strings are the same ones claude.nix's agentsJsonTemplate
  # uses for the same agent, so the two Drivers present identical subagent
  # framing regardless of which composes via JSON and which via files. The
  # model is passed VERBATIM (never string-processed) -- the operator
  # supplies the full provider-prefixed model id, matching driver-exec's
  # unprefixed `-m <model>` invocation.
  agentFilesTemplate =
    {
      scoutModel,
      reviewModel,
      filerModel,
      workerModel,
    }:
    lib.optionalAttrs (scoutModel != "") {
      ".config/opencode/agents/scout.md" = ''
        ---
        description: Map relevant files, seams, and tests; return a structured brief
        mode: subagent
        model: ${scoutModel}
        ---
        Map relevant files, seams, and tests; return a structured brief
      '';
    }
    // lib.optionalAttrs (reviewModel != "") {
      ".config/opencode/agents/reviewer.md" = ''
        ---
        description: Review the branch diff for spec compliance and coding standards
        mode: subagent
        model: ${reviewModel}
        ---
        Review the branch diff for spec compliance and coding standards
      '';
    }
    // lib.optionalAttrs (filerModel != "") {
      ".config/opencode/agents/filer.md" = ''
        ---
        description: File issues from a review's non-blocking findings, best-effort
        mode: subagent
        model: ${filerModel}
        ---
        File issues from a review's non-blocking findings, best-effort
      '';
    }
    // lib.optionalAttrs (workerModel != "") {
      ".config/opencode/agents/worker.md" = ''
        ---
        description: Implement a scoped slice of work delegated to it, with full implement-capable tools
        mode: subagent
        model: ${workerModel}
        ---
        Implement a scoped slice of work delegated to it, with full implement-capable tools
      '';
    };
}
