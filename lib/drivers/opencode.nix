# The opencode Driver: pure data only (ADR 0009, issue #624). ./default.nix
# validates this entry's shape and renders it into what lib/mkHarness.nix bakes
# into the image. The bats harness sources the registry's rendered preamble,
# byte-identical to the baked one, before exec-ing the entrypoint, so the suite
# exercises the same bytes (issue #433).
{ lib }:
let
  # Claude Code roster tool names to opencode permission keys, the one home of
  # the mapping (issue #5347). Edit, Write and MultiEdit all land on opencode's
  # single `edit` key, so granting any of them grants all three.
  rosterToolPermissions = {
    Read = "read";
    Edit = "edit";
    Write = "edit";
    MultiEdit = "edit";
    Bash = "bash";
    Glob = "glob";
    Grep = "grep";
    WebFetch = "webfetch";
    WebSearch = "websearch";
    Agent = "task";
    Task = "task";
    TodoWrite = "todowrite";
    Skill = "skill";
  };

  permissionKeys = lib.unique (lib.attrValues rosterToolPermissions);

  # opencode's SQLite session store lives here instead of beside auth.json: the
  # launcher mounts a writable per-issue host directory over this path, which
  # would shadow auth.json were it ~/.local/share/opencode itself. The launcher's
  # resume lookup reads OPENCODE_DB (envCommon below), so no Go copy exists.
  sessionCacheDirRelative = ".local/share/opencode/spindrift-session";

  # opencode treats an unset permission key as allow, so an entry's tools list
  # restricts nothing until every other mapped key is explicitly denied. An
  # unmapped tool throws rather than being dropped, which would silently change
  # what the subagent may do. Only the table's keys are set; keys it never
  # produces (list, lsp, question, external_directory, doom_loop, MCP/custom
  # tools) keep opencode's defaults. An explicit `tools = [ ]` denies every
  # mapped key, read included, unlike an omitted `tools`.
  #
  # An entry allowing `task` with `spawns` gets `task` as a map (issue #5368).
  # opencode evaluates rules last-match-wins, so `*` must come first:
  # builtins.toJSON sorts keys and `*` (0x2A) sorts before letters. The `*` deny
  # also hides opencode's built-in subagents, so a target absent from the
  # roster would leave nothing to spawn and throws instead. An empty `spawns`
  # is deliberate: spawn nothing. Returns "" when the entry sets neither key.
  permissionLineFor =
    rosterNames: e:
    let
      tools = e.tools or [ ];
      unmapped = lib.filter (t: !(rosterToolPermissions ? ${t})) tools;
      allowed = map (t: rosterToolPermissions.${t}) tools;
      taskAllowed = !(e ? tools) || lib.elem "task" allowed;
      spawns = e.spawns or [ ];
      missing = lib.optionals taskAllowed (lib.filter (n: !(lib.elem n rosterNames)) spawns);
      scopedTask = {
        "*" = "deny";
      }
      // lib.genAttrs spawns (_: "allow");
      keys =
        if e ? tools then
          permissionKeys
        else if e ? spawns then
          [ "task" ]
        else
          [ ];
      valueFor =
        k:
        if e ? tools && !(lib.elem k allowed) then
          "deny"
        else if k == "task" && e ? spawns then
          scopedTask
        else
          "allow";
    in
    if unmapped != [ ] then
      throw "opencode Driver: roster agent '${e.name}' lists tools with no opencode permission mapping: ${lib.concatStringsSep ", " unmapped}"
    else if missing != [ ] then
      throw "opencode Driver: roster agent '${e.name}' spawns ${lib.concatStringsSep ", " missing}, absent from the roster; add the entry or drop it from spawns"
    else if keys == [ ] then
      ""
    else
      "permission: ${builtins.toJSON (lib.genAttrs keys valueFor)}\n";
in
{
  name = "opencode";

  package = pkgs: pkgs.opencode;

  bin = "opencode";

  # The github-copilot Provider is OAuth-only (ADR 0009 amendment, #260) and
  # reads the credential from OPENCODE_AUTH_CONTENT. Other opencode Providers
  # carry their own apiKey via the {env:} config leg, so the rule applies only
  # when some model is Copilot.
  credentials = [
    {
      env = [ "OPENCODE_AUTH_CONTENT" ];
      modelPrefix = "github-copilot/";
      remedy = "set OPENCODE_AUTH_CONTENT for the github-copilot Provider (run 'opencode auth login -p github-copilot' on a host, then export the auth slice) under the opencode Driver";
    }
  ];

  # opencode model ids are `<provider>/<model>`; lib/mkHarness.nix drops a
  # worker whose model lacks the "/" rather than bake an id opencode would
  # reject (issue #5331), and fails eval on any other bare roster or default
  # model (issue #5355). Optional: claude's ids are bare.
  providerNamespacedModels = true;

  # Only the flags shared by every invocation. driver-exec assembles the rest
  # of opencode's argv (positional prompt, -m, no --agents) around them.
  flagsCommon = "run --format json --auto";

  # opencode reads .claude/skills/ directly (ADR 0009); no opencode-specific
  # skills directory exists or is wired.
  skillsDirRelative = ".claude/skills";

  # The file-rewrite loop in cmd/launcher/internal/promptassembly (issue
  # #2153) rewrites each baked agent file's body at runtime through this
  # path, so it must stay in lockstep with the path agentFilesTemplate below
  # bakes into or the loop silently no-ops. Optional, not in default.nix's
  # requiredAttrs: a Driver whose subagents are not on-disk files (claude)
  # has nothing to rewrite.
  agentFilesDirRelative = ".config/opencode/agents";

  # Issue #5349, ADR 0009: the launcher mounts a per-issue host directory over
  # this so a corrective resume continues the initial run's pinned session.
  inherit sessionCacheDirRelative;

  # OPENCODE_DB (an absolute path, used verbatim) relocates the store into the
  # mounted directory above; derived from it so the two cannot drift.
  #
  # OPENCODE_CONFIG_CONTENT (issue #5374) pins formatters and LSP servers off.
  # opencode spawns both with its full env, credentials included, and a Target
  # repo's own opencode.json can enable them; this source merges after project
  # config, so the Target repo cannot turn them back on.
  envCommon = {
    OPENCODE_DB = "/home/agent/${sessionCacheDirRelative}/opencode.db";
    OPENCODE_CONFIG_CONTENT = builtins.toJSON {
      formatter = false;
      lsp = false;
    };
  };

  # Issue #5351: opencode's bash tool defaults to a 2-minute timeout. This sets
  # that default only; a model-set per-call `timeout` overrides it and opencode
  # applies no maximum. It has no config-file key, so env is the only route.
  bashTimeoutEnv = [ "OPENCODE_EXPERIMENTAL_BASH_DEFAULT_TIMEOUT_MS" ];

  # opencode composes subagents from the on-disk files agentFilesTemplate below
  # bakes, not a CLI flag, so this always returns "". It keeps claude.nix's
  # roster argument (issue #264) only to give mkHarness.nix one call site.
  agentsJsonTemplate = { roster }: "";

  # opencode discovers subagents by scanning these files, and an entry's effort
  # becomes opencode's reasoningEffort key (issue #2242). rosterLib.dropOptedOut
  # drops empty-model entries upstream, so e.model needs no fallback and reaches
  # driver-exec verbatim. Every frontmatter scalar is JSON-encoded (issue #2152
  # slice C) so a newline or colon cannot inject a second YAML key.
  # An entry with a `tools` list also gets a `permission` map (`spawns` alone
  # gets a task-only one); one with neither keeps opencode's full tool set.
  agentFilesTemplate =
    { roster }:
    {
      # The claude PreToolUse Bash hooks never run under opencode, so this
      # plugin (auto-registered from ~/.config/opencode/plugins/) runs the same
      # baked hooks for opencode's bash tool (issue #5346). It is not a
      # subagent file, so the rewrite loop in promptassembly never touches it.
      ".config/opencode/plugins/spindrift-guards.js" = builtins.readFile ../../agent/opencode-guards.js;
    }
    // lib.listToAttrs (
      map (e: {
        name = ".config/opencode/agents/${e.name}.md";
        value = ''
          ---
          description: ${builtins.toJSON (e.description or "")}
          mode: ${builtins.toJSON (e.mode or "subagent")}
          model: ${builtins.toJSON e.model}
          ${permissionLineFor (map (r: r.name) roster) e}${
            lib.optionalString ((e.effort or "") != "") "reasoningEffort: ${builtins.toJSON e.effort}\n"
          }---
          ${e.description or ""}
        '';
      }) roster
    );

  # Files baked under /home/agent, path -> content (issue #5345). Optional: a
  # Driver whose config lives in hooks (claude) bakes none here. opencode has no
  # PreToolUse hook, so its Box refuses credential paths through deny rules
  # instead. They must be a literal "deny": the Box runs with --auto
  # (flagsCommon), which approves every ask. opencode globs have no character
  # classes, so the bash boundaries enumerate credential-deny.sh's lead and
  # trail classes char by char, minus `*` and `?` (wildcards here, so a literal
  # one cannot be written) and `\` (opencode rewrites it to `/`).
  configFiles =
    let
      paths = (import ../credential-paths.nix) ++ [ ".local/share/opencode/auth.json" ];
      # The hook's classes negate [:alnum:] plus the excluded chars. Keep the
      # always-excluded `* ? \` in step with sweepChars in
      # nix/checks/drivers/opencode.nix.
      boundaryChars =
        excluded:
        lib.subtractLists (
          excluded
          ++ [
            "*"
            "?"
            "\\"
          ]
        ) (lib.stringToCharacters " \t\n!\"#$%&'()*+,-./:;<=>?@[\\]^_`{|}~");
      leads = [
        ""
      ]
      ++ boundaryChars [
        "_"
        "."
        "-"
      ];
      trails = [
        ""
      ]
      ++ map (c: "${c}*") (boundaryChars [
        "_"
        "."
        "-"
        "/"
      ]);
      deny = patterns: lib.genAttrs (lib.unique patterns) (_: "deny");
    in
    {
      ".config/opencode/opencode.json" = builtins.toJSON {
        permission = {
          # Read matches the path relative to the worktree, so a credential
          # under $HOME arrives as ../../home/agent/...
          read = deny (
            lib.concatMap (p: [
              p
              "*/${p}"
            ]) paths
          );
          bash = deny (
            lib.concatMap (
              p:
              lib.concatMap (
                lead: map (trail: if lead == "" then p + trail else "*${lead}${p}${trail}") trails
              ) leads
            ) paths
          );
        };
      };
    };

  # opencode's CLI argv shape (ADR 0009, issue #2534). It has no --agents
  # equivalent, so agentsFlag is deliberately omitted: the nullable slot's
  # absent case.
  argvShape = {
    promptStyle = "positional";
    modelFlag = "-m";
    modelOmitEmpty = true;
    effortFlag = "--variant";
    order = [
      "driverFlags"
      "model"
      "effort"
      "session"
      "prompt"
    ];
  };
}
