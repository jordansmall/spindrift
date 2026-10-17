# Eval-level pins for lib/drivers/default.nix (issue #624): the registry's
# required-attribute shape assertion.
args@{
  pkgs,
  ...
}:
let
  driverRegistry = import ../../lib/drivers/default.nix { inherit (pkgs) lib; };
  rosterLib = import ../../lib/roster.nix { inherit (pkgs) lib; };
  inherit (pkgs.lib)
    assertMsg
    hasInfix
    filter
    concatStringsSep
    imap0
    all
    generators
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
  # One Nix check file per registered Driver, unioned over the registry's
  # entry names (ADR 0066, issue #5502); a new registry row without its file
  # fails eval naming the file to add.
  expectedCheckFile = name: "nix/checks/drivers/${name}.nix";
  defaultCheckFileOf = name: ./drivers + "/${name}.nix";
  missingCheckFiles =
    { entries, checkFileOf }:
    map expectedCheckFile (
      filter (n: !builtins.pathExists (checkFileOf n)) (builtins.attrNames entries)
    );
  # `//` silently drops a duplicate attr name, so every merge asserts the
  # two sides share none.
  mergeDisjoint =
    a: b:
    let
      dup = filter (n: a ? ${n}) (builtins.attrNames b);
    in
    assert assertMsg (
      dup == [ ]
    ) "nix/checks/drivers.nix: check attr(s) defined twice: ${concatStringsSep ", " dup}";
    a // b;
  driverChecksFor =
    {
      entries,
      checkFileOf ? defaultCheckFileOf,
    }:
    let
      missing = missingCheckFiles { inherit entries checkFileOf; };
    in
    if missing != [ ] then
      throw "nix/checks/drivers.nix: registered Driver(s) have no Nix check file; add ${concatStringsSep ", " missing}"
    else
      builtins.foldl' (
        acc: name:
        mergeDisjoint acc (
          import (checkFileOf name) (
            args
            // {
              inherit driverRegistry defaultRosterFixture;
              entry = entries.${name};
            }
          )
        )
      ) { } (builtins.attrNames entries);
in
mergeDisjoint (driverChecksFor { inherit (driverRegistry) entries; }) {
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

  drivers-declare-bash-timeout-env =
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
    assert assertMsg
      ((opencodeEntry.bashTimeoutEnv or null) == [ "OPENCODE_EXPERIMENTAL_BASH_DEFAULT_TIMEOUT_MS" ])
      "opencode Driver's bashTimeoutEnv must name OPENCODE_EXPERIMENTAL_BASH_DEFAULT_TIMEOUT_MS, got: ${
        builtins.toJSON (opencodeEntry.bashTimeoutEnv or null)
      }";
    assert assertMsg
      (hasInfix "DRIVER_BASH_TIMEOUT_ENV=OPENCODE_EXPERIMENTAL_BASH_DEFAULT_TIMEOUT_MS\n" (
        driverRegistry.renderPreamble opencodeEntry
      ))
      "renderPreamble must bake opencode's DRIVER_BASH_TIMEOUT_ENV";
    pkgs.runCommand "drivers-declare-bash-timeout-env" { } "touch $out";

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

  # A Driver entry that declares no sessionCacheDirRelative (every stub
  # fixture here) must render no DRIVER_SESSION_CACHE_DIR line at all,
  # symmetric with the agent-files-dir omission check above.
  drivers-render-preamble-omits-session-cache-dir-when-absent =
    let
      out = driverRegistry.renderPreamble stubDriverBase;
    in
    assert assertMsg (!(hasInfix "DRIVER_SESSION_CACHE_DIR" out))
      "renderPreamble must omit DRIVER_SESSION_CACHE_DIR entirely for a Driver entry with no sessionCacheDirRelative, got: ${out}";
    pkgs.runCommand "drivers-render-preamble-omits-session-cache-dir-when-absent" { } "touch $out";

  # Issue #3348: assertShape also validates sessionCacheDirRelative's shape
  # when present (absence stays valid -- a Driver with no resumable session
  # state omits it): a non-empty
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
}
