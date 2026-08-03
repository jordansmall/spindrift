# Eval-level pins for lib/drivers/default.nix (issue #624): the registry's
# required-attribute shape assertion, on top of nix/checks/bats.nix's use of
# the same renderPreamble output the image bakes in.
{
  pkgs,
  nixpkgs,
  system,
  ...
}:
let
  driverRegistry = import ../../lib/drivers/default.nix { inherit (pkgs) lib; };
  inherit (pkgs.lib)
    assertMsg
    hasInfix
    filter
    concatStringsSep
    splitString
    imap0
    escapeShellArg
    ;
  # Shared stub-cli fixture (issue #1144): drivers-render-preamble-shape
  # consumes it as-is; drivers-assert-shape-succeeds extends it with the
  # four attrs renderPreamble doesn't read but assertShape requires.
  stubDriverBase = {
    name = "stub";
    bin = "stub-cli";
    flagsCommon = "--stub-flag --two";
    skillsDirRelative = ".stub/skills";
    outcomeExtractFnBody = "echo stub-outcome\n";
    outcomeExtractNearMissFnBody = "echo stub-near-miss\n";
    sessionFlagsFnBody = "echo stub-session\n";
  };
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
        outcomeExtractFnBody = "";
        # sessionFlagsFnBody and agentsJsonTemplate deliberately omitted.
      };
      result = builtins.tryEval (driverRegistry.assertShape "incomplete" incomplete);
    in
    # tryEval exposes only success/failure, never the thrown message text, so
    # this can assert throw/no-throw but not that the message names the
    # Driver and missing attribute (see drivers-assert-shape-succeeds below
    # for the complementary positive-shape case).
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
    assert assertMsg (hasInfix "_driver_extract_outcome() {\necho stub-outcome" out)
      "renderPreamble must fold in the Driver entry's outcomeExtractFnBody, got: ${out}";
    assert assertMsg (hasInfix "_driver_extract_near_miss_outcome() {\necho stub-near-miss" out)
      "renderPreamble must fold in the Driver entry's outcomeExtractNearMissFnBody, got: ${out}";
    assert assertMsg (hasInfix "_driver_session_flags() {\necho stub-session" out)
      "renderPreamble must fold in the Driver entry's sessionFlagsFnBody, got: ${out}";
    pkgs.runCommand "drivers-render-preamble-shape" { } "touch $out";

  # Issue #2011: a Driver entry may declare envCommon, a set of env vars
  # renderPreamble must export into entrypoint.sh's own shell process so a
  # child it execs (driver-exec/orchestrator, and beyond that claude itself,
  # via plain os/exec env inheritance) sees them too -- envCommon is the
  # generic seam a claude-specific var like CLAUDE_CODE_DISABLE_BACKGROUND_TASKS
  # rides in on, optional (omitted here) so a Driver with no env vars of its
  # own, or a future opencode.nix, needn't declare it.
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
  # non-identifier key (unlike the value, which is shell-escaped above)
  # would render broken/unquoted shell rather than merely fail safe -- assert
  # eval-time instead of trusting every Driver entry's data to already be a
  # valid shell identifier.
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

  drivers-assert-shape-succeeds =
    let
      complete = stubDriverBase // {
        name = "stub";
        package = pkgs: pkgs.hello;
        agentsJsonTemplate = "{}";
        agentFilesTemplate = _: { };
      };
      result = builtins.tryEval (driverRegistry.assertShape "stub" complete);
    in
    assert assertMsg (result.success
    ) "assertShape must not throw when a Driver entry has every required attribute";
    assert assertMsg (
      result.value == complete
    ) "assertShape must return the Driver entry unchanged when it has every required attribute";
    pkgs.runCommand "drivers-assert-shape-succeeds" { } "touch $out";

  # Issue #1609: a Box Driver session must never see the harness's
  # re-invocation-promising tools -- each is a promise the headless runner
  # will not keep (a backgrounded gate + ScheduleWakeup on #1542 lost a run
  # outright). Checked against the real claude entry (not stubDriverBase),
  # since flagsCommon is shared verbatim across the main run, conflict-resolve
  # pass, and fix pass (issue #1609 AC4) -- one flagsCommon, one assertion.
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
      # entrypoint.sh's DRIVER_FLAGS_COMMON splice is unquoted (whitespace
      # word-split, matching driver-exec/args.go's strings.Fields), so the
      # --disallowedTools value is the single word right after the flag.
      # Split into tokens for exact matching, not hasInfix substring
      # matching, so a typo'd sibling like "ScheduleWakeupX" can't slip a
      # false pass by.
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

  # Issue #2011: --disallowedTools (above) can't strip run_in_background --
  # it's a parameter of the Bash/Agent/Task/PowerShell tool calls, not a tool
  # name of its own -- and reject-background-bash.sh (agent/reject-background-bash.sh)
  # only ever covers the Bash tool, leaving the async Agent/Task subagent
  # launch tool free to background and park a headless run's turn. claude
  # itself honors CLAUDE_CODE_DISABLE_BACKGROUND_TASKS by omitting
  # run_in_background from every one of those tools' own input schema, so the
  # model can never request async in the first place -- exported here via the
  # generic envCommon renderPreamble seam (see drivers-render-preamble-env-common)
  # rather than a per-tool hook, since it closes every current and future
  # async-capable tool at once, not just Bash's.
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

  # drivers-claude-disables-background-tasks (above) only pins that this repo
  # *sets* CLAUDE_CODE_DISABLE_BACKGROUND_TASKS -- it says nothing about
  # whether the pinned claude-code build still honors it. Confirmed by
  # reading the pinned 2.1.204 binary directly (no public doc names this var):
  # `strings bin/.claude-wrapped | grep -o 'CLAUDE_CODE_DISABLE_BACKGROUND_TASKS'`
  # finds it read once (`Te.CLAUDE_CODE_DISABLE_BACKGROUND_TASKS`) and gating
  # `.omit({run_in_background:!0})` on three separate tool schemas (Bash,
  # Agent/Task, PowerShell). A future claude-code bump could rename or drop
  # the var silently -- this check greps the actual pinned binary on every
  # run so that drift fails loudly here instead of leaving envCommon's export
  # inert in a live Box.
  drivers-claude-cli-knows-disable-background-tasks-env =
    let
      claudeEntry = driverRegistry.entries.claude;
      # claude-code carries an unfree license; the plain `pkgs` above (no
      # config override) refuses to evaluate it, so this one check -- the
      # only one in this file that needs the real package rather than just
      # its registry data -- builds its own allowUnfree-enabled pkgs, the
      # same way mkHarness.nix (lib/mkHarness.nix:118) does for the actual
      # image bake.
      unfreePkgs = import nixpkgs {
        inherit system;
        config.allowUnfree = true;
      };
      claudePackage = claudeEntry.package unfreePkgs;
      # Named explicitly, not derived from envCommon's keys -- envCommon may
      # grow a second entry later, and attrNames' alphabetical head would
      # then silently start pinning the wrong one instead of this var.
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
  # composes subagents from on-disk agents/*.md files under $HOME instead
  # (agentFilesTemplate). Rendering scout with a model must produce YAML
  # frontmatter carrying `mode: "subagent"`, the model JSON-encoded (issue
  # #2152 slice C), and the same description claude.nix's agentsJsonTemplate
  # uses for scout -- so the two Drivers present identical subagent framing
  # regardless of which mechanism composes it.
  drivers-opencode-agent-files-scout-frontmatter =
    let
      opencodeEntry = driverRegistry.entries.opencode;
      rendered = opencodeEntry.agentFilesTemplate {
        roster = [
          {
            name = "scout";
            model = "solo-scout-model";
            mode = "subagent";
            description = "Map relevant files, seams, and tests; return a structured brief";
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
            description = "Review the branch diff for spec compliance and coding standards";
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
    assert assertMsg
      (hasInfix "Map relevant files, seams, and tests; return a structured brief" scoutFile)
      "opencode agentFilesTemplate's scout.md must carry the scout description, got: ${scoutFile}";
    pkgs.runCommand "drivers-opencode-agent-files-scout-frontmatter" { } "touch $out";

  # An empty model for an agent must omit that agent's file entirely (not
  # bake a modelless stub), mirroring agentsJsonTemplate's per-agent
  # lib.optionalAttrs omission (drivers-render-preamble-shape's neighbors
  # above, and nix/checks/image.nix's agents-json-baked, pin the JSON side of
  # this same contract).
  drivers-opencode-agent-files-omits-empty-model =
    let
      opencodeEntry = driverRegistry.entries.opencode;
      rendered = opencodeEntry.agentFilesTemplate {
        roster = [
          {
            name = "scout";
            model = "solo-scout-model";
            mode = "subagent";
            description = "Map relevant files, seams, and tests; return a structured brief";
            tools = [ "Read" ];
            promptFile = "scout-prompt.md";
            prompt = null;
          }
          {
            name = "reviewer";
            model = "";
            mode = "subagent";
            description = "Review the branch diff for spec compliance and coding standards";
            tools = [ "Read" ];
            promptFile = "review-prompt.md";
            prompt = null;
          }
          {
            name = "filer";
            model = "";
            mode = "subagent";
            description = "File issues from a review's non-blocking findings, best-effort";
            tools = [ "Read" ];
            promptFile = "filer-prompt.md";
            prompt = null;
          }
          {
            name = "worker";
            model = "";
            mode = "subagent";
            description = "Implement a scoped slice of work delegated to it, with full implement-capable tools";
            tools = [ "Read" ];
            promptFile = "worker-prompt.md";
            prompt = null;
          }
        ];
      };
    in
    assert assertMsg (!(rendered ? ".config/opencode/agents/reviewer.md"))
      "opencode agentFilesTemplate must omit reviewer.md when reviewModel is empty, got keys: ${concatStringsSep ", " (builtins.attrNames rendered)}";
    assert assertMsg (!(rendered ? ".config/opencode/agents/filer.md"))
      "opencode agentFilesTemplate must omit filer.md when filerModel is empty, got keys: ${concatStringsSep ", " (builtins.attrNames rendered)}";
    assert assertMsg (!(rendered ? ".config/opencode/agents/worker.md"))
      "opencode agentFilesTemplate must omit worker.md when workerModel is empty, got keys: ${concatStringsSep ", " (builtins.attrNames rendered)}";
    pkgs.runCommand "drivers-opencode-agent-files-omits-empty-model" { } "touch $out";

  # All-empty-models call must return the empty attrset -- no stray keys, no
  # empty-string values -- mirroring agentsJsonTemplate's own all-empty ""
  # return (nonRustHarness / agents-json-baked in nix/checks/image.nix).
  drivers-opencode-agent-files-all-empty-returns-empty-set =
    let
      opencodeEntry = driverRegistry.entries.opencode;
      rendered = opencodeEntry.agentFilesTemplate {
        roster = [
          {
            name = "scout";
            model = "";
            mode = "subagent";
            description = "Map relevant files, seams, and tests; return a structured brief";
            tools = [ "Read" ];
            promptFile = "scout-prompt.md";
            prompt = null;
          }
          {
            name = "reviewer";
            model = "";
            mode = "subagent";
            description = "Review the branch diff for spec compliance and coding standards";
            tools = [ "Read" ];
            promptFile = "review-prompt.md";
            prompt = null;
          }
          {
            name = "filer";
            model = "";
            mode = "subagent";
            description = "File issues from a review's non-blocking findings, best-effort";
            tools = [ "Read" ];
            promptFile = "filer-prompt.md";
            prompt = null;
          }
          {
            name = "worker";
            model = "";
            mode = "subagent";
            description = "Implement a scoped slice of work delegated to it, with full implement-capable tools";
            tools = [ "Read" ];
            promptFile = "worker-prompt.md";
            prompt = null;
          }
        ];
      };
    in
    assert assertMsg (rendered == { })
      "opencode agentFilesTemplate must return {} when every model is empty, got: ${builtins.toJSON rendered}";
    pkgs.runCommand "drivers-opencode-agent-files-all-empty-returns-empty-set" { } "touch $out";

  # Issue #264: claude's agentsJsonTemplate now takes a roster list rather
  # than four fixed model-knob args -- a custom 5th agent ("auditor", not one
  # of scout/reviewer/filer/worker) must render into the --agents JSON the
  # same as any built-in entry, an empty-model entry (reviewer here) must be
  # dropped (#392 semantics), and the rendered JSON must never gain a `mode`
  # key (claude's --agents schema has none; opencode.nix's agentFilesTemplate
  # is the only Driver that emits mode).
  drivers-claude-agents-json-roster =
    let
      claudeEntry = driverRegistry.entries.claude;
      rendered = claudeEntry.agentsJsonTemplate {
        roster = [
          {
            name = "scout";
            model = "solo-scout-model";
            mode = "subagent";
            description = "Map relevant files, seams, and tests; return a structured brief";
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
            description = "Review the branch diff for spec compliance and coding standards";
            tools = [
              "Read"
              "Bash"
              "WebFetch"
              "Agent"
            ];
            promptFile = "review-prompt.md";
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
    assert assertMsg (!(parsed ? reviewer))
      "claude agentsJsonTemplate must omit a roster entry with an empty model (reviewer), got keys: ${concatStringsSep ", " (builtins.attrNames parsed)}";
    assert assertMsg (parsed.auditor.model or "" == "audit-model")
      "claude agentsJsonTemplate's auditor entry must carry the roster model verbatim, got: ${builtins.toJSON (parsed.auditor or { })}";
    assert assertMsg (parsed.auditor.tools or [ ] == [ "Read" ])
      "claude agentsJsonTemplate's auditor entry must carry the roster tools verbatim, got: ${builtins.toJSON (parsed.auditor or { })}";
    assert assertMsg (
      filter (name: (parsed.${name} or { }) ? mode) (builtins.attrNames parsed) == [ ]
    ) "claude agentsJsonTemplate must never emit a `mode` key on any agent (claude's --agents schema has none), got: ${rendered}";
    pkgs.runCommand "drivers-claude-agents-json-roster" { } "touch $out";

  # Issue #2242 slice 1: a roster entry may set an optional `effort` field
  # (a per-agent reasoning-effort knob, e.g. "high"/"low") alongside `model`.
  # claude's agentsJsonTemplate must pass it through into the rendered
  # --agents JSON verbatim, same handling as `model` -- no normalization or
  # validation.
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
            description = "Map relevant files, seams, and tests; return a structured brief";
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
      "claude agentsJsonTemplate must carry a roster entry's effort field verbatim into the rendered JSON, got: ${builtins.toJSON (parsed.scout or { })}";
    pkgs.runCommand "drivers-claude-agents-json-effort-present" { } "touch $out";

  # Issue #2242 slice 1: an entry that omits `effort` (or sets it to "") must
  # not carry an `effort` key in the rendered JSON at all -- no default is
  # emitted, so a Driver invocation with no effort knob renders byte-stable
  # with the pre-#2242 shape.
  drivers-claude-agents-json-effort-absent =
    let
      claudeEntry = driverRegistry.entries.claude;
      rendered = claudeEntry.agentsJsonTemplate {
        roster = [
          {
            name = "scout";
            model = "solo-scout-model";
            mode = "subagent";
            description = "Map relevant files, seams, and tests; return a structured brief";
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

  # Issue #2242 slice 1: `effort` must not affect the existing filter-by-
  # model-presence gate (#392 semantics) -- an entry with effort set but no
  # (or empty) model is still dropped entirely, same as today.
  drivers-claude-agents-json-effort-does-not-bypass-model-filter =
    let
      claudeEntry = driverRegistry.entries.claude;
      rendered = claudeEntry.agentsJsonTemplate {
        roster = [
          {
            name = "reviewer";
            model = "";
            effort = "high";
            mode = "subagent";
            description = "Review the branch diff for spec compliance and coding standards";
            tools = [ "Read" ];
            promptFile = "review-prompt.md";
            prompt = null;
          }
        ];
      };
      parsed = if rendered == "" then { } else builtins.fromJSON rendered;
    in
    assert assertMsg (!(parsed ? reviewer))
      "claude agentsJsonTemplate must omit a roster entry with an empty model even when effort is set (filter-by-model-presence gate must stay unaffected by effort), got keys: ${concatStringsSep ", " (builtins.attrNames parsed)}";
    pkgs.runCommand "drivers-claude-agents-json-effort-does-not-bypass-model-filter" { } "touch $out";

  # AC#4: a single roster containing a custom agent not in the historical
  # scout/reviewer/filer/worker set must render into BOTH Drivers' output --
  # claude's --agents JSON and opencode's on-disk agents/*.md -- from the same
  # roster list, since the roster (not per-Driver hardcoding) is now the
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
      "claude agentsJsonTemplate's auditor entry must carry the roster model verbatim, got: ${builtins.toJSON (claudeRendered.auditor or { })}";
    assert assertMsg (opencodeRendered ? ".config/opencode/agents/auditor.md")
      "opencode agentFilesTemplate must render a custom roster entry (auditor.md), got keys: ${concatStringsSep ", " (builtins.attrNames opencodeRendered)}";
    assert assertMsg (hasInfix ''model: "audit-model"'' opencodeAuditorFile)
      "opencode agentFilesTemplate's auditor.md must carry the roster model JSON-encoded (issue #2152 slice C), got: ${opencodeAuditorFile}";
    assert assertMsg (hasInfix ''mode: "subagent"'' opencodeAuditorFile)
      "opencode agentFilesTemplate's auditor.md must carry the roster mode JSON-encoded (issue #2152 slice C), got: ${opencodeAuditorFile}";
    pkgs.runCommand "drivers-roster-custom-agent-both-drivers" { } "touch $out";

  # A description containing a colon (e.g. "Audit: colon") must not break the
  # YAML frontmatter's `description:` scalar -- a raw, unquoted colon splits
  # the line into two mapping keys and yields malformed YAML (issue #264
  # review finding). agentFilesTemplate must JSON-encode the description in
  # the frontmatter (JSON is a valid YAML scalar), so the rendered file
  # carries the quoted form rather than the raw, unquoted text.
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

  # opencode reads .claude/skills/ directly (ADR 0009) rather than a
  # opencode-specific skills directory -- pin the exact value so a future
  # edit to lib/drivers/opencode.nix can't silently change the Driver's
  # skills-discovery path without this check catching it.
  drivers-opencode-skills-dir-pinned =
    let
      opencodeEntry = driverRegistry.entries.opencode;
    in
    assert assertMsg (opencodeEntry.skillsDirRelative == ".claude/skills")
      "opencode Driver's skillsDirRelative must stay .claude/skills, got: ${opencodeEntry.skillsDirRelative}";
    pkgs.runCommand "drivers-opencode-skills-dir-pinned" { } "touch $out";

  # Issue #2153: renderPreamble must bake DRIVER_AGENT_FILES_DIR from the
  # real opencode entry's agentFilesDirRelative so agent/entrypoint.sh's
  # file-rewrite loop (agent/entrypoint.sh:784+) actually runs in a real Box
  # -- the loop is gated entirely on this var being non-empty. The baked
  # value must match agentFilesTemplate's own on-disk path
  # (.config/opencode/agents), or the entrypoint would rewrite files the
  # template never bakes.
  drivers-render-preamble-opencode-agent-files-dir =
    let
      opencodeEntry = driverRegistry.entries.opencode;
      out = driverRegistry.renderPreamble opencodeEntry;
    in
    assert assertMsg (hasInfix "DRIVER_AGENT_FILES_DIR=/home/agent/.config/opencode/agents" out)
      "renderPreamble must bake DRIVER_AGENT_FILES_DIR from the opencode Driver entry's agentFilesDirRelative under /home/agent, got: ${out}";
    pkgs.runCommand "drivers-render-preamble-opencode-agent-files-dir" { } "touch $out";

  # A Driver entry that declares no agentFilesDirRelative (every stub fixture
  # in this file, and claude.nix in production) must render no
  # DRIVER_AGENT_FILES_DIR line at all -- an empty/unset var, not an empty
  # string assignment -- so agent/entrypoint.sh's `[ -n "${DRIVER_AGENT_FILES_DIR:-}" ]`
  # gate stays a true no-op for such a Driver.
  drivers-render-preamble-omits-agent-files-dir-when-absent =
    let
      out = driverRegistry.renderPreamble stubDriverBase;
    in
    assert assertMsg (!(hasInfix "DRIVER_AGENT_FILES_DIR" out))
      "renderPreamble must omit DRIVER_AGENT_FILES_DIR entirely for a Driver entry with no agentFilesDirRelative, got: ${out}";
    pkgs.runCommand "drivers-render-preamble-omits-agent-files-dir-when-absent" { } "touch $out";

  # Issue #2152 slice C: claude's agentsJsonTemplate builds an attrset and
  # returns builtins.toJSON over the whole thing, so every scalar -- not just
  # description -- is already properly JSON-encoded. Round-trip a
  # JSON-breaking payload (an embedded double quote and a newline) through
  # both description and model and assert the parsed value equals the exact
  # payload -- a bare string-concatenation template would instead corrupt the
  # JSON structure or silently truncate/mangle the payload.
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
      "claude agentsJsonTemplate must round-trip a JSON-breaking description exactly, got: ${builtins.toJSON (parsed.escapee.description or "")}";
    assert assertMsg (parsed.escapee.model or "" == payload)
      "claude agentsJsonTemplate must round-trip a JSON-breaking model exactly, got: ${builtins.toJSON (parsed.escapee.model or "")}";
    pkgs.runCommand "drivers-claude-agents-json-escapes-every-scalar" { } "touch $out";

  # Issue #2152 slice C: opencode's agentFilesTemplate now JSON-encodes the
  # `model` frontmatter scalar (previously raw-interpolated), so a model value
  # carrying a double quote and a newline can no longer break out of the YAML
  # scalar and inject a second frontmatter key. The rendered file must carry
  # the JSON-encoded form of the payload, and must NOT carry the payload's
  # embedded newline followed by the injected key as a raw YAML line.
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

  # Same injection vector as drivers-opencode-agent-files-escapes-model above,
  # but through the `mode` frontmatter scalar instead of `model`.
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

  # Issue #2152 slice C (documents the deliberate agent-less image, mirroring
  # normalizeRoster's own empty-roster comment in lib/roster.nix): an empty
  # roster must render into the same empty shape both Drivers already return
  # for an all-empty-model roster -- claude's agentsJsonTemplate returns "",
  # opencode's agentFilesTemplate returns {}.
  drivers-render-empty-roster =
    let
      claudeEntry = driverRegistry.entries.claude;
      opencodeEntry = driverRegistry.entries.opencode;
      claudeRendered = claudeEntry.agentsJsonTemplate { roster = [ ]; };
      opencodeRendered = opencodeEntry.agentFilesTemplate { roster = [ ]; };
    in
    assert assertMsg (claudeRendered == "")
      "claude agentsJsonTemplate must return \"\" for an empty roster, got: ${claudeRendered}";
    assert assertMsg (opencodeRendered == { })
      "opencode agentFilesTemplate must return {} for an empty roster, got: ${builtins.toJSON opencodeRendered}";
    pkgs.runCommand "drivers-render-empty-roster" { } "touch $out";

  # Issue #2152 slice C: one custom, multi-agent roster (a builtin scout plus
  # a custom "auditor" agent beyond the historical
  # scout/reviewer/filer/worker set) rendered by BOTH Drivers from the same
  # roster list -- claude's --agents JSON carries the custom agent's model,
  # and opencode's on-disk auditor.md frontmatter carries the JSON-encoded
  # model/mode/description.
  drivers-render-custom-roster-both =
    let
      claudeEntry = driverRegistry.entries.claude;
      opencodeEntry = driverRegistry.entries.opencode;
      roster = [
        {
          name = "scout";
          model = "solo-scout-model";
          mode = "subagent";
          description = "Map relevant files, seams, and tests; return a structured brief";
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
      "claude agentsJsonTemplate's auditor entry must carry the roster model, got: ${builtins.toJSON (claudeRendered.auditor or { })}";
    assert assertMsg (opencodeRendered ? ".config/opencode/agents/auditor.md")
      "opencode agentFilesTemplate must render the custom roster entry (auditor.md), got keys: ${concatStringsSep ", " (builtins.attrNames opencodeRendered)}";
    assert assertMsg (hasInfix ''model: "audit-model"'' auditorFile)
      "opencode agentFilesTemplate's auditor.md must carry the JSON-encoded model, got: ${auditorFile}";
    assert assertMsg (hasInfix ''mode: "subagent"'' auditorFile)
      "opencode agentFilesTemplate's auditor.md must carry the JSON-encoded mode, got: ${auditorFile}";
    assert assertMsg (hasInfix ''description: "Audit stuff"'' auditorFile)
      "opencode agentFilesTemplate's auditor.md must carry the JSON-encoded description, got: ${auditorFile}";
    pkgs.runCommand "drivers-render-custom-roster-both" { } "touch $out";
}
