# The claude Driver: pure data only (ADR 0009, issue #624). The registry
# (./default.nix) validates this entry's shape and renders it into what
# lib/mkHarness.nix bakes into the image. The bats harness sources
# mkHarness.internals.driverPreambleFile, byte-identical to the preamble the
# image bakes in, so the suite exercises the same bytes (issue #433).
{ lib }:
let
  outcomeExtractor = import ./outcome-extractor.nix;
  jqSelector = ''select(.type == "result") | .result // empty'';
in
{
  name = "claude";

  package = pkgs: pkgs.claude-code;

  bin = "claude";

  # Space-separated so agent/entrypoint.sh can splice these in unquoted.
  # --disallowedTools removes the tools that promise a later re-invocation the
  # headless runner never makes (issue #1609; #1542 lost a run when the Driver
  # backgrounded its test gate behind ScheduleWakeup). Keep the tool names one
  # comma-separated token: driver-exec/args.go splits this value on whitespace.
  flagsCommon = "--verbose --output-format stream-json --dangerously-skip-permissions --disallowedTools ScheduleWakeup,CronCreate,CronDelete,CronList,RemoteTrigger,Monitor";

  # Issue #2011: claude then omits run_in_background from the Bash, Agent/Task,
  # and PowerShell input schemas, so the model cannot park a headless turn on a
  # notification a one-shot `claude -p` never receives. --disallowedTools reaches
  # a tool name, never a parameter. reject-background-bash.sh still covers
  # shell-level backgrounding (`&`, nohup, setsid, coproc).
  envCommon = {
    CLAUDE_CODE_DISABLE_BACKGROUND_TASKS = "1";
  };

  skillsDirRelative = ".claude/skills";

  # Issue #427/#447/#448, ADR 0009: the launcher mounts a writable per-issue
  # host directory over this so a fix pass resumes the initial run's session.
  # A Driver that omits this attribute gets no per-issue cache and no mount,
  # so it has no resumable session state.
  sessionCacheDirRelative = ".claude/projects";

  # Called as `_driver_extract_outcome "$stream_log"`. Shares its pipeline
  # shape (markdown-strip, issue #1611; colon/space delimiter tolerance, issue
  # #2012; required landing=/status= fields) with every other Driver's "match"
  # body; outcome-extractor.nix's mkOutcomeExtractor doc comment explains both.
  outcomeExtractFnBody = outcomeExtractor.mkOutcomeExtractor {
    inherit jqSelector;
    variant = "match";
  };

  # Called as `_driver_extract_near_miss_outcome "$stream_log"` (issue #1900).
  # The complement of outcomeExtractFnBody above; mkOutcomeExtractor explains
  # why this variant leaves the colon alone and requires neither field.
  outcomeExtractNearMissFnBody = outcomeExtractor.mkOutcomeExtractor {
    inherit jqSelector;
    variant = "near-miss";
  };

  # Called as `_driver_extract_result_text "$stream_log"`: the shared prefix
  # the two extractors above classify further, with no landing/status grep and
  # no `tail -1`. The driver-exec marker gate (issue #2978) scans this raw text
  # through cmd/launcher/internal/outcome/outcome.go rather than trusting a
  # bash-side classification for the SPINDRIFT_OUTCOME nudge decision.
  resultTextExtractFnBody = ''
    # The backtick below is a literal char in a single-quoted sed script, not
    # an unexpanded command substitution.
    # shellcheck disable=SC2016
    jq -r '${jqSelector}' "$1" 2>/dev/null \
      | sed -E 's/^[[:space:]]*(\*\*|`)?//; s/(\*\*|`)?[[:space:]]*$//' || true
  '';

  # Issue #427/ADR 0009. The session id is derived so that REPO_SLUG and
  # ISSUE_NUMBER alone recompute it, with no stored state. `resume` prints
  # nothing when that session's transcript is missing under the mounted
  # projects directory (an evicted cache, or the first fix pass after a
  # crash), and the caller then falls back to the cold-context fix flow.
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

  # Rendered at eval time by builtins.toJSON (ADR 0007 tier-1) so model names
  # never reach bash as interpolated strings. Takes the whole roster (issue
  # #264); lib/mkHarness.nix already dropped empty-model entries through
  # rosterLib.dropOptedOut, so e.model is non-empty. `prompt` stays "" because
  # entrypoint.sh injects it at runtime; claude's schema has no `mode` key.
  agentsJsonTemplate =
    { roster }:
    let
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
        }) roster
      );
    in
    if agents == { } then "" else builtins.toJSON agents;

  # claude declares subagents through agentsJsonTemplate's --agents flag, not
  # the on-disk agents/*.md files opencode uses, so lib/image.nix bakes none.
  agentFilesTemplate = _: { };

  # claude's CLI argv shape (ADR 0009, issue #2534). driver-exec assembles the
  # invocation in the order listed.
  argvShape = {
    promptStyle = "flag";
    promptFlag = "-p";
    modelFlag = "--model";
    modelOmitEmpty = false;
    agentsFlag = "--agents";
    effortFlag = "--effort";
    order = [
      "prompt"
      "model"
      "agents"
      "session"
      "driverFlags"
      "effort"
    ];
  };
}
