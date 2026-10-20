# The pi Driver: pure data only (ADR 0009, ADR 0066). ./default.nix validates
# this entry's shape and renders it into what lib/mkHarness.nix bakes into the
# image.
{ lib }:
let
  # Claude Code roster tool names to pi tool names, the one home of the
  # mapping. pi's `--tools` is an allowlist that also gates extension tools, so
  # `subagent` (the subagent extension's tool) must be listed to be callable.
  # A name with no pi tool (web access, todo list, skills) maps to nothing
  # rather than throwing, so the default roster still renders.
  rosterToolNames = {
    Read = [ "read" ];
    Bash = [ "bash" ];
    Edit = [ "edit" ];
    MultiEdit = [ "edit" ];
    Write = [ "write" ];
    Glob = [
      "find"
      "ls"
    ];
    Grep = [ "grep" ];
    Agent = [ "subagent" ];
    Task = [ "subagent" ];
    WebFetch = [ ];
    WebSearch = [ ];
    TodoWrite = [ ];
    Skill = [ ];
  };

  # An empty `tools` makes the extension omit --tools, which grants pi's whole
  # tool set, so a list mapping to nothing throws rather than widen access.
  # An unmapped name throws too, as opencode's permissionLineFor does.
  toolsLineFor =
    e:
    let
      unmapped = lib.filter (t: !(rosterToolNames ? ${t})) e.tools;
      mapped = lib.unique (lib.concatMap (t: rosterToolNames.${t}) e.tools);
    in
    if unmapped != [ ] then
      throw "pi Driver: roster agent '${e.name}' lists tools with no pi tool mapping: ${lib.concatStringsSep ", " unmapped}"
    else if mapped == [ ] then
      throw "pi Driver: roster agent '${e.name}' lists no tool pi can grant, and an empty tools line would grant them all"
    else
      "tools: ${builtins.toJSON (lib.concatStringsSep ", " mapped)}\n";
in
{
  name = "pi";

  # nixpkgs' own build, from the Consumer's locked nixpkgs: no overlay, no
  # vendoring.
  package = pkgs: pkgs.pi-coding-agent;

  bin = "pi";

  # pi's official subagent extension is a bundled example, not a package
  # feature (ADR 0009 amendment): linked from the package so a nixpkgs bump
  # moves it, and found by the agent directory's extensions scan, which child
  # pi processes run too. pi loads only the directory's index.ts; the sample
  # agents/ and prompts/ beside it are never discovered (agents load from
  # <agentDir>/agents only). Agent scope stays user-level because the hook
  # bridge refuses a subagent call asking for "project" or "both".
  packageLinks.".pi/agent/extensions/subagent" =
    "lib/node_modules/pi-monorepo/examples/extensions/subagent";

  # One rule per Provider pi reads an API key for, gated on that Provider's
  # model prefix (ADR 0066 decision 7). The OAuth tokens pi also accepts are
  # left out; the one OAuth Provider, github-copilot, reads PI_AUTH_CONTENT.
  credentials =
    (builtins.map
      (p: {
        env = [ p.env ];
        modelPrefix = "${p.provider}/";
        remedy = "set ${p.env} for the ${p.provider} Provider under the pi Driver";
      })
      [
        {
          provider = "anthropic";
          env = "ANTHROPIC_API_KEY";
        }
        {
          provider = "openai";
          env = "OPENAI_API_KEY";
        }
        {
          provider = "google";
          env = "GEMINI_API_KEY";
        }
        {
          provider = "openrouter";
          env = "OPENROUTER_API_KEY";
        }
        {
          provider = "groq";
          env = "GROQ_API_KEY";
        }
        {
          provider = "xai";
          env = "XAI_API_KEY";
        }
        {
          provider = "mistral";
          env = "MISTRAL_API_KEY";
        }
        {
          provider = "deepseek";
          env = "DEEPSEEK_API_KEY";
        }
      ]
    )
    ++ [
      {
        env = [ "PI_AUTH_CONTENT" ];
        modelPrefix = "github-copilot/";
        remedy = "set PI_AUTH_CONTENT to a pi auth.json holding a github-copilot OAuth entry under the pi Driver";
      }
    ];

  # PI_AUTH_CONTENT may hold OAuth entries only for these Providers. pi
  # refreshes an OAuth token inside the Box; github-copilot's refresh token is
  # a stable GitHub token, so that never invalidates the host's copy, while a
  # Provider that rotates refresh tokens would have the Box burn the host's
  # credential. Preflight rejects any other OAuth entry (use that Provider's
  # API key instead).
  authContent = {
    env = "PI_AUTH_CONTENT";
    oauthProviders = [ "github-copilot" ];
    # pi's ChatGPT-subscription OAuth id differs from the Provider its API key
    # is under; this lets preflight name the variable to set instead.
    apiKeyProviders = {
      "openai-codex" = "openai";
    };
  };

  # pi model ids are `<provider>/<model>`, so the bare-worker gate (issue
  # #5331) and the bare-model eval failure (issue #5355) apply as for opencode.
  providerNamespacedModels = true;

  # The roster runs through pi's subagent extension, so scout, worker and filer
  # provision as under the other Drivers.
  subagents = true;

  # Only the flags shared by every invocation; driver-exec adds --model,
  # --thinking and the positional prompt around them.
  flagsCommon = "--print --mode json --no-approve";

  # pi reads skills from its agent directory (PI_CODING_AGENT_DIR, default
  # ~/.pi/agent), not from claude's.
  skillsDirRelative = ".pi/agent/skills";

  # pi keeps sessions at sessions/<one dir per cwd>/<id>.jsonl under its agent
  # dir. The per-issue mount takes the sessions subdir, not .pi/agent, or it
  # would shadow auth.json and skills/ beside it (ADR 0009 amendment, issue
  # #5481).
  sessionCacheDirRelative = ".pi/agent/sessions";

  # pi reads its OAuth credentials only from auth.json under its agent dir.
  # The sessions mount above takes .pi/agent/sessions, so it does not shadow
  # this file.
  envFiles = [
    {
      env = "PI_AUTH_CONTENT";
      pathRelative = ".pi/agent/auth.json";
    }
  ];

  # Keeps pi from checking for updates or reporting telemetry from inside the
  # Box; the piseam integration tests set the same two.
  envCommon = {
    PI_SKIP_VERSION_CHECK = "1";
    PI_TELEMETRY = "0";
  };

  # The path promptassembly's file-rewrite loop rewrites bodies through; keep it
  # in lockstep with the agents/ paths agentFilesTemplate bakes.
  agentFilesDirRelative = ".pi/agent/agents";

  # pi has no --agents equivalent; the subagent extension scans agent files.
  agentsJsonTemplate = { roster }: "";

  # The hook bridge (issue #5482) is an extension in pi's agent directory, not
  # the Box's cwd, so the subagents' child pi processes load it too.
  # Frontmatter scalars are JSON-encoded so a colon or newline cannot inject a
  # YAML key, and promptassembly's frontmatterScalar still reads `model:`.
  # Effort rides as a `<model>:<level>` suffix because the extension passes the
  # parent's --thinking only to agents without a model; `reasoningEffort`, which
  # the extension ignores, lets promptassembly's reviewer handoff read the effort
  # so driver-exec's --thinking matches the suffix. An entry with no `tools` gets
  # no line, which keeps pi's full tool set.
  agentFilesTemplate =
    { roster }:
    {
      ".pi/agent/extensions/spindrift-hooks.js" = builtins.readFile ../../agent/pi-hook-bridge.js;
    }
    // lib.listToAttrs (
      map (
        e:
        let
          effort = e.effort or "";
          suffix = lib.optionalString (effort != "") ":${effort}";
          effortLine = lib.optionalString (effort != "") "reasoningEffort: ${builtins.toJSON effort}\n";
          toolsLine = lib.optionalString (e ? tools) (toolsLineFor e);
        in
        {
          name = ".pi/agent/agents/${e.name}.md";
          value = ''
            ---
            name: ${builtins.toJSON e.name}
            description: ${builtins.toJSON (e.description or "")}
            model: ${builtins.toJSON (e.model + suffix)}
            ${effortLine}${toolsLine}---
            ${e.description or ""}
          '';
        }
      ) roster
    );

  # Effort rides on the model (`<model>:<level>`, see agentFilesTemplate), so
  # mkHarness requires a role effort to have a model, falling back to the
  # default model, and one of these levels: pi sends an unknown level as part
  # of the model id (ADR 0009 amendment). A model already ending in one of
  # these levels keeps its own suffix and the role effort is dropped (issue
  # #5811).
  effortOnModelLevels = [
    "off"
    "minimal"
    "low"
    "medium"
    "high"
    "xhigh"
    "max"
  ];

  # agentsFlag is omitted. Effort values pass through to --thinking
  # unnormalised.
  argvShape = {
    promptStyle = "positional";
    modelFlag = "--model";
    modelOmitEmpty = true;
    effortFlag = "--thinking";
    order = [
      "driverFlags"
      "model"
      "effort"
      "session"
      "prompt"
    ];
  };
}
