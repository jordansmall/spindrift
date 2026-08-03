# The claude Driver: pure data only (ADR 0009, issue #624) -- the registry
# (./default.nix) validates this entry's shape and renders it into the data
# lib/mkHarness.nix bakes into the image: the claude-code package, the
# entrypoint's DRIVER_* preamble, and the --agents JSON. The bats harness
# sources mkHarness.driverPreambleFile (the registry's rendered preamble,
# byte-identical to what the image bakes in) before exec-ing the entrypoint,
# so the suite exercises the exact same bytes (issue #433).
{ lib }:
{
  name = "claude";

  # In-box package providing the `claude` binary.
  package = pkgs: pkgs.claude-code;

  # Binary name agent/entrypoint.sh invokes.
  bin = "claude";

  # Flags common to every claude invocation in agent/entrypoint.sh (the
  # conflict-resolve agent, the main run, and the devShell wrapper),
  # space-separated so the entrypoint can splice them in unquoted.
  #
  # --disallowedTools strips the harness's re-invocation-promising tools from
  # the Driver's tool surface (issue #1609): none of ScheduleWakeup,
  # CronCreate, CronDelete, CronList, RemoteTrigger, or Monitor has a
  # legitimate use in a single-shot headless Box run, since each is a promise
  # of a later re-invocation the headless runner will not keep (#1542 lost a
  # run outright when the Driver backgrounded its test gate and called
  # ScheduleWakeup, trusting a re-invocation that never came). The model
  # cannot call a tool it never sees. A single comma-separated token so the
  # entrypoint's unquoted word-split (see driver-exec/args.go's
  # strings.Fields) never breaks it into separate argv elements.
  flagsCommon = "--verbose --output-format stream-json --dangerously-skip-permissions --disallowedTools ScheduleWakeup,CronCreate,CronDelete,CronList,RemoteTrigger,Monitor";

  # Env vars every claude invocation needs in its process environment (issue
  # #2011, distinct from flagsCommon's CLI args): CLAUDE_CODE_DISABLE_BACKGROUND_TASKS
  # makes claude itself omit the run_in_background parameter from the Bash,
  # Agent/Task, and PowerShell tools' own input schema, so none of them can
  # ever be called asynchronously in the first place -- --disallowedTools
  # above can't reach this, since run_in_background is a parameter of those
  # tool calls, not a tool name of its own, and reject-background-bash.sh
  # (agent/reject-background-bash.sh) only ever covered Bash, leaving the
  # Agent/Task subagent-launch tool (which backgrounds by default, unlike
  # Bash) free to park a headless run's turn awaiting a notification a
  # one-shot `claude -p` session can never receive. Schema-level omission is
  # strictly stronger than a PreToolUse deny hook here: the model can't even
  # attempt the call, rather than attempting it and having to correctly act
  # on a denial mid-turn. reject-background-bash.sh stays as defense in depth
  # for the vector this env var doesn't reach -- a Bash command that
  # self-backgrounds at the shell level (a trailing `&`, `nohup`, `setsid`,
  # `coproc`) rather than through the structured parameter.
  envCommon = {
    CLAUDE_CODE_DISABLE_BACKGROUND_TASKS = "1";
  };

  # Directory Claude Code scans for skill files, relative to $HOME.
  skillsDirRelative = ".claude/skills";

  # Directory claude's session transcripts live under, relative to $HOME
  # (issue #427/#447/#448, ADR 0009): the launcher mounts an ephemeral
  # per-issue host directory writable over it so a fix pass can resume the
  # initial run's session instead of cold-starting one. A Driver that omits
  # this attribute has no resumable session state -- the launcher creates no
  # per-issue cache and the runner adapters add no mount for it.
  sessionCacheDirRelative = ".claude/projects";

  # Shell function body extracting the SPINDRIFT_OUTCOME line from claude's
  # stream-json result event; called as `_driver_extract_outcome "$stream_log"`.
  # claude's own result text sometimes wraps the line in inline backticks,
  # bold markers, or pads it with whitespace (issue #1611, seen on the #1582
  # dogfood run) -- strip that wrapping per line before the `^SPINDRIFT_OUTCOME `
  # anchor is tested, so the launcher's grep and outcome.Parse both see the
  # line bare. Stripping runs before grep -- grepping the raw wrapped line
  # first would never match.
  #
  # A near-miss sign-off that mis-types the delimiter as a colon instead of
  # the required space (issue #2012, seen on the #1998 dogfood run) is still
  # genuinely machine-recoverable, so the token match tolerates either
  # delimiter and normalizes a colon back to the canonical space before
  # anything downstream sees the line -- the same space/colon tolerance
  # outcome.Parse's stripToken already applies launcher-side. The final pair
  # of field-marker greps is what keeps this from over-reaching: they require
  # both landing= and status= (outcome.Parse's own two required fields, not
  # just any one of issue=/landing=/status= appearing anywhere) so a bare
  # prose sign-off -- either no field markers at all ("SPINDRIFT_OUTCOME:
  # Complete — ...") or a single stray one caught mid-sentence -- has no
  # status this extractor can safely infer. Left unmatched, it falls through
  # to the backstop, which salvages any uncommitted work regardless of why
  # the outcome line was unusable.
  outcomeExtractFnBody = ''
    # The backtick below is a literal char in a single-quoted sed script, not
    # an unexpanded command substitution.
    # shellcheck disable=SC2016
    jq -r 'select(.type == "result") | .result // empty' "$1" 2>/dev/null \
      | sed -E 's/^[[:space:]]*(\*\*|`)?//; s/(\*\*|`)?[[:space:]]*$//' \
      | grep -E '^SPINDRIFT_OUTCOME[: ]' \
      | sed -E 's/^SPINDRIFT_OUTCOME:[[:space:]]*/SPINDRIFT_OUTCOME /' \
      | grep -E '(^| )landing=' \
      | grep -E '(^| )status=' \
      | tail -1 || true
  '';

  # Shell function body extracting a *near-miss* SPINDRIFT_OUTCOME line from
  # claude's stream-json result event (issue #1900); called as
  # `_driver_extract_near_miss_outcome "$stream_log"`. The complement of
  # outcomeExtractFnBody above: a line that leads with the SPINDRIFT_OUTCOME
  # token (colon- or space-delimited, markdown wrapping stripped the same way)
  # but does NOT carry both landing= and status= -- outcome.Parse's own two
  # required fields (cmd/launcher/internal/outcome/outcome.go), whose absence
  # is exactly what it classifies as ErrNearMiss. Unlike the extractor above
  # this deliberately does not normalize the colon delimiter back to a space:
  # the recovery nudge quotes this line back to the agent verbatim, so it must
  # read as the agent actually typed it. Emits nothing when the only lines
  # present are valid outcomes (handled by the extractor above) or carry the
  # token in prose with no field markers at all.
  outcomeExtractNearMissFnBody = ''
    # The backtick below is a literal char in a single-quoted sed script, not
    # an unexpanded command substitution.
    # shellcheck disable=SC2016
    jq -r 'select(.type == "result") | .result // empty' "$1" 2>/dev/null \
      | sed -E 's/^[[:space:]]*(\*\*|`)?//; s/(\*\*|`)?[[:space:]]*$//' \
      | grep -E '^SPINDRIFT_OUTCOME[: ]' \
      | grep -vE '(^| )landing=.*(^| )status=|(^| )status=.*(^| )landing=' \
      | tail -1 || true
  '';

  # Shell function body computing the claude-specific session pin/resume
  # flags (issue #427/ADR 0009): a deterministic per-issue session id (so no
  # state beyond ISSUE_NUMBER/REPO_SLUG is needed to recompute it) plus the
  # verb claude itself uses. Called as `_driver_session_flags initial` on the
  # cold run (pins the id) or `_driver_session_flags resume` on a fix pass
  # (resumes it only if that session's transcript is actually present under
  # the mounted /home/agent/.claude/projects — e.g. absent after the cache
  # was evicted, or on the first fix pass following a crash — in which case
  # this prints nothing and the caller falls back to the cold-context fix
  # flow with no error).
  sessionFlagsFnBody = ''
    local h id
    h="$(printf '%s' "spindrift-session:''${REPO_SLUG:-}:''${ISSUE_NUMBER:-}" | sha256sum | cut -c1-32)"
    id="''${h:0:8}-''${h:8:4}-''${h:12:4}-''${h:16:4}-''${h:20:12}"
    case "$1" in
      initial)
        printf -- '--session-id %s' "$id"
        ;;
      resume)
        if compgen -G "''${HOME:-}/.claude/projects/*/''${id}.jsonl" >/dev/null 2>&1; then
          printf -- '--resume %s' "$id"
        fi
        ;;
    esac
  '';

  # --agents JSON rendered at eval time via builtins.toJSON (ADR 0007 tier-1):
  # model names are never string-interpolated in bash. Takes the first-class
  # roster (issue #264, lib/roster.nix) rather than four fixed model-knob
  # args, so an arbitrary N-agent roster -- including a custom agent beyond
  # the historical scout/reviewer/filer/worker set -- renders the same way.
  # Each roster entry with a non-empty model becomes one key; an entry with
  # an empty model is dropped entirely (#392 semantics), and the flag is
  # omitted (empty string return) when every entry is dropped. `prompt` is
  # always "" here -- entrypoint.sh injects each agent's rendered prompt at
  # runtime, never at eval time. Deliberately NO `mode` key: claude's
  # --agents schema has none (contrast opencode.nix's agentFilesTemplate,
  # which does emit `mode` in its YAML frontmatter).
  agentsJsonTemplate =
    { roster }:
    let
      present = lib.filter (e: (e.model or "") != "") roster;
      agents = lib.listToAttrs (
        map (e: {
          name = e.name;
          value =
            {
              description = e.description or "";
              prompt = "";
              tools = e.tools or [ ];
              model = e.model;
            }
            // (if (e.effort or "") != "" then { effort = e.effort; } else { });
        }) present
      );
    in
    if agents == { } then "" else builtins.toJSON agents;

  # opencode composes subagents from on-disk agents/*.md files under $HOME
  # (lib/drivers/opencode.nix's agentFilesTemplate); claude declines that
  # on-disk mechanism entirely -- its subagents ride agentsJsonTemplate's
  # --agents JSON flag above instead. Always returns the empty attrset, so
  # lib/image.nix's agentFiles bakes no per-agent files for this Driver.
  agentFilesTemplate = _: { };
}
