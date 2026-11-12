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
    (goodRoute // { followRedirect = true; })
    (goodRoute // { followRedirect = false; })
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
        jq -e '.backends[0] | keys == ["credentialEnv", "credentialScheme", "identityEnv", "module", "name", "routes"]
          and .credentialScheme == "basic" and .identityEnv == "FIXTURE_EMAIL"' ${
            (withRows { fixture = basicRow; }).internals.runInputDocumentFile
          }
        jq -e '.backends[0] | keys == ["baseEnv", "credentialEnv", "module", "name", "routes"]
          and .baseEnv == "FIXTURE_BASE_URL" and .routes[0].host == "{base}"' ${
            (withRows { fixture = baseRow; }).internals.runInputDocumentFile
          }
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
      probeBackend = {
        module = p: "${fixtures.connectorWasm}/network.wasm";
        routes = [ goodRoute ];
        credential = "probeToken";
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
            acceptedRoutes: []
          }]' "$doc"
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

  # Issue #6810: shadow mode on a backend row. mkHarness carries `shadow` and
  # `shadowEvery` into the document only when set, and refuses the shapes the Go
  # `backendrows.validate` refuses, naming the backend.
  connector-backend-rows-shadow-fields =
    let
      shadowed = row // {
        shadow = true;
        shadowEvery = 3;
      };
      refused =
        r:
        !(evaluates (withRows {
          fixture = r;
        }));
    in
    assert assertMsg (evaluates (withRows {
      fixture = shadowed;
    })) "control: a shadowed backend row must evaluate";
    assert assertMsg (refused (row // { shadow = "yes"; })) "a non-bool shadow must fail evaluation";
    assert assertMsg (refused (shadowed // { shadowEvery = 0; })) "shadowEvery 0 must fail evaluation";
    assert assertMsg (refused (
      shadowed // { shadowEvery = -1; }
    )) "a negative shadowEvery must fail evaluation";
    assert assertMsg (refused (
      shadowed // { shadowEvery = "3"; }
    )) "a non-int shadowEvery must fail evaluation";
    assert assertMsg (refused (
      row // { shadowEvery = 3; }
    )) "shadowEvery without shadow must fail evaluation";
    assert assertMsg (refused (
      row
      // {
        shadow = false;
        shadowEvery = 3;
      }
    )) "shadowEvery with shadow off must fail evaluation";
    assert assertMsg (refused (
      row // { shadow = true; }
    )) "shadow without shadowEvery must fail evaluation";
    pkgs.runCommand "connector-backend-rows-shadow-fields" { nativeBuildInputs = [ pkgs.jq ]; } ''
      jq -e '.backends[0] | .shadow == true and .shadowEvery == 3' ${
        (withRows { fixture = shadowed; }).internals.runInputDocumentFile
      }
      jq -e '.backends[0] | has("shadow") or has("shadowEvery") | not' ${
        (withRows { fixture = row; }).internals.runInputDocumentFile
      }
      touch $out
    '';

  # Issue #6810: `spindrift.connectors.<name>.shadow` on a backend row reaches
  # the input document with the row's `shadowEvery` (default 10); off, the app
  # is the unshadowed Consumer's; a row with neither a driver nor a backend has
  # nothing to shadow and is refused; `shadowEvery` below 1 or non-int is
  # refused by the option type.
  connector-backend-rows-flake-module-shadow =
    let
      consumerWith = modules: fixtures.connectorConsumerVia ([ ../../lib/flakeModule.nix ] ++ modules);
      # Backend-only: a row that also had a non-core `driver` would send
      # `shadow` to shadowDrivers, which refuses it.
      probeModule = {
        spindrift.connectors.probe = {
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
            credential = "probeToken";
          };
        };
      };
      withFixture =
        extra:
        consumerWith [
          probeModule
          { spindrift.connectors.probe = extra; }
        ];
      programOf = c: c.apps.${system}.default.program;
      builds = c: (builtins.tryEval (builtins.seq (programOf c) true)).success;
      unshadowed = consumerWith [ probeModule ];
    in
    assert assertMsg (builds unshadowed) "control: the unshadowed fixture Consumer must evaluate";
    assert assertMsg (
      programOf (withFixture {
        shadow = false;
        shadowEvery = 3;
      }) == programOf unshadowed
    ) "shadow off must leave the app unchanged whatever shadowEvery says";
    assert assertMsg (builds (withFixture {
      shadow = true;
    })) "shadow on a backend row must evaluate with the default rate";
    assert assertMsg (
      programOf (withFixture {
        shadow = true;
      }) != programOf unshadowed
    ) "shadow on a backend row must reach the input document";
    assert assertMsg (
      !(builds (withFixture {
        shadow = true;
        shadowEvery = 0;
      }))
    ) "shadowEvery 0 must fail evaluation";
    assert assertMsg (
      !(builds (withFixture {
        shadow = true;
        shadowEvery = "3";
      }))
    ) "a non-int shadowEvery must fail evaluation";
    assert assertMsg (
      !(builds (consumerWith [ { spindrift.connectors.nothing.shadow = true; } ]))
    ) "a shadowed row with neither a driver nor a backend must fail evaluation";
    pkgs.runCommand "connector-backend-rows-flake-module-shadow" { nativeBuildInputs = [ pkgs.jq ]; } ''
      ${fixtures.runInputDocOf (
        programOf (withFixture {
          shadow = true;
        })
      )}
      jq -e '.backends[0] | .shadow == true and .shadowEvery == 10' "$doc"
      ${fixtures.runInputDocOf (
        programOf (withFixture {
          shadow = true;
          shadowEvery = 3;
        })
      )}
      jq -e '.backends[0] | .shadow == true and .shadowEvery == 3' "$doc"
      ${fixtures.runInputDocOf (programOf unshadowed)}
      jq -e '.backends[0] | has("shadow") or has("shadowEvery") | not' "$doc"
      touch $out
    '';
}
