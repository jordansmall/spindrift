# The opencode Driver: pure data only (ADR 0009, issue #624). ./default.nix
# validates this entry's shape and renders it into what lib/mkHarness.nix bakes
# into the image. The bats harness sources the registry's rendered preamble,
# byte-identical to the baked one, before exec-ing the entrypoint, so the suite
# exercises the same bytes (issue #433).
{ lib }:
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
          ${lib.optionalString ((e.effort or "") != "") "reasoningEffort: ${builtins.toJSON e.effort}\n"}---
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
