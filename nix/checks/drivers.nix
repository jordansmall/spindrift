# Eval-level pins for lib/drivers/default.nix (issue #624): the registry's
# generic shape and argv-shape checks, and the parity checks over every entry.
# Each Driver's own checks live in nix/checks/drivers/<name>.nix, which this
# file unions in (issue #5502).
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
    credentials = [ ];
  };
  # The real registry plus a well-formed stub row, for the checks that pin
  # what adding a Driver to the registry yields (issue #5502).
  stubRegistryEntries = driverRegistry.entries // {
    stub = driverRegistry.assertArgvShape "stub" (driverRegistry.assertShape "stub" stubDriverComplete);
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
  # The old mixed-check names survive as Driver-free aggregates over each
  # Driver's half, kept so existing attr names keep their meaning. `-both` /
  # `-both-drivers` is legacy wording from the two-Driver era, kept for attr
  # stability: every aggregate covers every registered Driver.
  aggregateSuffixes = {
    drivers-declare-bash-timeout-env = "declare-bash-timeout-env";
    drivers-render-empty-roster = "render-empty-roster";
    drivers-render-custom-roster-both = "render-custom-roster";
    drivers-roster-custom-agent-both-drivers = "roster-custom-agent";
  };
  driverChecksFor =
    {
      entries,
      checkFileOf ? defaultCheckFileOf,
    }:
    let
      missing = missingCheckFiles { inherit entries checkFileOf; };
      names = builtins.attrNames entries;
      perDriver = builtins.foldl' (
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
      ) { } names;
      # A Driver may lack a half (bashTimeoutEnv is optional), but an aggregate
      # with no half at all would pass vacuously.
      aggregates = builtins.mapAttrs (
        old: suffix:
        let
          halves = map (n: perDriver."drivers-${n}-${suffix}") (
            filter (n: perDriver ? "drivers-${n}-${suffix}") names
          );
        in
        assert assertMsg (halves != [ ])
          "nix/checks/drivers.nix: aggregate ${old} has no Driver half; expected a drivers-<name>-${suffix} check from at least one Driver";
        pkgs.runCommand old { inherit halves; } "touch $out"
      ) aggregateSuffixes;
    in
    if missing != [ ] then
      throw "nix/checks/drivers.nix: registered Driver(s) have no Nix check file; add ${concatStringsSep ", " missing}"
    else
      mergeDisjoint perDriver aggregates;
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

  # A misspelt optional attribute (providerNamespacedModel, no trailing s) would
  # otherwise vanish silently and disable the Nix bare-model check that reads
  # providerNamespacedModels.
  drivers-assert-shape-unknown-attribute-throws =
    let
      control = builtins.tryEval (driverRegistry.assertShape "control" stubDriverComplete);
      result = builtins.tryEval (
        driverRegistry.assertShape "misspelt" (stubDriverComplete // { providerNamespacedModel = true; })
      );
    in
    # tryEval cannot expose the thrown message, so only throw/no-throw is pinned;
    # the control keeps the throw attributable to the one added attribute.
    assert assertMsg control.success "assertShape must accept stubDriverComplete";
    assert assertMsg (
      !result.success
    ) "assertShape must throw when a Driver entry carries an attribute outside the closed set";
    pkgs.runCommand "drivers-assert-shape-unknown-attribute-throws" { } "touch $out";

  # A stub row added to the registry yields its file's checks with no other edit
  # (issue #5502). Only attr names are forced: the stub's check values would
  # build its fake rendering.
  drivers-registry-stub-row-yields-checks =
    let
      entries = stubRegistryEntries;
      stubCheckFile = builtins.toFile "stub.nix" "{ entry, ... }: { drivers-stub-fixture = entry.name; }";
      names = builtins.attrNames (driverChecksFor {
        inherit entries;
        checkFileOf = n: if n == "stub" then stubCheckFile else defaultCheckFileOf n;
      });
    in
    assert assertMsg (builtins.elem "drivers-stub-fixture" names)
      "a stub registry row must yield its check file's checks, got: ${concatStringsSep ", " names}";
    pkgs.runCommand "drivers-registry-stub-row-yields-checks" { } "touch $out";

  # A registry row without its check file fails eval (issue #5502).
  drivers-registry-row-without-check-file-throws =
    let
      entries = stubRegistryEntries;
      result = builtins.tryEval (
        builtins.attrNames (driverChecksFor {
          inherit entries;
        })
      );
    in
    # tryEval cannot expose the thrown message, so the named file is pinned
    # via missingCheckFiles, the list the message is built from.
    assert assertMsg (!result.success) "a registry row with no check file must fail evaluation";
    assert assertMsg (
      missingCheckFiles {
        inherit entries;
        checkFileOf = defaultCheckFileOf;
      } == [ "nix/checks/drivers/stub.nix" ]
    ) "the missing-file error must name nix/checks/drivers/stub.nix";
    pkgs.runCommand "drivers-registry-row-without-check-file-throws" { } "touch $out";

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
  # beyond that driver-exec/orchestrator and the Driver CLI itself) inherits them.
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

  # A Driver entry that declares no agentFilesDirRelative (every stub fixture
  # here, and some production Drivers) must render no DRIVER_AGENT_FILES_DIR
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

  drivers-render-preamble-omits-env-files-when-absent =
    let
      out = driverRegistry.renderPreamble stubDriverBase;
    in
    assert assertMsg (!(hasInfix "DRIVER_ENV_FILES" out))
      "renderPreamble must omit DRIVER_ENV_FILES entirely for a Driver entry with no envFiles, got: ${out}";
    pkgs.runCommand "drivers-render-preamble-omits-env-files-when-absent" { } "touch $out";

  drivers-render-preamble-env-files =
    let
      out = driverRegistry.renderPreamble (
        stubDriverBase
        // {
          envFiles = [
            {
              env = "A_VAR";
              pathRelative = ".a/auth.json";
            }
            {
              env = "B_VAR";
              pathRelative = "b.json";
            }
          ];
        }
      );
    in
    assert assertMsg (hasInfix "DRIVER_ENV_FILES='A_VAR=.a/auth.json B_VAR=b.json'\n" out)
      "renderPreamble must render envFiles as one escaped ENV=path word list, got: ${out}";
    pkgs.runCommand "drivers-render-preamble-env-files" { } "touch $out";

  # assertShape validates envFiles when present: a list of exactly
  # { env, pathRelative } with a shell-style env name and a non-empty,
  # whitespace-free relative path with no empty, "." or ".." segment.
  drivers-assert-shape-env-files =
    let
      ok = {
        env = "A_VAR";
        pathRelative = ".a/auth.json";
      };
      throws =
        v:
        !(builtins.tryEval (driverRegistry.assertShape "stub" (stubDriverComplete // { envFiles = v; })))
        .success;
      badEntries = [
        "not-an-attrset"
        { env = "A_VAR"; }
        { pathRelative = ".a/x"; }
        (ok // { env = ""; })
        (ok // { env = "bad name"; })
        (ok // { env = 1; })
        (ok // { pathRelative = ""; })
        (ok // { pathRelative = "/abs/x"; })
        (ok // { pathRelative = "../x"; })
        (ok // { pathRelative = "a/../x"; })
        (ok // { pathRelative = "a b"; })
        (ok // { pathRelative = "a//b"; })
        (ok // { pathRelative = ".pi/agent/"; })
        (ok // { pathRelative = "./x"; })
        (ok // { pathRelative = "x/."; })
        (ok // { pathRelative = 3; })
        (ok // { extra = "x"; })
      ];
      notThrown = filter (e: !(throws [ e ])) badEntries;
    in
    assert assertMsg (notThrown == [ ])
      "assertShape must throw for every malformed envFiles entry, but did not for: ${
        concatStringsSep ", " (map (e: generators.toPretty { } e) notThrown)
      }";
    assert assertMsg (throws "oops") "assertShape must throw when envFiles is not a list";
    assert assertMsg (
      !(throws [ ]) && !(throws [ ok ])
    ) "assertShape must accept an empty envFiles list and a well-formed entry";
    pkgs.runCommand "drivers-assert-shape-env-files" { } "touch $out";

  # assertShape validates authContent when present: { env, oauthProviders,
  # apiKeyProviders? } with a non-empty env string, a non-empty list of
  # non-empty provider strings, and an optional string -> string map.
  drivers-assert-shape-auth-content =
    let
      ok = {
        env = "A_VAR";
        oauthProviders = [ "p" ];
      };
      # authContent.env must be declared by the row (a credentials rule or an
      # envFiles entry), so the fixture row declares A_VAR as a credential.
      row = stubDriverComplete // {
        credentials = [
          {
            env = [ "A_VAR" ];
            remedy = "set A_VAR";
          }
        ];
      };
      throws =
        v: !(builtins.tryEval (driverRegistry.assertShape "stub" (row // { authContent = v; }))).success;
      bad = [
        "not-an-attrset"
        (ok // { env = "NOWHERE"; })
        { env = "A_VAR"; }
        { oauthProviders = [ "p" ]; }
        (ok // { env = ""; })
        (ok // { env = 1; })
        (ok // { oauthProviders = [ ]; })
        (ok // { oauthProviders = "p"; })
        (ok // { oauthProviders = [ "" ]; })
        (ok // { oauthProviders = [ 1 ]; })
        (ok // { extra = "x"; })
        (ok // { apiKeyProviders = "p"; })
        (ok // { apiKeyProviders = [ "p" ]; })
        (
          ok
          // {
            apiKeyProviders = {
              "" = "q";
            };
          }
        )
        (
          ok
          // {
            apiKeyProviders = {
              p = "";
            };
          }
        )
        (
          ok
          // {
            apiKeyProviders = {
              p = 1;
            };
          }
        )
      ];
      notThrown = filter (e: !(throws e)) bad;
    in
    assert assertMsg (
      !(throws (
        ok
        // {
          apiKeyProviders = {
            p = "q";
          };
        }
      ))
    ) "assertShape must accept a non-empty apiKeyProviders map";
    assert assertMsg (
      !(throws (ok // { apiKeyProviders = { }; }))
    ) "assertShape must accept an empty apiKeyProviders map";
    assert assertMsg ((builtins.tryEval (
      driverRegistry.assertShape "stub" (
        stubDriverComplete
        // {
          envFiles = [
            {
              env = "A_VAR";
              pathRelative = ".a/auth.json";
            }
          ];
          authContent = ok;
        }
      )
    )).success
    ) "assertShape must accept an authContent.env declared only by an envFiles entry";
    assert assertMsg (notThrown == [ ])
      "assertShape must throw for every malformed authContent, but did not for: ${
        concatStringsSep ", " (map (e: generators.toPretty { } e) notThrown)
      }";
    assert assertMsg (!(throws ok)) "assertShape must accept a well-formed authContent";
    pkgs.runCommand "drivers-assert-shape-auth-content" { } "touch $out";

  # assertShape validates closedCredentials when present: it must be a bool.
  drivers-assert-shape-closed-credentials =
    let
      throws =
        v:
        !(builtins.tryEval (
          driverRegistry.assertShape "stub" (stubDriverComplete // { closedCredentials = v; })
        )).success;
    in
    assert assertMsg (
      throws "yes" && throws 1 && throws null && throws [ ]
    ) "assertShape must throw when closedCredentials is not a bool";
    assert assertMsg (
      !(throws true) && !(throws false)
    ) "assertShape must accept a bool closedCredentials";
    pkgs.runCommand "drivers-assert-shape-closed-credentials" { } "touch $out";

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

  # Issue #5505: credentials rules are plain data; every malformed rule must
  # throw, and the well-formed shapes (with and without the optional keys) pass.
  drivers-assert-shape-credentials-rules =
    let
      complete = stubDriverComplete;
      okRule = {
        env = [ "STUB_TOKEN" ];
        remedy = "set STUB_TOKEN";
      };
      # Labels apply only to the variables after Env[0], and need setupCommand.
      labelled = okRule // {
        env = [
          "STUB_TOKEN"
          "STUB_KEY"
        ];
        setupCommand = [ "stub" ];
        labels.STUB_KEY = "Stub key";
      };
      throws =
        credentials:
        !(builtins.tryEval (driverRegistry.assertShape "stub" (complete // { inherit credentials; })))
        .success;
      badRules = [
        (okRule // { env = [ ]; })
        (okRule // { env = "STUB_TOKEN"; })
        (okRule // { env = [ 1 ]; })
        (removeAttrs okRule [ "env" ])
        (removeAttrs okRule [ "remedy" ])
        (okRule // { remedy = ""; })
        (okRule // { modelPrefix = ""; })
        (okRule // { modelPrefix = 1; })
        (okRule // { setupCommand = [ ]; })
        (okRule // { setupCommand = "stub login"; })
        (okRule // { modelPrefx = "x/"; })
        (labelled // { labels.STUB_OTHER = "Other"; })
        (labelled // { labels.STUB_TOKEN = "Stub token"; })
        (labelled // { labels.STUB_KEY = ""; })
        (labelled // { labels.STUB_KEY = 1; })
        (removeAttrs labelled [ "setupCommand" ])
        (labelled // { labels = { }; })
        (labelled // { labels = [ "Stub key" ]; })
        (labelled // { labels = "Stub key"; })
        (
          okRule
          // {
            setupCommand = [ "stub" ];
            setupHint = "";
          }
        )
        (
          okRule
          // {
            setupCommand = [ "stub" ];
            setupHint = 1;
          }
        )
        (okRule // { setupHint = "browser OAuth"; })
        "not-a-rule"
      ];
      notThrown = filter (r: !(throws [ r ])) badRules;
      fullRule = labelled // {
        modelPrefix = "stub/";
        setupHint = "browser OAuth";
        setupCommand = [
          "stub"
          "login"
        ];
      };
    in
    assert assertMsg (notThrown == [ ])
      "assertShape must throw for every malformed credentials rule, but did not for: ${
        concatStringsSep ", " (map (r: generators.toPretty { } r) notThrown)
      }";
    assert assertMsg (throws "oops") "assertShape must throw when credentials is not a list";
    assert assertMsg (
      !(throws [ ]) && !(throws [ okRule ]) && !(throws [ labelled ]) && !(throws [ fullRule ])
    ) "assertShape must accept an empty credentials list and well-formed rules";
    pkgs.runCommand "drivers-assert-shape-credentials-rules" { } "touch $out";

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

  # The nullable slot (issue #2534): absent entirely is valid (a Driver may have
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
        credentials = [ ];
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
