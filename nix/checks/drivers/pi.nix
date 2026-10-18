# pi Driver's eval-level pins (ADR 0066): the checks that read only the pi
# registry row, plus the pi Harness's roster bakes.
{
  pkgs,
  fixtures,
  driverRegistry,
  entry,
  ...
}:
let
  inherit (pkgs.lib) assertMsg hasInfix;
  inherit (fixtures) piHarness;
  preamble = driverRegistry.renderPreamble entry;
in
{
  drivers-pi-attributes-pinned =
    assert assertMsg (entry.name == "pi") "pi Driver's name must stay \"pi\", got: ${entry.name}";
    assert assertMsg (
      entry.package pkgs == pkgs.pi-coding-agent
    ) "pi Driver's package must be nixpkgs pi-coding-agent from the Consumer's locked nixpkgs";
    assert assertMsg (entry.bin == "pi") "pi Driver's bin must stay \"pi\", got: ${entry.bin}";
    assert assertMsg (entry.flagsCommon == "--print --mode json --no-approve")
      "pi Driver's flagsCommon must stay \"--print --mode json --no-approve\", got: ${entry.flagsCommon}";
    assert assertMsg (entry.providerNamespacedModels or false
    ) "pi Driver must declare providerNamespacedModels so the bare-worker gate (issue #5331) applies";
    assert assertMsg (
      (entry.subagents or true) == false
    ) "pi Driver must declare subagents = false until a roster ships";
    assert assertMsg
      (
        map (r: { inherit (r) modelPrefix env; }) entry.credentials == [
          {
            modelPrefix = "anthropic/";
            env = [ "ANTHROPIC_API_KEY" ];
          }
          {
            modelPrefix = "openai/";
            env = [ "OPENAI_API_KEY" ];
          }
          {
            modelPrefix = "google/";
            env = [ "GEMINI_API_KEY" ];
          }
          {
            modelPrefix = "openrouter/";
            env = [ "OPENROUTER_API_KEY" ];
          }
          {
            modelPrefix = "groq/";
            env = [ "GROQ_API_KEY" ];
          }
          {
            modelPrefix = "xai/";
            env = [ "XAI_API_KEY" ];
          }
          {
            modelPrefix = "mistral/";
            env = [ "MISTRAL_API_KEY" ];
          }
          {
            modelPrefix = "deepseek/";
            env = [ "DEEPSEEK_API_KEY" ];
          }
          {
            modelPrefix = "github-copilot/";
            env = [ "PI_AUTH_CONTENT" ];
          }
        ]
      )
      "pi Driver must declare one modelPrefix-gated rule per Provider API key (mapping per pi upstream), got: ${builtins.toJSON entry.credentials}";
    assert assertMsg (builtins.all (r: !(r ? setupCommand))
      entry.credentials
    ) "pi Driver's rules must carry no setupCommand, got: ${builtins.toJSON entry.credentials}";
    assert assertMsg (
      entry.envCommon == {
        PI_SKIP_VERSION_CHECK = "1";
        PI_TELEMETRY = "0";
      }
    ) "pi Driver's envCommon must stay offline-quiet, got: ${builtins.toJSON entry.envCommon}";
    assert assertMsg (
      entry.agentsJsonTemplate { roster = [ ]; } == ""
    ) "pi Driver has no --agents equivalent, so agentsJsonTemplate must render \"\"";
    assert assertMsg (
      entry.agentFilesTemplate {
        roster = [
          {
            name = "scout";
            model = "p/m";
          }
        ];
      } == {
        ".pi/agent/extensions/spindrift-hooks.js" = builtins.readFile ../../../agent/pi-hook-bridge.js;
      }
    ) "pi Driver bakes only the hook bridge (issue #5482), no subagent files yet";
    pkgs.runCommand "drivers-pi-attributes-pinned" { } "touch $out";

  # pi reads skills from its agent directory (PI_CODING_AGENT_DIR, default
  # ~/.pi/agent), unlike opencode, which reads claude's directory.
  drivers-pi-skills-dir =
    assert assertMsg (entry.skillsDirRelative == ".pi/agent/skills")
      "pi Driver's skillsDirRelative must be pi's agent-directory skills path, got: ${entry.skillsDirRelative}";
    assert assertMsg (hasInfix "DRIVER_SKILLS_DIR=/home/agent/.pi/agent/skills\n" preamble)
      "renderPreamble must bake pi's DRIVER_SKILLS_DIR, got: ${preamble}";
    pkgs.runCommand "drivers-pi-skills-dir" { } "touch $out";

  # Issue #5481: the mount must stay the sessions subdir (see lib/drivers/pi.nix).
  drivers-pi-session-cache-dir =
    assert assertMsg ((entry.sessionCacheDirRelative or "") == ".pi/agent/sessions")
      "pi Driver's sessionCacheDirRelative must be pi's sessions subdir, got: ${
        entry.sessionCacheDirRelative or "<absent>"
      }";
    assert assertMsg (hasInfix "DRIVER_SESSION_CACHE_DIR=/home/agent/.pi/agent/sessions" preamble)
      "renderPreamble must bake pi's DRIVER_SESSION_CACHE_DIR, got: ${preamble}";
    pkgs.runCommand "drivers-pi-session-cache-dir" { } "touch $out";

  drivers-pi-argv-shape-pinned =
    let
      shape = entry.argvShape;
    in
    assert assertMsg (
      shape.promptStyle == "positional"
    ) "pi Driver's argvShape.promptStyle must stay \"positional\", got: ${shape.promptStyle}";
    assert assertMsg (!(shape ? promptFlag))
      "pi Driver's argvShape must not declare promptFlag for a positional prompt style, got: ${builtins.toJSON shape}";
    assert assertMsg (
      shape.modelFlag == "--model"
    ) "pi Driver's argvShape.modelFlag must stay \"--model\", got: ${shape.modelFlag}";
    assert assertMsg (!(shape ? agentsFlag))
      "pi Driver's argvShape must not declare agentsFlag (pi has no --agents equivalent), got: ${builtins.toJSON shape}";
    assert assertMsg (
      shape.effortFlag == "--thinking"
    ) "pi Driver's argvShape.effortFlag must stay \"--thinking\", got: ${shape.effortFlag}";
    assert assertMsg
      (
        shape.order == [
          "driverFlags"
          "model"
          "effort"
          "session"
          "prompt"
        ]
      )
      "pi Driver's argvShape.order must stay the 5-slot permutation excluding \"agents\", got: ${builtins.toJSON shape.order}";
    pkgs.runCommand "drivers-pi-argv-shape-pinned" { } "touch $out";

  # pi's effort values pass through unnormalised, so --thinking carries the
  # Consumer's string as written; the preamble must bake the flag verbatim.
  drivers-pi-render-preamble-argv =
    assert assertMsg (hasInfix "DRIVER_ARGV_MODEL_FLAG=--model\n" preamble)
      "renderPreamble must bake pi's model flag, got: ${preamble}";
    assert assertMsg (hasInfix "DRIVER_ARGV_EFFORT_FLAG=--thinking\n" preamble)
      "renderPreamble must bake pi's effort flag, got: ${preamble}";
    assert assertMsg (hasInfix "export PI_SKIP_VERSION_CHECK=1\n" preamble)
      "renderPreamble must export PI_SKIP_VERSION_CHECK, got: ${preamble}";
    assert assertMsg (hasInfix "export PI_TELEMETRY=0\n" preamble)
      "renderPreamble must export PI_TELEMETRY=0, got: ${preamble}";
    pkgs.runCommand "drivers-pi-render-preamble-argv" { } "touch $out";

  # Under pi no subagent is provisioned yet: every subagent role in the
  # default roster drops, so the bakes say so. The reviewer stays for the
  # review pass.
  drivers-pi-provisions-no-subagents =
    assert assertMsg (
      map (e: e.name) piHarness.internals.roster == [ "reviewer" ]
    ) "pi roster must hold only the reviewer: ${builtins.toJSON piHarness.internals.roster}";
    pkgs.runCommand "drivers-pi-provisions-no-subagents" { } ''
      runDoc=${piHarness.internals.runInputDocumentFile}

      grep -q '"SCOUT_PROVISIONED":"false"' "$runDoc"
      grep -q '"WORKER_PROVISIONED":"false"' "$runDoc"
      grep -q '"FILER_ENABLED":"false"' "$runDoc"

      touch $out
    '';
}
