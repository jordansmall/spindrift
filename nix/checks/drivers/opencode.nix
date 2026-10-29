# OpenCode Driver's eval-level pins (issue #5502, ADR 0066): the checks that
# read only the opencode registry row.
{
  pkgs,
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
    escapeShellArg
    all
    hasSuffix
    removeSuffix
    stringToCharacters
    ;
  # Shared opencode agentFilesTemplate fixtures (issue #5347, following the
  # #3348 shared-fixture pattern): a roster entry without `tools`, which each
  # permission check extends, and the permission-line parse. The no-tools check
  # uses the entry bare.
  opencodeFixtureEntry = {
    name = "fixture";
    model = "fixture-model";
    mode = "subagent";
    description = "fixture description";
    promptFile = "fixture-prompt.md";
    prompt = null;
  };
  opencodePermissionLineOf =
    file: pkgs.lib.findFirst (pkgs.lib.hasPrefix "permission: ") null (splitString "\n" file);
  opencodePermissionOf =
    rendered: agent:
    let
      file = rendered.".config/opencode/agents/${agent}.md" or "";
      line = opencodePermissionLineOf file;
    in
    if line == null then
      throw "opencode ${agent}.md carries no permission line, got: ${file}"
    else
      builtins.fromJSON (pkgs.lib.removePrefix "permission: " line);
in
{
  # Issue #5351: opencode's Bash-timeout env is an EXPERIMENTAL-prefixed flag
  # a nixpkgs bump could rename or drop, silently restoring the 2-minute
  # default. The built binary holds no plain-text copy of the name, so this
  # greps the pinned source for the env read and the default it overrides.
  drivers-opencode-src-reads-bash-timeout-env =
    let
      src = (entry.package pkgs).src;
      envVarName = builtins.head entry.bashTimeoutEnv;
      # Joined to the field name, so an env read into some other field fails.
      envRead = ''bashDefaultTimeoutMs: positiveInteger("${envVarName}")'';
      defaultFallback = "flags.bashDefaultTimeoutMs ?? 2 * 60 * 1000";
    in
    pkgs.runCommand "drivers-opencode-src-reads-bash-timeout-env"
      { nativeBuildInputs = [ pkgs.gnugrep ]; }
      ''
        pkg=${src}/packages/opencode/src
        if ! grep -qF ${escapeShellArg envRead} "$pkg/effect/runtime-flags.ts"; then
          echo "pinned opencode no longer reads ${envVarName} in effect/runtime-flags.ts -- lib/drivers/opencode.nix's bashTimeoutEnv relies on it (issue #5351); re-verify against the new source and update the Driver" >&2
          exit 1
        fi
        if ! grep -qF ${escapeShellArg defaultFallback} "$pkg/tool/shell.ts"; then
          echo "pinned opencode's tool/shell.ts no longer defaults the bash timeout via bashDefaultTimeoutMs -- lib/drivers/opencode.nix's bashTimeoutEnv may be inert (issue #5351); re-verify against the new source and update the Driver" >&2
          exit 1
        fi
        touch $out
      '';

  # Issue #262 slice 5 (AC4): opencode has no --agents JSON flag, so it
  # composes subagents from on-disk agents/*.md files under $HOME. Rendering
  # scout must produce frontmatter carrying `mode: "subagent"`, the model
  # JSON-encoded (issue #2152 slice C), and the same description lib/drivers/claude.nix
  # uses, so both Drivers frame subagents identically. Fixture descriptions
  # here are synthetic on purpose: see roster-fixtures-carry-no-live-descriptions
  # in nix/checks/roster.nix.
  drivers-opencode-agent-files-scout-frontmatter =
    let
      rendered = entry.agentFilesTemplate {
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
            name = "reviewer";
            model = "";
            mode = "subagent";
            description = "fixture reviewer description";
            tools = [
              "Read"
              "Bash"
              "WebFetch"
              "Agent"
            ];
            promptFile = "review-prompt.md";
            prompt = null;
          }
        ];
      };
      scoutFile = rendered.".config/opencode/agents/scout.md" or "";
    in
    assert assertMsg (hasInfix ''mode: "subagent"'' scoutFile)
      "opencode agentFilesTemplate's scout.md must set mode: \"subagent\" (JSON-encoded, issue #2152 slice C), got: ${scoutFile}";
    assert assertMsg (hasInfix ''model: "solo-scout-model"'' scoutFile)
      "opencode agentFilesTemplate's scout.md must carry the scout model JSON-encoded (issue #2152 slice C), got: ${scoutFile}";
    assert assertMsg (hasInfix "fixture scout description" scoutFile)
      "opencode agentFilesTemplate's scout.md must carry the scout description, got: ${scoutFile}";
    pkgs.runCommand "drivers-opencode-agent-files-scout-frontmatter" { } "touch $out";

  # Issue #5347: opencode subagents default to the full tool set, so each
  # roster entry's `tools` list must render as an explicit `permission` map;
  # a reviewer holding no Edit/Write must therefore render `edit: deny`.
  drivers-opencode-agent-files-default-roster-permissions =
    let
      rendered = entry.agentFilesTemplate { roster = defaultRosterFixture; };
      expectedEdit = {
        reviewer = "deny";
        scout = "deny";
        filer = "deny";
        review-axis = "deny";
        worker = "allow";
      };
    in
    assert builtins.all (
      agent:
      let
        permission = opencodePermissionOf rendered agent;
      in
      assertMsg (permission.edit == expectedEdit.${agent})
        "opencode ${agent} agent must render edit: ${expectedEdit.${agent}}, got: ${builtins.toJSON permission}"
    ) (builtins.attrNames expectedEdit);
    # Issue #5368: the default reviewer declares `spawns = [ "review-axis" ]`,
    # so its task grant must name only that target, never worker. Matched as a
    # string so the key order (`*` first, opencode's last-match-wins) is pinned.
    assert assertMsg (hasInfix ''"task":{"*":"deny","review-axis":"allow"}'' (
      rendered.".config/opencode/agents/reviewer.md" or ""
    )) "opencode reviewer must render a scoped task map reaching only review-axis";
    assert builtins.all (
      agent:
      assertMsg (
        (opencodePermissionOf rendered agent).task == "deny"
      ) "opencode ${agent} holds no Agent tool and must render task: deny"
    ) (filter (a: a != "reviewer") (builtins.attrNames expectedEdit));
    # Issue #6182: the worker's roster Skill grant reaches opencode as skill.
    assert assertMsg ((opencodePermissionOf rendered "worker").skill == "allow")
      "opencode worker must render skill: allow, got: ${builtins.toJSON (opencodePermissionOf rendered "worker")}";
    pkgs.runCommand "drivers-opencode-agent-files-default-roster-permissions" { } "touch $out";

  # An explicit tool list maps to exactly the allow set; every other mapped
  # opencode key is denied because opencode treats an unset key as allow.
  drivers-opencode-agent-files-permission-map =
    let
      rendered = entry.agentFilesTemplate {
        roster = [
          (
            opencodeFixtureEntry
            // {
              tools = [
                "Read"
                "Agent"
              ];
            }
          )
        ];
      };
      parsed = opencodePermissionOf rendered "fixture";
      expected = {
        read = "allow";
        task = "allow";
        edit = "deny";
        bash = "deny";
        glob = "deny";
        grep = "deny";
        webfetch = "deny";
        websearch = "deny";
        todowrite = "deny";
        skill = "deny";
      };
    in
    assert assertMsg (parsed == expected)
      "opencode permission map for [Read Agent] must allow only read/task, got: ${builtins.toJSON parsed}";
    pkgs.runCommand "drivers-opencode-agent-files-permission-map" { } "touch $out";

  # Issue #5368: an entry holding the task tool and declaring `spawns` gets task
  # as a map denying everything but the named targets; `spawns` alone, without
  # a task-mapped tool, grants nothing.
  drivers-opencode-agent-files-spawns-scopes-task =
    let
      target = opencodeFixtureEntry // {
        name = "x";
        tools = [ "Read" ];
      };
      render =
        spawns: tools:
        entry.agentFilesTemplate {
          roster = [
            (
              opencodeFixtureEntry
              // {
                inherit tools;
                inherit spawns;
              }
            )
            target
          ];
        };
      agentTools = [
        "Read"
        "Agent"
      ];
      scoped = render [ "x" ] agentTools;
      scopedFile = scoped.".config/opencode/agents/fixture.md";
      noTask = opencodePermissionOf (render [ "x" ] [ "Read" ]) "fixture";
      spawnsNone = opencodePermissionOf (render [ ] agentTools) "fixture";
      # Omitted tools leave task allowed, so spawns alone must still narrow it
      # (and set no other key).
      bare = entry.agentFilesTemplate {
        roster = [
          (opencodeFixtureEntry // { spawns = [ "x" ]; })
          target
        ];
      };
      bareFile = bare.".config/opencode/agents/fixture.md";
    in
    assert assertMsg (
      opencodePermissionOf bare "fixture" == {
        task = {
          "*" = "deny";
          x = "allow";
        };
      }
    ) "opencode spawns without tools must render only a scoped task map, got: ${bareFile}";
    assert assertMsg (hasInfix ''permission: {"task":{"*":"deny","x":"allow"}}'' bareFile)
      "opencode spawns without tools must render the task-only permission line, got: ${bareFile}";
    assert assertMsg (hasInfix ''"task":{"*":"deny","x":"allow"}'' scopedFile)
      "opencode [Read Agent] with spawns = [ x ] must render a scoped task map, got: ${scopedFile}";
    assert assertMsg (
      (opencodePermissionOf scoped "x").task == "deny"
    ) "opencode entry without spawns or Agent must render task: deny";
    assert assertMsg (
      noTask.task == "deny"
    ) "opencode spawns without a task-mapped tool must keep task: deny, got: ${builtins.toJSON noTask}";
    assert assertMsg (spawnsNone.task == { "*" = "deny"; })
      "opencode [Read Agent] with spawns = [ ] must deny every subagent, got: ${builtins.toJSON spawnsNone}";
    pkgs.runCommand "drivers-opencode-agent-files-spawns-scopes-task" { } "touch $out";

  # A spawn target absent from the roster would leave the entry no subagent at
  # all, so the render must fail rather than hand the fan-out nothing to run on.
  drivers-opencode-agent-files-spawns-missing-target-throws =
    let
      rendersWith =
        extra: spawns:
        let
          rendered = entry.agentFilesTemplate {
            roster = [
              (opencodeFixtureEntry // extra // { inherit spawns; })
            ];
          };
        in
        builtins.tryEval (builtins.deepSeq rendered rendered);
      renders = rendersWith { tools = [ "Agent" ]; };
    in
    # Omitted tools leave task allowed, so the check must fire there too.
    assert assertMsg (rendersWith { } [ "fixture" ]).success
      "opencode agentFilesTemplate control (no tools, spawns naming a roster entry) must render";
    assert assertMsg (
      !(rendersWith { } [ "absent" ]).success
    ) "opencode agentFilesTemplate must throw when an entry without tools spawns an absent entry";
    # With task not granted, spawns grants nothing, so an absent target is moot.
    assert assertMsg (rendersWith { tools = [ "Read" ]; } [ "absent" ]).success
      "opencode agentFilesTemplate must not throw on an absent spawns target when no tool maps to task";
    assert assertMsg (renders [ "fixture" ]).success
      "opencode agentFilesTemplate control (spawns naming a roster entry) must render";
    assert assertMsg (
      !(renders [ "absent" ]).success
    ) "opencode agentFilesTemplate must throw when spawns names an entry absent from the roster";
    pkgs.runCommand "drivers-opencode-agent-files-spawns-missing-target-throws" { } "touch $out";

  # A tool name with no opencode mapping must fail the render rather than be
  # dropped, which would silently widen or narrow the subagent's grant.
  drivers-opencode-agent-files-unmapped-tool-throws =
    let
      rendered = entry.agentFilesTemplate {
        roster = [
          (
            opencodeFixtureEntry
            // {
              tools = [
                "Read"
                "NotebookEdit"
              ];
            }
          )
        ];
      };
      result = builtins.tryEval (builtins.deepSeq rendered rendered);
      # Same fixture minus the unmapped tool: it must render, so the failure
      # above is attributable to NotebookEdit and not a fixture typo.
      control =
        let
          ok = entry.agentFilesTemplate {
            roster = [ (opencodeFixtureEntry // { tools = [ "Read" ]; }) ];
          };
        in
        builtins.tryEval (builtins.deepSeq ok ok);
    in
    assert assertMsg control.success
      "opencode agentFilesTemplate control (tools = [ Read ]) must render";
    assert assertMsg (
      !result.success
    ) "opencode agentFilesTemplate must throw on a tool name with no opencode permission mapping";
    pkgs.runCommand "drivers-opencode-agent-files-unmapped-tool-throws" { } "touch $out";

  # An entry without `tools` keeps opencode's full tool set, so it must render
  # no permission line at all.
  drivers-opencode-agent-files-no-tools-no-permission =
    let
      rendered = entry.agentFilesTemplate {
        roster = [ opencodeFixtureEntry ];
      };
      file = rendered.".config/opencode/agents/fixture.md" or "";
    in
    assert assertMsg (file != "") "opencode fixture.md must render for an entry without tools";
    assert assertMsg (
      opencodePermissionLineOf file == null
    ) "opencode entry without tools must render no permission line, got: ${file}";
    pkgs.runCommand "drivers-opencode-agent-files-no-tools-no-permission" { } "touch $out";

  # The empty-model-entry-dropped invariant (#392) lives at the dropOptedOut
  # layer, in nix/checks/roster.nix, not here.

  # Issue #2242 slice 2: a roster entry may set an optional `effort` field
  # alongside `model`. opencode has no --agents JSON flag, so this Driver
  # passes it through as a `reasoningEffort` frontmatter scalar instead,
  # JSON-encoded like `model` and `mode`. The key name differs because
  # opencode reads provider-native passthrough keys directly on the agent.
  drivers-opencode-agent-files-effort-present =
    let
      rendered = entry.agentFilesTemplate {
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
      scoutFile = rendered.".config/opencode/agents/scout.md" or "";
    in
    assert assertMsg (hasInfix ''reasoningEffort: "high"'' scoutFile)
      "opencode agentFilesTemplate's scout.md must carry a roster entry's effort field as a JSON-encoded reasoningEffort frontmatter scalar, got: ${scoutFile}";
    pkgs.runCommand "drivers-opencode-agent-files-effort-present" { } "touch $out";

  # Issue #2242 slice 2: an entry that omits `effort` (or sets it to "") must
  # carry no `reasoningEffort` line at all, keeping output byte-stable
  # (mirrors drivers-claude-agents-json-effort-absent).
  drivers-opencode-agent-files-effort-absent =
    let
      rendered = entry.agentFilesTemplate {
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
      scoutFile = rendered.".config/opencode/agents/scout.md" or "";
      filerFile = rendered.".config/opencode/agents/filer.md" or "";
    in
    assert assertMsg (!(hasInfix "reasoningEffort:" scoutFile))
      "opencode agentFilesTemplate must omit the reasoningEffort line entirely when a roster entry doesn't set effort, got: ${scoutFile}";
    assert assertMsg (!(hasInfix "reasoningEffort:" filerFile))
      "opencode agentFilesTemplate must omit the reasoningEffort line entirely when a roster entry sets effort to the empty string, got: ${filerFile}";
    pkgs.runCommand "drivers-opencode-agent-files-effort-absent" { } "touch $out";

  # Same injection as drivers-opencode-agent-files-escapes-model, but through
  # the `effort` frontmatter scalar instead of `model`.
  drivers-opencode-agent-files-escapes-effort =
    let
      payload = "evil\"\ninjected: true";
      rendered = entry.agentFilesTemplate {
        roster = [
          {
            name = "escapee";
            model = "escapee-model";
            effort = payload;
            mode = "subagent";
            description = "Escapee";
            tools = [ "Read" ];
            promptFile = "escapee-prompt.md";
            prompt = null;
          }
        ];
      };
      file = rendered.".config/opencode/agents/escapee.md" or "";
    in
    assert assertMsg (hasInfix (builtins.toJSON payload) file)
      "opencode agentFilesTemplate must JSON-encode an effort value carrying a quote/newline, got: ${file}";
    assert assertMsg (!(hasInfix "\ninjected: true" file))
      "opencode agentFilesTemplate must not let an effort value's embedded newline inject a raw YAML key, got: ${file}";
    pkgs.runCommand "drivers-opencode-agent-files-escapes-effort" { } "touch $out";

  # A raw, unquoted colon in a description splits the frontmatter line into
  # two mapping keys and yields malformed YAML (issue #264 review finding),
  # so agentFilesTemplate must JSON-encode the description; JSON is a valid
  # YAML scalar.
  drivers-opencode-agent-files-description-colon-frontmatter =
    let
      rendered = entry.agentFilesTemplate {
        roster = [
          {
            name = "auditor";
            model = "audit-model";
            mode = "subagent";
            description = "Audit: colon";
            tools = [ "Read" ];
            promptFile = "auditor-prompt.md";
            prompt = null;
          }
        ];
      };
      auditorFile = rendered.".config/opencode/agents/auditor.md" or "";
    in
    assert assertMsg (hasInfix ''description: "Audit: colon"'' auditorFile)
      "opencode agentFilesTemplate's frontmatter description must be JSON-quoted so an embedded colon can't break YAML, got: ${auditorFile}";
    pkgs.runCommand "drivers-opencode-agent-files-description-colon-frontmatter" { } "touch $out";

  # Issue #2153: renderPreamble must bake DRIVER_AGENT_FILES_DIR from the real
  # opencode entry's agentFilesDirRelative, because box's
  # file-rewrite loop is gated entirely on that var being non-empty. The baked
  # value must match agentFilesTemplate's own on-disk path, or box
  # rewrites files the template never bakes.
  drivers-render-preamble-opencode-agent-files-dir =
    let
      out = driverRegistry.renderPreamble entry;
    in
    assert assertMsg (hasInfix "DRIVER_AGENT_FILES_DIR=/home/agent/.config/opencode/agents" out)
      "renderPreamble must bake DRIVER_AGENT_FILES_DIR from the opencode Driver entry's agentFilesDirRelative under /home/agent, got: ${out}";
    pkgs.runCommand "drivers-render-preamble-opencode-agent-files-dir" { } "touch $out";

  # Issue #5349: opencode's corrective resume reads its SQLite store from a
  # dedicated subdir (so the per-issue mount cannot shadow auth.json); the
  # baked preamble must carry both the mount path and the OPENCODE_DB export.
  drivers-render-preamble-session-cache-dir-opencode =
    let
      out = driverRegistry.renderPreamble entry;
    in
    assert assertMsg
      (hasInfix "DRIVER_SESSION_CACHE_DIR=/home/agent/.local/share/opencode/spindrift-session" out)
      "renderPreamble must bake DRIVER_SESSION_CACHE_DIR from the opencode Driver entry's sessionCacheDirRelative under /home/agent, got: ${out}";
    assert assertMsg
      (hasInfix "export OPENCODE_DB=/home/agent/.local/share/opencode/spindrift-session/opencode.db" out)
      "renderPreamble must export OPENCODE_DB inside the opencode session cache dir, got: ${out}";
    pkgs.runCommand "drivers-render-preamble-session-cache-dir-opencode" { } "touch $out";

  # Pins opencode's sessionCacheDirRelative (issue #5349) and that OPENCODE_DB
  # sits inside it. The Go resume lookup reads OPENCODE_DB itself, so this pin
  # is the whole tie to the Go side. Claude's ".claude/projects" value is
  # pinned by drivers-render-preamble-session-cache-dir.
  drivers-session-cache-dir-relative-opencode =
    assert assertMsg
      ((entry.sessionCacheDirRelative or null) == ".local/share/opencode/spindrift-session")
      "opencode Driver entry's sessionCacheDirRelative must be .local/share/opencode/spindrift-session, got: ${
        builtins.toJSON (entry.sessionCacheDirRelative or null)
      }";
    assert assertMsg
      (
        (entry.envCommon.OPENCODE_DB or null) == "/home/agent/${entry.sessionCacheDirRelative}/opencode.db"
      )
      "opencode Driver entry's OPENCODE_DB must sit inside its sessionCacheDirRelative, got: ${
        builtins.toJSON (entry.envCommon or null)
      }";
    pkgs.runCommand "drivers-session-cache-dir-relative-opencode" { } "touch $out";

  # Issue #5374: opencode spawns formatters and LSP servers with the Box's full
  # env, so envCommon pins both off through OPENCODE_CONFIG_CONTENT, which
  # outranks a Target repo's opencode.json. opencode-formatter-lsp-pin proves
  # the behaviour; this keeps the value from vanishing.
  drivers-opencode-formatter-lsp-pinned-off =
    assert assertMsg
      (
        builtins.fromJSON (entry.envCommon.OPENCODE_CONFIG_CONTENT or "null") == {
          formatter = false;
          lsp = false;
        }
      )
      "opencode Driver entry's envCommon must pin OPENCODE_CONFIG_CONTENT to {formatter:false,lsp:false}, got: ${
        builtins.toJSON (entry.envCommon or null)
      }";
    assert assertMsg (hasInfix "export OPENCODE_CONFIG_CONTENT=" (
      driverRegistry.renderPreamble entry
    )) "renderPreamble must export OPENCODE_CONFIG_CONTENT from the opencode Driver entry";
    pkgs.runCommand "drivers-opencode-formatter-lsp-pinned-off" { } "touch $out";

  # Issue #2152 slice C: opencode's agentFilesTemplate JSON-encodes the
  # `model` frontmatter scalar, so a model value carrying a double quote and
  # a newline cannot break out of the YAML scalar and inject a second
  # frontmatter key.
  drivers-opencode-agent-files-escapes-model =
    let
      payload = "evil\"\ninjected: true";
      rendered = entry.agentFilesTemplate {
        roster = [
          {
            name = "escapee";
            model = payload;
            mode = "subagent";
            description = "Escapee";
            tools = [ "Read" ];
            promptFile = "escapee-prompt.md";
            prompt = null;
          }
        ];
      };
      file = rendered.".config/opencode/agents/escapee.md" or "";
    in
    assert assertMsg (hasInfix (builtins.toJSON payload) file)
      "opencode agentFilesTemplate must JSON-encode a model value carrying a quote/newline, got: ${file}";
    assert assertMsg (!(hasInfix "\ninjected: true" file))
      "opencode agentFilesTemplate must not let a model value's embedded newline inject a raw YAML key, got: ${file}";
    pkgs.runCommand "drivers-opencode-agent-files-escapes-model" { } "touch $out";

  # Same injection as drivers-opencode-agent-files-escapes-model, but through
  # the `mode` frontmatter scalar instead of `model`.
  drivers-opencode-agent-files-escapes-mode =
    let
      payload = "evil\"\ninjected: true";
      rendered = entry.agentFilesTemplate {
        roster = [
          {
            name = "escapee";
            model = "escapee-model";
            mode = payload;
            description = "Escapee";
            tools = [ "Read" ];
            promptFile = "escapee-prompt.md";
            prompt = null;
          }
        ];
      };
      file = rendered.".config/opencode/agents/escapee.md" or "";
    in
    assert assertMsg (hasInfix (builtins.toJSON payload) file)
      "opencode agentFilesTemplate must JSON-encode a mode value carrying a quote/newline, got: ${file}";
    assert assertMsg (!(hasInfix "\ninjected: true" file))
      "opencode agentFilesTemplate must not let a mode value's embedded newline inject a raw YAML key, got: ${file}";
    pkgs.runCommand "drivers-opencode-agent-files-escapes-mode" { } "touch $out";

  # Same defaultRoster defaults as
  # drivers-claude-agents-json-default-roster-effort, but through opencode's
  # reasoningEffort frontmatter scalar instead of claude's --agents JSON.
  drivers-opencode-agent-files-default-roster-effort =
    let
      roster = defaultRosterFixture;
      rendered = entry.agentFilesTemplate { inherit roster; };
      scoutFile = rendered.".config/opencode/agents/scout.md" or "";
      reviewerFile = rendered.".config/opencode/agents/reviewer.md" or "";
      filerFile = rendered.".config/opencode/agents/filer.md" or "";
      workerFile = rendered.".config/opencode/agents/worker.md" or "";
    in
    assert assertMsg
      (hasInfix ''reasoningEffort: "${rosterHelper.rosterDefaults.scout.effort}"'' scoutFile)
      "opencode agentFilesTemplate must render defaultRoster's scout effort default (${rosterHelper.rosterDefaults.scout.effort}), got: ${scoutFile}";
    assert assertMsg
      (hasInfix ''reasoningEffort: "${rosterHelper.rosterDefaults.reviewer.effort}"'' reviewerFile)
      "opencode agentFilesTemplate must render defaultRoster's reviewer effort default (${rosterHelper.rosterDefaults.reviewer.effort}), got: ${reviewerFile}";
    assert assertMsg
      (hasInfix ''reasoningEffort: "${rosterHelper.rosterDefaults.filer.effort}"'' filerFile)
      "opencode agentFilesTemplate must render defaultRoster's filer effort default (${rosterHelper.rosterDefaults.filer.effort}), got: ${filerFile}";
    assert assertMsg
      (hasInfix ''reasoningEffort: "${rosterHelper.rosterDefaults.worker.effort}"'' workerFile)
      "opencode agentFilesTemplate must render defaultRoster's worker effort default (${rosterHelper.rosterDefaults.worker.effort}), got: ${workerFile}";
    pkgs.runCommand "drivers-opencode-agent-files-default-roster-effort" { } "touch $out";

  # ADR 0009: opencode reads claude's skills directory natively, so it wires
  # none of its own. Compared against claude's row rather than a literal, so a
  # rename of that directory still catches opencode drifting off it.
  drivers-opencode-skills-dir-matches-claude =
    assert assertMsg (entry.skillsDirRelative == driverRegistry.entries.claude.skillsDirRelative)
      "opencode Driver's skillsDirRelative must match claude's (ADR 0009), got opencode: ${entry.skillsDirRelative}, claude: ${driverRegistry.entries.claude.skillsDirRelative}";
    pkgs.runCommand "drivers-opencode-skills-dir-matches-claude" { } "touch $out";

  # Pins the real opencode entry's argvShape the same way: agentsFlag stays
  # absent (opencode has no --agents equivalent) and order stays the 5-slot
  # permutation excluding "agents".
  drivers-opencode-argv-shape-pinned =
    let
      shape = entry.argvShape;
    in
    assert assertMsg (
      shape.promptStyle == "positional"
    ) "opencode Driver's argvShape.promptStyle must stay \"positional\", got: ${shape.promptStyle}";
    assert assertMsg (!(shape ? promptFlag))
      "opencode Driver's argvShape must not declare promptFlag for a positional prompt style, got: ${builtins.toJSON shape}";
    assert assertMsg (
      shape.modelFlag == "-m"
    ) "opencode Driver's argvShape.modelFlag must stay \"-m\", got: ${shape.modelFlag}";
    assert assertMsg (shape.modelOmitEmpty == true)
      "opencode Driver's argvShape.modelOmitEmpty must stay true, got: ${builtins.toJSON shape.modelOmitEmpty}";
    assert assertMsg (!(shape ? agentsFlag))
      "opencode Driver's argvShape must not declare agentsFlag (opencode has no --agents equivalent), got: ${builtins.toJSON shape}";
    assert assertMsg (
      shape.effortFlag == "--variant"
    ) "opencode Driver's argvShape.effortFlag must stay \"--variant\", got: ${shape.effortFlag}";
    assert assertMsg (
      shape.order == [
        "driverFlags"
        "model"
        "effort"
        "session"
        "prompt"
      ]
    ) "opencode Driver's argvShape.order must stay pinned, got: ${builtins.toJSON shape.order}";
    pkgs.runCommand "drivers-opencode-argv-shape-pinned" { } "touch $out";

  # Pins the baked opencode config (issue #5345): a Box must refuse a Read or
  # Bash call naming a credential path, as the claude Box does through
  # credential-deny.sh. The shape is pinned first; the behavioural half then
  # re-implements opencode's Wildcard.match (util/wildcard.ts) in Nix and
  # replays real tool inputs against the rendered rules.
  drivers-opencode-credential-deny-config =
    let
      configFiles = entry.configFiles or { };
      configPath = ".config/opencode/opencode.json";
      config = builtins.fromJSON configFiles.${configPath};
      regexSpecials = [
        "."
        "+"
        "^"
        "$"
        "{"
        "}"
        "("
        ")"
        "|"
        "["
        "]"
        "\\"
      ];
      # `*` crosses `/`, `?` is one char, and a trailing " *" makes that tail
      # optional, so `cat *` also matches a bare `cat`.
      wildcardRegex =
        pat:
        let
          esc =
            builtins.replaceStrings
              (
                regexSpecials
                ++ [
                  "*"
                  "?"
                ]
              )
              (
                map (
                  c:
                  if c == "]" then
                    "[]]"
                  else if c == "}" then
                    "[}]"
                  else
                    "\\" + c
                ) regexSpecials
                ++ [
                  ".*"
                  "."
                ]
              )
              pat;
        in
        if hasSuffix " .*" esc then removeSuffix " .*" esc + "( .*)?" else esc;
      denied =
        rules: input:
        builtins.any (pat: builtins.match (wildcardRegex pat) input != null) (builtins.attrNames rules);
      expect =
        rules: want: inputs:
        all (
          input:
          assertMsg (denied rules input == want)
            "opencode ${if want then "must deny" else "must allow"} ${builtins.toJSON input}"
        ) inputs;
      readRules = config.permission.read;
      bashRules = config.permission.bash;
      # opencode matches each command node's text, or its enclosing
      # redirected_statement's, so every input below is a whole such string.
      separatorFormsDeny = [
        "cat .env>/tmp/ocx"
        "head -1\t.env"
        "echo $(cat .env)"
        "cat .env;"
        "cat .env|wc"
        "cat <.env"
        "cat .env&&true"
      ];
      # Newline is left out: builtins.match's newline handling is unreliable.
      # Keep the excluded chars (* ? \) in step with boundaryChars in
      # lib/drivers/opencode.nix.
      sweepChars =
        filter (c: c != "*" && c != "?" && c != "\\") (
          stringToCharacters " !\"#$%&'()*+,-./:;<=>?@[\\]^_`{|}~0123456789abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ"
        )
        ++ [ "\t" ];
      hookCases = map (input: {
        inherit input;
        deny = denied bashRules input;
      }) (map (c: "x${c}.env") sweepChars ++ map (c: "cat .env${c}x") sweepChars ++ separatorFormsDeny);
    in
    assert assertMsg (builtins.attrNames configFiles == [ configPath ])
      "opencode configFiles must carry exactly ${configPath}, got: ${builtins.toJSON (builtins.attrNames configFiles)}";
    assert assertMsg (builtins.attrNames config == [ "permission" ])
      "opencode.json top-level keys must be exactly [permission], got: ${builtins.toJSON (builtins.attrNames config)}";
    assert assertMsg
      (
        builtins.attrNames config.permission == [
          "bash"
          "read"
        ]
      )
      "opencode.json permission keys must be exactly [bash read], got: ${builtins.toJSON (builtins.attrNames config.permission)}";
    # --auto only approves asks, so anything but a literal deny leaves the
    # credential readable.
    assert assertMsg (
      readRules != { }
      && bashRules != { }
      && all (v: v == "deny") (builtins.attrValues (readRules // bashRules))
    ) "every opencode.json permission rule must be a literal \"deny\"";
    assert expect readRules true [
      ".env"
      "sub/.env.local"
      ".env.example"
      "../../home/agent/.config/gh/hosts.yml"
      "../../home/agent/.claude/.credentials.json"
      "../../home/agent/.local/share/opencode/auth.json"
    ];
    assert expect readRules false [
      "config.env"
      ".envrc"
      "src/main.go"
    ];
    assert expect bashRules true [
      "cat ~/.config/gh/hosts.yml"
      "cat .env"
      "source .env.local"
      "cp /home/agent/.claude/.credentials.json /tmp/x"
      ''cat "$HOME/.config/gh/hosts.yml"''
      "cat ~/.local/share/opencode/auth.json"
      "env --file=.env"
    ];
    assert expect bashRules false [
      "printenv"
      "cat config.env"
      "ls .envrc"
      "go test ./..."
    ];
    assert expect bashRules true separatorFormsDeny;
    # The hook is the reference, so each sweep verdict (opencode's, computed
    # above) must equal what the real hook answers; the lists cannot drift
    # into asserting only what opencode.nix happens to emit.
    pkgs.runCommand "drivers-opencode-credential-deny-config" { nativeBuildInputs = [ pkgs.jq ]; } ''
      cases=${pkgs.writeText "opencode-hook-cases.json" (builtins.toJSON hookCases)}
      for i in $(seq 0 $(($(jq length "$cases") - 1))); do
        want=$(jq -r ".[$i].deny" "$cases")
        got=false
        jq -c ".[$i] | {tool_name:\"Bash\",tool_input:{command:.input}}" "$cases" \
          | bash ${../../../agent/credential-deny.sh} \
          | jq -e '.hookSpecificOutput.permissionDecision == "deny"' >/dev/null && got=true
        [ "$got" = "$want" ] || {
          echo "credential-deny.sh denies=$got but opencode denies=$want for: $(jq -c ".[$i].input" "$cases")" >&2
          exit 1
        }
      done
      touch $out
    '';

  drivers-opencode-declare-bash-timeout-env =
    assert assertMsg
      ((entry.bashTimeoutEnv or null) == [ "OPENCODE_EXPERIMENTAL_BASH_DEFAULT_TIMEOUT_MS" ])
      "opencode Driver's bashTimeoutEnv must name OPENCODE_EXPERIMENTAL_BASH_DEFAULT_TIMEOUT_MS, got: ${
        builtins.toJSON (entry.bashTimeoutEnv or null)
      }";
    assert assertMsg
      (hasInfix "DRIVER_BASH_TIMEOUT_ENV=OPENCODE_EXPERIMENTAL_BASH_DEFAULT_TIMEOUT_MS\n" (
        driverRegistry.renderPreamble entry
      ))
      "renderPreamble must bake opencode's DRIVER_BASH_TIMEOUT_ENV";
    pkgs.runCommand "drivers-opencode-declare-bash-timeout-env" { } "touch $out";

  # AC#4: one roster carrying a custom agent outside the
  # scout/reviewer/filer/worker set must render from the same list, since the
  # roster, not per-Driver hardcoding, is the single source of agent identity.
  drivers-opencode-roster-custom-agent =
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
      opencodeRendered = entry.agentFilesTemplate { inherit roster; };
      opencodeAuditorFile = opencodeRendered.".config/opencode/agents/auditor.md" or "";
    in
    assert assertMsg (opencodeRendered ? ".config/opencode/agents/auditor.md")
      "opencode agentFilesTemplate must render a custom roster entry (auditor.md), got keys: ${concatStringsSep ", " (builtins.attrNames opencodeRendered)}";
    assert assertMsg (hasInfix ''model: "audit-model"'' opencodeAuditorFile)
      "opencode agentFilesTemplate's auditor.md must carry the roster model JSON-encoded (issue #2152 slice C), got: ${opencodeAuditorFile}";
    assert assertMsg (hasInfix ''mode: "subagent"'' opencodeAuditorFile)
      "opencode agentFilesTemplate's auditor.md must carry the roster mode JSON-encoded (issue #2152 slice C), got: ${opencodeAuditorFile}";
    pkgs.runCommand "drivers-opencode-roster-custom-agent" { } "touch $out";

  # Issue #2152 slice C: an empty roster (`roster = []`) is a deliberate
  # agent-less image, so opencode's agentFilesTemplate returns no agent file,
  # only the guard plugin (issue #5346), which is not roster-derived.
  drivers-opencode-render-empty-roster =
    let
      opencodeRendered = entry.agentFilesTemplate { roster = [ ]; };
    in
    assert assertMsg
      (builtins.attrNames opencodeRendered == [ ".config/opencode/plugins/spindrift-guards.js" ])
      "opencode agentFilesTemplate must return only the guard plugin for an empty roster, got keys: ${builtins.toJSON (builtins.attrNames opencodeRendered)}";
    pkgs.runCommand "drivers-opencode-render-empty-roster" { } "touch $out";

  # Issue #2152 slice C: one multi-agent roster (a built-in scout plus a
  # custom "auditor"). opencode's auditor.md frontmatter carries the
  # JSON-encoded model, mode, and description.
  drivers-opencode-render-custom-roster =
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
      opencodeRendered = entry.agentFilesTemplate { inherit roster; };
      auditorFile = opencodeRendered.".config/opencode/agents/auditor.md" or "";
    in
    assert assertMsg (opencodeRendered ? ".config/opencode/agents/auditor.md")
      "opencode agentFilesTemplate must render the custom roster entry (auditor.md), got keys: ${concatStringsSep ", " (builtins.attrNames opencodeRendered)}";
    assert assertMsg (hasInfix ''model: "audit-model"'' auditorFile)
      "opencode agentFilesTemplate's auditor.md must carry the JSON-encoded model, got: ${auditorFile}";
    assert assertMsg (hasInfix ''mode: "subagent"'' auditorFile)
      "opencode agentFilesTemplate's auditor.md must carry the JSON-encoded mode, got: ${auditorFile}";
    assert assertMsg (hasInfix ''description: "Audit stuff"'' auditorFile)
      "opencode agentFilesTemplate's auditor.md must carry the JSON-encoded description, got: ${auditorFile}";
    pkgs.runCommand "drivers-opencode-render-custom-roster" { } "touch $out";
}
