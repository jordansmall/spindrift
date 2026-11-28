# Issue #6805: a network Connector's backend row (spindrift.connectors.<name>.backend)
# reaches mkHarness as `backendRows` and the launcher input document's
# `backends` section. Its Route table is validated at evaluation with the
# registry-route rules, so a blanket or non-canonical route fails the build
# naming the Connector, and a row the Go side would refuse fails here first.
{
  pkgs,
  fixtures,
  nixpkgs,
  system,
  ...
}:
let
  inherit (pkgs.lib) assertMsg;
  routes = import ../../lib/connector-routes.nix;
  names = import ../../lib/connector-names.nix;
  mkHarness =
    extra:
    import ../../lib/mkHarness.nix (
      {
        inherit nixpkgs system;
        packages = p: [ p.hello ];
      }
      // extra
    );
  goodRoute = {
    method = "GET";
    host = "api.fixture.test";
    path = "/repos";
  };
  row = {
    module = p: "${fixtures.connectorWasm}/network.wasm";
    routes = [ goodRoute ];
    axes = [ "tracker" ];
    displayName = "Fixture";
  };
  # The batteries Connectors' own modules: a row built from one is first-party,
  # as nix/fixtures.nix builds the dogfood rows. Only the module carries the
  # mark; the routes are the fixture's.
  batteries =
    name: credentialEnv:
    let
      c = (import (../../connectors + "/${name}")).lib.connector { core = import ../../core; };
    in
    {
      module = p: "${c.package p}/${name}.wasm";
      routes = [ goodRoute ];
      inherit credentialEnv;
    };
  ghRow = batteries "github" "GH_TOKEN";
  fjRow = batteries "forgejo" "FORGEJO_TOKEN";
  jiraRow = batteries "jira" "JIRA_TOKEN";
  withRows = rows: mkHarness { backendRows = rows; };
  basicRow = row // {
    credentialScheme = "basic";
    identityEnv = "FIXTURE_EMAIL";
  };
  baseRow = row // {
    baseEnv = "FIXTURE_BASE_URL";
    routes = [ (goodRoute // { host = "{base}"; }) ];
  };
  plain = mkHarness { };
  # Forces the validation chain without a build.
  evaluates =
    h: (builtins.tryEval (builtins.seq h.internals.runInputDocumentFile.drvPath true)).success;
  withRoute =
    r:
    withRows {
      fixture = row // {
        routes = [ r ];
      };
    };

  # Each bad route with the substring its message must carry. The message also
  # names the Connector and the route.
  badRoutes = [
    {
      route = goodRoute // {
        path = "/";
      };
      why = "blanket-authorize";
    }
    {
      route = goodRoute // {
        path = "/*";
      };
      why = ''leading "*" segment'';
    }
    {
      route = goodRoute // {
        path = "/*/x";
      };
      why = ''leading "*" segment'';
    }
    {
      route = goodRoute // {
        path = "/a/";
      };
      why = "canonical form";
    }
    {
      route = goodRoute // {
        path = "/a//b";
      };
      why = "canonical form";
    }
    {
      route = goodRoute // {
        path = "/a/./b";
      };
      why = "canonical form";
    }
    {
      route = goodRoute // {
        path = "/a/../b";
      };
      why = "canonical form";
    }
    {
      route = goodRoute // {
        path = "a";
      };
      why = "canonical form";
    }
    {
      route = goodRoute // {
        path = "";
      };
      why = "canonical form";
    }
    {
      route = goodRoute // {
        path = "/a?x=1";
      };
      why = ''"?" or "#"'';
    }
    {
      route = goodRoute // {
        path = "/a#f";
      };
      why = ''"?" or "#"'';
    }
    {
      route = goodRoute // {
        method = "POST";
        followRedirect = true;
      };
      why = "GET only";
    }
    {
      route = goodRoute // {
        followRedirect = "yes";
      };
      why = "optional boolean";
    }
    {
      route = goodRoute // {
        method = "get";
      };
      why = "GET, HEAD, POST, PUT, PATCH, DELETE";
    }
    {
      route = goodRoute // {
        method = "GE T";
      };
      why = "GET, HEAD, POST, PUT, PATCH, DELETE";
    }
    {
      route = goodRoute // {
        method = "TRACE";
      };
      why = "GET, HEAD, POST, PUT, PATCH, DELETE";
    }
    {
      route = goodRoute // {
        host = "api.fixture.test/repos";
      };
      why = "bare host";
    }
    {
      route = goodRoute // {
        host = "user@api.fixture.test";
      };
      why = "bare host";
    }
    {
      route = goodRoute // {
        host = "api.fixture.test:";
      };
      why = "bare host";
    }
    {
      route = goodRoute // {
        host = "";
      };
      why = "bare host";
    }
    {
      route = goodRoute // {
        host = "{base}:8443";
      };
      why = "bare host";
    }
    {
      route = goodRoute // {
        host = "x{base}";
      };
      why = "bare host";
    }
    {
      route = goodRoute // {
        extra = "x";
      };
      why = "exactly";
    }
    {
      route = removeAttrs goodRoute [ "path" ];
      why = "exactly";
    }
    {
      route = goodRoute // {
        requestHeaders = [ "Cookie" ];
      };
      why = ''requestHeaders: "Cookie" can never be declared'';
    }
    {
      route = goodRoute // {
        requestHeaders = [ "X-Forwarded-For" ];
      };
      why = ''requestHeaders: "X-Forwarded-For" can never be declared'';
    }
    {
      route = goodRoute // {
        requestHeaders = [ "proxy-authorization" ];
      };
      why = ''requestHeaders: "proxy-authorization" can never be declared'';
    }
    {
      route = goodRoute // {
        requestHeaders = [ "X-Trace" ];
        responseHeaders = [ "Set-Cookie" ];
      };
      why = ''responseHeaders: "Set-Cookie" can never be declared'';
    }
    {
      route = goodRoute // {
        responseHeaders = [ "set-cookie2" ];
      };
      why = ''responseHeaders: "set-cookie2" can never be declared'';
    }
    {
      route = goodRoute // {
        requestHeaders = [ "X Bad" ];
      };
      why = ''requestHeaders: "X Bad" is not a valid header name'';
    }
    {
      route = goodRoute // {
        requestHeaders = [
          "X-Trace"
          "x-TRACE"
        ];
      };
      why = ''requestHeaders: "x-TRACE" is declared twice'';
    }
    {
      route = goodRoute // {
        requestHeaders = "X-Trace";
      };
      why = "exactly";
    }
    {
      route = goodRoute // {
        responseHeaders = [ 1 ];
      };
      why = "exactly";
    }
    {
      route = goodRoute // {
        method = "POST";
        documents = { };
      };
      why = "documents: must declare at least one";
    }
    {
      route = goodRoute // {
        method = "POST";
        documents = {
          "1bad" = "query{a}";
        };
      };
      why = ''documents: "1bad" is not a valid name'';
    }
    {
      route = goodRoute // {
        method = "POST";
        documents = {
          a = "";
        };
      };
      why = ''documents: "a" has no text'';
    }
    {
      route = goodRoute // {
        method = "POST";
        documents = {
          a = "query{a}";
          b = "query{a}";
        };
      };
      why = ''documents: "a" and "b" declare the same text'';
    }
    {
      route = goodRoute // {
        method = "POST";
        documents = [ "query{a}" ];
      };
      why = "exactly";
    }
    {
      route = goodRoute // {
        method = "POST";
        documents = {
          a = 1;
        };
      };
      why = "exactly";
    }
    {
      route = goodRoute // {
        documents = {
          a = "query{a}";
        };
      };
      why = "documents are allowed on POST only";
    }
  ];
  goodRoutes = [
    goodRoute
    (
      goodRoute
      // {
        path = "/repos/issues";
        method = "POST";
      }
    )
    (goodRoute // { host = "api.fixture.test:8443"; })
    (goodRoute // { host = "127.0.0.1:8080"; })
    (goodRoute // { host = "[::1]:8080"; })
    (goodRoute // { host = "[::1]"; })
    (goodRoute // { host = "{base}"; })
    (goodRoute // { path = "/a/*/c"; })
    (goodRoute // { followRedirect = true; })
    (goodRoute // { followRedirect = false; })
    (goodRoute // { requestHeaders = [ "X-Trace" ]; })
    (
      goodRoute
      // {
        requestHeaders = [
          "X-Trace"
          "If-Match"
        ];
        responseHeaders = [
          "X-RateLimit-Remaining"
          "Link"
        ];
      }
    )
    (
      goodRoute
      // {
        requestHeaders = [ ];
        responseHeaders = [ ];
      }
    )
    (
      goodRoute
      // {
        method = "POST";
        documents = {
          closeIssue = "mutation($id:ID!){closeIssue(input:{issueId:$id}){issue{id}}}";
          Viewer_2 = "query{viewer{login}}";
        };
      }
    )
  ];
  problemsOf = r: routes.problems "fixture" [ r ];
  has = sub: s: builtins.match ".*${pkgs.lib.escapeRegex sub}.*" s != null;
  # Evaluation-only: forcing an app's program path forces its derivation.
  forces = v: (builtins.tryEval (builtins.seq v true)).success;
  bothRun = h: forces h.apps.default.program && forces h.apps.daemon.program;
  eitherRuns = h: forces h.apps.default.program || forces h.apps.daemon.program;
in
{
  connector-backend-rows-route-table-errors-name-connector-and-route =
    assert assertMsg (builtins.all (
      r: routes.problems "fixture" [ r ] == [ ]
    ) goodRoutes) "control: well-formed routes must yield no problem";
    assert assertMsg (routes.problems "fixture" [ ] != [ ]) "an empty Route table must be a problem";
    assert assertMsg (builtins.all (
      b:
      let
        ps = problemsOf b.route;
      in
      builtins.length ps == 1
      && has "Connector 'fixture'" (builtins.head ps)
      && has b.why (builtins.head ps)
      && (
        b.route ? path && b.route ? host && b.route ? method
        -> has "${b.route.method} ${b.route.host} ${b.route.path}" (builtins.head ps)
      )
    ) badRoutes) "every bad route must yield one message naming the Connector, the route and the rule";
    pkgs.runCommand "connector-backend-rows-route-table-errors-name-connector-and-route" { }
      "touch $out";

  connector-backend-rows-mkharness-validates-rows =
    assert assertMsg (evaluates (withRows {
      fixture = row;
    })) "control: a well-formed row must evaluate";
    assert assertMsg (
      !(evaluates (withRoute (goodRoute // { path = "/"; })))
    ) "a blanket-root route must fail evaluation";
    assert assertMsg (
      !(evaluates (withRoute (goodRoute // { path = "/a/"; })))
    ) "a non-canonical route must fail evaluation";
    assert assertMsg (evaluates (
      withRoute (goodRoute // { requestHeaders = [ "X-Trace" ]; })
    )) "control: a route declaring an ordinary request header must evaluate";
    assert assertMsg (
      !(evaluates (withRoute (goodRoute // { requestHeaders = [ "Cookie" ]; })))
    ) "a route declaring a never-declarable request header must fail evaluation";
    assert assertMsg (
      !(evaluates (withRoute (goodRoute // { responseHeaders = [ "Set-Cookie" ]; })))
    ) "a route declaring a never-declarable response header must fail evaluation";
    assert assertMsg (evaluates (
      withRoute (
        goodRoute
        // {
          method = "POST";
          documents = {
            a = "query{a}";
          };
        }
      )
    )) "control: a POST route declaring documents must evaluate";
    assert assertMsg (
      !(evaluates (
        withRoute (
          goodRoute
          // {
            documents = {
              a = "query{a}";
            };
          }
        )
      ))
    ) "a GET route declaring documents must fail evaluation";
    assert assertMsg (
      !(evaluates (withRows {
        fixture = row // {
          routes = [ ];
        };
      }))
    ) "an empty Route table must fail evaluation";
    assert assertMsg (
      !(evaluates (withRows {
        git = row;
      }))
    ) "a row replacing git must fail";
    assert assertMsg (
      !(evaluates (withRows {
        local = row;
      }))
    ) "a row replacing local must fail";
    assert assertMsg (
      !(evaluates (withRows {
        fixture = removeAttrs row [ "module" ];
      }))
    ) "a row without a module must fail";
    assert assertMsg (
      !(evaluates (withRows {
        fixture = row // {
          module = "/x.wasm";
        };
      }))
    ) "a module that is not a function of pkgs must fail";
    assert assertMsg (evaluates (withRows {
      github = ghRow // {
        gitRemote = "https://github.com";
      };
    })) "control: a first-party row naming its credentialEnv must evaluate";
    assert assertMsg (
      !(evaluates (withRows {
        github = removeAttrs ghRow [ "credentialEnv" ] // {
          gitRemote = "https://github.com";
        };
      }))
    ) "a first-party row without a credentialEnv must fail";
    assert assertMsg (
      !(evaluates (withRows {
        github = ghRow // {
          gitRemote = "https://github.com";
          credentialEnv = "";
        };
      }))
    ) "a first-party row with an empty credentialEnv must fail";
    assert assertMsg (
      !(evaluates (withRows {
        fixture = row // {
          credentialKnob = "";
        };
      }))
    ) "a row with an empty credentialKnob must fail";
    assert assertMsg (evaluates (withRows {
      fixture = row // {
        credentialKnob = "fixtureToken";
      };
    })) "a row with a credentialKnob must evaluate";
    assert assertMsg (evaluates (withRows {
      fixture = basicRow;
    })) "control: a basic row with an identityEnv must evaluate";
    assert assertMsg (
      !(evaluates (withRows {
        fixture = removeAttrs basicRow [ "identityEnv" ];
      }))
    ) "a basic row without an identityEnv must fail";
    assert assertMsg (
      !(evaluates (withRows {
        fixture = row // {
          identityEnv = "FIXTURE_EMAIL";
        };
      }))
    ) "an identityEnv on a bearer row must fail";
    assert assertMsg (
      !(evaluates (withRows {
        fixture = row // {
          credentialScheme = "digest";
        };
      }))
    ) "an unknown credentialScheme must fail";
    assert assertMsg (evaluates (withRows {
      fixture = baseRow;
    })) "control: a {base} route with a baseEnv must evaluate";
    assert assertMsg (
      !(evaluates (withRows {
        fixture = removeAttrs baseRow [ "baseEnv" ];
      }))
    ) "a {base} route without a baseEnv must fail";
    assert assertMsg (
      !(evaluates (withRows {
        fixture = baseRow // {
          baseEnv = "";
        };
      }))
    ) "an empty baseEnv must fail";
    # A Code Forge row (github, forgejo) declares its Git remote, either
    # `gitRemote` (a bare https origin) or `gitRemoteEnv`; other rows may omit it.
    assert assertMsg (evaluates (withRows {
      github = ghRow // {
        gitRemote = "https://github.com";
      };
    })) "control: a forge row with a literal gitRemote must evaluate";
    assert assertMsg (evaluates (withRows {
      forgejo = fjRow // {
        gitRemoteEnv = "FORGEJO_BASE_URL";
      };
    })) "control: a forge row with a gitRemoteEnv must evaluate";
    assert assertMsg (
      !(evaluates (withRows {
        github = ghRow;
      }))
    ) "a forge row with no Git remote declaration must fail";
    assert assertMsg (
      !(evaluates (withRows {
        github = ghRow // {
          gitRemote = "https://github.com/o";
        };
      }))
    ) "a gitRemote with a path must fail";
    assert assertMsg (
      !(evaluates (withRows {
        github = ghRow // {
          gitRemote = "http://github.com";
        };
      }))
    ) "a non-https gitRemote must fail";
    assert assertMsg (
      !(evaluates (withRows {
        github = ghRow // {
          gitRemote = "https://github.com";
          gitRemoteEnv = "GITHUB_URL";
        };
      }))
    ) "both gitRemote and gitRemoteEnv must fail";
    assert assertMsg (
      !(evaluates (withRows {
        forgejo = fjRow // {
          gitRemoteEnv = "";
        };
      }))
    ) "an empty gitRemoteEnv must fail";
    # Checked whichever Driver the Harness selects, so a bad second row fails too.
    assert assertMsg (
      !(evaluates (withRows {
        fixture = row;
        other = row // {
          routes = [ (goodRoute // { path = "/"; }) ];
        };
      }))
    ) "one bad row among good ones must fail evaluation";
    pkgs.runCommand "connector-backend-rows-mkharness-validates-rows" { } "touch $out";

  connector-backend-rows-reach-document =
    pkgs.runCommand "connector-backend-rows-reach-document" { nativeBuildInputs = [ pkgs.jq ]; }
      ''
        doc=${(withRows { fixture = row; }).internals.runInputDocumentFile}
        jq -e '.backends | length == 1
          and (.[0] | keys == ["axes", "credentialEnv", "module", "name", "routes"])
          and .[0].name == "fixture"
          and .[0].credentialEnv == "FIXTURE_TOKEN"
          and .[0].routes == [{method: "GET", host: "api.fixture.test", path: "/repos"}]
          and (.[0].module | endswith("/network.wasm"))' "$doc"
        test -f "$(jq -r '.backends[0].module' "$doc")"
        jq -e '.backends[0] | keys == ["axes", "credentialEnv", "credentialScheme", "identityEnv", "module", "name", "routes"]
          and .credentialScheme == "basic" and .identityEnv == "FIXTURE_EMAIL"' ${
            (withRows { fixture = basicRow; }).internals.runInputDocumentFile
          }
        jq -e '.backends[0] | keys == ["axes", "baseEnv", "credentialEnv", "module", "name", "routes"]
          and .baseEnv == "FIXTURE_BASE_URL" and .routes[0].host == "{base}"' ${
            (withRows { fixture = baseRow; }).internals.runInputDocumentFile
          }
        jq -e '.backends[0] | keys == ["credentialEnv", "gitRemote", "module", "name", "routes"] and .gitRemote == "https://github.com"' ${
          (withRows {
            github = ghRow // {
              gitRemote = "https://github.com";
            };
          }).internals.runInputDocumentFile
        }
        jq -e '.backends[0] | keys == ["credentialEnv", "gitRemoteEnv", "module", "name", "routes"] and .gitRemoteEnv == "FORGEJO_BASE_URL"' ${
          (withRows {
            forgejo = fjRow // {
              gitRemoteEnv = "FORGEJO_BASE_URL";
            };
          }).internals.runInputDocumentFile
        }
        touch $out
      '';

  # Issue #7560: a backend row is first-party when its module is the batteries
  # Connector of the same name, never by its name. The mark is read from
  # `internals` only; the launcher input document gains no field for it.
  connector-backend-rows-first-party-mark-follows-wasm =
    let
      mark = rows: (withRows rows).internals.backendFirstParty;
      batteriesMarks = mark {
        github = ghRow;
        forgejo = fjRow;
        jira = jiraRow;
      };
    in
    assert assertMsg (
      batteriesMarks == {
        github = true;
        forgejo = true;
        jira = true;
      }
    ) "rows built from the batteries github, forgejo and jira modules must be first-party";
    assert assertMsg (
      (mark { fixture = row; }) == { fixture = false; }
    ) "a row on the fixture wasm must be third-party";
    assert assertMsg
      (
        (mark {
          github = row;
          forgejo = ghRow;
        }) == {
          github = false;
          forgejo = false;
        }
      )
      "the name alone must grant no standing: a github-named fixture row and a forgejo-named github module are third-party";
    pkgs.runCommand "connector-backend-rows-first-party-mark-follows-wasm" { } "touch $out";

  # Issue #7647: a third-party row declares `axes` and `displayName`, a
  # first-party row refuses both (its facts stay in core's rows), and a
  # third-party row never takes a first-party backend's name. A thrown message
  # is invisible to tryEval, so the wording is pinned on the pure decisions.
  connector-backend-rows-axes-and-display-name =
    let
      inherit (fixtures) hasAll;
      axesErr =
        r:
        names.backendFactsError {
          name = "fixture";
          firstParty = false;
          row = r;
        };
      firstPartyErr =
        r:
        names.backendFactsError {
          name = "github";
          firstParty = true;
          row = r;
        };
      firstPartyNames = builtins.attrNames {
        github = ghRow;
        forgejo = fjRow;
        jira = jiraRow;
      };
      nameErr =
        name:
        names.firstPartyNameError {
          inherit name firstPartyNames;
          firstParty = false;
        };
      probe =
        backend:
        fixtures.connectorConsumerVia [
          ../../lib/flakeModule.nix
          (fixtures.rowAsData "probe" { inherit backend; })
        ];
      builds = c: forces c.apps.${system}.connectors-accept.program;
    in
    assert assertMsg (evaluates (withRows {
      fixture = row;
    })) "control: a third-party row with axes and displayName must evaluate";
    assert assertMsg (evaluates (withRows {
      fixture = row // {
        axes = [
          "tracker"
          "forge"
        ];
      };
    })) "a row on both axes must evaluate";
    assert assertMsg (
      !(evaluates (withRows {
        fixture = removeAttrs row [ "axes" ];
      }))
    ) "a third-party row without axes must fail evaluation";
    assert assertMsg (
      !(evaluates (withRows {
        fixture = removeAttrs row [ "displayName" ];
      }))
    ) "a third-party row without displayName must fail evaluation";
    assert assertMsg (
      !(evaluates (withRows {
        fixture = row // {
          axes = [ ];
        };
      }))
    ) "a row with empty axes must fail evaluation";
    assert assertMsg (
      !(evaluates (withRows {
        fixture = row // {
          axes = [ "driver" ];
        };
      }))
    ) "a row with an unknown axis must fail evaluation";
    assert assertMsg (
      !(evaluates (withRows {
        fixture = row // {
          displayName = 1;
        };
      }))
    ) "a row with a non-string displayName must fail evaluation";
    assert assertMsg (
      !(evaluates (withRows {
        jira = jiraRow // {
          axes = [ "forge" ];
        };
      }))
    ) "a first-party row setting axes must fail evaluation";
    assert assertMsg (
      !(evaluates (withRows {
        jira = jiraRow // {
          displayName = "Jira";
        };
      }))
    ) "a first-party row setting displayName must fail evaluation";
    assert assertMsg (evaluates (withRows {
      jira = jiraRow;
    })) "control: a first-party row sets neither";
    assert assertMsg (evaluates (withRows {
      acme = row;
    })) "control: the same row under a non-first-party name must evaluate";
    assert assertMsg (builtins.all (
      n:
      !(evaluates (withRows {
        ${n} = row;
      }))
    ) firstPartyNames) "a third-party row named after a first-party backend must fail evaluation";
    assert assertMsg (hasAll (axesErr (removeAttrs row [ "axes" ])) [
      "backendRows.fixture"
      "`axes`"
      "third-party"
    ]) "the missing-axes refusal must name the row and the field";
    assert assertMsg (hasAll (axesErr (removeAttrs row [ "displayName" ])) [
      "backendRows.fixture"
      "`displayName`"
    ]) "the missing-displayName refusal must name the row and the field";
    assert assertMsg (hasAll (axesErr (row // { axes = [ ]; })) [
      "backendRows.fixture"
      "`axes`"
      "non-empty"
    ]) "the empty-axes refusal must name the row and the field";
    assert assertMsg (hasAll
      (axesErr (
        row
        // {
          axes = [
            "tracker"
            "driver"
          ];
        }
      ))
      [
        "backendRows.fixture"
        "`axes`"
        "'driver'"
      ]
    ) "the unknown-axis refusal must name the row, the field and the axis";
    assert assertMsg (hasAll
      (axesErr (
        row
        // {
          axes = [
            "tracker"
            "tracker"
          ];
        }
      ))
      [
        "backendRows.fixture"
        "`axes`"
        "'tracker' twice"
      ]
    ) "the duplicate-axis refusal must name the row, the field and the axis";
    assert assertMsg (
      !(evaluates (withRows {
        fixture = row // {
          axes = [
            "tracker"
            "tracker"
          ];
        };
      }))
    ) "a row naming an axis twice must fail evaluation";
    assert assertMsg (hasAll (axesErr (row // { displayName = 1; })) [
      "backendRows.fixture"
      "`displayName`"
      "non-empty string"
    ]) "the bad-displayName refusal must name the row and the field";
    assert assertMsg (axesErr row == null) "a well-formed third-party row must draw no refusal";
    assert assertMsg (hasAll (firstPartyErr { axes = [ "forge" ]; }) [
      "backendRows.github"
      "first-party"
      "`axes`"
    ]) "a first-party row's axes refusal must name the row and the field";
    assert assertMsg (hasAll (firstPartyErr { displayName = "GitHub"; }) [
      "backendRows.github"
      "first-party"
      "`displayName`"
    ]) "a first-party row's displayName refusal must name the row and the field";
    assert assertMsg (
      firstPartyErr { } == null
    ) "a first-party row setting neither must draw no refusal";
    assert assertMsg (builtins.all (
      n:
      hasAll (nameErr n) [
        "backendRows.${n}"
        "first-party ${n} Connector"
        "ADR 0073"
      ]
    ) firstPartyNames) "the first-party-name refusal must name the row and the rule";
    assert assertMsg (
      names.firstPartyNameError {
        name = "github";
        firstParty = true;
        inherit firstPartyNames;
      } == null
      && nameErr "fixture" == null
    ) "only a non-first-party row on a first-party name is refused";
    # The same values through the data registration path.
    assert assertMsg (builds (
      probe row
    )) "control: backend data with axes and displayName must evaluate";
    assert assertMsg (
      !(builds (probe (removeAttrs row [ "axes" ])))
    ) "backend data without axes must fail evaluation";
    assert assertMsg (
      !(builds (probe (row // { axes = [ ]; })))
    ) "backend data with empty axes must fail evaluation";
    assert assertMsg (
      !(builds (probe (row // { axes = [ "driver" ]; })))
    ) "backend data with an unknown axis must fail evaluation";
    assert assertMsg (
      !(builds (probe (row // { displayName = 1; })))
    ) "backend data with a non-string displayName must fail evaluation";
    pkgs.runCommand "connector-backend-rows-axes-and-display-name" { } "touch $out";

  # Issue #7560: a third-party row's credential variable is derived from its
  # row name, reaching the document through the existing `credentialEnv` field.
  connector-backend-rows-third-party-credential-derived =
    let
      doc =
        (withRows {
          my-thing = row;
          fixture = row;
        }).internals.runInputDocumentFile;
    in
    assert assertMsg (
      names.derivedCredentialEnv "fixture" == "FIXTURE_TOKEN"
    ) "a row named fixture must derive FIXTURE_TOKEN";
    assert assertMsg (
      names.derivedCredentialEnv "my-thing" == "MY_THING_TOKEN"
    ) "a hyphen in the row name must become an underscore";
    pkgs.runCommand "connector-backend-rows-third-party-credential-derived"
      { nativeBuildInputs = [ pkgs.jq ]; }
      ''
        jq -e '.backends | map({key: .name, value: .credentialEnv}) | from_entries
          == {"my-thing": "MY_THING_TOKEN", "fixture": "FIXTURE_TOKEN"}' ${doc}
        jq -e '.backends | all(.[]; keys == ["axes", "credentialEnv", "module", "name", "routes"])' ${doc}
        touch $out
      '';

  # Issue #7560: each hostile row fails evaluation, and the pure decision
  # behind the refusal names the row and the rule (a thrown message is
  # invisible to tryEval).
  connector-backend-rows-credential-refusals =
    let
      envSchema = import ../../lib/env-schema.nix;
      coreEnvs = builtins.listToAttrs (
        builtins.concatMap (
          k:
          pkgs.lib.optional ((envSchema.${k}.env or null) != null) (
            pkgs.lib.nameValuePair envSchema.${k}.env k
          )
        ) (builtins.attrNames envSchema)
      );
      decide =
        {
          name,
          r,
          firstParty ? false,
          otherEnvs ? [ ],
        }:
        names.backendCredentialError {
          inherit
            name
            firstParty
            coreEnvs
            otherEnvs
            ;
          row = r;
        };
      pinned =
        args: subs:
        let
          msg = decide args;
        in
        msg != null && builtins.all (s: has s msg) ([ "mkHarness: backendRows.${args.name}" ] ++ subs);
      forgejoRow = row // {
        gitRemoteEnv = "FORGEJO_BASE_URL";
      };
    in
    assert assertMsg (
      decide {
        name = "fixture";
        r = row;
      } == null
    ) "control: a third-party row naming nothing must pass the decision";
    assert assertMsg (
      decide {
        name = "github";
        r = ghRow;
        firstParty = true;
      } == null
    ) "control: a first-party row naming its credentialEnv must pass the decision";
    # A third-party row naming a core knob's env as its credential.
    assert assertMsg (
      !(evaluates (withRows {
        fixture = row // {
          credentialEnv = "GH_TOKEN";
        };
      }))
    ) "a third-party row naming a core knob's env must fail evaluation";
    assert assertMsg (pinned
      {
        name = "fixture";
        r = row // {
          credentialEnv = "GH_TOKEN";
        };
      }
      [
        "third-party"
        "cannot name its own `credentialEnv`"
        "`FIXTURE_TOKEN`"
      ]
    ) "the refusal must name the row, the rule and the derived name";
    # A third-party row naming another row's variable.
    assert assertMsg (
      !(evaluates (withRows {
        fixture = row // {
          credentialEnv = "OTHER_TOKEN";
        };
        other = row;
      }))
    ) "a third-party row naming another row's variable must fail evaluation";
    assert assertMsg (pinned
      {
        name = "fixture";
        r = row // {
          credentialEnv = "OTHER_TOKEN";
        };
        otherEnvs = [
          {
            row = "other";
            field = "credentialEnv";
            env = "OTHER_TOKEN";
          }
        ];
      }
      [
        "cannot name its own `credentialEnv`"
        "`FIXTURE_TOKEN`"
      ]
    ) "the refusal must name the row and the rule, not the variable it tried to claim";
    # A row name deriving something no shell can export.
    assert assertMsg (
      !(evaluates (withRows {
        "my.thing" = row;
      }))
    ) "a row whose derived credential variable is not a valid name must fail evaluation";
    assert assertMsg (pinned
      {
        name = "my.thing";
        r = row;
      }
      [
        "`MY.THING_TOKEN`"
        "not a valid environment variable name"
        "rename the row"
      ]
    ) "the refusal must name the row and the derived name";
    assert assertMsg (
      names.derivedCredentialClashError {
        name = "widget";
        derived = "WIDGET_TOKEN";
        owner = null;
      } == null
    ) "control: no owning Connector must pass the clash decision";
    assert assertMsg (
      let
        msg = names.derivedCredentialClashError {
          name = "widget";
          derived = "WIDGET_TOKEN";
          owner = "acme";
        };
      in
      msg != null
      && builtins.all (s: has s msg) [
        "'widget'"
        "`WIDGET_TOKEN`"
        "'acme'"
        "rename the row"
      ]
    ) "the clash refusal must name the row, the derived name, the owner and the fix";
    # A row claiming the mark in data.
    assert assertMsg (
      !(evaluates (withRows {
        fixture = row // {
          firstParty = true;
        };
      }))
    ) "a third-party row setting firstParty must fail evaluation";
    assert assertMsg (
      !(evaluates (withRows {
        github = ghRow // {
          gitRemote = "https://github.com";
          firstParty = true;
        };
      }))
    ) "a first-party row setting firstParty must fail evaluation";
    assert assertMsg (pinned
      {
        name = "fixture";
        r = row // {
          firstParty = true;
        };
      }
      [
        "sets `firstParty`"
        "no row may set it"
      ]
    ) "the refusal must name the row and the rule";
    # A first-party row must name its own backend's credential.
    assert assertMsg (pinned {
      name = "github";
      r = removeAttrs ghRow [ "credentialEnv" ];
      firstParty = true;
    } [ "must carry a non-empty string `credentialEnv`" ]) "the refusal must name the row and the rule";
    # A derived name that is a core knob's env: a third-party row named for a
    # core backend (forgejo, jira) would otherwise shadow that backend's token.
    assert assertMsg (
      !(evaluates (withRows {
        forgejo = forgejoRow;
      }))
    ) "a third-party row named forgejo (FORGEJO_TOKEN) must fail evaluation";
    assert assertMsg (
      !(evaluates (withRows {
        jira = row;
      }))
    ) "a third-party row named jira (JIRA_TOKEN) must fail evaluation";
    assert assertMsg (pinned
      {
        name = "forgejo";
        r = forgejoRow;
      }
      [
        "`FORGEJO_TOKEN`"
        "core knob `forgejoToken`"
      ]
    ) "the refusal must name the row, the derived name and the core knob";
    assert assertMsg (pinned
      {
        name = "jira";
        r = row;
      }
      [
        "`JIRA_TOKEN`"
        "core knob `jiraToken`"
      ]
    ) "the refusal must name the row, the derived name and the core knob";
    # A derived name that is another row's variable.
    assert assertMsg (
      !(evaluates (withRows {
        a-b = row;
        a_b = row;
      }))
    ) "two third-party rows deriving the same variable must fail evaluation";
    assert assertMsg (
      !(evaluates (withRows {
        fixture = row;
        basic = basicRow // {
          identityEnv = "FIXTURE_TOKEN";
        };
      }))
    ) "a third-party row deriving another row's identityEnv must fail evaluation";
    assert assertMsg (pinned
      {
        name = "fixture";
        r = row;
        otherEnvs = [
          {
            row = "basic";
            field = "identityEnv";
            env = "FIXTURE_TOKEN";
          }
        ];
      }
      [
        "`FIXTURE_TOKEN`"
        "backendRows.basic's `identityEnv`"
      ]
    ) "the refusal must name the row, the derived name, the other row and its field";
    pkgs.runCommand "connector-backend-rows-credential-refusals" { } "touch $out";

  # No backend row: the document has no `backends` key and is the plain
  # Harness's own derivation, so existing Consumers build unchanged.
  connector-backend-rows-absent-is-unchanged =
    assert assertMsg (
      (withRows { }).internals.runInputDocumentFile.drvPath
      == plain.internals.runInputDocumentFile.drvPath
    ) "no backend rows must leave the input document byte-identical";
    pkgs.runCommand "connector-backend-rows-absent-is-unchanged" { nativeBuildInputs = [ pkgs.jq ]; } ''
      jq -e 'has("backends") | not' ${plain.internals.runInputDocumentFile}
      touch $out
    '';

  # Issue #6805: a fixture Consumer importing a Connector from outside core
  # gets that Connector's backend row in the launcher's input document, with
  # the credential resolved from the Connector's own knob to its env var.
  connector-backend-rows-data-forwards-row =
    let
      fixtureConnector = (import ../fixtures/connector).lib.connector { core = import ../../core; };
      consumerWith = modules: fixtures.connectorConsumerVia ([ ../../lib/flakeModule.nix ] ++ modules);
      withRow = consumerWith [ (fixtures.registerData fixtureConnector.data) ];
      # A Connector row is registered by one data entry only (a second one of
      # the same name throws), so each negative case is a fresh `probe` row
      # rather than an override of the fixture's.
      secretKnob = {
        env = "PROBE_TOKEN";
        group = "dispatch";
        secret = true;
        doc = "credential knob of a probe Connector in the backend-row checks";
        flakeOption = false;
        boxEnv = false;
      };
      emailKnob = {
        env = "PROBE_EMAIL";
        group = "dispatch";
        doc = "Basic identity knob of a probe Connector in the backend-row checks";
        flakeOption = false;
        boxEnv = false;
      };
      baseKnob = {
        env = "PROBE_BASE_URL";
        group = "dispatch";
        doc = "site base URL knob of a probe Connector in the backend-row checks";
        flakeOption = false;
        boxEnv = false;
      };
      probeKnobs = {
        probeToken = secretKnob;
        probeEmail = emailKnob;
        probeBase = baseKnob;
      };
      # A third-party row: the fixture wasm, and no credential (core derives
      # PROBE_TOKEN from the row name, which `probeToken` above carries).
      probeBackend = {
        module = p: "${fixtures.connectorWasm}/network.wasm";
        routes = [ goodRoute ];
        axes = [ "tracker" ];
        displayName = "Probe";
      };
      # A first-party row of core backend `rowName`: the batteries Connector's
      # own wasm, the only thing that grants it a credential of its own.
      firstParty =
        rowName:
        removeAttrs probeBackend [
          "axes"
          "displayName"
        ]
        // {
          module = (batteries rowName null).module;
        };
      basicBackend = probeBackend // {
        scheme = "basic";
        identity = "probeEmail";
        base = "probeBase";
        routes = [ (goodRoute // { host = "{base}"; }) ];
      };
      probe =
        {
          knobs ? probeKnobs,
          backend ? probeBackend,
        }:
        consumerWith [ (fixtures.rowAsData "probe" { inherit knobs backend; }) ];
      names = import ../../lib/connector-names.nix;
      coreBackends = import ../../lib/backends/default.nix;
      # A first-party row of core backend `rowName` naming core knob `knob` as
      # its own credential.
      probeRow = rowName: knob: probeGitRow rowName knob "probeBase";
      # The same row declaring `gitRemote` as `git` (null leaves it undeclared).
      probeGitRow =
        rowName: knob: git:
        probeBackendRow rowName (
          (firstParty rowName)
          // {
            credential = knob;
            base = "probeBase";
            gitRemote = git;
          }
        );
      # A third-party row named `rowName` (the fixture wasm) naming core knob
      # `knob` as its credential: the name alone must grant no standing.
      thirdPartyRow =
        rowName: knob:
        probeBackendRow rowName (
          probeBackend
          // {
            credential = knob;
            base = "probeBase";
            gitRemote = "probeBase";
          }
        );
      probeBackendRow =
        rowName: backend:
        consumerWith [
          (fixtures.rowAsData rowName {
            # The checked-in accepted baseline (spindrift-connector-routes.json)
            # holds `{base}` routes for the forgejo and jira rows, which
            # mkHarness refuses without a `baseEnv`.
            knobs.probeBase = baseKnob;
            inherit backend;
          })
        ];
      # A basic first-party row of core backend `rowName` with its own
      # credential, naming core knob `knob` as its identity.
      probeIdentityRow =
        rowName: knob:
        consumerWith [
          (fixtures.rowAsData rowName {
            knobs.probeBase = baseKnob;
            knobs.probeToken = secretKnob;
            backend =
              removeAttrs basicBackend [
                "axes"
                "displayName"
              ]
              // {
                inherit (firstParty rowName) module;
                credential = "probeToken";
                identity = knob;
              };
          })
        ];
      programOf = c: c.apps.${system}.connectors-accept.program;
      builds = c: (builtins.tryEval (builtins.seq (programOf c) true)).success;
    in
    assert assertMsg (builds withRow) "control: the fixture Connector's row must evaluate";
    assert assertMsg (builds (probe { })) "control: a secret host-only knob must be accepted";
    # A third-party row names no credential at all (issue #7560): core derives
    # it, so even its own secret knob, a missing knob or a core knob is refused.
    assert assertMsg (
      !(builds (probe {
        backend = probeBackend // {
          credential = "probeToken";
        };
      }))
    ) "a third-party row naming its own credential knob must fail evaluation";
    assert assertMsg (
      !(builds (probe {
        backend = probeBackend // {
          credential = "noSuchKnob";
        };
      }))
    ) "a credential that is no knob of the Connector must fail evaluation";
    assert assertMsg (
      !(builds (probe {
        backend = probeBackend // {
          credential = "ghToken";
        };
      }))
    ) "a credential naming a core knob must fail evaluation";
    # The point of issue #7560: registering under a core backend's name, with
    # a module that is not the batteries build, buys no core token.
    assert assertMsg (
      !(builds (thirdPartyRow "github" "ghToken"))
    ) "a third-party row named github must not name GH_TOKEN";
    assert assertMsg (
      !(builds (thirdPartyRow "forgejo" "forgejoToken"))
    ) "a third-party row named forgejo must not name its core token";
    assert assertMsg (
      !(builds (thirdPartyRow "jira" "jiraToken"))
    ) "a third-party row named jira must not name its core token";
    assert assertMsg (
      !(builds (probeBackendRow "github" (firstParty "github" // { credential = null; })))
    ) "a first-party row must name its credential";
    # A third-party row with no knob of the derived env still evaluates; the
    # credential then has no doctor-visible knob.
    assert assertMsg (builds (probe {
      knobs = removeAttrs probeKnobs [ "probeToken" ];
    })) "a third-party row with no credential knob must evaluate";
    # Another Connector's knob already carries the derived variable: the host
    # would hand that knob's value to this row's routes.
    assert assertMsg (
      !(builds (consumerWith [
        (fixtures.rowAsData "acme" {
          knobs.acmeWidget = secretKnob // {
            env = "WIDGET_TOKEN";
          };
        })
        (fixtures.rowAsData "widget" { backend = probeBackend; })
      ]))
    ) "a derived credential variable that is another Connector's knob must fail evaluation";
    assert assertMsg (builds (consumerWith [
      (fixtures.rowAsData "acme" {
        knobs.acmeWidget = secretKnob // {
          env = "OTHER_WIDGET_TOKEN";
        };
      })
      (fixtures.rowAsData "widget" { backend = probeBackend; })
    ])) "control: a distinct knob env on another Connector must evaluate";
    # A row may name its own backend's core token (issue #7498). `probe` is
    # no core backend, so a core knob stays refused there above.
    assert assertMsg (builds (
      probeRow "github" "ghToken"
    )) "a github row naming its own token must evaluate";
    assert assertMsg (builds (
      probeRow "forgejo" "forgejoToken"
    )) "a forgejo row naming its own token must evaluate";
    assert assertMsg (builds (
      probeRow "jira" "jiraToken"
    )) "a jira row naming its own token must evaluate";
    assert assertMsg (
      !(builds (probeRow "github" "forgejoToken"))
    ) "a github row naming the forgejo token must fail evaluation";
    assert assertMsg (
      !(builds (probeRow "github" "issueTracker"))
    ) "a github row naming a non-token core knob must fail evaluation";
    # A forge row declares its Git remote: a literal https origin or a known
    # knob; the message naming the row is pinned on the pure decision below.
    assert assertMsg (builds (
      probeGitRow "github" "ghToken" "https://github.com"
    )) "a forge row with a literal https origin must evaluate";
    assert assertMsg (builds (
      probeGitRow "forgejo" "forgejoToken" "probeBase"
    )) "a forge row naming a knob must evaluate";
    assert assertMsg (builds (
      probeGitRow "jira" "jiraToken" null
    )) "a non-forge row may omit gitRemote";
    assert assertMsg (
      !(builds (probeGitRow "github" "ghToken" null))
    ) "a forge row with no gitRemote must fail evaluation";
    assert assertMsg (
      !(builds (probeGitRow "github" "ghToken" "notAKnob"))
    ) "a gitRemote that is neither an origin nor a knob must fail evaluation";
    assert assertMsg (
      !(builds (probeGitRow "github" "ghToken" "http://github.com"))
    ) "an http gitRemote must fail evaluation";
    assert assertMsg (
      !(builds (probeGitRow "github" "ghToken" "https://github.com/o"))
    ) "a gitRemote origin with a path must fail evaluation";
    assert assertMsg (
      let
        err =
          name: value:
          names.gitRemoteError {
            backends = coreBackends;
            inherit name value;
            knobKnown = value == "forgejoBaseURL";
          };
      in
      err "github" null
      == "Connector 'github': a Code Forge backend row must declare `gitRemote` (a literal https origin, or the env-schema key of the knob holding the forge's https base URL)"
      && err "jira" null == null
      && err "github" "https://github.com" == null
      && err "github" "https://git.example:8443" == null
      && err "forgejo" "forgejoBaseURL" == null
      &&
        err "github" "notAKnob"
        == "Connector 'github': backend gitRemote 'notAKnob' is neither an https origin nor an env-schema knob"
      &&
        err "github" "http://github.com"
        == "Connector 'github': backend gitRemote 'http://github.com' is neither an https origin nor an env-schema knob"
      &&
        err "github" "https://github.com/o"
        == "Connector 'github': backend gitRemote 'https://github.com/o' is not a bare https origin (`https://host[:port]`, no path, query or userinfo)"
    ) "a gitRemote refusal must name the Connector and the offending value";
    # A basic row's identity may name its own backend's `identityEnvVar` knob
    # (issue #7501), and no other core knob.
    assert assertMsg (builds (
      probeIdentityRow "jira" "jiraEmail"
    )) "a jira row naming its own email as identity must evaluate";
    assert assertMsg (
      !(builds (probeIdentityRow "github" "ghToken"))
    ) "a github row naming a core knob as identity must fail evaluation";
    assert assertMsg (
      !(builds (probeIdentityRow "jira" "jiraToken"))
    ) "a jira row naming its token as identity must fail evaluation";
    assert assertMsg (
      !(builds (probeRow "jira" "jiraEmail"))
    ) "a jira row naming its email as credential must fail evaluation";
    # `builds` cannot see why a row was refused, so the refusal text is pinned
    # on the pure decision, against the real core entries.
    assert assertMsg
      (
        let
          core = (import ../../lib/row-knobs.nix).fold (import ../../lib/connector-knobs.nix [ ]);
          errAs =
            field: secret: name: key:
            names.backendKnobError {
              backends = coreBackends;
              inherit
                name
                key
                field
                secret
                ;
              ownKnob = null;
              coreEntry = core.${key} or null;
            };
          err = errAs "credential" true;
          errId = errAs "identity" false;
          notOwn =
            field: name: key:
            "Connector '${name}': backend ${field} '${key}' is not in this Connector's `knobs` (a core knob cannot be a backend ${field})";
        in
        err "github" "ghToken" == null
        && err "forgejo" "forgejoToken" == null
        && err "jira" "jiraToken" == null
        && err "github" "forgejoToken" == notOwn "credential" "github" "forgejoToken"
        && err "github" "issueTracker" == notOwn "credential" "github" "issueTracker"
        && err "probe" "ghToken" == notOwn "credential" "probe" "ghToken"
        && errId "jira" "jiraEmail" == null
        && errId "jira" "jiraToken" == notOwn "identity" "jira" "jiraToken"
        && errId "github" "ghToken" == notOwn "identity" "github" "ghToken"
        && err "jira" "jiraEmail" == notOwn "credential" "jira" "jiraEmail"
      )
      "only a core backend's own token knob (credential) or identity knob (identity) may be named by its row, and every other core knob is refused as such";
    assert assertMsg (
      !(builds (probe {
        knobs.probeToken = secretKnob // {
          boxEnv = true;
        };
      }))
    ) "a credential knob with boxEnv = true must fail evaluation";
    assert assertMsg (
      !(builds (probe {
        knobs.probeToken = secretKnob // {
          secret = false;
        };
      }))
    ) "a credential knob that is not secret must fail evaluation";
    assert assertMsg (
      !(builds (probeBackendRow "gh" probeBackend))
    ) "a third-party row whose derived credential variable is a core env var must fail evaluation";
    assert assertMsg (
      names.backendKnobError {
        backends = coreBackends;
        name = "probe";
        field = "credential";
        secret = true;
        key = "probeToken";
        ownKnob = removeAttrs secretKnob [ "env" ];
        coreEntry = null;
      } == "Connector 'probe': backend credential 'probeToken' has no `env`"
    ) "a credential knob without an env var must be refused";
    assert assertMsg (
      !(builds (probe {
        backend = probeBackend // {
          routes = [ (goodRoute // { path = "/"; }) ];
        };
      }))
    ) "a blanket-root route must fail evaluation";
    assert assertMsg (
      !(builds (probe {
        backend = removeAttrs probeBackend [ "module" ];
      }))
    ) "a backend row without a module must fail evaluation";
    assert assertMsg (
      !(builds (probe {
        backend = removeAttrs probeBackend [ "routes" ] // {
          route = [ goodRoute ];
        };
      }))
    ) "a misspelled backend field must fail evaluation";
    assert assertMsg (builds (probe {
      backend = basicBackend;
    })) "control: a basic row must evaluate";
    assert assertMsg (
      !(builds (probe {
        backend = removeAttrs basicBackend [ "identity" ];
      }))
    ) "a basic scheme without an identity must fail evaluation";
    assert assertMsg (
      !(builds (probe {
        backend = probeBackend // {
          identity = "probeEmail";
        };
      }))
    ) "an identity on a bearer row must fail evaluation";
    assert assertMsg (
      !(builds (probe {
        backend = basicBackend // {
          identity = "noSuchKnob";
        };
      }))
    ) "an identity that is no knob of the Connector must fail evaluation";
    assert assertMsg (
      !(builds (probe {
        backend = basicBackend // {
          identity = "ghToken";
        };
      }))
    ) "an identity naming a core knob must fail evaluation";
    assert assertMsg (
      !(builds (probe {
        knobs = probeKnobs // {
          probeEmail = emailKnob // {
            boxEnv = true;
          };
        };
        backend = basicBackend;
      }))
    ) "an identity knob with boxEnv = true must fail evaluation";
    assert assertMsg (
      !(builds (probe {
        backend = basicBackend // {
          base = "noSuchKnob";
        };
      }))
    ) "a base that is no env-schema knob must fail evaluation";
    assert assertMsg (
      !(builds (probe {
        backend = removeAttrs basicBackend [ "base" ];
      }))
    ) "a {base} route without a base must fail evaluation";
    assert assertMsg (builds (probe {
      backend = basicBackend // {
        base = "ghToken";
      };
    })) "a base may be any env-schema knob, core included";
    pkgs.runCommand "connector-backend-rows-data-forwards-row" { nativeBuildInputs = [ pkgs.jq ]; } ''
      ${fixtures.runInputDocOf (programOf withRow)}
      jq -e '.backends == [{
          name: "fixture",
          module: .backends[0].module,
          routes: [{method: "GET", host: "api.fixture.test", path: "/repos"}],
          credentialEnv: "FIXTURE_TOKEN",
          credentialKnob: "fixtureToken",
          axes: ["tracker"]
        }] and (.backends[0].module | endswith("/network.wasm"))' "$doc"
      test -f "$(jq -r '.backends[0].module' "$doc")"
      ${fixtures.runInputDocOf (
        programOf (probe {
          backend = basicBackend;
        })
      )}
      jq -e '.backends == [{
          name: "probe",
          module: .backends[0].module,
          routes: [{method: "GET", host: "{base}", path: "/repos"}],
          credentialEnv: "PROBE_TOKEN",
          credentialKnob: "probeToken",
          credentialScheme: "basic",
          identityEnv: "PROBE_EMAIL",
          baseEnv: "PROBE_BASE_URL",
          axes: ["tracker"]
        }]' "$doc"
      ${fixtures.runInputDocOf (
        programOf (probe {
          knobs = removeAttrs probeKnobs [ "probeToken" ];
        })
      )}
      jq -e '.backends[0] | .credentialEnv == "PROBE_TOKEN" and (has("credentialKnob") | not)' "$doc"
      touch $out
    '';

  # Issue #6806: the accepted Route baseline is read from the Consumer's own
  # source tree (spindrift-connector-routes.json), keyed by Connector name, and
  # rides on the document's backend row. A missing file or key is no entry,
  # which refuses the run (issue #7564).
  connector-backend-rows-accepted-baseline =
    let
      consumers = fixtures.baselineConsumers;
      programOf = c: c.apps.${system}.connectors-accept.program;
      builds = c: (builtins.tryEval (builtins.seq (programOf c) true)).success;
      withAccepted =
        a:
        withRows {
          fixture = row // {
            acceptedRoutes = a;
          };
        };
      blanket = [ (goodRoute // { path = "/"; }) ];
    in
    assert assertMsg (builds consumers.accepted) "control: a well-formed baseline must evaluate";
    assert assertMsg (evaluates (withAccepted [ ])) "an empty accepted baseline must be allowed";
    assert assertMsg (evaluates (withAccepted [ goodRoute ])) "a well-formed baseline must evaluate";
    assert assertMsg (
      !(evaluates (withAccepted blanket))
    ) "a blanket-root accepted baseline must fail evaluation";
    assert assertMsg (
      !(evaluates (withAccepted "x"))
    ) "an accepted baseline that is not a list must fail evaluation";
    assert assertMsg (
      !(builds (fixtures.baselineConsumerAt ../fixtures/connector-routes/blanket))
    ) "a blanket-root path in the baseline file must fail evaluation";
    assert assertMsg (
      !(builds (fixtures.baselineConsumerAt ../fixtures/connector-routes/list))
    ) "a baseline file whose top level is not an object must fail evaluation";
    assert assertMsg (
      !(builds (fixtures.baselineConsumerAt ../fixtures/connector-routes/grant-badkey))
    ) "a grant entry with an unknown key must fail evaluation";
    assert assertMsg (
      !(builds (fixtures.baselineConsumerAt ../fixtures/connector-routes/grant-badvalue))
    ) "a grant entry with a non-string value must fail evaluation";
    assert assertMsg (
      !(builds (fixtures.baselineConsumerAt ../fixtures/connector-routes/grant-badscheme))
    ) "a grant entry with an unknown credentialScheme must fail evaluation";
    assert assertMsg (
      !(builds (fixtures.baselineConsumerAt ../fixtures/connector-routes/grant-bothremotes))
    ) "a grant entry with both gitRemote and gitRemoteEnv must fail evaluation";
    assert assertMsg (evaluates (withRows {
      fixture = row // {
        acceptedGrant = {
          credentialEnv = "FIXTURE_TOKEN";
        };
      };
    })) "a direct row may carry an acceptedGrant";
    assert assertMsg (evaluates (withRows {
      fixture = row // {
        acceptedGrant = {
          credentialEnv = "FIXTURE_TOKEN";
          axes = [
            "tracker"
            "forge"
          ];
        };
      };
    })) "a direct acceptedGrant may record axes";
    assert assertMsg (
      !(evaluates (withRows {
        fixture = row // {
          acceptedGrant = {
            axes = "tracker";
          };
        };
      }))
    ) "a direct acceptedGrant whose axes are no list must fail evaluation";
    assert assertMsg (
      !(evaluates (withRows {
        fixture = row // {
          acceptedGrant = {
            axes = [ "filer" ];
          };
        };
      }))
    ) "a direct acceptedGrant naming an unknown axis must fail evaluation";
    assert assertMsg (
      !(evaluates (withRows {
        fixture = row // {
          acceptedGrant = {
            tokenEnv = "X";
          };
        };
      }))
    ) "a direct acceptedGrant with an unknown key must fail evaluation";
    assert assertMsg (
      !(evaluates (withRows {
        fixture = row // {
          acceptedGrant = {
            credentialEnv = 7;
          };
        };
      }))
    ) "a direct acceptedGrant with a non-string value must fail evaluation";
    pkgs.runCommand "connector-backend-rows-accepted-baseline" { nativeBuildInputs = [ pkgs.jq ]; } ''
      jq -e '.backends[0] | .acceptedRoutes == [{method: "GET", host: "api.fixture.test", path: "/repos"}] and .acceptedGrant == {credentialEnv: "FIXTURE_TOKEN", credentialScheme: "bearer", baseEnv: "FIXTURE_BASE_URL", gitRemote: "https://fixture.test", axes: ["tracker"]}' ${fixtures.connectorBaselineDocs}/grant.json
      jq -e '.backends[0].acceptedGrant == {credentialEnv: "FIXTURE_TOKEN", credentialScheme: "bearer", baseEnv: "FIXTURE_BASE_URL", gitRemoteEnv: "FIXTURE_REMOTE", axes: ["tracker"]}' ${fixtures.connectorBaselineDocs}/grant-remoteenv.json
      jq -e '.backends[0] | .axes == ["tracker"] and (.acceptedGrant | has("axes") | not)' ${fixtures.connectorBaselineDocs}/grant-noaxes.json
      jq -e '.backends[0] | has("acceptedGrant") | not' ${fixtures.connectorBaselineDocs}/accepted.json
      jq -e '.backends[0].acceptedRoutes == [{method: "GET", host: "api.fixture.test", path: "/repos/x"}]' ${fixtures.connectorBaselineDocs}/widened.json
      jq -e '.backends[0].acceptedRoutes == [{method: "GET", host: "api.fixture.test", path: "/repos"}]' ${fixtures.connectorBaselineDocs}/accepted.json
      jq -e '.backends[0].credentialKnob == "fixtureToken"' ${fixtures.connectorBaselineDocs}/accepted.json
      jq -e '.backends[0].acceptedRoutes | map(.method) == ["GET", "POST"]' ${fixtures.connectorBaselineDocs}/narrowed.json
      jq -e '.backends[0].acceptedRoutes == [{method: "GET", host: "api.fixture.test", path: "/repos"}]' ${
        (withAccepted [ goodRoute ]).internals.runInputDocumentFile
      }
      touch $out
    '';

  # Issue #7564: the pure decision behind a third-party row's baseline
  # refusal (a thrown message is invisible to tryEval, so assert on it).
  connector-backend-rows-baseline-refusal-decision =
    let
      baseline = import ../../lib/connector-baseline.nix;
      grant = {
        credentialEnv = "FIXTURE_TOKEN";
        credentialScheme = "bearer";
        baseEnv = "FIXTURE_BASE_URL";
        gitRemote = "https://fixture.test";
        axes = [ "tracker" ];
      };
      live = {
        routes = [ goodRoute ];
        credentialEnv = "FIXTURE_TOKEN";
        credentialScheme = "bearer";
        baseEnv = "FIXTURE_BASE_URL";
        gitRemote = "https://fixture.test";
        axes = [ "tracker" ];
        acceptedRoutes = [ goodRoute ];
        acceptedGrant = grant;
      };
      refusal =
        r:
        baseline.refusal {
          name = "fixture";
          row = r;
        };
      head = "mkHarness: third-party Connector 'fixture' declares more than its entry in the accepted baseline (spindrift-connector-routes.json):";
      mentions = r: items: builtins.all (i: pkgs.lib.hasInfix i (refusal r)) ([ head ] ++ items);
      noEntry = removeAttrs live [
        "acceptedRoutes"
        "acceptedGrant"
      ];
      legacy = removeAttrs live [ "acceptedGrant" ];
      accept = [
        "nix run .#connectors-accept"
        "spindrift connectors accept"
      ];
    in
    assert assertMsg (refusal live == null) "an exact-match entry must not refuse";
    assert assertMsg (
      refusal (
        live
        // {
          routes = [ (goodRoute // { path = "/repos/x"; }) ];
          credentialScheme = "";
          acceptedGrant = grant // {
            credentialScheme = "";
          };
        }
      ) == null
    ) "a narrower path and an empty scheme against an absent accepted scheme must not refuse";
    assert assertMsg (
      refusal (removeAttrs live [ "baseEnv" ] // { routes = [ ]; }) == null
    ) "fewer Routes and an absent base are narrowings, never refused";
    assert assertMsg (
      refusal noEntry != null
      && builtins.all (s: pkgs.lib.hasInfix s (refusal noEntry)) (
        [
          "Connector 'fixture'"
          "spindrift-connector-routes.json"
        ]
        ++ accept
      )
    ) "no entry must refuse, naming the Connector, the file and the accept command";
    assert assertMsg (
      refusal (live // { acceptedRoutes = [ ]; }) != null
    ) "an empty accepted list is an entry, and a live Route is added against it";
    assert assertMsg (mentions (
      live
      // {
        routes = [
          goodRoute
          (goodRoute // { path = "/repos/y"; })
        ];
        acceptedRoutes = [ (goodRoute // { path = "/other"; }) ];
      }
    ) ([ "added Route GET api.fixture.test /repos/y" ] ++ accept)) "a new Route must be listed";
    assert assertMsg (mentions (live // { acceptedRoutes = [ (goodRoute // { path = "/repos/x"; }) ]; })
      [
        "added Route GET api.fixture.test /repos"
      ]
    ) "a broader path than the accepted one must be listed";
    assert assertMsg (mentions (live // { routes = [ (goodRoute // { followRedirect = true; }) ]; }) [
      "added Route GET api.fixture.test /repos (followRedirect)"
    ]) "a new followRedirect opt-in must be listed";
    assert assertMsg (
      refusal (
        live
        // {
          routes = [ (goodRoute // { followRedirect = true; }) ];
          acceptedRoutes = [ (goodRoute // { followRedirect = true; }) ];
        }
      ) == null
    ) "an accepted followRedirect must not refuse";
    assert assertMsg (
      refusal (live // { routes = [ (goodRoute // { path = "/reposx"; }) ]; }) != null
    ) "a sibling path sharing only a prefix must not be covered";
    assert assertMsg (mentions (live // { credentialEnv = "OTHER_TOKEN"; }) [
      "changed credential variable OTHER_TOKEN (accepted: FIXTURE_TOKEN)"
    ]) "a changed credential variable must be listed";
    assert assertMsg (mentions (live // { credentialScheme = "basic"; }) [
      "changed authorization scheme basic (accepted: bearer)"
    ]) "a basic scheme against an accepted bearer must be listed";
    assert assertMsg (mentions (live // { baseEnv = "OTHER_BASE"; }) [
      "changed base OTHER_BASE (accepted: FIXTURE_BASE_URL)"
    ]) "a changed base must be listed";
    assert assertMsg (mentions (live // { acceptedGrant = removeAttrs grant [ "baseEnv" ]; }) [
      "added base FIXTURE_BASE_URL"
    ]) "a base the grant lacks must be listed as added";
    assert assertMsg (mentions
      (removeAttrs live [ "gitRemote" ] // { gitRemoteEnv = "FIXTURE_REMOTE"; })
      [ "changed Git remote env FIXTURE_REMOTE (accepted: https://fixture.test)" ]
    ) "a Git remote switched from a literal to an env must be listed";
    assert assertMsg (mentions (live // { gitRemote = "https://other.test"; }) [
      "changed Git remote https://other.test (accepted: https://fixture.test)"
    ]) "a changed literal Git remote must be listed";
    assert assertMsg (mentions legacy [
      "added credential variable FIXTURE_TOKEN"
      "added authorization scheme bearer"
      "added base FIXTURE_BASE_URL"
      "added Git remote https://fixture.test"
      "added axis tracker"
    ]) "a Routes-only entry has no grant, so every declared part is added";
    assert assertMsg (mentions (live // {
      axes = [
        "tracker"
        "forge"
      ];
    }) [ "added axis forge" ]) "an axis the accepted grant lacks must be listed as added";
    assert assertMsg (
      !(pkgs.lib.hasInfix "added axis tracker" (
        refusal (
          live
          // {
            axes = [
              "tracker"
              "forge"
            ];
          }
        )
      ))
    ) "an accepted axis must not be listed";
    assert assertMsg (
      refusal (
        live
        // {
          axes = [ "forge" ];
          acceptedGrant = grant // {
            axes = [
              "tracker"
              "forge"
            ];
          };
        }
      ) == null
    ) "declaring fewer axes than accepted is a narrowing, never refused";
    assert assertMsg (
      refusal (live // { acceptedGrant = removeAttrs grant [ "axes" ]; }) != null
    ) "a grant recording no axes accepts none";
    assert assertMsg (mentions (legacy // { credentialScheme = "basic"; }) [
      "added authorization scheme basic"
    ]) "a basic scheme against a Routes-only entry is added, not changed";
    assert assertMsg (
      !(pkgs.lib.hasInfix "changed" (refusal legacy))
    ) "nothing is changed against a Routes-only entry";
    assert assertMsg (
      refusal (live // { acceptedGrant = removeAttrs grant [ "credentialScheme" ]; }) == null
    ) "an accepted grant without a scheme accepts bearer";
    pkgs.runCommand "connector-backend-rows-baseline-refusal-decision" { } "touch $out";

  # Issue #7456: a backend row carries no shadow or primary field. mkHarness
  # ignores one a Consumer hands it, so the document never grows the retired
  # switches.
  connector-backend-rows-shadow-fields =
    let
      switched = row // {
        shadow = true;
        shadowEvery = 3;
        primary = true;
      };
    in
    assert assertMsg (evaluates (withRows {
      fixture = switched;
    })) "control: a row carrying stale switches must still evaluate";
    pkgs.runCommand "connector-backend-rows-shadow-fields" { nativeBuildInputs = [ pkgs.jq ]; } ''
      jq -e '.backends[0] | has("shadow") or has("shadowEvery") or has("primary") | not' ${
        (withRows { fixture = switched; }).internals.runInputDocumentFile
      }
      jq -e '.backends[0] | has("shadow") or has("shadowEvery") or has("primary") | not' ${
        (withRows { fixture = row; }).internals.runInputDocumentFile
      }
      touch $out
    '';

  # Issue #7456: `shadow` on a backend row, or on a row with neither a driver
  # nor a backend, is refused at evaluation; the document carries no switch.
  connector-backend-rows-flake-module-shadow =
    let
      consumerWith = modules: fixtures.connectorConsumerVia ([ ../../lib/flakeModule.nix ] ++ modules);
      probeModule = fixtures.rowAsData "probe" {
        knobs.probeToken = {
          env = "PROBE_TOKEN";
          group = "dispatch";
          secret = true;
          doc = "credential knob of a probe Connector in the backend-row shadow checks";
          flakeOption = false;
          boxEnv = false;
        };
        backend = {
          module = p: "${fixtures.connectorWasm}/network.wasm";
          routes = [ goodRoute ];
          axes = [ "tracker" ];
          displayName = "Probe";
        };
      };
      programOf = c: c.apps.${system}.connectors-accept.program;
      builds = c: (builtins.tryEval (builtins.seq (programOf c) true)).success;
      plain = consumerWith [ probeModule ];
    in
    assert assertMsg (builds plain) "control: the fixture Consumer must evaluate";
    assert assertMsg (
      !(builds (consumerWith [
        probeModule
        { spindrift.connectors.probe.shadow = true; }
      ]))
    ) "shadow on a backend row must fail evaluation";
    assert assertMsg (
      !(builds (consumerWith [ { spindrift.connectors.nothing.shadow = true; } ]))
    ) "a shadowed row with neither a driver nor a backend must fail evaluation";
    pkgs.runCommand "connector-backend-rows-flake-module-shadow" { nativeBuildInputs = [ pkgs.jq ]; } ''
      ${fixtures.runInputDocOf (programOf plain)}
      jq -e '.backends[0] | has("shadow") or has("shadowEvery") or has("primary") | not' "$doc"
      touch $out
    '';

  # Issue #7564: the baseline refusal gates the programs that run Connectors
  # (the CLI and the daemon), never the input document, so `connectors accept`
  # still runs for the Connector it blocks.
  connector-backend-rows-third-party-beyond-baseline-refused =
    let
      accepts = h: forces h.apps.connectors-accept.program;
      grant = {
        credentialEnv = "FIXTURE_TOKEN";
        credentialScheme = "bearer";
        axes = [ "tracker" ];
      };
      narrower = goodRoute // {
        path = "/repos/x";
      };
      widened = withRows {
        fixture = row // {
          acceptedRoutes = [ narrower ];
          acceptedGrant = grant;
        };
      };
      grantWidened = withRows {
        fixture = row // {
          acceptedRoutes = [ goodRoute ];
          acceptedGrant = grant // {
            credentialEnv = "OTHER_TOKEN";
          };
        };
      };
      schemeWidened = withRows {
        fixture = row // {
          credentialScheme = "basic";
          identityEnv = "FIXTURE_EMAIL";
          acceptedRoutes = [ goodRoute ];
          acceptedGrant = grant;
        };
      };
      axisWidened = withRows {
        fixture = row // {
          axes = [
            "tracker"
            "forge"
          ];
          acceptedRoutes = [ goodRoute ];
          acceptedGrant = grant;
        };
      };
      axisWithin = withRows {
        fixture = row // {
          acceptedRoutes = [ goodRoute ];
          acceptedGrant = grant;
        };
      };
      flakeNoAxes = fixtures.baselineConsumers.grant-noaxes;
      messages = h: builtins.concatStringsSep "\n" h.internals.baselineRefusals;
      flakeWidened = fixtures.baselineConsumers.widened;
    in
    assert assertMsg (
      !(forces widened.apps.default.program)
    ) "a widened Route table must refuse the CLI";
    assert assertMsg (
      !(forces widened.apps.daemon.program)
    ) "a widened Route table must refuse the daemon";
    assert assertMsg (!(eitherRuns grantWidened)) "a widened grant must refuse the CLI and the daemon";
    assert assertMsg (
      accepts widened && accepts grantWidened
    ) "connectors accept must still evaluate for a refused Connector";
    assert assertMsg (
      evaluates widened && evaluates grantWidened
    ) "the input document must still render";
    assert assertMsg (
      pkgs.lib.hasInfix "'fixture'" (messages widened)
      && pkgs.lib.hasInfix "added Route GET api.fixture.test /repos" (messages widened)
    ) "the refusal must name the Connector and the added Route";
    assert assertMsg (pkgs.lib.hasInfix "changed credential variable" (
      messages grantWidened
    )) "the refusal must name the widened grant";
    assert assertMsg (pkgs.lib.hasInfix "changed authorization scheme basic (accepted: bearer)" (
      messages schemeWidened
    )) "the refusal must name the widened authorization scheme";
    assert assertMsg (bothRun axisWithin) "control: a row inside its accepted axes must run";
    assert assertMsg (!(eitherRuns axisWidened)) "an axis beyond the accepted baseline must refuse the CLI and the daemon";
    assert assertMsg (
      accepts axisWidened && evaluates axisWidened
    ) "connectors accept and the input document must still evaluate for an axis-refused Connector";
    assert assertMsg (pkgs.lib.hasInfix "added axis forge" (messages axisWidened)
      && !(pkgs.lib.hasInfix "added axis tracker" (messages axisWidened))
    ) "the refusal must name the added axis alone";
    assert assertMsg (
      !(forces flakeNoAxes.apps.${system}.default.program)
    ) "a Consumer whose baseline file records no axes must refuse a Connector declaring one";
    assert assertMsg (
      !(forces flakeWidened.apps.${system}.default.program)
    ) "a Consumer whose baseline file is narrower than its Connector must refuse";
    assert assertMsg (forces
      flakeWidened.apps.${system}.connectors-accept.program
    ) "that Consumer must still evaluate connectors-accept";
    pkgs.runCommand "connector-backend-rows-third-party-beyond-baseline-refused" { } "touch $out";

  connector-backend-rows-third-party-no-entry-refused =
    let
      noEntry = withRows { fixture = row; };
      fixtureConnector = (import ../fixtures/connector).lib.connector { core = import ../../core; };
      flakeNoEntry = fixtures.connectorConsumerVia [
        ../../lib/flakeModule.nix
        (fixtures.registerData fixtureConnector.data)
      ];
      message = builtins.concatStringsSep "\n" noEntry.internals.baselineRefusals;
    in
    assert assertMsg (
      !(forces noEntry.apps.default.program)
    ) "a third-party row with no baseline entry must refuse the CLI";
    assert assertMsg (
      !(forces noEntry.apps.daemon.program)
    ) "a third-party row with no baseline entry must refuse the daemon";
    assert assertMsg (forces noEntry.apps.connectors-accept.program)
      "connectors accept must evaluate for a row with no entry";
    assert assertMsg (
      pkgs.lib.hasInfix "'fixture'" message && pkgs.lib.hasInfix "nix run .#connectors-accept" message
    ) "the refusal must name the Connector and the accept command";
    assert assertMsg (
      !(forces flakeNoEntry.apps.${system}.default.program)
    ) "a Consumer with no baseline entry must refuse";
    assert assertMsg (
      !(forces flakeNoEntry.apps.${system}.daemon.program)
    ) "a Consumer with no baseline entry must refuse the daemon";
    assert assertMsg (forces
      flakeNoEntry.apps.${system}.connectors-accept.program
    ) "a Consumer with no baseline entry must still evaluate connectors-accept";
    pkgs.runCommand "connector-backend-rows-third-party-no-entry-refused"
      { nativeBuildInputs = [ pkgs.jq ]; }
      ''
        ${fixtures.runInputDocOf flakeNoEntry.apps.${system}.connectors-accept.program}
        jq -e '.backends[0] | .name == "fixture" and (has("acceptedRoutes") | not) and (has("acceptedGrant") | not)' "$doc"
        touch $out
      '';

  connector-backend-rows-third-party-within-baseline-runs =
    let
      grant = {
        credentialEnv = "FIXTURE_TOKEN";
        credentialScheme = "bearer";
        axes = [ "tracker" ];
      };
      within =
        extra:
        withRows {
          fixture = row // extra;
        };
    in
    assert assertMsg (bothRun (within {
      acceptedRoutes = [ goodRoute ];
      acceptedGrant = grant;
    })) "a row equal to its baseline entry must run";
    assert assertMsg (bothRun (within {
      acceptedRoutes = [
        goodRoute
        (goodRoute // { path = "/other"; })
      ];
      acceptedGrant = grant;
    })) "a row declaring less than its entry must run";
    assert assertMsg (
      forces fixtures.baselineConsumers.grant.apps.${system}.default.program
      && forces fixtures.baselineConsumers.grant.apps.${system}.daemon.program
    ) "a Consumer whose baseline file matches its Connector exactly must run";
    pkgs.runCommand "connector-backend-rows-third-party-within-baseline-runs" { } "touch $out";

  connector-backend-rows-first-party-beyond-baseline-runs =
    let
      firstParty = ghRow // {
        gitRemote = "https://github.com";
      };
      widened = withRows {
        github = firstParty // {
          acceptedRoutes = [ ];
        };
      };
      noEntry = withRows { github = firstParty; };
    in
    assert assertMsg (widened.internals.backendFirstParty.github
    ) "control: the batteries row is first-party";
    assert assertMsg (bothRun widened) "a first-party row beyond its baseline must still run";
    assert assertMsg (bothRun noEntry) "a first-party row with no baseline entry must still run";
    assert assertMsg (widened.internals.baselineRefusals == [ ]) "a first-party row never refuses";
    pkgs.runCommand "connector-backend-rows-first-party-beyond-baseline-runs"
      { nativeBuildInputs = [ pkgs.jq ]; }
      ''
        jq -e '.backends[0] | .routes == [{method: "GET", host: "api.fixture.test", path: "/repos"}] and .acceptedRoutes == []' ${widened.internals.runInputDocumentFile}
        touch $out
      '';
}
