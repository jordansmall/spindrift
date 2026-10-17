# The Bash hook table (issue #5408): the one place a Bash hook script is named.
# lib/image.nix renders the claude settings.json hooks from it and
# lib/agent-hooks.nix bakes the ~/.claude/hooks files from it;
# nix/checks/bats.nix's shellcheck lints `scripts`.
#
# Row order is load-bearing: it is the claude settings.json order within an
# event, and (for rows with an `opencode` attr) the opencode plugin's run
# order, which decides whose denial wins.
#
# A row with an `opencode` attr is run by the opencode plugin
# (agent/opencode-guards.js); `rewrites` marks a hook that rewrites the
# command rather than only allowing or denying it. A row without one has no
# plugin counterpart: credential-deny.sh is stood in for by opencode's own
# deny config (lib/drivers/opencode.nix).
rec {
  hooks = [
    # reject-background-bash.sh (issue #1609): --disallowedTools strips whole
    # tools, but run_in_background is a parameter of the Bash tool, not a tool
    # name, so only a PreToolUse hook can reject it. Home-wide rather than gated
    # behind a Driver attribute, because the restriction applies to every pass
    # sharing this $HOME, not to one Driver invocation's flags.
    {
      script = "reject-background-bash.sh";
      event = "PreToolUse";
      matchers = [ "Bash" ];
      opencode.rewrites = false;
    }
    # credential-deny.sh (issue #1909, spec #1907) rejects a Read or Bash call
    # targeting a known credential path. Read and Bash each need their own matcher
    # entry pointing at the same script, because the PreToolUse matcher is
    # per-tool.
    {
      script = "credential-deny.sh";
      event = "PreToolUse";
      matchers = [
        "Read"
        "Bash"
      ];
    }
    # env-credential-scrub.sh (issue #1927, spec #1907) unsets every variable
    # agent/credential-vars names for every Bash call and denies any reference to
    # /proc/<pid>/environ. Claude Code's own CLAUDE_CODE_SUBPROCESS_ENV_SCRUB
    # cannot do this job: it forces the Driver's permission mode to `default` and
    # nests a bwrap sandbox that cannot mount /proc inside the Box's own (#1926).
    {
      script = "env-credential-scrub.sh";
      event = "PreToolUse";
      matchers = [ "Bash" ];
      opencode.rewrites = true;
    }
    # bash-output-tee.sh and bash-output-summary.sh (issue #1988) tee every Bash
    # call's combined output to a log file and replace the tool result with a
    # bounded tail, so full output lands on disk and only the tail enters the
    # model's context. This covers every call, not just the overflow case
    # BASH_MAX_OUTPUT_LENGTH already handles (issue #1987).
    {
      script = "bash-output-tee.sh";
      event = "PreToolUse";
      matchers = [ "Bash" ];
    }
    {
      script = "bash-output-summary.sh";
      event = "PostToolUse";
      matchers = [ "Bash" ];
    }
  ];

  scripts = map (r: r.script) hooks;

  # Non-script files baked beside the hooks (read by both Drivers).
  dataFiles = [ "credential-vars" ];
}
