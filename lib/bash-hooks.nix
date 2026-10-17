# The Bash hook table (issue #5408): the one place the Bash hook list is named.
# lib/image.nix renders the claude settings.json hooks from it and
# lib/agent-hooks.nix bakes the ~/.claude/hooks files from it (the image and
# the opencode-guards check both use that directory); `nix run .#regen` renders
# the opencode plugin's hook list and its docs/reference.md copy;
# nix/checks/bats.nix's shellcheck lints `scripts`.
#
# Row order is load-bearing: it is the claude settings.json order within an
# event, and the opencode plugin's run order, where each hook sees the args the
# earlier ones rewrote (the tee must stay last to wrap the scrubbed command).
#
# A row with an `opencode` attr is run by the opencode plugin
# (agent/opencode-guards.js): a PreToolUse row in its tool.execute.before
# HOOKS, where `rewrites` marks a hook whose silence on a non-empty command is
# a failure rather than "allow", and the one PostToolUse row as its
# tool.execute.after SUMMARY_HOOK. A row without one has no plugin
# counterpart: credential-deny.sh is stood in for by opencode's own deny config
# (lib/drivers/opencode.nix).
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
      opencode.rewrites = true;
    }
    {
      script = "bash-output-summary.sh";
      event = "PostToolUse";
      matchers = [ "Bash" ];
      opencode = { };
    }
  ];

  scripts = map (r: r.script) hooks;

  # The opencode plugin runs exactly one PostToolUse hook and needs `rewrites`
  # on every PreToolUse one; checked here so every renderer sees a valid table.
  opencodeHooks =
    let
      rows = builtins.filter (r: r ? opencode) hooks;
      after = builtins.filter (r: r.event == "PostToolUse") rows;
      unset = builtins.filter (r: r.event == "PreToolUse" && !(r.opencode ? rewrites)) rows;
    in
    if builtins.length after != 1 then
      throw "lib/bash-hooks.nix: the opencode plugin runs exactly one PostToolUse hook, got ${toString (builtins.length after)} -- give exactly one PostToolUse row an opencode attr"
    else if unset != [ ] then
      throw "lib/bash-hooks.nix: ${(builtins.head unset).script}: a PreToolUse opencode row has no opencode.rewrites -- set it true when the hook rewrites the command"
    else
      rows;

  # Bash the hooks source from beside themselves: baked verbatim (no shebang
  # to pin), never a settings.json entry, and named without .sh so the
  # `*.sh` hook globs in the image and opencode checks skip them.
  helpers = [ "credential-vars-parse" ];

  # Non-script files baked beside the hooks (read by both Drivers).
  dataFiles = [ "credential-vars" ];
}
