# Eval-level pins for lib/drivers/default.nix (issue #624): the registry's
# required-attribute shape assertion.
{
  pkgs,
  nixpkgs,
  system,
  ...
}:
let
  driverRegistry = import ../../lib/drivers/default.nix { inherit (pkgs) lib; };
  rosterLib = import ../../lib/roster.nix { inherit (pkgs) lib; };
  rosterHelper = import ../../lib/roster-schema-defaults.nix { inherit (pkgs) lib; };
  inherit (pkgs.lib)
    assertMsg
    hasInfix
    filter
    concatStringsSep
    splitString
    imap0
    escapeShellArg
    all
    generators
    hasSuffix
    removeSuffix
    stringToCharacters
    ;
  # Shared stub-cli fixture (issue #1144): drivers-render-preamble-shape uses
  # it as-is; stubDriverComplete below adds the four attrs renderPreamble
  # doesn't read but assertShape requires.
  stubDriverBase = {
    name = "stub";
    bin = "stub-cli";
    flagsCommon = "--stub-flag --two";
    skillsDirRelative = ".stub/skills";
    # Minimal well-formed argvShape (issue #2534): agentsFlag is deliberately
    # omitted (like opencode.nix) and modelOmitEmpty is false, so
    # drivers-render-preamble-omits-argv-shape-optional-vars can assert both
    # vars are absent from renderPreamble's output. order tracks agentsFlag's
    # absence by dropping the "agents" slot.
    argvShape = {
      promptStyle = "flag";
      promptFlag = "-p";
      modelFlag = "--model";
      modelOmitEmpty = false;
      effortFlag = "--effort";
      order = [
        "prompt"
        "model"
        "session"
        "driverFlags"
        "effort"
      ];
    };
  };
  # Shared complete-stub fixture (issue #3348): stubDriverBase plus the four
  # attrs renderPreamble doesn't read but assertShape requires. Every
  # assertShape check below starts from this rather than redeclaring it.
  stubDriverComplete = stubDriverBase // {
    name = "stub";
    package = pkgs: pkgs.hello;
    agentsJsonTemplate = "{}";
    agentFilesTemplate = _: { };
  };
  # Shared defaultRoster fixture (issue #2386): the two
  # default-roster-effort checks assert the same roster defaults through
  # their own Driver's render path.
  defaultRosterFixture = rosterLib.defaultRoster {
    scoutModel = "scout-m";
    reviewModel = "review-m";
    filerModel = "filer-m";
    workerModel = "worker-m";
  };
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
  drivers-assert-shape-missing-attribute-throws =
    let
      incomplete = {
        name = "incomplete";
        package = pkgs: pkgs.hello;
        bin = "incomplete";
        flagsCommon = "";
        skillsDirRelative = ".incomplete/skills";
        # agentsJsonTemplate deliberately omitted.
      };
      result = builtins.tryEval (driverRegistry.assertShape "incomplete" incomplete);
    in
    # tryEval exposes only success/failure, never the thrown message text, so
    # this can assert throw/no-throw but not that the message names the
    # Driver and the missing attribute.
    assert assertMsg (
      !result.success
    ) "assertShape must throw when a Driver entry is missing a required attribute";
    pkgs.runCommand "drivers-assert-shape-missing-attribute-throws" { } "touch $out";

  drivers-render-preamble-shape =
    let
      out = driverRegistry.renderPreamble stubDriverBase;
    in
    assert assertMsg (hasInfix "DRIVER_NAME=stub" out)
      "renderPreamble must bake DRIVER_NAME from the Driver entry's name, got: ${out}";
    assert assertMsg (hasInfix "DRIVER_BIN=stub-cli" out)
      "renderPreamble must bake DRIVER_BIN from the Driver entry's bin, got: ${out}";
    assert assertMsg (hasInfix "DRIVER_FLAGS_COMMON='--stub-flag --two'" out)
      "renderPreamble must shell-escape DRIVER_FLAGS_COMMON from the Driver entry's flagsCommon, got: ${out}";
    assert assertMsg (hasInfix "DRIVER_SKILLS_DIR=/home/agent/.stub/skills" out)
      "renderPreamble must bake DRIVER_SKILLS_DIR under /home/agent, got: ${out}";
    pkgs.runCommand "drivers-render-preamble-shape" { } "touch $out";

  # Issue #2011: renderPreamble exports a Driver entry's envCommon into
  # entrypoint.sh's own shell process, so the box program it execs (and
  # beyond that driver-exec/orchestrator and claude itself) inherits them.
  # envCommon is optional, so a Driver with no env vars of its own needn't
  # declare it.
  drivers-render-preamble-env-common =
    let
      out = driverRegistry.renderPreamble (
        stubDriverBase
        // {
          envCommon = {
            STUB_ENV_ONE = "1";
            STUB_ENV_TWO = "two words";
          };
        }
      );
    in
    assert assertMsg (hasInfix "export STUB_ENV_ONE=1" out)
      "renderPreamble must export each envCommon entry, got: ${out}";
    assert assertMsg (hasInfix "export STUB_ENV_TWO='two words'" out)
      "renderPreamble must shell-escape each envCommon value, got: ${out}";
    pkgs.runCommand "drivers-render-preamble-env-common" { } "touch $out";

  # envCommon keys splice unquoted onto the left of `export NAME=`, so a
  # non-identifier key (unlike the value, which is shell-escaped) renders
  # broken shell rather than failing safe. Assert at eval time instead of
  # trusting every Driver entry's data to be a valid shell identifier.
  drivers-render-preamble-env-common-rejects-bad-key =
    let
      result = builtins.tryEval (
        driverRegistry.renderPreamble (
          stubDriverBase
          // {
            envCommon = {
              "not an identifier" = "1";
            };
          }
        )
      );
    in
    assert assertMsg (
      !result.success
    ) "renderPreamble must reject an envCommon key that isn't a valid shell identifier";
    pkgs.runCommand "drivers-render-preamble-env-common-rejects-bad-key" { } "touch $out";

  # Issue #4409: bashTimeoutEnv renders as a plain DRIVER_ var, never `export`.
  drivers-render-preamble-bash-timeout-env =
    let
      out = driverRegistry.renderPreamble (
        stubDriverBase
        // {
          bashTimeoutEnv = [
            "STUB_DEFAULT_MS"
            "STUB_MAX_MS"
          ];
        }
      );
    in
    assert assertMsg (hasInfix "DRIVER_BASH_TIMEOUT_ENV='STUB_DEFAULT_MS STUB_MAX_MS'\n" out)
      "renderPreamble must bake DRIVER_BASH_TIMEOUT_ENV as a space-separated list from bashTimeoutEnv, got: ${out}";
    assert assertMsg (
      !(hasInfix "export DRIVER_BASH_TIMEOUT_ENV" out)
    ) "renderPreamble must not export DRIVER_BASH_TIMEOUT_ENV, got: ${out}";
    pkgs.runCommand "drivers-render-preamble-bash-timeout-env" { } "touch $out";

  drivers-render-preamble-omits-bash-timeout-env-when-absent =
    let
      out = driverRegistry.renderPreamble stubDriverBase;
    in
    assert assertMsg (!(hasInfix "DRIVER_BASH_TIMEOUT_ENV" out))
      "renderPreamble must omit DRIVER_BASH_TIMEOUT_ENV entirely for a Driver entry with no bashTimeoutEnv, got: ${out}";
    pkgs.runCommand "drivers-render-preamble-omits-bash-timeout-env-when-absent" { } "touch $out";

  # Names end up as env var names the box program (cmd/launcher/box) exports,
  # so a non-identifier must fail at eval like an envCommon key does.
  drivers-render-preamble-bash-timeout-env-rejects-bad-name =
    let
      result = builtins.tryEval (
        driverRegistry.renderPreamble (stubDriverBase // { bashTimeoutEnv = [ "not an identifier" ]; })
      );
    in
    assert assertMsg (
      !result.success
    ) "renderPreamble must reject a bashTimeoutEnv name that isn't a valid shell identifier";
    pkgs.runCommand "drivers-render-preamble-bash-timeout-env-rejects-bad-name" { } "touch $out";

  drivers-claude-declares-bash-timeout-env =
    let
      claudeEntry = driverRegistry.entries.claude;
      opencodeEntry = driverRegistry.entries.opencode;
    in
    assert assertMsg
      (
        (claudeEntry.bashTimeoutEnv or null) == [
          "BASH_DEFAULT_TIMEOUT_MS"
          "BASH_MAX_TIMEOUT_MS"
        ]
      )
      "claude Driver's bashTimeoutEnv must name BASH_DEFAULT_TIMEOUT_MS and BASH_MAX_TIMEOUT_MS, got: ${
        builtins.toJSON (claudeEntry.bashTimeoutEnv or null)
      }";
    assert assertMsg (
      !(opencodeEntry ? bashTimeoutEnv)
    ) "opencode Driver entry must declare no bashTimeoutEnv (it has no Bash-timeout env vars)";
    pkgs.runCommand "drivers-claude-declares-bash-timeout-env" { } "touch $out";

  drivers-assert-shape-succeeds =
    let
      complete = stubDriverComplete;
      result = builtins.tryEval (driverRegistry.assertShape "stub" complete);
    in
    assert assertMsg (result.success
    ) "assertShape must not throw when a Driver entry has every required attribute";
    assert assertMsg (
      result.value == complete
    ) "assertShape must return the Driver entry unchanged when it has every required attribute";
    pkgs.runCommand "drivers-assert-shape-succeeds" { } "touch $out";

  # Issue #1609: a Box Driver session must never see the harness's
  # re-invocation-promising tools, because the headless runner will not keep
  # that promise (a backgrounded gate plus ScheduleWakeup on #1542 lost a run
  # outright). Checked against the real claude entry, since flagsCommon is
  # shared verbatim across the main, conflict-resolve, and fix passes (AC4).
  drivers-claude-blocks-loop-background-affordances =
    let
      claudeEntry = driverRegistry.entries.claude;
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
      words = splitString " " claudeEntry.flagsCommon;
      indexedWords = imap0 (i: w: { inherit i w; }) words;
      flagMatches = filter (iw: iw.w == "--disallowedTools") indexedWords;
      flagIndex = if flagMatches == [ ] then null else (builtins.head flagMatches).i;
      deniedTools =
        if flagIndex == null then [ ] else splitString "," (builtins.elemAt words (flagIndex + 1));
      missing = filter (t: !(builtins.elem t deniedTools)) disallowed;
    in
    assert assertMsg (
      flagIndex != null
    ) "claude Driver's flagsCommon must include --disallowedTools, got: ${claudeEntry.flagsCommon}";
    assert assertMsg (missing == [ ])
      "claude Driver's flagsCommon --disallowedTools must deny ${concatStringsSep ", " missing}, got: ${claudeEntry.flagsCommon}";
    pkgs.runCommand "drivers-claude-blocks-loop-background-affordances" { } "touch $out";

  # A Box has no operator to approve a tool call, so claude runs headless with
  # --dangerously-skip-permissions, and under `--print` only stream-json (which
  # needs --verbose) emits events live (issue #113); text mode prints nothing
  # until the end and the box looks dead under `podman logs -f`. box hands
  # flagsCommon to driver-exec verbatim, so this pins the bytes it forwards.
  drivers-claude-runs-headless-and-streaming =
    let
      words = splitString " " driverRegistry.entries.claude.flagsCommon;
      streamIdx = filter (iw: iw.w == "--output-format") (imap0 (i: w: { inherit i w; }) words);
      format = if streamIdx == [ ] then null else builtins.elemAt words ((builtins.head streamIdx).i + 1);
    in
    assert assertMsg (builtins.elem "--dangerously-skip-permissions" words)
      "claude Driver's flagsCommon must include --dangerously-skip-permissions, got: ${driverRegistry.entries.claude.flagsCommon}";
    assert assertMsg (builtins.elem "--verbose" words)
      "claude Driver's flagsCommon must include --verbose (stream-json needs it under --print), got: ${driverRegistry.entries.claude.flagsCommon}";
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
    let
      claudeEntry = driverRegistry.entries.claude;
    in
    assert assertMsg
      ((claudeEntry.envCommon or { }).CLAUDE_CODE_DISABLE_BACKGROUND_TASKS or null == "1")
      "claude Driver's envCommon must set CLAUDE_CODE_DISABLE_BACKGROUND_TASKS=1, got: ${
        builtins.toJSON (claudeEntry.envCommon or { })
      }";
    pkgs.runCommand "drivers-claude-disables-background-tasks" { } "touch $out";

  # A claude-code bump could rename or drop
  # CLAUDE_CODE_DISABLE_BACKGROUND_TASKS silently, and no public doc names
  # the var, so this greps the pinned binary itself. In 2.1.204 it gates
  # `.omit({run_in_background:!0})` on the Bash, Agent/Task, and PowerShell
  # schemas. Drift fails here instead of leaving envCommon's export inert.
  drivers-claude-cli-knows-disable-background-tasks-env =
    let
      claudeEntry = driverRegistry.entries.claude;
      # claude-code carries an unfree license, so the plain `pkgs` above
      # refuses to evaluate it. This is the only check here that needs the
      # real package rather than its registry data, so it builds its own
      # allowUnfree pkgs the way lib/mkHarness.nix does for the image bake.
      unfreePkgs = import nixpkgs {
        inherit system;
        config.allowUnfree = true;
      };
      claudePackage = claudeEntry.package unfreePkgs;
      # Named explicitly rather than derived from envCommon's keys: envCommon
      # may grow a second entry, and attrNames' alphabetical head would then
      # silently pin the wrong var.
      envVarName = "CLAUDE_CODE_DISABLE_BACKGROUND_TASKS";
    in
    assert assertMsg (
      (claudeEntry.envCommon or { }) ? ${envVarName}
    ) "envVarName must name a key claudeEntry.envCommon actually sets, got: ${envVarName}";
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

  # Issue #262 slice 5 (AC4): opencode has no --agents JSON flag, so it
  # composes subagents from on-disk agents/*.md files under $HOME. Rendering
  # scout must produce frontmatter carrying `mode: "subagent"`, the model
  # JSON-encoded (issue #2152 slice C), and the same description claude.nix
  # uses, so both Drivers frame subagents identically. Fixture descriptions
  # here are synthetic on purpose: see roster-fixtures-carry-no-live-descriptions
  # in nix/checks/roster.nix.
  drivers-opencode-agent-files-scout-frontmatter =
    let
      opencodeEntry = driverRegistry.entries.opencode;
      rendered = opencodeEntry.agentFilesTemplate {
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
      rendered = driverRegistry.entries.opencode.agentFilesTemplate { roster = defaultRosterFixture; };
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
    pkgs.runCommand "drivers-opencode-agent-files-default-roster-permissions" { } "touch $out";

  # An explicit tool list maps to exactly the allow set; every other mapped
  # opencode key is denied because opencode treats an unset key as allow.
  drivers-opencode-agent-files-permission-map =
    let
      rendered = driverRegistry.entries.opencode.agentFilesTemplate {
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

  # A tool name with no opencode mapping must fail the render rather than be
  # dropped, which would silently widen or narrow the subagent's grant.
  drivers-opencode-agent-files-unmapped-tool-throws =
    let
      rendered = driverRegistry.entries.opencode.agentFilesTemplate {
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
          ok = driverRegistry.entries.opencode.agentFilesTemplate {
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
      rendered = driverRegistry.entries.opencode.agentFilesTemplate {
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
      opencodeEntry = driverRegistry.entries.opencode;
      rendered = opencodeEntry.agentFilesTemplate {
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
      opencodeEntry = driverRegistry.entries.opencode;
      rendered = opencodeEntry.agentFilesTemplate {
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
      opencodeEntry = driverRegistry.entries.opencode;
      payload = "evil\"\ninjected: true";
      rendered = opencodeEntry.agentFilesTemplate {
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

  # Issue #264: claude's agentsJsonTemplate takes a roster list rather than
  # four fixed model-knob args, so a custom agent ("auditor") must render
  # into the --agents JSON like any built-in entry. That JSON must never gain
  # a `mode` key: claude's --agents schema has none, and only opencode's
  # agentFilesTemplate emits mode.
  drivers-claude-agents-json-roster =
    let
      claudeEntry = driverRegistry.entries.claude;
      rendered = claudeEntry.agentsJsonTemplate {
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
      claudeEntry = driverRegistry.entries.claude;
      rendered = claudeEntry.agentsJsonTemplate {
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
      claudeEntry = driverRegistry.entries.claude;
      rendered = claudeEntry.agentsJsonTemplate {
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

  # AC#4: one roster carrying a custom agent outside the
  # scout/reviewer/filer/worker set must render into both Drivers' output
  # from the same list, since the roster, not per-Driver hardcoding, is the
  # single source of agent identity.
  drivers-roster-custom-agent-both-drivers =
    let
      claudeEntry = driverRegistry.entries.claude;
      opencodeEntry = driverRegistry.entries.opencode;
      roster = [
        {
          name = "auditor";
          model = "audit-model";
          mode = "subagent";
          description = "Audit stuff";
          tools = [ "Read" ];
        }
      ];
      claudeRendered = builtins.fromJSON (claudeEntry.agentsJsonTemplate { inherit roster; });
      opencodeRendered = opencodeEntry.agentFilesTemplate { inherit roster; };
      opencodeAuditorFile = opencodeRendered.".config/opencode/agents/auditor.md" or "";
    in
    assert assertMsg (claudeRendered ? auditor)
      "claude agentsJsonTemplate must render a custom roster entry (auditor), got keys: ${concatStringsSep ", " (builtins.attrNames claudeRendered)}";
    assert assertMsg (claudeRendered.auditor.model or "" == "audit-model")
      "claude agentsJsonTemplate's auditor entry must carry the roster model verbatim, got: ${
        builtins.toJSON (claudeRendered.auditor or { })
      }";
    assert assertMsg (opencodeRendered ? ".config/opencode/agents/auditor.md")
      "opencode agentFilesTemplate must render a custom roster entry (auditor.md), got keys: ${concatStringsSep ", " (builtins.attrNames opencodeRendered)}";
    assert assertMsg (hasInfix ''model: "audit-model"'' opencodeAuditorFile)
      "opencode agentFilesTemplate's auditor.md must carry the roster model JSON-encoded (issue #2152 slice C), got: ${opencodeAuditorFile}";
    assert assertMsg (hasInfix ''mode: "subagent"'' opencodeAuditorFile)
      "opencode agentFilesTemplate's auditor.md must carry the roster mode JSON-encoded (issue #2152 slice C), got: ${opencodeAuditorFile}";
    pkgs.runCommand "drivers-roster-custom-agent-both-drivers" { } "touch $out";

  # A raw, unquoted colon in a description splits the frontmatter line into
  # two mapping keys and yields malformed YAML (issue #264 review finding),
  # so agentFilesTemplate must JSON-encode the description; JSON is a valid
  # YAML scalar.
  drivers-opencode-agent-files-description-colon-frontmatter =
    let
      opencodeEntry = driverRegistry.entries.opencode;
      rendered = opencodeEntry.agentFilesTemplate {
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
      opencodeEntry = driverRegistry.entries.opencode;
      out = driverRegistry.renderPreamble opencodeEntry;
    in
    assert assertMsg (hasInfix "DRIVER_AGENT_FILES_DIR=/home/agent/.config/opencode/agents" out)
      "renderPreamble must bake DRIVER_AGENT_FILES_DIR from the opencode Driver entry's agentFilesDirRelative under /home/agent, got: ${out}";
    pkgs.runCommand "drivers-render-preamble-opencode-agent-files-dir" { } "touch $out";

  # A Driver entry that declares no agentFilesDirRelative (every stub fixture
  # here, and claude.nix in production) must render no DRIVER_AGENT_FILES_DIR
  # line at all, an unset var rather than an empty string assignment, so
  # box's non-empty gate stays a true no-op.
  drivers-render-preamble-omits-agent-files-dir-when-absent =
    let
      out = driverRegistry.renderPreamble stubDriverBase;
    in
    assert assertMsg (!(hasInfix "DRIVER_AGENT_FILES_DIR" out))
      "renderPreamble must omit DRIVER_AGENT_FILES_DIR entirely for a Driver entry with no agentFilesDirRelative, got: ${out}";
    pkgs.runCommand "drivers-render-preamble-omits-agent-files-dir-when-absent" { } "touch $out";

  # Issue #2843: renderPreamble must bake DRIVER_SESSION_CACHE_DIR from the
  # real claude entry's sessionCacheDirRelative so box sees
  # the session-cache path inside the box. lib/preambles.nix's
  # renderDriverMountPreamble renders the same-named var for the host-side
  # launcher, a separate consumer.
  drivers-render-preamble-session-cache-dir =
    let
      claudeEntry = driverRegistry.entries.claude;
      out = driverRegistry.renderPreamble claudeEntry;
    in
    assert assertMsg (hasInfix "DRIVER_SESSION_CACHE_DIR=/home/agent/.claude/projects" out)
      "renderPreamble must bake DRIVER_SESSION_CACHE_DIR from the claude Driver entry's sessionCacheDirRelative under /home/agent, got: ${out}";
    pkgs.runCommand "drivers-render-preamble-session-cache-dir" { } "touch $out";

  # A Driver entry that declares no sessionCacheDirRelative (every stub
  # fixture here, and opencode.nix in production) must render no
  # DRIVER_SESSION_CACHE_DIR line at all, symmetric with the agent-files-dir
  # omission check above.
  drivers-render-preamble-omits-session-cache-dir-when-absent =
    let
      out = driverRegistry.renderPreamble stubDriverBase;
    in
    assert assertMsg (!(hasInfix "DRIVER_SESSION_CACHE_DIR" out))
      "renderPreamble must omit DRIVER_SESSION_CACHE_DIR entirely for a Driver entry with no sessionCacheDirRelative, got: ${out}";
    pkgs.runCommand "drivers-render-preamble-omits-session-cache-dir-when-absent" { } "touch $out";

  # Issue #3348: assertShape also validates sessionCacheDirRelative's shape
  # when present (absence stays valid -- opencode omits it): a non-empty
  # relative path with no empty, "." or ".." segment (so no leading/
  # trailing/doubled slash either). tryEval can't report which condition
  # fired, so one list of malformed values, each spliced onto an otherwise-
  # complete entry, covers every bad shape in one check rather than one
  # throw-case per condition.
  drivers-assert-shape-rejects-bad-session-cache-dir-relative =
    let
      complete = stubDriverComplete;
      badValues = [
        ".stub/sessions/" # trailing slash
        "/stub/sessions" # leading slash
        "stub//sessions" # interior doubled slash
        "" # empty string
        42 # not a string
        "./stub/sessions" # leading "." segment
        "stub/sessions/." # trailing "." segment
        "stub/../sessions" # interior ".." segment
      ];
      results = map (
        v:
        (builtins.tryEval (
          driverRegistry.assertShape "stub" (complete // { sessionCacheDirRelative = v; })
        )).success
      ) badValues;
      notThrown = filter (v: v != null) (
        imap0 (i: v: if builtins.elemAt results i then v else null) badValues
      );
    in
    assert assertMsg (all (success: !success) results)
      "assertShape must throw for every malformed sessionCacheDirRelative, but did not throw for: ${
        concatStringsSep ", " (map (v: generators.toPretty { } v) notThrown)
      }";
    pkgs.runCommand "drivers-assert-shape-rejects-bad-session-cache-dir-relative" { } "touch $out";

  drivers-assert-shape-succeeds-with-session-cache-dir-relative =
    let
      complete = stubDriverComplete // {
        sessionCacheDirRelative = ".stub/sessions";
      };
      result = builtins.tryEval (driverRegistry.assertShape "stub" complete);
    in
    assert assertMsg (result.success
    ) "assertShape must not throw when sessionCacheDirRelative is a well-formed relative path";
    assert assertMsg (
      result.value == complete
    ) "assertShape must return the Driver entry unchanged when sessionCacheDirRelative is well-formed";
    pkgs.runCommand "drivers-assert-shape-succeeds-with-session-cache-dir-relative" { } "touch $out";

  # Pins that opencode's real entry still has no sessionCacheDirRelative
  # (claude's ".claude/projects" end-to-end value is already pinned by
  # drivers-render-preamble-session-cache-dir).
  drivers-session-cache-dir-relative-opencode-absent =
    let
      opencodeEntry = driverRegistry.entries.opencode;
    in
    assert assertMsg (
      !(opencodeEntry ? sessionCacheDirRelative)
    ) "opencode Driver entry must have no sessionCacheDirRelative attribute";
    pkgs.runCommand "drivers-session-cache-dir-relative-opencode-absent" { } "touch $out";

  # Issue #2152 slice C: claude's agentsJsonTemplate builds an attrset and
  # returns builtins.toJSON over the whole thing, so every scalar is encoded,
  # not just description. A payload with an embedded quote and newline must
  # round-trip exactly; a string-concatenation template would corrupt the
  # JSON structure or mangle the payload.
  drivers-claude-agents-json-escapes-every-scalar =
    let
      claudeEntry = driverRegistry.entries.claude;
      payload = "x\"y\nz";
      rendered = claudeEntry.agentsJsonTemplate {
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

  # Issue #2152 slice C: opencode's agentFilesTemplate JSON-encodes the
  # `model` frontmatter scalar, so a model value carrying a double quote and
  # a newline cannot break out of the YAML scalar and inject a second
  # frontmatter key.
  drivers-opencode-agent-files-escapes-model =
    let
      opencodeEntry = driverRegistry.entries.opencode;
      payload = "evil\"\ninjected: true";
      rendered = opencodeEntry.agentFilesTemplate {
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
      opencodeEntry = driverRegistry.entries.opencode;
      payload = "evil\"\ninjected: true";
      rendered = opencodeEntry.agentFilesTemplate {
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

  # Issue #2152 slice C: an empty roster (`roster = []`) is a deliberate
  # agent-less image, so claude's agentsJsonTemplate returns "" and
  # opencode's agentFilesTemplate returns no agent file, only the guard
  # plugin (issue #5346), which is not roster-derived.
  drivers-render-empty-roster =
    let
      claudeEntry = driverRegistry.entries.claude;
      opencodeEntry = driverRegistry.entries.opencode;
      claudeRendered = claudeEntry.agentsJsonTemplate { roster = [ ]; };
      opencodeRendered = opencodeEntry.agentFilesTemplate { roster = [ ]; };
    in
    assert assertMsg (
      claudeRendered == ""
    ) "claude agentsJsonTemplate must return \"\" for an empty roster, got: ${claudeRendered}";
    assert assertMsg
      (builtins.attrNames opencodeRendered == [ ".config/opencode/plugins/spindrift-guards.js" ])
      "opencode agentFilesTemplate must return only the guard plugin for an empty roster, got keys: ${builtins.toJSON (builtins.attrNames opencodeRendered)}";
    pkgs.runCommand "drivers-render-empty-roster" { } "touch $out";

  # Issue #2152 slice C: one multi-agent roster (a built-in scout plus a
  # custom "auditor") rendered by both Drivers from the same list. claude's
  # --agents JSON carries the custom agent's model, and opencode's auditor.md
  # frontmatter carries the JSON-encoded model, mode, and description.
  drivers-render-custom-roster-both =
    let
      claudeEntry = driverRegistry.entries.claude;
      opencodeEntry = driverRegistry.entries.opencode;
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
      claudeRendered = builtins.fromJSON (claudeEntry.agentsJsonTemplate { inherit roster; });
      opencodeRendered = opencodeEntry.agentFilesTemplate { inherit roster; };
      auditorFile = opencodeRendered.".config/opencode/agents/auditor.md" or "";
    in
    assert assertMsg (claudeRendered ? auditor)
      "claude agentsJsonTemplate must render the custom roster entry (auditor), got keys: ${concatStringsSep ", " (builtins.attrNames claudeRendered)}";
    assert assertMsg (claudeRendered.auditor.model or "" == "audit-model")
      "claude agentsJsonTemplate's auditor entry must carry the roster model, got: ${
        builtins.toJSON (claudeRendered.auditor or { })
      }";
    assert assertMsg (opencodeRendered ? ".config/opencode/agents/auditor.md")
      "opencode agentFilesTemplate must render the custom roster entry (auditor.md), got keys: ${concatStringsSep ", " (builtins.attrNames opencodeRendered)}";
    assert assertMsg (hasInfix ''model: "audit-model"'' auditorFile)
      "opencode agentFilesTemplate's auditor.md must carry the JSON-encoded model, got: ${auditorFile}";
    assert assertMsg (hasInfix ''mode: "subagent"'' auditorFile)
      "opencode agentFilesTemplate's auditor.md must carry the JSON-encoded mode, got: ${auditorFile}";
    assert assertMsg (hasInfix ''description: "Audit stuff"'' auditorFile)
      "opencode agentFilesTemplate's auditor.md must carry the JSON-encoded description, got: ${auditorFile}";
    pkgs.runCommand "drivers-render-custom-roster-both" { } "touch $out";

  # Issue #2386: defaultRoster ships a fixed default `effort` per agent,
  # looked up per name from rosterDefaults (issue #2506) rather than written
  # on each entry. Assert those defaults render end to end through claude's
  # --agents JSON for the four legacy built-in agents, not just that the
  # plumbing exists; review-axis's effort is pinned in nix/checks/image.nix.
  drivers-claude-agents-json-default-roster-effort =
    let
      claudeEntry = driverRegistry.entries.claude;
      roster = defaultRosterFixture;
      rendered = claudeEntry.agentsJsonTemplate { inherit roster; };
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
    pkgs.runCommand "drivers-claude-agents-json-default-roster-effort" { } "touch $out";

  # Same defaultRoster defaults as
  # drivers-claude-agents-json-default-roster-effort, but through opencode's
  # reasoningEffort frontmatter scalar instead of claude's --agents JSON.
  drivers-opencode-agent-files-default-roster-effort =
    let
      opencodeEntry = driverRegistry.entries.opencode;
      roster = defaultRosterFixture;
      rendered = opencodeEntry.agentFilesTemplate { inherit roster; };
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

  # ADR 0009: opencode reads .claude/skills/ directly rather than wiring its
  # own skills directory. Compared across the registry rather than against a
  # literal, so a rename of claude's skillsDirRelative still catches opencode
  # drifting off it instead of passing silently.
  drivers-opencode-skills-dir-matches-claude =
    let
      claudeEntry = driverRegistry.entries.claude;
      opencodeEntry = driverRegistry.entries.opencode;
    in
    assert assertMsg (opencodeEntry.skillsDirRelative == claudeEntry.skillsDirRelative)
      "opencode Driver's skillsDirRelative must match claude's (ADR 0009), got opencode: ${opencodeEntry.skillsDirRelative}, claude: ${claudeEntry.skillsDirRelative}";
    pkgs.runCommand "drivers-opencode-skills-dir-matches-claude" { } "touch $out";

  # Issue #2534: assertArgvShape validates argvShape's internal structure,
  # where assertShape only checks the attribute is present. Each throws-case
  # below perturbs exactly one field of stubDriverBase's well-formed
  # argvShape.
  drivers-assert-argv-shape-bad-prompt-style-throws =
    let
      entry = stubDriverBase // {
        argvShape = stubDriverBase.argvShape // {
          promptStyle = "bogus";
        };
      };
      result = builtins.tryEval (driverRegistry.assertArgvShape "stub" entry);
    in
    assert assertMsg (
      !result.success
    ) "assertArgvShape must throw when argvShape.promptStyle is neither \"flag\" nor \"positional\"";
    pkgs.runCommand "drivers-assert-argv-shape-bad-prompt-style-throws" { } "touch $out";

  drivers-assert-argv-shape-missing-prompt-flag-throws =
    let
      entry = stubDriverBase // {
        argvShape = builtins.removeAttrs stubDriverBase.argvShape [ "promptFlag" ];
      };
      result = builtins.tryEval (driverRegistry.assertArgvShape "stub" entry);
    in
    assert assertMsg (
      !result.success
    ) "assertArgvShape must throw when promptStyle is \"flag\" but promptFlag is missing";
    pkgs.runCommand "drivers-assert-argv-shape-missing-prompt-flag-throws" { } "touch $out";

  drivers-assert-argv-shape-empty-model-flag-throws =
    let
      entry = stubDriverBase // {
        argvShape = stubDriverBase.argvShape // {
          modelFlag = "";
        };
      };
      result = builtins.tryEval (driverRegistry.assertArgvShape "stub" entry);
    in
    assert assertMsg (!result.success) "assertArgvShape must throw when argvShape.modelFlag is empty";
    pkgs.runCommand "drivers-assert-argv-shape-empty-model-flag-throws" { } "touch $out";

  drivers-assert-argv-shape-non-bool-model-omit-empty-throws =
    let
      entry = stubDriverBase // {
        argvShape = stubDriverBase.argvShape // {
          modelOmitEmpty = "false";
        };
      };
      result = builtins.tryEval (driverRegistry.assertArgvShape "stub" entry);
    in
    assert assertMsg (
      !result.success
    ) "assertArgvShape must throw when argvShape.modelOmitEmpty is not a bool";
    pkgs.runCommand "drivers-assert-argv-shape-non-bool-model-omit-empty-throws" { } "touch $out";

  # The nullable slot (issue #2534): absent entirely is valid (opencode has
  # no --agents equivalent), but present-and-empty is a violation.
  drivers-assert-argv-shape-empty-agents-flag-throws =
    let
      entry = stubDriverBase // {
        argvShape = stubDriverBase.argvShape // {
          agentsFlag = "";
        };
      };
      result = builtins.tryEval (driverRegistry.assertArgvShape "stub" entry);
    in
    assert assertMsg (
      !result.success
    ) "assertArgvShape must throw when argvShape.agentsFlag is present but empty";
    pkgs.runCommand "drivers-assert-argv-shape-empty-agents-flag-throws" { } "touch $out";

  drivers-assert-argv-shape-empty-effort-flag-throws =
    let
      entry = stubDriverBase // {
        argvShape = stubDriverBase.argvShape // {
          effortFlag = "";
        };
      };
      result = builtins.tryEval (driverRegistry.assertArgvShape "stub" entry);
    in
    assert assertMsg (!result.success) "assertArgvShape must throw when argvShape.effortFlag is empty";
    pkgs.runCommand "drivers-assert-argv-shape-empty-effort-flag-throws" { } "touch $out";

  drivers-assert-argv-shape-order-missing-slot-throws =
    let
      entry = stubDriverBase // {
        argvShape = stubDriverBase.argvShape // {
          order = [
            "prompt"
            "model"
            "session"
            "driverFlags"
          ];
        };
      };
      result = builtins.tryEval (driverRegistry.assertArgvShape "stub" entry);
    in
    assert assertMsg (
      !result.success
    ) "assertArgvShape must throw when argvShape.order is missing a slot (effort, here)";
    pkgs.runCommand "drivers-assert-argv-shape-order-missing-slot-throws" { } "touch $out";

  drivers-assert-argv-shape-order-duplicate-or-unknown-throws =
    let
      entry = stubDriverBase // {
        argvShape = stubDriverBase.argvShape // {
          order = [
            "prompt"
            "prompt"
            "session"
            "driverFlags"
            "bogus"
          ];
        };
      };
      result = builtins.tryEval (driverRegistry.assertArgvShape "stub" entry);
    in
    assert assertMsg (!result.success)
      "assertArgvShape must throw when argvShape.order has a duplicated slot name (prompt) and an unknown one (bogus)";
    pkgs.runCommand "drivers-assert-argv-shape-order-duplicate-or-unknown-throws" { } "touch $out";

  drivers-assert-argv-shape-succeeds =
    let
      entry = stubDriverBase // {
        package = pkgs: pkgs.hello;
        agentsJsonTemplate = "{}";
        agentFilesTemplate = _: { };
      };
      result = builtins.tryEval (driverRegistry.assertArgvShape "stub" entry);
    in
    assert assertMsg (result.success
    ) "assertArgvShape must not throw when argvShape is fully well-formed";
    assert assertMsg (
      result.value == entry
    ) "assertArgvShape must return the Driver entry unchanged when argvShape is fully well-formed";
    pkgs.runCommand "drivers-assert-argv-shape-succeeds" { } "touch $out";

  drivers-render-preamble-argv-shape =
    let
      out = driverRegistry.renderPreamble stubDriverBase;
    in
    assert assertMsg (hasInfix "DRIVER_ARGV_PROMPT_STYLE=flag" out)
      "renderPreamble must bake DRIVER_ARGV_PROMPT_STYLE from the Driver entry's argvShape.promptStyle, got: ${out}";
    assert assertMsg (hasInfix "DRIVER_ARGV_PROMPT_FLAG=-p" out)
      "renderPreamble must bake DRIVER_ARGV_PROMPT_FLAG from the Driver entry's argvShape.promptFlag, got: ${out}";
    assert assertMsg (hasInfix "DRIVER_ARGV_MODEL_FLAG=--model" out)
      "renderPreamble must bake DRIVER_ARGV_MODEL_FLAG from the Driver entry's argvShape.modelFlag, got: ${out}";
    assert assertMsg (hasInfix "DRIVER_ARGV_EFFORT_FLAG=--effort" out)
      "renderPreamble must bake DRIVER_ARGV_EFFORT_FLAG from the Driver entry's argvShape.effortFlag, got: ${out}";
    assert assertMsg (hasInfix "DRIVER_ARGV_ORDER='prompt model session driverFlags effort'" out)
      "renderPreamble must bake DRIVER_ARGV_ORDER as a single space-separated, shell-escaped var from argvShape.order, got: ${out}";
    pkgs.runCommand "drivers-render-preamble-argv-shape" { } "touch $out";

  # stubDriverBase's argvShape sets modelOmitEmpty=false and omits agentsFlag,
  # so both optional vars must be absent from renderPreamble's output
  # entirely, not rendered empty.
  drivers-render-preamble-omits-argv-shape-optional-vars =
    let
      out = driverRegistry.renderPreamble stubDriverBase;
    in
    assert assertMsg (!(hasInfix "DRIVER_ARGV_AGENTS_FLAG" out))
      "renderPreamble must omit DRIVER_ARGV_AGENTS_FLAG entirely for a Driver entry whose argvShape has no agentsFlag, got: ${out}";
    assert assertMsg (!(hasInfix "DRIVER_ARGV_MODEL_OMIT_EMPTY" out))
      "renderPreamble must omit DRIVER_ARGV_MODEL_OMIT_EMPTY entirely when argvShape.modelOmitEmpty is false, got: ${out}";
    pkgs.runCommand "drivers-render-preamble-omits-argv-shape-optional-vars" { } "touch $out";

  # Pins the real claude entry's argvShape rather than stubDriverBase's, so
  # the "both entries carry argvShape" criterion has eval-level coverage
  # against lib/drivers/claude.nix's declared values.
  drivers-claude-argv-shape-pinned =
    let
      claudeEntry = driverRegistry.entries.claude;
      shape = claudeEntry.argvShape;
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

  # Pins the real opencode entry's argvShape the same way: agentsFlag stays
  # absent (opencode has no --agents equivalent) and order stays the 5-slot
  # permutation excluding "agents".
  drivers-opencode-argv-shape-pinned =
    let
      opencodeEntry = driverRegistry.entries.opencode;
      shape = opencodeEntry.argvShape;
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
      configFiles = driverRegistry.entries.opencode.configFiles or { };
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
    assert assertMsg (
      !(driverRegistry.entries.claude ? configFiles)
    ) "the claude Driver must not carry configFiles";
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
          | bash ${../../agent/credential-deny.sh} \
          | jq -e '.hookSpecificOutput.permissionDecision == "deny"' >/dev/null && got=true
        [ "$got" = "$want" ] || {
          echo "credential-deny.sh denies=$got but opencode denies=$want for: $(jq -c ".[$i].input" "$cases")" >&2
          exit 1
        }
      done
      touch $out
    '';

  # The shared credential-paths list feeds opencode's rules; the claude hook is
  # the reference for what counts as a credential. Parity runs both ways: the
  # hook denies every listed path, and every path the hook names is listed.
  drivers-credential-paths-hook-parity =
    let
      credentialPaths = import ../../lib/credential-paths.nix;
      paths = map (builtins.replaceStrings [ "*" ] [ "local" ]) credentialPaths;
      listed = pkgs.writeText "credential-paths.txt" (concatStringsSep "\n" credentialPaths);
    in
    pkgs.runCommand "drivers-credential-paths-hook-parity" { nativeBuildInputs = [ pkgs.jq ]; } ''
      hookPaths=$(sed -n '/^targets_credential_path()/,/^}/p' ${../../agent/credential-deny.sh} \
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
          verdict=$(printf '%s' "$input" | bash ${../../agent/credential-deny.sh})
          printf '%s' "$verdict" | jq -e '.hookSpecificOutput.permissionDecision == "deny"' >/dev/null || {
            echo "credential-deny.sh lets $tool of $p through, but lib/credential-paths.nix lists it" >&2
            exit 1
          }
        done
      done
      touch $out
    '';
}
