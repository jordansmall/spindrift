# The claude Driver: the in-box half (ADR 0009). Provides the data
# lib/mkHarness.nix bakes into the image — the claude-code package, the
# entrypoint's DRIVER_* preamble, and the --agents JSON — so the rendered
# entrypoint is byte-identical to the hand-written version this registry
# replaces. agent/entrypoint.sh's own `${DRIVER_*:-<default>}` fallbacks carry
# the same values, so the bats suite (which execs the script raw, without any
# nix preamble) exercises the identical claude behavior.
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
  flagsCommon = "--verbose --output-format stream-json --dangerously-skip-permissions";

  # Directory Claude Code scans for skill files, relative to $HOME.
  skillsDirRelative = ".claude/skills";

  # Shell function body extracting the SPINDRIFT_OUTCOME line from claude's
  # stream-json result event; called as `_driver_extract_outcome "$stream_log"`.
  outcomeExtractFnBody = ''
    jq -r 'select(.type == "result") | .result // empty' "$1" 2>/dev/null \
      | grep '^SPINDRIFT_OUTCOME ' | tail -1 || true
  '';

  # Shell function body computing the claude-specific session pin/resume
  # flags (issue #427/ADR 0009): a deterministic per-issue session id (so no
  # state beyond ISSUE_NUMBER/REPO_SLUG is needed to recompute it) plus the
  # verb claude itself uses. Called as `_driver_session_flags initial` on the
  # cold run (pins the id) or `_driver_session_flags resume` on a fix pass
  # (resumes it only if that session's transcript is actually present under
  # the mounted /home/agent/.claude — e.g. absent after the cache was
  # evicted, or on the first fix pass following a crash — in which case this
  # prints nothing and the caller falls back to the cold-context fix flow
  # with no error).
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
  # model names are never string-interpolated in bash. Each subagent is
  # composed independently by its own model knob; the flag is omitted when no
  # subagent model is configured (empty string return).
  agentsJsonTemplate =
    {
      scoutModel,
      reviewModel,
      filerModel,
    }:
    let
      agents =
        lib.optionalAttrs (scoutModel != "") {
          scout = {
            description = "Map relevant files, seams, and tests; return a structured brief";
            prompt = "";
            tools = [
              "Read"
              "Bash"
              "WebFetch"
              "WebSearch"
              "Glob"
              "Grep"
            ];
            model = scoutModel;
          };
        }
        // lib.optionalAttrs (reviewModel != "") {
          reviewer = {
            description = "Review the branch diff for spec compliance and coding standards";
            prompt = "";
            tools = [
              "Read"
              "Bash"
              "WebFetch"
            ];
            model = reviewModel;
          };
        }
        // lib.optionalAttrs (filerModel != "") {
          filer = {
            description = "File issues from a review's non-blocking findings, best-effort";
            prompt = "";
            tools = [
              "Read"
              "Bash"
              "WebFetch"
            ];
            model = filerModel;
          };
        };
    in
    if agents == { } then "" else builtins.toJSON agents;
}
