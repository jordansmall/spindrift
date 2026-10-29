# Claude Driver's eval-level pins (issue #5502, ADR 0066): the checks that
# read only the claude registry row, plus the claude credential-deny hook.
{
  pkgs,
  nixpkgs,
  system,
  driverRegistry,
  entry,
  defaultRosterFixture,
  ...
}:
let
  rosterHelper = import ../../../lib/roster-schema-defaults.nix { inherit (pkgs) lib; };
  inherit (pkgs.lib)
    assertMsg
    hasInfix
    filter
    concatStringsSep
    splitString
    imap0
    escapeShellArg
    ;
in
{
  # Issue #1609: a Box Driver session must never see the harness's
  # re-invocation-promising tools, because the headless runner will not keep
  # that promise (a backgrounded gate plus ScheduleWakeup on #1542 lost a run
  # outright). Checked against the real claude entry, since flagsCommon is
  # shared verbatim across the main, conflict-resolve, and fix passes (AC4).
  drivers-claude-blocks-loop-background-affordances =
    let
      disallowed = [
        "ScheduleWakeup"
        "CronCreate"
        "CronDelete"
        "CronList"
        "RemoteTrigger"
        "Monitor"
      ];
      # driver-exec word-splits DRIVER_FLAGS_COMMON on whitespace
      # (driver-exec/args.go's strings.Fields), so the --disallowedTools
      # value is the single word right after the flag.
      # Match tokens exactly rather than by substring, so a typo'd sibling
      # like "ScheduleWakeupX" can't slip a false pass by.
      words = splitString " " entry.flagsCommon;
      indexedWords = imap0 (i: w: { inherit i w; }) words;
      flagMatches = filter (iw: iw.w == "--disallowedTools") indexedWords;
      flagIndex = if flagMatches == [ ] then null else (builtins.head flagMatches).i;
      deniedTools =
        if flagIndex == null then [ ] else splitString "," (builtins.elemAt words (flagIndex + 1));
      missing = filter (t: !(builtins.elem t deniedTools)) disallowed;
    in
    assert assertMsg (
      flagIndex != null
    ) "claude Driver's flagsCommon must include --disallowedTools, got: ${entry.flagsCommon}";
    assert assertMsg (missing == [ ])
      "claude Driver's flagsCommon --disallowedTools must deny ${concatStringsSep ", " missing}, got: ${entry.flagsCommon}";
    pkgs.runCommand "drivers-claude-blocks-loop-background-affordances" { } "touch $out";

  # A Box has no operator to approve a tool call, so claude runs headless with
  # --dangerously-skip-permissions, and under `--print` only stream-json (which
  # needs --verbose) emits events live (issue #113); text mode prints nothing
  # until the end and the box looks dead under `podman logs -f`. box hands
  # flagsCommon to driver-exec verbatim, so this pins the bytes it forwards.
  drivers-claude-runs-headless-and-streaming =
    let
      words = splitString " " entry.flagsCommon;
      streamIdx = filter (iw: iw.w == "--output-format") (imap0 (i: w: { inherit i w; }) words);
      format = if streamIdx == [ ] then null else builtins.elemAt words ((builtins.head streamIdx).i + 1);
    in
    assert assertMsg (builtins.elem "--dangerously-skip-permissions" words)
      "claude Driver's flagsCommon must include --dangerously-skip-permissions, got: ${entry.flagsCommon}";
    assert assertMsg (builtins.elem "--verbose" words)
      "claude Driver's flagsCommon must include --verbose (stream-json needs it under --print), got: ${entry.flagsCommon}";
    assert assertMsg (
      format == "stream-json"
    ) "claude Driver's flagsCommon must run --output-format stream-json, got: ${toString format}";
    pkgs.runCommand "drivers-claude-runs-headless-and-streaming" { } "touch $out";

  # Issue #2011: --disallowedTools can't strip run_in_background, a parameter
  # of the Bash/Agent/Task/PowerShell calls rather than a tool name, and
  # agent/reject-background-bash.sh covers only Bash. claude honors
  # CLAUDE_CODE_DISABLE_BACKGROUND_TASKS by omitting run_in_background from
  # every one of those schemas, closing them all at once.
  drivers-claude-disables-background-tasks =
    assert assertMsg ((entry.envCommon or { }).CLAUDE_CODE_DISABLE_BACKGROUND_TASKS or null == "1")
      "claude Driver's envCommon must set CLAUDE_CODE_DISABLE_BACKGROUND_TASKS=1, got: ${
        builtins.toJSON (entry.envCommon or { })
      }";
    pkgs.runCommand "drivers-claude-disables-background-tasks" { } "touch $out";

  # A claude-code bump could rename or drop
  # CLAUDE_CODE_DISABLE_BACKGROUND_TASKS silently, and no public doc names
  # the var, so this greps the pinned binary itself. In 2.1.204 it gates
  # `.omit({run_in_background:!0})` on the Bash, Agent/Task, and PowerShell
  # schemas. Drift fails here instead of leaving envCommon's export inert.
  drivers-claude-cli-knows-disable-background-tasks-env =
    let
      # claude-code carries an unfree license, so the plain `pkgs` above
      # refuses to evaluate it. This is the only check here that needs the
      # real package rather than its registry data, so it builds its own
      # allowUnfree pkgs the way lib/mkHarness.nix does for the image bake.
      unfreePkgs = import nixpkgs {
        inherit system;
        config.allowUnfree = true;
      };
      claudePackage = entry.package unfreePkgs;
      # Named explicitly rather than derived from envCommon's keys: envCommon
      # may grow a second entry, and attrNames' alphabetical head would then
      # silently pin the wrong var.
      envVarName = "CLAUDE_CODE_DISABLE_BACKGROUND_TASKS";
    in
    assert assertMsg (
      (entry.envCommon or { }) ? ${envVarName}
    ) "envVarName must name a key entry.envCommon actually sets, got: ${envVarName}";
    pkgs.runCommand "drivers-claude-cli-knows-disable-background-tasks-env"
      { nativeBuildInputs = [ pkgs.gnugrep ]; }
      ''
        # Grepped over the whole package output, not just bin/, since the
        # string's exact location inside claude-code's packaging is an
        # upstream implementation detail this check shouldn't assume.
        if grep -aqr ${escapeShellArg envVarName} ${claudePackage}; then
          touch $out
        else
          echo "pinned claude-code no longer references ${envVarName} -- lib/drivers/claude.nix's envCommon relies on it to omit run_in_background from the Bash/Agent/Task/PowerShell tool schemas (issue #2011); re-verify against the new build and update the Driver" >&2
          exit 1
        fi
      '';

  # Issue #5368: `spawns` scopes opencode's task permission only. claude's
  # --agents JSON must render byte-identically with or without it.
  drivers-claude-agents-json-ignores-spawns =
    let
      base = {
        name = "scout";
        model = "solo-scout-model";
        mode = "subagent";
        description = "fixture scout description";
        tools = [
          "Read"
          "Agent"
        ];
        promptFile = "scout-prompt.md";
        prompt = null;
      };
      render = e: entry.agentsJsonTemplate { roster = [ e ]; };
    in
    assert assertMsg (
      render base == render (base // { spawns = [ "scout" ]; })
    ) "claude agentsJsonTemplate must render identically when an entry carries spawns";
    pkgs.runCommand "drivers-claude-agents-json-ignores-spawns" { } "touch $out";

  # Issue #264: claude's agentsJsonTemplate takes a roster list rather than
  # four fixed model-knob args, so a custom agent ("auditor") must render
  # into the --agents JSON like any built-in entry. That JSON must never gain
  # a `mode` key: claude's --agents schema has none, and only opencode's
  # agentFilesTemplate emits mode.
  drivers-claude-agents-json-roster =
    let
      rendered = entry.agentsJsonTemplate {
        roster = [
          {
            name = "scout";
            model = "solo-scout-model";
            mode = "subagent";
            description = "fixture scout description";
            tools = [
              "Read"
              "Bash"
              "WebFetch"
              "WebSearch"
              "Glob"
              "Grep"
            ];
            promptFile = "scout-prompt.md";
            prompt = null;
          }
          {
            name = "auditor";
            model = "audit-model";
            mode = "subagent";
            description = "Audit stuff";
            tools = [ "Read" ];
            promptFile = "auditor-prompt.md";
            prompt = null;
          }
        ];
      };
      parsed = builtins.fromJSON rendered;
    in
    assert assertMsg (parsed ? scout)
      "claude agentsJsonTemplate must render a roster entry with a non-empty model, got keys: ${concatStringsSep ", " (builtins.attrNames parsed)}";
    assert assertMsg (parsed ? auditor)
      "claude agentsJsonTemplate must render a custom roster entry (auditor), got keys: ${concatStringsSep ", " (builtins.attrNames parsed)}";
    assert assertMsg (parsed.auditor.model or "" == "audit-model")
      "claude agentsJsonTemplate's auditor entry must carry the roster model verbatim, got: ${
        builtins.toJSON (parsed.auditor or { })
      }";
    assert assertMsg (parsed.auditor.tools or [ ] == [ "Read" ])
      "claude agentsJsonTemplate's auditor entry must carry the roster tools verbatim, got: ${
        builtins.toJSON (parsed.auditor or { })
      }";
    assert assertMsg (filter (name: (parsed.${name} or { }) ? mode) (builtins.attrNames parsed) == [ ])
      "claude agentsJsonTemplate must never emit a `mode` key on any agent (claude's --agents schema has none), got: ${rendered}";
    pkgs.runCommand "drivers-claude-agents-json-roster" { } "touch $out";

  # Issue #2242 slice 1: a roster entry may set an optional `effort` field
  # alongside `model`, which claude's agentsJsonTemplate passes into the
  # rendered --agents JSON verbatim, with no normalization or validation.
  drivers-claude-agents-json-effort-present =
    let
      rendered = entry.agentsJsonTemplate {
        roster = [
          {
            name = "scout";
            model = "solo-scout-model";
            effort = "high";
            mode = "subagent";
            description = "fixture scout description";
            tools = [ "Read" ];
            promptFile = "scout-prompt.md";
            prompt = null;
          }
        ];
      };
      parsed = builtins.fromJSON rendered;
    in
    assert assertMsg (parsed ? scout)
      "claude agentsJsonTemplate must render a roster entry with a non-empty model, got keys: ${concatStringsSep ", " (builtins.attrNames parsed)}";
    assert assertMsg (parsed.scout.effort or "" == "high")
      "claude agentsJsonTemplate must carry a roster entry's effort field verbatim into the rendered JSON, got: ${
        builtins.toJSON (parsed.scout or { })
      }";
    pkgs.runCommand "drivers-claude-agents-json-effort-present" { } "touch $out";

  # Issue #2242 slice 1: an entry that omits `effort` (or sets it to "") must
  # carry no `effort` key at all, so a Driver invocation with no effort knob
  # renders byte-stable with the pre-#2242 shape.
  drivers-claude-agents-json-effort-absent =
    let
      rendered = entry.agentsJsonTemplate {
        roster = [
          {
            name = "scout";
            model = "solo-scout-model";
            mode = "subagent";
            description = "fixture scout description";
            tools = [ "Read" ];
            promptFile = "scout-prompt.md";
            prompt = null;
          }
          {
            name = "filer";
            model = "filer-model";
            effort = "";
            mode = "subagent";
            description = "File tickets";
            tools = [ "Read" ];
            promptFile = "filer-prompt.md";
            prompt = null;
          }
        ];
      };
      parsed = builtins.fromJSON rendered;
    in
    assert assertMsg (parsed ? scout)
      "claude agentsJsonTemplate must render a roster entry with a non-empty model, got keys: ${concatStringsSep ", " (builtins.attrNames parsed)}";
    assert assertMsg (!(parsed.scout ? effort))
      "claude agentsJsonTemplate must omit the effort key entirely when a roster entry doesn't set effort, got: ${builtins.toJSON parsed.scout}";
    assert assertMsg (parsed ? filer)
      "claude agentsJsonTemplate must render a roster entry with a non-empty model, got keys: ${concatStringsSep ", " (builtins.attrNames parsed)}";
    assert assertMsg (!(parsed.filer ? effort))
      "claude agentsJsonTemplate must omit the effort key entirely when a roster entry sets effort to the empty string, got: ${builtins.toJSON parsed.filer}";
    pkgs.runCommand "drivers-claude-agents-json-effort-absent" { } "touch $out";

  # Issue #2843: renderPreamble must bake DRIVER_SESSION_CACHE_DIR from the
  # real claude entry's sessionCacheDirRelative so box sees
  # the session-cache path inside the box. lib/preambles.nix's
  # renderDriverMountPreamble renders the same-named var for the host-side
  # launcher, a separate consumer.
  drivers-render-preamble-session-cache-dir =
    let
      out = driverRegistry.renderPreamble entry;
    in
    assert assertMsg (hasInfix "DRIVER_SESSION_CACHE_DIR=/home/agent/.claude/projects" out)
      "renderPreamble must bake DRIVER_SESSION_CACHE_DIR from the claude Driver entry's sessionCacheDirRelative under /home/agent, got: ${out}";
    pkgs.runCommand "drivers-render-preamble-session-cache-dir" { } "touch $out";

  # Issue #2152 slice C: claude's agentsJsonTemplate builds an attrset and
  # returns builtins.toJSON over the whole thing, so every scalar is encoded,
  # not just description. A payload with an embedded quote and newline must
  # round-trip exactly; a string-concatenation template would corrupt the
  # JSON structure or mangle the payload.
  drivers-claude-agents-json-escapes-every-scalar =
    let
      payload = "x\"y\nz";
      rendered = entry.agentsJsonTemplate {
        roster = [
          {
            name = "escapee";
            model = payload;
            mode = "subagent";
            description = payload;
            tools = [ "Read" ];
            promptFile = "escapee-prompt.md";
            prompt = null;
          }
        ];
      };
      parsed = builtins.fromJSON rendered;
    in
    assert assertMsg (parsed ? escapee)
      "claude agentsJsonTemplate must render the escapee roster entry, got keys: ${concatStringsSep ", " (builtins.attrNames parsed)}";
    assert assertMsg (parsed.escapee.description or "" == payload)
      "claude agentsJsonTemplate must round-trip a JSON-breaking description exactly, got: ${
        builtins.toJSON (parsed.escapee.description or "")
      }";
    assert assertMsg (parsed.escapee.model or "" == payload)
      "claude agentsJsonTemplate must round-trip a JSON-breaking model exactly, got: ${
        builtins.toJSON (parsed.escapee.model or "")
      }";
    pkgs.runCommand "drivers-claude-agents-json-escapes-every-scalar" { } "touch $out";

  # Issue #2386: defaultRoster ships a fixed default `effort` per agent,
  # looked up per name from rosterDefaults (issue #2506) rather than written
  # on each entry. Assert those defaults render end to end through claude's
  # --agents JSON for the four legacy built-in agents, not just that the
  # plumbing exists; review-axis's effort is pinned in nix/checks/image.nix.
  drivers-claude-agents-json-default-roster-effort =
    let
      roster = defaultRosterFixture;
      rendered = entry.agentsJsonTemplate { inherit roster; };
      parsed = builtins.fromJSON rendered;
    in
    assert assertMsg (parsed.scout.effort or "" == rosterHelper.rosterDefaults.scout.effort)
      "claude agentsJsonTemplate must render defaultRoster's scout effort default (${rosterHelper.rosterDefaults.scout.effort}), got: ${
        builtins.toJSON (parsed.scout or { })
      }";
    assert assertMsg (parsed.reviewer.effort or "" == rosterHelper.rosterDefaults.reviewer.effort)
      "claude agentsJsonTemplate must render defaultRoster's reviewer effort default (${rosterHelper.rosterDefaults.reviewer.effort}), got: ${
        builtins.toJSON (parsed.reviewer or { })
      }";
    assert assertMsg (parsed.filer.effort or "" == rosterHelper.rosterDefaults.filer.effort)
      "claude agentsJsonTemplate must render defaultRoster's filer effort default (${rosterHelper.rosterDefaults.filer.effort}), got: ${
        builtins.toJSON (parsed.filer or { })
      }";
    assert assertMsg (parsed.worker.effort or "" == rosterHelper.rosterDefaults.worker.effort)
      "claude agentsJsonTemplate must render defaultRoster's worker effort default (${rosterHelper.rosterDefaults.worker.effort}), got: ${
        builtins.toJSON (parsed.worker or { })
      }";
    # Issue #6182: the worker's roster Skill grant reaches claude's --agents tools.
    assert assertMsg (builtins.elem "Skill" (parsed.worker.tools or [ ]))
      "claude agentsJsonTemplate must render the worker's Skill tool, got: ${
        builtins.toJSON (parsed.worker or { })
      }";
    pkgs.runCommand "drivers-claude-agents-json-default-roster-effort" { } "touch $out";

  # Pins the real claude entry's argvShape rather than stubDriverBase's, so
  # the "both entries carry argvShape" criterion has eval-level coverage
  # against lib/drivers/claude.nix's declared values.
  drivers-claude-argv-shape-pinned =
    let
      shape = entry.argvShape;
    in
    assert assertMsg (
      shape.promptStyle == "flag"
    ) "claude Driver's argvShape.promptStyle must stay \"flag\", got: ${shape.promptStyle}";
    assert assertMsg (
      shape.promptFlag == "-p"
    ) "claude Driver's argvShape.promptFlag must stay \"-p\", got: ${shape.promptFlag}";
    assert assertMsg (
      shape.modelFlag == "--model"
    ) "claude Driver's argvShape.modelFlag must stay \"--model\", got: ${shape.modelFlag}";
    assert assertMsg (shape.modelOmitEmpty == false)
      "claude Driver's argvShape.modelOmitEmpty must stay false, got: ${builtins.toJSON shape.modelOmitEmpty}";
    assert assertMsg (
      shape.agentsFlag == "--agents"
    ) "claude Driver's argvShape.agentsFlag must stay \"--agents\", got: ${shape.agentsFlag}";
    assert assertMsg (
      shape.effortFlag == "--effort"
    ) "claude Driver's argvShape.effortFlag must stay \"--effort\", got: ${shape.effortFlag}";
    assert assertMsg (
      shape.order == [
        "prompt"
        "model"
        "agents"
        "session"
        "driverFlags"
        "effort"
      ]
    ) "claude Driver's argvShape.order must stay pinned, got: ${builtins.toJSON shape.order}";
    pkgs.runCommand "drivers-claude-argv-shape-pinned" { } "touch $out";

  # The shared credential-paths list feeds opencode's rules; the claude hook is
  # the reference for what counts as a credential. Parity runs both ways: the
  # hook denies every listed path, and every path the hook names is listed.
  drivers-credential-paths-hook-parity =
    let
      credentialPaths = import ../../../lib/credential-paths.nix;
      paths = map (builtins.replaceStrings [ "*" ] [ "local" ]) credentialPaths;
      listed = pkgs.writeText "credential-paths.txt" (concatStringsSep "\n" credentialPaths);
    in
    pkgs.runCommand "drivers-credential-paths-hook-parity" { nativeBuildInputs = [ pkgs.jq ]; } ''
      hookPaths=$(sed -n '/^targets_credential_path()/,/^}/p' ${../../../agent/credential-deny.sh} \
        | sed -nE 's/.*\(\^\|\$lead\)([^(]+)\(.*/\1/p' | sed 's/\\\././g')
      [ -n "$hookPaths" ] || {
        echo "extracted no paths from credential-deny.sh's targets_credential_path" >&2
        exit 1
      }
      while IFS= read -r hp; do
        grep -qxF -- "$hp" ${listed} || {
          echo "credential-deny.sh denies $hp, but lib/credential-paths.nix does not list it" >&2
          exit 1
        }
      done <<<"$hookPaths"
      for p in ${concatStringsSep " " (map escapeShellArg paths)}; do
        for tool in Read Bash; do
          if [ "$tool" = Read ]; then
            input=$(jq -nc --arg p "/home/agent/$p" '{tool_name:"Read",tool_input:{file_path:$p}}')
          else
            input=$(jq -nc --arg c "cat /home/agent/$p" '{tool_name:"Bash",tool_input:{command:$c}}')
          fi
          verdict=$(printf '%s' "$input" | bash ${../../../agent/credential-deny.sh})
          printf '%s' "$verdict" | jq -e '.hookSpecificOutput.permissionDecision == "deny"' >/dev/null || {
            echo "credential-deny.sh lets $tool of $p through, but lib/credential-paths.nix lists it" >&2
            exit 1
          }
        done
      done
      touch $out
    '';

  drivers-claude-declare-bash-timeout-env =
    assert assertMsg
      (
        (entry.bashTimeoutEnv or null) == [
          "BASH_DEFAULT_TIMEOUT_MS"
          "BASH_MAX_TIMEOUT_MS"
        ]
      )
      "claude Driver's bashTimeoutEnv must name BASH_DEFAULT_TIMEOUT_MS and BASH_MAX_TIMEOUT_MS, got: ${
        builtins.toJSON (entry.bashTimeoutEnv or null)
      }";
    pkgs.runCommand "drivers-claude-declare-bash-timeout-env" { } "touch $out";

  # AC#4: one roster carrying a custom agent outside the
  # scout/reviewer/filer/worker set must render from the same list, since the
  # roster, not per-Driver hardcoding, is the single source of agent identity.
  drivers-claude-roster-custom-agent =
    let
      roster = [
        {
          name = "auditor";
          model = "audit-model";
          mode = "subagent";
          description = "Audit stuff";
          tools = [ "Read" ];
        }
      ];
      claudeRendered = builtins.fromJSON (entry.agentsJsonTemplate { inherit roster; });
    in
    assert assertMsg (claudeRendered ? auditor)
      "claude agentsJsonTemplate must render a custom roster entry (auditor), got keys: ${concatStringsSep ", " (builtins.attrNames claudeRendered)}";
    assert assertMsg (claudeRendered.auditor.model or "" == "audit-model")
      "claude agentsJsonTemplate's auditor entry must carry the roster model verbatim, got: ${
        builtins.toJSON (claudeRendered.auditor or { })
      }";
    pkgs.runCommand "drivers-claude-roster-custom-agent" { } "touch $out";

  # Issue #2152 slice C: an empty roster (`roster = []`) is a deliberate
  # agent-less image, so claude's agentsJsonTemplate returns "".
  drivers-claude-render-empty-roster =
    let
      claudeRendered = entry.agentsJsonTemplate { roster = [ ]; };
    in
    assert assertMsg (
      claudeRendered == ""
    ) "claude agentsJsonTemplate must return \"\" for an empty roster, got: ${claudeRendered}";
    pkgs.runCommand "drivers-claude-render-empty-roster" { } "touch $out";

  # Issue #2152 slice C: one multi-agent roster (a built-in scout plus a
  # custom "auditor"). claude's --agents JSON carries the custom agent's model.
  drivers-claude-render-custom-roster =
    let
      roster = [
        {
          name = "scout";
          model = "solo-scout-model";
          mode = "subagent";
          description = "fixture scout description";
          tools = [ "Read" ];
          promptFile = "scout-prompt.md";
          prompt = null;
        }
        {
          name = "auditor";
          model = "audit-model";
          mode = "subagent";
          description = "Audit stuff";
          tools = [ "Read" ];
          promptFile = "auditor-prompt.md";
          prompt = null;
        }
      ];
      claudeRendered = builtins.fromJSON (entry.agentsJsonTemplate { inherit roster; });
    in
    assert assertMsg (claudeRendered ? auditor)
      "claude agentsJsonTemplate must render the custom roster entry (auditor), got keys: ${concatStringsSep ", " (builtins.attrNames claudeRendered)}";
    assert assertMsg (claudeRendered.auditor.model or "" == "audit-model")
      "claude agentsJsonTemplate's auditor entry must carry the roster model, got: ${
        builtins.toJSON (claudeRendered.auditor or { })
      }";
    pkgs.runCommand "drivers-claude-render-custom-roster" { } "touch $out";

  # The credential-deny rules live in opencode's configFiles; claude enforces
  # the same policy through its hook, so its row carries none.
  drivers-claude-carries-no-config-files =
    assert assertMsg (!(entry ? configFiles)) "the claude Driver must not carry configFiles";
    pkgs.runCommand "drivers-claude-carries-no-config-files" { } "touch $out";
}
