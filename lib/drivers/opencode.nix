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
  # roster arg as claude.nix's template (issue #264, for a uniform call site
  # in mkHarness.nix) but always returns "", meaning no --agents-equivalent
  # flag is ever rendered.
  agentsJsonTemplate = { roster }: "";

  # opencode has no --agents JSON flag; it discovers subagents by scanning
  # HOME-relative markdown files under .config/opencode/agents/, each with a
  # YAML frontmatter block (description/mode/model) plus a body that seeds
  # the subagent's system prompt. Takes the same first-class roster (issue
  # #264, lib/roster.nix) as claude.nix's agentsJsonTemplate: each roster
  # entry with a non-empty model becomes one file, so an arbitrary N-agent
  # roster -- including a custom agent beyond the historical
  # scout/reviewer/filer/worker set -- renders the same way; an entry with an
  # empty model is dropped entirely (#392 semantics) rather than baking a
  # modelless stub. The model is passed VERBATIM (never string-processed) --
  # the operator supplies the full provider-prefixed model id, matching
  # driver-exec's unprefixed `-m <model>` invocation.
  agentFilesTemplate =
    { roster }:
    let
      present = lib.filter (e: (e.model or "") != "") roster;
    in
    lib.listToAttrs (
      map (e: {
        name = ".config/opencode/agents/${e.name}.md";
        value = ''
          ---
          description: ${builtins.toJSON (e.description or "")}
          mode: ${e.mode or "subagent"}
          model: ${e.model}
          ---
          ${e.description or ""}
        '';
      }) present
    );
}
