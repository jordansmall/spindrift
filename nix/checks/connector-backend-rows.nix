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
    credentialEnv = "FIXTURE_TOKEN";
  };
  withRows = rows: mkHarness { backendRows = rows; };
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
        method = "get";
      };
      why = "uppercase HTTP token";
    }
    {
      route = goodRoute // {
        method = "GE T";
      };
      why = "uppercase HTTP token";
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
        extra = "x";
      };
      why = "exactly";
    }
    {
      route = removeAttrs goodRoute [ "path" ];
      why = "exactly";
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
  ];
  problemsOf = r: routes.problems "fixture" [ r ];
  has = sub: s: builtins.match ".*${pkgs.lib.escapeRegex sub}.*" s != null;
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
    assert assertMsg (
      !(evaluates (withRows {
        fixture = removeAttrs row [ "credentialEnv" ];
      }))
    ) "a row without a credentialEnv must fail";
    assert assertMsg (
      !(evaluates (withRows {
        fixture = row // {
          credentialEnv = "";
        };
      }))
    ) "a row with an empty credentialEnv must fail";
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
          and (.[0] | keys == ["credentialEnv", "module", "name", "routes"])
          and .[0].name == "fixture"
          and .[0].credentialEnv == "FIXTURE_TOKEN"
          and .[0].routes == [{method: "GET", host: "api.fixture.test", path: "/repos"}]
          and (.[0].module | endswith("/network.wasm"))' "$doc"
        test -f "$(jq -r '.backends[0].module' "$doc")"
        touch $out
      '';

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
  connector-backend-rows-flake-module-forwards-row =
    let
      fixtureConnector = (import ../fixtures/connector).lib.connector { core = import ../../core; };
      consumerWith = modules: fixtures.connectorConsumerVia ([ ../../lib/flakeModule.nix ] ++ modules);
      withRow = consumerWith [ fixtureConnector.flakeModule ];
      # A Connector row is registered by one module only (a second definition of
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
      probeBackend = {
        module = p: "${fixtures.connectorWasm}/network.wasm";
        routes = [ goodRoute ];
        credential = "probeToken";
      };
      probe =
        {
          knobs ? {
            probeToken = secretKnob;
          },
          backend ? probeBackend,
        }:
        consumerWith [ { spindrift.connectors.probe = { inherit knobs backend; }; } ];
      programOf = c: c.apps.${system}.default.program;
      builds = c: (builtins.tryEval (builtins.seq (programOf c) true)).success;
    in
    assert assertMsg (builds withRow) "control: the fixture Connector's row must evaluate";
    assert assertMsg (builds (probe { })) "control: a secret host-only knob must be accepted";
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
      !(builds (probe {
        knobs.probeToken = secretKnob // {
          env = "GH_TOKEN";
        };
      }))
    ) "a credential knob aliasing a core env var must fail evaluation";
    assert assertMsg (
      !(builds (probe {
        knobs.probeToken = removeAttrs secretKnob [ "env" ];
      }))
    ) "a credential knob without an env var must fail evaluation";
    assert assertMsg (
      !(builds (probe {
        backend = probeBackend // {
          routes = [ (goodRoute // { path = "/"; }) ];
        };
      }))
    ) "a blanket-root route must fail evaluation";
    assert assertMsg (
      !(builds (probe {
        backend = removeAttrs probeBackend [ "credential" ];
      }))
    ) "a backend row without a credential must fail evaluation";
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
    pkgs.runCommand "connector-backend-rows-flake-module-forwards-row"
      { nativeBuildInputs = [ pkgs.jq ]; }
      ''
        ${fixtures.runInputDocOf (programOf withRow)}
        jq -e '.backends == [{
            name: "fixture",
            module: .backends[0].module,
            routes: [{method: "GET", host: "api.fixture.test", path: "/repos"}],
            credentialEnv: "FIXTURE_TOKEN",
            credentialKnob: "fixtureToken",
            acceptedRoutes: []
          }] and (.backends[0].module | endswith("/network.wasm"))' "$doc"
        test -f "$(jq -r '.backends[0].module' "$doc")"
        touch $out
      '';

  # Issue #6806: the accepted Route baseline is read from the Consumer's own
  # source tree (spindrift-connector-routes.json), keyed by Connector name, and
  # rides on the document's backend row. A missing file is an empty baseline.
  connector-backend-rows-accepted-baseline =
    let
      consumers = fixtures.baselineConsumers;
      programOf = c: c.apps.${system}.default.program;
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
    pkgs.runCommand "connector-backend-rows-accepted-baseline" { nativeBuildInputs = [ pkgs.jq ]; } ''
      jq -e '.backends[0].acceptedRoutes == [{method: "GET", host: "api.fixture.test", path: "/repos/x"}]' ${fixtures.connectorBaselineDocs}/widened.json
      jq -e '.backends[0].acceptedRoutes == [{method: "GET", host: "api.fixture.test", path: "/repos"}]' ${fixtures.connectorBaselineDocs}/accepted.json
      jq -e '.backends[0].credentialKnob == "fixtureToken"' ${fixtures.connectorBaselineDocs}/accepted.json
      jq -e '.backends[0].acceptedRoutes | map(.method) == ["GET", "POST"]' ${fixtures.connectorBaselineDocs}/narrowed.json
      jq -e '.backends[0].acceptedRoutes == [{method: "GET", host: "api.fixture.test", path: "/repos"}]' ${
        (withAccepted [ goodRoute ]).internals.runInputDocumentFile
      }
      touch $out
    '';
}
