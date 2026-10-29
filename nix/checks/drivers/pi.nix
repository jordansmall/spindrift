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
  inherit (pkgs.lib) assertMsg concatStringsSep hasInfix;
  inherit (fixtures) piFullRosterHarness;
  preamble = driverRegistry.renderPreamble entry;
  piFixtureEntry = {
    name = "fixture";
    model = "p/m";
    mode = "subagent";
    description = "fixture description: a\nb";
    tools = [
      "Read"
      "Bash"
    ];
    promptFile = "fixture-prompt.md";
    prompt = null;
  };
  # Shared by the spawns checks: tools that map to subagent, and the spawn
  # target the reviewer fixtures name.
  spawnerTools = [
    "Read"
    "Agent"
  ];
  axis = piFixtureEntry // {
    name = "review-axis";
  };
  renderers = import ../../../lib/renderers.nix;
  effortLib = import ../../../lib/effort-on-model.nix { inherit (pkgs) lib; };
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
    assert assertMsg (entry.subagents or false)
      "pi Driver must declare subagents = true so the full roster provisions through pi's subagent extension (issue #5483)";
    assert assertMsg ((entry.agentFilesDirRelative or "") == ".pi/agent/agents")
      "pi Driver's agentFilesDirRelative must be the directory the subagent extension scans, got: ${
        entry.agentFilesDirRelative or "<absent>"
      }";
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
    assert assertMsg
      (
        builtins.attrNames (entry.agentFilesTemplate { roster = [ ]; }) == [
          ".pi/agent/extensions/spindrift-hooks.js"
          ".pi/agent/spindrift-spawns.json"
        ]
      )
      "pi Driver's agent files for an empty roster must be only the hook bridge and the spawns policy (issues #5482, #5809)";
    assert assertMsg (
      (entry.agentFilesTemplate { roster = [ ]; }).".pi/agent/extensions/spindrift-hooks.js"
      == builtins.readFile ../../../agent/pi-hook-bridge.js
    ) "pi Driver must bake agent/pi-hook-bridge.js verbatim as the hook bridge (issue #5482)";
    assert assertMsg
      (
        (entry.packageLinks or { }) == {
          ".pi/agent/extensions/subagent" = "lib/node_modules/pi-monorepo/examples/extensions/subagent";
        }
      )
      "pi Driver must link the package's bundled subagent extension into the agent directory (issue #5483)";
    pkgs.runCommand "drivers-pi-attributes-pinned" { } "touch $out";

  # Issue #5483: each role renders as a Markdown agent file the subagent
  # extension discovers. Pins the frontmatter keys and the effort suffix; the
  # why is in lib/drivers/pi.nix's agentFilesTemplate.
  drivers-pi-agent-files-render =
    let
      rendered = entry.agentFilesTemplate {
        roster = [
          (piFixtureEntry // { effort = "high"; })
          (
            piFixtureEntry
            // {
              name = "plain";
              effort = "";
              tools = [
                "Glob"
                "Grep"
                "Agent"
                "WebFetch"
                "Edit"
                "MultiEdit"
              ];
            }
          )
          (removeAttrs piFixtureEntry [ "tools" ] // { name = "notools"; })
        ];
      };
      file = name: rendered.".pi/agent/agents/${name}.md" or "";
      effortFile = file "fixture";
      plainFile = file "plain";
      lines = f: pkgs.lib.splitString "\n" f;
    in
    assert assertMsg (builtins.elem "name: \"fixture\"" (
      lines effortFile
    )) "pi agent file must carry a JSON-encoded name, got: ${effortFile}";
    assert assertMsg (builtins.elem "description: \"fixture description: a\\nb\"" (lines effortFile))
      "pi agent file must carry a JSON-encoded description (no YAML key injection), got: ${effortFile}";
    assert assertMsg (builtins.elem "model: \"p/m:high\"" (lines effortFile))
      "pi agent file for a role with an effort must carry the <model>:<level> suffix, got: ${effortFile}";
    assert assertMsg (builtins.elem "reasoningEffort: \"high\"" (lines effortFile))
      "pi agent file for a role with an effort must carry reasoningEffort for the reviewer handoff, got: ${effortFile}";
    assert assertMsg (!(hasInfix "reasoningEffort" plainFile))
      "pi agent file for a role with an empty effort must carry no reasoningEffort line, got: ${plainFile}";
    assert assertMsg (!(hasInfix "reasoningEffort" (file "notools")))
      "pi agent file for a role without an effort key must carry no reasoningEffort line, got: ${file "notools"}";
    assert assertMsg (builtins.elem "tools: \"read, bash\"" (
      lines effortFile
    )) "pi agent file must map Claude tool names to pi's, got: ${effortFile}";
    assert assertMsg (builtins.elem "model: \"p/m\"" (
      lines plainFile
    )) "pi agent file for a role with an empty effort must carry the bare model, got: ${plainFile}";
    assert assertMsg (builtins.elem "tools: \"find, ls, grep, subagent, edit\"" (
      lines plainFile
    )) "pi agent file must dedupe mapped tools and drop ones pi has no tool for, got: ${plainFile}";
    assert assertMsg (!(hasInfix "tools:" (file "notools")))
      "pi agent file for a role without tools must carry no tools line (full tool set), got: ${file "notools"}";
    assert assertMsg (builtins.elem "model: \"p/m\"" (lines (file "notools")))
      "pi agent file for a role without an effort key must carry the bare model, got: ${file "notools"}";
    assert assertMsg (pkgs.lib.hasPrefix "---\nname: " effortFile)
      "pi agent file must open with its frontmatter fence, got: ${effortFile}";
    assert assertMsg (hasInfix "DRIVER_AGENT_FILES_DIR=/home/agent/.pi/agent/agents\n" preamble)
      "renderPreamble must bake pi's DRIVER_AGENT_FILES_DIR, got: ${preamble}";
    pkgs.runCommand "drivers-pi-agent-files-render" { } "touch $out";

  # An unmapped tool name must fail eval rather than silently change what the
  # subagent may do; a list mapping to no pi tool must too, since an empty
  # tools line would grant pi's whole tool set.
  drivers-pi-agent-files-unmapped-tool-throws =
    let
      renderWith =
        tools:
        let
          r = entry.agentFilesTemplate { roster = [ (piFixtureEntry // { inherit tools; }) ]; };
        in
        builtins.tryEval (builtins.deepSeq r r);
    in
    assert assertMsg (renderWith [ "Read" ]).success
      "pi agentFilesTemplate control (tools = [ Read ]) must render";
    assert assertMsg (
      !(renderWith [
        "Read"
        "NotebookEdit"
      ]).success
    ) "pi agentFilesTemplate must throw on a tool name with no pi mapping";
    assert assertMsg (
      !(renderWith [ "WebFetch" ]).success
    ) "pi agentFilesTemplate must throw when no listed tool maps to a pi tool";
    assert assertMsg (
      !(renderWith [ ]).success
    ) "pi agentFilesTemplate must throw on an empty tools list";
    pkgs.runCommand "drivers-pi-agent-files-unmapped-tool-throws" { } "touch $out";

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

  # pi has no env-native auth store: PI_AUTH_CONTENT is written to auth.json,
  # PI_MODELS_CONTENT to models.json, PI_SETTINGS_CONTENT to settings.json
  # (see lib/drivers/pi.nix).
  drivers-pi-env-files =
    assert assertMsg
      (
        (entry.envFiles or [ ]) == [
          {
            env = "PI_AUTH_CONTENT";
            pathRelative = ".pi/agent/auth.json";
          }
          {
            env = "PI_MODELS_CONTENT";
            pathRelative = ".pi/agent/models.json";
          }
          {
            env = "PI_SETTINGS_CONTENT";
            pathRelative = ".pi/agent/settings.json";
          }
        ]
      )
      "pi Driver must declare PI_AUTH_CONTENT -> .pi/agent/auth.json, PI_MODELS_CONTENT -> .pi/agent/models.json and PI_SETTINGS_CONTENT -> .pi/agent/settings.json in envFiles, got: ${
        builtins.toJSON (entry.envFiles or [ ])
      }";
    assert assertMsg
      (hasInfix "DRIVER_ENV_FILES='PI_AUTH_CONTENT=.pi/agent/auth.json PI_MODELS_CONTENT=.pi/agent/models.json PI_SETTINGS_CONTENT=.pi/agent/settings.json'\n" preamble)
      "renderPreamble must bake pi's DRIVER_ENV_FILES, got: ${preamble}";
    pkgs.runCommand "drivers-pi-env-files" { } "touch $out";

  # Preflight reads the OAuth allow-list from this datum (issue #5484).
  drivers-pi-auth-content =
    assert assertMsg
      (
        (entry.authContent or null) == {
          env = "PI_AUTH_CONTENT";
          oauthProviders = [ "github-copilot" ];
          apiKeyProviders = {
            "openai-codex" = "openai";
          };
        }
      )
      "pi Driver must allow OAuth in PI_AUTH_CONTENT for github-copilot only, got: ${
        builtins.toJSON (entry.authContent or null)
      }";
    pkgs.runCommand "drivers-pi-auth-content" { } "touch $out";

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

  # Issue #5483: provisioning follows the roster under pi as under the other
  # Drivers. piFullRosterHarness has Provider-namespaced models for every role, so the
  # bare-worker gate (issue #5331) drops nothing.
  drivers-pi-provisions-full-roster =
    let
      inherit (piFullRosterHarness.internals) roster driverEntry;
      names = map (e: e.name) roster;
      # Rendered at eval rather than read from internals.agentFiles, which is
      # built for Linux and so cannot be realized by a darwin flake check.
      rendered = driverEntry.agentFilesTemplate { inherit roster; };
      agentFiles = builtins.attrNames rendered;
      workerFile = rendered.".pi/agent/agents/worker.md" or "";
      roles = [
        "scout"
        "reviewer"
        "filer"
        "worker"
      ];
    in
    assert assertMsg (builtins.all (
      n: builtins.elem n names
    ) roles) "pi roster must hold the full roster: ${builtins.toJSON names}";
    assert assertMsg (builtins.all (
      n: builtins.elem ".pi/agent/agents/${n}.md" agentFiles
    ) roles) "pi agentFiles must hold an agent file per role: ${builtins.toJSON agentFiles}";
    # Issue #6182: the worker's Skill grant maps to no pi tool, so its line is unchanged.
    assert assertMsg (hasInfix "tools: \"read, bash, edit, write, find, ls, grep\"\n" workerFile)
      "pi worker must keep its tools line, got: ${workerFile}";
    pkgs.runCommand "drivers-pi-provisions-full-roster" { } ''
      runDoc=${piFullRosterHarness.internals.runInputDocumentFile}

      grep -q '"SCOUT_PROVISIONED":"true"' "$runDoc"
      grep -q '"WORKER_PROVISIONED":"true"' "$runDoc"
      grep -q '"FILER_ENABLED":"true"' "$runDoc"

      touch $out
    '';

  # Issue #5483: evaluation rejects an unknown effort level (rationale in
  # lib/drivers/pi.nix). A bad reviewEffort is the reject fixture; it differs
  # from the passing one by that single value.
  drivers-pi-rejects-unknown-effort-level =
    let
      evalsWith =
        reviewEffort:
        builtins.tryEval (
          builtins.deepSeq (map (e: e.effort or "") (
            (fixtures.piHarnessWith (fixtures.opencodeNamespacedDefaults // { inherit reviewEffort; }))
            .internals.roster
          )) true
        );
    in
    assert assertMsg (evalsWith "high").success "a valid reviewEffort must evaluate under pi";
    assert assertMsg (
      !(evalsWith "bogus").success
    ) "an unknown reviewEffort level must fail eval under pi, not reach the model id";
    assert assertMsg (
      hasInfix "fixture" (effortLib.message.badLevel "fixture" "bogus" [ "high" ])
      && hasInfix "bogus" (effortLib.message.badLevel "fixture" "bogus" [ "high" ])
    ) "the bad-level message must name the role and the effort";
    pkgs.runCommand "drivers-pi-rejects-unknown-effort-level" { } "touch $out";

  # An opted-out role (model "") loses its default effort with it.
  drivers-pi-opt-out-role-drops-its-effort =
    let
      names = map (e: e.name) (
        (fixtures.piHarnessWith (fixtures.opencodeNamespacedDefaults // { filerModel = ""; }))
        .internals.roster
      );
    in
    assert assertMsg (
      !(builtins.elem "filer" names)
    ) "an opted-out filer must stay dropped under pi, got roster: ${builtins.toJSON names}";
    pkgs.runCommand "drivers-pi-opt-out-role-drops-its-effort" { } "touch $out";

  # Issue #5368: pi's per-agent files are unaffected by `spawns`; enforcement
  # lives in the separate spindrift-spawns.json policy file (issue #5809).
  drivers-pi-agent-files-ignore-spawns =
    let
      render =
        e:
        pkgs.lib.filterAttrs (n: _: pkgs.lib.hasPrefix ".pi/agent/agents/" n) (
          entry.agentFilesTemplate { roster = [ e ]; }
        );
    in
    assert assertMsg (
      render piFixtureEntry == render (piFixtureEntry // { spawns = [ "fixture" ]; })
    ) "pi agentFilesTemplate must render identical agent files when an entry carries spawns";
    pkgs.runCommand "drivers-pi-agent-files-ignore-spawns" { } "touch $out";

  # Issue #5809: the hook bridge reads this file to gate which agents a
  # roster entry's subagent tool may spawn; an entry absent from it is
  # unrestricted, and the file is always written so the bridge can fail closed
  # when it is missing.
  drivers-pi-spawns-policy =
    let
      policyPath = ".pi/agent/spindrift-spawns.json";
      policy = roster: (entry.agentFilesTemplate { inherit roster; }).${policyPath} or "<absent>";
      reviewer = piFixtureEntry // {
        name = "reviewer";
        spawns = [ "review-axis" ];
        tools = spawnerTools;
      };
    in
    assert assertMsg (
      policy [
        reviewer
        axis
      ] == ''{"reviewer":["review-axis"]}''
    ) "pi spawns policy must map each entry declaring spawns to its list and omit the rest";
    assert assertMsg (
      policy [
        axis
        (removeAttrs reviewer [ "spawns" ])
      ] == "{}"
    ) "pi spawns policy must render {} when no entry declares spawns, not be absent";
    assert assertMsg (
      policy [ (reviewer // { spawns = [ ]; }) ] == ''{"reviewer":[]}''
    ) "pi spawns policy must keep an empty spawns list (spawn nothing), not drop it as unrestricted";
    pkgs.runCommand "drivers-pi-spawns-policy" { } "touch $out";

  # Issue #5809: the bridge enforces the rendered policy at run time. The
  # policy is the real one for a reviewer that may spawn review-axis and a scout
  # that may spawn nothing; the bridge is staged with its SPAWNS_FILE pointed at
  # it, as opencode-guards.nix does for HOOKS_DIR.
  drivers-pi-hook-bridge-spawns-test =
    let
      withSpawns =
        name: spawns:
        piFixtureEntry
        // {
          inherit name spawns;
          tools = spawnerTools;
        };
      policy = pkgs.writeText "spindrift-spawns.json" (
        (entry.agentFilesTemplate {
          roster = [
            (withSpawns "reviewer" [ "review-axis" ])
            (withSpawns "scout" [ ])
            axis
            (piFixtureEntry // { name = "worker"; })
          ];
        }).".pi/agent/spindrift-spawns.json"
      );
      # A caller's value that is not a list must be refused, not read as unrestricted.
      badPolicy = pkgs.writeText "spindrift-spawns-bad.json" ''{"reviewer":"review-axis"}'';
    in
    pkgs.runCommand "drivers-pi-hook-bridge-spawns-test"
      {
        nativeBuildInputs = [
          pkgs.gnugrep
          pkgs.gnused
        ];
      }
      ''
        stage() {
          sed "s|^const SPAWNS_FILE = .*|const SPAWNS_FILE = \"$2\";|" ${../../../agent/pi-hook-bridge.js} > "$1"
          grep -qF "const SPAWNS_FILE = \"$2\";" "$1"
        }
        stage $TMPDIR/bridge.mjs ${policy}
        stage $TMPDIR/bridge-missing.mjs $TMPDIR/no-such-policy.json
        stage $TMPDIR/bridge-bad.mjs ${badPolicy}
        BRIDGE=$TMPDIR/bridge.mjs BRIDGE_MISSING=$TMPDIR/bridge-missing.mjs \
          BRIDGE_BAD=$TMPDIR/bridge-bad.mjs \
          ${pkgs.nodejs}/bin/node ${../../../tests/pi-hook-bridge-spawns.test.mjs}
        touch $out
      '';

  # A spawns target absent from the roster must fail eval, as under opencode;
  # an entry that cannot call subagent has no spawn to check. pi hands a
  # subagent its identity only through a non-empty body, so spawns on an entry
  # with a blank description could never be enforced.
  drivers-pi-spawns-policy-throws =
    let
      renders =
        roster:
        let
          r = entry.agentFilesTemplate { inherit roster; };
        in
        (builtins.tryEval (builtins.deepSeq r r)).success;
      parent = piFixtureEntry // {
        name = "parent";
        spawns = [ "review-axis" ];
        tools = [ "Agent" ];
      };
    in
    assert assertMsg (renders [
      parent
      axis
    ]) "pi spawns control (target in roster) must render";
    assert assertMsg (
      !(renders [ parent ])
    ) "pi must throw when spawns names an entry absent from the roster";
    assert assertMsg (
      !(renders [ (removeAttrs parent [ "tools" ]) ])
    ) "pi must throw on an absent spawns target when the entry has no tools key (full tool set)";
    assert assertMsg (renders [
      (parent // { tools = [ "Read" ]; })
    ]) "pi must not throw on an absent spawns target when no tool maps to subagent";
    assert assertMsg (
      !(renders [
        (parent // { description = "  \n"; })
        axis
      ])
    ) "pi must throw when an entry declares spawns but has a blank description";
    assert assertMsg (
      !(renders [
        (removeAttrs parent [ "description" ])
        axis
      ])
    ) "pi must throw when an entry declares spawns but has no description";
    pkgs.runCommand "drivers-pi-spawns-policy-throws" { } "touch $out";

  # Issue #5811: a model already ending in a known `:<level>` keeps that
  # suffix and the role's effort is dropped, so no `:low:high` doubling.
  drivers-pi-effort-suffixed-model-wins =
    let
      render =
        model: effort:
        let
          resolved = effortLib.applyEffort {
            roster = [ (piFixtureEntry // { inherit model effort; }) ];
            levels = entry.effortOnModelLevels;
          };
        in
        (entry.agentFilesTemplate { roster = resolved; }).".pi/agent/agents/fixture.md";
      own = render "github-copilot/some-model:low" "high";
      tag = render "ollama/llama:8b" "high";
      noEffort = render "github-copilot/some-model:low" "";
      rejectsBad =
        !(builtins.tryEval (builtins.deepSeq (render "github-copilot/some-model:low" "bogus") true))
        .success;
      harness = fixtures.piHarnessWith (
        fixtures.opencodeNamespacedDefaults // { reviewModel = "anthropic/claude-opus-5:low"; }
      );
      reviewer =
        (harness.internals.driverEntry.agentFilesTemplate { inherit (harness.internals) roster; })
        .".pi/agent/agents/reviewer.md";
    in
    assert assertMsg (
      hasInfix ''model: "github-copilot/some-model:low"'' own
      && hasInfix ''reasoningEffort: "low"'' own
      && !(hasInfix ":low:high" own)
    ) "the model's own suffix must win over the role effort, got: ${own}";
    assert assertMsg (hasInfix ''model: "ollama/llama:8b:high"'' tag)
      "a colon tag that is not a level is part of the model id, got: ${tag}";
    assert assertMsg (
      hasInfix ''model: "github-copilot/some-model:low"'' noEffort
      && !(hasInfix "reasoningEffort" noEffort)
    ) "a suffixed model with no effort renders as-is, got: ${noEffort}";
    assert assertMsg rejectsBad "an invalid role effort must fail even when the model carries a suffix";
    assert assertMsg (
      hasInfix ''model: "anthropic/claude-opus-5:low"'' reviewer
      && hasInfix ''reasoningEffort: "low"'' reviewer
      && !(hasInfix ":low:high" reviewer)
    ) "a suffixed reviewModel must render once through mkHarness, got: ${reviewer}";
    pkgs.runCommand "drivers-pi-effort-suffixed-model-wins" { } "touch $out";

  # Issue #5625: Go reads the levels from the generated Descriptor, so the pi
  # row's own list must reach descriptor_gen.go's pi Descriptor.
  drivers-pi-effort-on-model-levels-reach-descriptor =
    let
      rendered = renderers.renderDriverDescriptorsGo driverRegistry.entries;
      levels = "[]string{${concatStringsSep ", " (map builtins.toJSON entry.effortOnModelLevels)}}";
    in
    assert assertMsg (hasInfix "EffortOnModelLevels: ${levels}," rendered)
      "the rendered pi Descriptor must carry the row's effortOnModelLevels ${levels}";
    pkgs.runCommand "drivers-pi-effort-on-model-levels-reach-descriptor" { } "touch $out";
}
