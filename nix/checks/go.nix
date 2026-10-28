{
  pkgs,
  config,
  fixtures,
  launcherGoModules,
  goCheckEnv,
  imageNixCores,
  ...
}:
let
  inherit (fixtures) consumerFormatter;
  fs = pkgs.lib.fileset;
  driverRegistry = import ../../lib/drivers/default.nix { inherit (pkgs) lib; };
  driverNames = builtins.attrNames driverRegistry.entries;
  backendNames = map (row: row.name) (import ../../lib/backends/default.nix);
  runnerNames = map (row: row.name) (import ../../lib/runners/index.nix { }).rows;
  driverDir = name: ../../cmd/launcher/internal/driver + "/${name}";
  adapterGoTestAttr = prefix: name: "${prefix}-${name}-go-test";
  launcherDir = ../../cmd/launcher;
  allDriverDirs = map driverDir driverNames;
  # Everything that links the Driver bundle: the root package, its testdata,
  # the mains and the bundled fixture. The root's *.go files are listed one by
  # one because internal/ lies beneath it. They run in launcher-binaries-go-test,
  # so the core go test derivation compiles without any Driver (ADR 0066
  # decision 8). bundle-binaries-match-guard pins the mains and fixture to
  # bundleBinaries and bundleFixtures in internal/driver/bundle_guard_test.go.
  bundleMains = [
    "box"
    "daemon"
    "driver-exec"
    "orchestrator"
    "quickstart"
  ];
  bundleFixtureDirs = [ "internal/seamtest/bundled" ];
  bundleBinaryDirs =
    pkgs.lib.mapAttrsToList (n: _: launcherDir + "/${n}") (
      pkgs.lib.filterAttrs (n: t: t == "regular" && pkgs.lib.hasSuffix ".go" n) (
        builtins.readDir launcherDir
      )
    )
    ++ map (d: launcherDir + "/${d}") ([ "testdata" ] ++ bundleMains ++ bundleFixtureDirs);
  # The backends core keeps (ADR 0068, decision 7): the rows marked `core`.
  # Mirrors coreBackends in internal/driver/bundle_guard_test.go
  # (bundle-binaries-match-guard pins them). Every other backend row is
  # stripped from the core run.
  coreBackendNames = map (row: row.name) (builtins.filter (row: row.core or false) backendRowList);
  bundledBackendNames = builtins.filter (n: !builtins.elem n coreBackendNames) backendNames;
  bundledBackendDirs = map (n: launcherDir + "/internal/forge/${n}") bundledBackendNames;
  runnerDir = name: launcherDir + "/internal/runner/${name}";
  bundleDir = launcherDir + "/internal/driver/bundle";
  allRunnerDirs = map runnerDir runnerNames;
  # One Ecosystem package per directory beneath internal/ecosystem, so a new
  # ecosystem yields its go test derivation with no hand-pinned attr. The kit
  # (ecosystemkit) is shared library code, not an ecosystem; the testdata
  # exclusion stays so a corpus dir restored under the core package is never
  # mistaken for an ecosystem package.
  ecosystemDir = launcherDir + "/internal/ecosystem";
  ecosystemKitDir = "ecosystemkit";
  ecosystemNamesIn =
    listing:
    builtins.attrNames (
      pkgs.lib.filterAttrs (
        n: t:
        t == "directory"
        && !builtins.elem n [
          ecosystemKitDir
          "testdata"
        ]
      ) listing
    );
  ecosystemNames = ecosystemNamesIn (builtins.readDir ecosystemDir);
  ecosystemPkgDirs = map (n: ecosystemDir + "/${n}") ecosystemNames;
  # internal/ecosystem as the core ecosystem package and the kit: every
  # ecosystem package's directory removed, so editing one never moves a
  # derivation that merely compiles the core package.
  ecosystemCoreFiles = fs.difference ecosystemDir (fs.unions ecosystemPkgDirs);
  # `files` without the Drivers (and so their corpora), the runner families,
  # the Ecosystem packages, the bundled backends, the bundle and the binaries.
  coreFiles =
    files:
    fs.difference files (
      fs.unions (
        allDriverDirs
        ++ allRunnerDirs
        ++ ecosystemPkgDirs
        ++ bundledBackendDirs
        ++ [ bundleDir ]
        ++ bundleBinaryDirs
      )
    );
  # The vendor tree is re-derived from a source without any Driver, runner
  # family, Ecosystem package, bundled backend, bundle or binary, so editing one
  # Driver, family, ecosystem or bundled backend never changes another's
  # drvPath nor the core go test's (a plain launcherGoModules would, its src
  # being the whole tree). Fixed-output with the same vendorHash, so its
  # outPath equals launcherGoModules' and nothing is fetched twice. `go mod
  # vendor -e` tolerates the packages that import the absent directories.
  # Gotcha: a Driver, runner family, ecosystem, bundled backend or main
  # importing a third-party module no other package uses changes the vendored
  # set, and the narrowed derivation then mismatches launcherVendorHash.
  vendorFor =
    files:
    launcherGoModules.overrideAttrs (_: {
      src = fs.toSource {
        root = launcherDir;
        fileset = coreFiles files;
      };
      preBuild = "goModVendorFlags=(-e)";
    });
  nixSrc = fs.toSource {
    root = ../..;
    fileset = fs.fileFilter (f: f.hasExt "nix") ../..;
  };
  # Vendored modules, go env and the bwrap LookPath stub shared by the go test
  # checks. Expects the caller to have copied cmd/launcher to src/cmd/launcher.
  # Ends with src/cmd/launcher as the working directory.
  goTestPrologueWith = vendor: ''
    cp -r ${vendor} src/cmd/launcher/vendor
    ${goCheckEnv}
    mkdir -p "$TMPDIR/fakebin"
    cat > "$TMPDIR/fakebin/bwrap" <<'EOF'
    #!/bin/sh
    echo "fakebin/bwrap: stub for LookPath only, tests never invoke it for real" >&2
    exit 1
    EOF
    chmod +x "$TMPDIR/fakebin/bwrap"
    export PATH="$TMPDIR/fakebin:$PATH"
    cd src/cmd/launcher
  '';
  goTestPrologue = goTestPrologueWith launcherGoModules;
  # Fails listing every file under `paths` that gofmt would rewrite.
  gofmtCheck = paths: ''
    unformatted=$(gofmt -l ${paths})
    if [ -n "$unformatted" ]; then
      echo "gofmt violations:" >&2
      echo "$unformatted" >&2
      exit 1
    fi
  '';
  # Packages a Driver's tests reach (go list -deps -test over every Driver's
  # package). Hand-listed so one Driver's edits never rebuild another's check;
  # a Driver whose tests import a new internal package must add it here, and a
  # stale list fails the build loudly. Never the registry root internal/driver
  # or another Driver's directory (internal/driver/closure_test.go).
  driverGoTestShared = map (p: ../../cmd/launcher/internal + "/${p}") [
    "dispatchkey"
    "dispatchkind"
    "driver/corpus"
    "driver/driverkit"
    "golden"
    "hostop"
    "landdelta"
    "logscan"
    "outcome"
    "passmachine"
    "report"
    "testutil"
    "usage"
  ];
  # One go test derivation per adapter of any registry (ADR 0066 decision 5,
  # ADR 0068 decision 7) or Ecosystem package, keyed by registry name (for the
  # ecosystems, the directory name) so a new row or directory yields one with
  # no hand-pinned attr: `${prefix}-${name}-go-test`. `pkgFor name` is the
  # adapter's package directory under cmd/launcher; `sharedFor name` the extra
  # packages its tests reach. `files` is the cmd/launcher fileset every source
  # is cut from; the independence check perturbs it. The vendor tree is
  # `vendorFor files`, so one Driver's or runner family's edits never move
  # another's drvPath; coreFiles keeps the core backends (git, local), so
  # editing one moves every backend's drvPath, though not its outPath.
  # `setupFor name` is shell run after the copy for files outside cmd/launcher
  # its tests read. Each also gofmts and vets the adapter's own package, so
  # launcher-go-fmt and launcher-go-vet need not see it.
  adapterGoTests =
    {
      files,
      prefix,
      names,
      pkgFor,
      sharedFor,
      setupFor ? _: "",
      nativeBuildInputs ? [ pkgs.go ],
    }:
    let
      vendor = vendorFor files;
    in
    pkgs.lib.listToAttrs (
      map (name: {
        name = adapterGoTestAttr prefix name;
        value =
          let
            src = fs.toSource {
              root = launcherDir;
              fileset = fs.intersection files (
                fs.unions (
                  [
                    (launcherDir + "/go.mod")
                    (launcherDir + "/go.sum")
                    (launcherDir + "/${pkgFor name}")
                  ]
                  ++ sharedFor name
                )
              );
            };
          in
          pkgs.runCommand (adapterGoTestAttr prefix name) { inherit nativeBuildInputs; } ''
            mkdir -p src/cmd
            cp -r ${src} src/cmd/launcher
            chmod -R +w src
            ${setupFor name}
            ${goTestPrologueWith vendor}
            ${gofmtCheck "./${pkgFor name}"}
            go vet ./${pkgFor name}/...
            go test ./${pkgFor name}/...
            touch $out
          '';
      }) names
    );
  driverGoTestsFor =
    { files, names }:
    adapterGoTests {
      inherit files names;
      prefix = "drivers";
      pkgFor = name: "internal/driver/${name}";
      sharedFor = _: driverGoTestShared;
    };
  driverGoTests = driverGoTestsFor {
    files = launcherDir;
    names = driverNames;
  };
  # Packages the backends' tests reach (go list -test -deps over every
  # backend's package): the union of their closures, so one list serves
  # every backend. Never a backend's directory. The forge, driver and runner
  # roots are listed file by file because backend, Driver and runner family
  # directories lie beneath them; the driver root is seamtest's registry
  # import, non-test files only. Backends need only the runner root package.
  rootGoFiles =
    dir:
    pkgs.lib.mapAttrsToList (n: _: dir + "/${n}") (
      pkgs.lib.filterAttrs (
        n: t: t == "regular" && pkgs.lib.hasSuffix ".go" n && !pkgs.lib.hasSuffix "_test.go" n
      ) (builtins.readDir dir)
    );
  internalDir = launcherDir + "/internal";
  backendGoTestShared =
    map (p: internalDir + "/${p}") [
      "agentpaths"
      "backend"
      "backendkit"
      "dispatchkey"
      "dispatchkind"
      "doctor"
      "driver/driverkit"
      "flock"
      "forge/bundlerelay"
      "forge/forgetest"
      "forge/gitplumbing"
      "forge/rest"
      "gitexec"
      "gitremote"
      "hostop"
      "landdelta"
      "ledger"
      "logscan"
      "outcome"
      "panicguard"
      "registrymanifest"
      "registryprobe"
      "registryvocab"
      "report"
      "retry"
      "seambundle"
      "seamtest"
      "testutil"
      "unixsocket"
      "usage"
    ]
    ++ rootGoFiles (internalDir + "/forge")
    ++ rootGoFiles (internalDir + "/driver")
    ++ rootGoFiles (internalDir + "/runner");
  # A row's goTestBuildsOn names a sibling it uses as a library, not as an
  # adapter: no other backend's fileset holds a sibling backend.
  backendRowList = import ../../lib/backends/default.nix;
  rowFor = name: pkgs.lib.findFirst (row: row.name == name) null backendRowList;
  buildsOnFor = name: (rowFor name).goTestBuildsOn or null;
  backendGoTestSharedFor =
    name:
    let
      buildsOn = buildsOnFor name;
    in
    backendGoTestShared ++ pkgs.lib.optional (buildsOn != null) (internalDir + "/forge/${buildsOn}");
  backendGoTestsFor =
    { files, names }:
    adapterGoTests {
      inherit files names;
      prefix = "backends";
      pkgFor = name: "internal/forge/${name}";
      sharedFor = backendGoTestSharedFor;
      setupFor =
        name:
        pkgs.lib.concatMapStrings (f: ''
          mkdir -p src/${builtins.dirOf f}
          cp ${../.. + "/${f}"} src/${f}
        '') ((rowFor name).goTestRepoFiles or [ ]);
      nativeBuildInputs = goTestInputs;
    };
  backendGoTests = backendGoTestsFor {
    files = launcherDir;
    names = backendNames;
  };
  # Packages the runner families' tests reach (go list -test -deps over every
  # family's package): the union of their closures, so one list serves every
  # family. A family whose tests import a new internal package must add it
  # here, and a stale list fails the build loudly. Never a family's
  # directory. The runner and driver roots are listed
  # file by file because the family and Driver directories lie beneath them;
  # the driver root is bwrap's credential-rule import, non-test files only.
  runnerGoTestShared =
    map (p: internalDir + "/${p}") [
      "agentpaths"
      "backend"
      "chore"
      "connlimit"
      "dispatch"
      "dispatchkey"
      "dispatchkind"
      "dispatchrecord"
      "doctor"
      "driver/driverkit"
      "flock"
      "freshness"
      "gitexec"
      "hostop"
      "hostpaths"
      "landdelta"
      "ledger"
      "logscan"
      "outcome"
      "panicguard"
      "passmachine"
      "passmanifest"
      "promptassembly"
      "promptfence"
      "registrymanifest"
      "registryprobe"
      "registryproxy"
      "registryvocab"
      "report"
      "retry"
      "runner/runnertest"
      "signalclient"
      "signalsocket"
      "signalwire"
      "terminate"
      "testutil/repopath"
      "unixsocket"
      "usage"
    ]
    ++ [ ecosystemCoreFiles ]
    ++ rootGoFiles (internalDir + "/forge")
    ++ rootGoFiles (internalDir + "/driver")
    ++ rootGoFiles (internalDir + "/runner");
  runnerGoTestsFor =
    { files, names }:
    adapterGoTests {
      inherit files names;
      prefix = "runners";
      pkgFor = name: "internal/runner/${name}";
      sharedFor = _: runnerGoTestShared;
      # runnertest reads the prompt templates beside cmd/launcher.
      setupFor = _: ''
        cp -r ${../../templates} src/templates
      '';
      nativeBuildInputs = goTestInputs;
    };
  runnerGoTests = runnerGoTestsFor {
    files = launcherDir;
    names = runnerNames;
  };
  # Packages an ecosystem's tests reach (go list -deps -test over every
  # ecosystem package): hand-listed like driverGoTestShared, and a stale list
  # fails the build loudly. The kit and the core ecosystem package's non-test
  # files, never another ecosystem's directory, so one ecosystem's edit leaves
  # every other's derivation alone while a kit edit reruns them all.
  ecosystemGoTestShared =
    map (p: internalDir + "/${p}") [
      "bindregistry"
      "connlimit"
      "credresolver"
      "ecosystem/${ecosystemKitDir}"
      "ecosystemtest"
      "glob"
      "golden"
      "hostprofile"
      "panicguard"
      "profileprobe"
      "registrydiscover"
      "registrymanifest"
      "registrypathset"
      "registryproxy"
      "registryroutes"
      "registryvocab"
      "unixsocket"
    ]
    ++ rootGoFiles ecosystemDir;
  ecosystemGoTestsFor =
    { files, names }:
    adapterGoTests {
      inherit files names;
      prefix = "ecosystems";
      pkgFor = name: "internal/ecosystem/${name}";
      sharedFor = _: ecosystemGoTestShared;
      nativeBuildInputs = goTestInputs;
    };
  ecosystemGoTests = ecosystemGoTestsFor {
    files = launcherDir;
    names = ecosystemNames;
  };
  # Directories under internal/ecosystem holding Go files, the kit apart: each
  # is a package and needs its go test derivation. Filtered separately from
  # ecosystemNames, though both read the same listing: on the real tree the
  # every-directory check fails only if a package directory is excluded from
  # ecosystemNames; the stub case proves the check can fail at all.
  ecosystemPackageDirs = builtins.attrNames (
    pkgs.lib.filterAttrs (
      n: t:
      t == "directory"
      && n != ecosystemKitDir
      && builtins.any (f: pkgs.lib.hasSuffix ".go" f) (
        builtins.attrNames (builtins.readDir (ecosystemDir + "/${n}"))
      )
    ) (builtins.readDir ecosystemDir)
  );
  ecosystemsLackingDerivation =
    dirs: set: builtins.filter (d: !(set ? ${adapterGoTestAttr "ecosystems" d})) dirs;
  # The two registry-row checks every adapter registry shares. `testsFor` is the
  # registry's `{ files, names }` go test builder.
  #
  # A registry row yields its go test derivation with no other edit (issues
  # #5508, #5637, #5688). Only attr names are forced: building the stub's
  # derivation would fail on its missing package directory.
  #
  # With `independence = { dirFor, noun; }` (`dirFor name` the adapter's
  # directory, `noun` what the assert messages call an adapter), editing one
  # adapter leaves every other adapter's go test derivation path, and every core
  # check's, unchanged (issues #5508, #5545, #5562, #5688): coreFiles strips
  # the adapter directories, so no vendor tree or source is shared. Each adapter
  # in turn has one file removed; the sensitivity assert keeps the check from
  # passing vacuously. `others` lists further `{ prefix, testsFor, names }`
  # registries whose derivations the edit must not move either.
  adapterRegistryChecks =
    {
      prefix,
      testsFor,
      names,
      independence ? null,
      others ? [ ],
    }:
    let
      inherit (pkgs.lib)
        assertMsg
        concatStringsSep
        filterAttrs
        optionalAttrs
        ;
      stubName = "${prefix}-go-test-stub-row-yields-derivation";
      independentName = "${prefix}-go-test-independent";
      baseline = testsFor {
        files = launcherDir;
        inherit names;
      };
      drv = set: n: set.${adapterGoTestAttr prefix n}.drvPath;
      # The pattern rejects an `independence` missing either key at the call,
      # not later when a failing assert first builds its message.
      independent =
        { dirFor, noun }:
        p:
        let
          file = builtins.head (
            builtins.attrNames (filterAttrs (_: t: t == "regular") (builtins.readDir (dirFor p)))
          );
          edited = fs.difference launcherDir (dirFor p + "/${file}");
          after = testsFor {
            files = edited;
            inherit names;
          };
          moved = builtins.filter (n: n != p && drv baseline n != drv after n) names;
          coreMoved = coreChecksMovedBy edited;
          othersMoved = pkgs.lib.concatMap (
            o:
            let
              at =
                files:
                o.testsFor {
                  inherit files;
                  inherit (o) names;
                };
              before = at launcherDir;
              now = at edited;
            in
            builtins.filter (attr: before.${attr}.drvPath != now.${attr}.drvPath) (
              map (adapterGoTestAttr o.prefix) o.names
            )
          ) others;
        in
        assertMsg (drv baseline p != drv after p)
          "removing ${file} from the ${p} ${noun} did not change its own go test derivation; the perturbation is inert"
        && assertMsg (
          moved == [ ]
        ) "editing the ${p} ${noun} changed the go test derivation of: ${concatStringsSep ", " moved}"
        && assertMsg (
          coreMoved == [ ]
        ) "editing the ${p} ${noun} changed: ${concatStringsSep ", " coreMoved}"
        && assertMsg (
          othersMoved == [ ]
        ) "editing the ${p} ${noun} changed: ${concatStringsSep ", " othersMoved}";
    in
    {
      ${stubName} =
        let
          stubbed = builtins.attrNames (testsFor {
            files = launcherDir;
            names = names ++ [ "stub" ];
          });
        in
        assert assertMsg (builtins.elem "${prefix}-stub-go-test" stubbed)
          "a stub registry row must yield ${prefix}-stub-go-test, got: ${concatStringsSep ", " stubbed}";
        pkgs.runCommand stubName { } "touch $out";
    }
    // optionalAttrs (independence != null) {
      ${independentName} =
        assert builtins.all (independent independence) names;
        pkgs.runCommand independentName { } "touch $out";
    };
  # go test guards config-parsing regressions (#112). docs/, .github/,
  # .forgejo/, templates/, agent/, README.md (if present) and lib/ are copied
  # beside cmd/launcher, mirroring the repo layout, so the tests resolve their relative
  # paths (#611, #1985, #2038, #2507, #2566, #2741, #2743, #3988). forge's tests shell out to git;
  # the RUNTIME=bwrap tests LookPath "bwrap" (#2441), and bubblewrap is Linux-only.
  sideTrees = ''
    cp -r ${../../docs} src/docs
    cp -r ${../../.github} src/.github
    cp -r ${../../.forgejo} src/.forgejo
    cp -r ${../../templates} src/templates
    cp -r ${../../agent} src/agent
    mkdir -p src/tests/testdata
    cp -r ${../../tests/testdata/prompt-assembly-golden} src/tests/testdata/prompt-assembly-golden
    # Skipped only when docs-edit-isolation removes README.md from the root;
    # a real deletion still fails go test via collectMarkdownDocs in
    # cmd/launcher/main_test.go.
    ${pkgs.lib.optionalString (builtins.pathExists ../../README.md) "cp ${../../README.md} src/README.md"}
    cp -r ${../../lib} src/lib
  '';
  goTestInputs = [
    pkgs.go
    pkgs.git
  ];
  coreSrc =
    files:
    fs.toSource {
      root = launcherDir;
      fileset = coreFiles files;
    };
  # The registry root's guard tests need every Driver, and run in
  # launcher-binaries-go-test.
  corePkgs = ''$(go list ./... | grep -vxF "$(go list -m)/internal/driver")'';
  # The core run: every package that needs no Driver, bundle or main. It cuts
  # its source from coreFiles, so a Driver edit never reaches this derivation;
  # the assert fails if the copied tree still holds one. The corpus find is
  # defence in depth, for a corpus landing under internal/driver outside a
  # Driver directory.
  launcherGoTestFor =
    files:
    pkgs.runCommand "launcher-go-test" { nativeBuildInputs = goTestInputs; } ''
      mkdir -p src/cmd
      cp -r ${coreSrc files} src/cmd/launcher
      ${sideTrees}
      chmod -R +w src
      for held in bundle ${pkgs.lib.concatStringsSep " " driverNames}; do
        if [ -e "src/cmd/launcher/internal/driver/$held" ]; then
          echo "launcher-go-test's source holds internal/driver/$held" >&2
          exit 1
        fi
      done
      for held in ${pkgs.lib.concatStringsSep " " bundledBackendNames}; do
        if [ -e "src/cmd/launcher/internal/forge/$held" ]; then
          echo "launcher-go-test's source holds internal/forge/$held" >&2
          exit 1
        fi
      done
      for held in ${pkgs.lib.concatStringsSep " " runnerNames}; do
        if [ -e "src/cmd/launcher/internal/runner/$held" ]; then
          echo "launcher-go-test's source holds internal/runner/$held" >&2
          exit 1
        fi
      done
      for held in ${pkgs.lib.concatStringsSep " " ecosystemNames}; do
        if [ -e "src/cmd/launcher/internal/ecosystem/$held" ]; then
          echo "launcher-go-test's source holds internal/ecosystem/$held" >&2
          exit 1
        fi
      done
      corpora=$(find src/cmd/launcher/internal/driver -path '*/testdata/corpus')
      if [ -n "$corpora" ]; then
        echo "launcher-go-test's source holds a Driver corpus: $corpora" >&2
        exit 1
      fi
      ${goTestPrologueWith (vendorFor files)}
      go test ${corePkgs}
      touch $out
    '';
  # Copies the core source and its vendor tree; ends with src as the working
  # directory.
  corePrologue = files: ''
    cp -r ${coreSrc files} src
    chmod -R +w src
    cp -r ${vendorFor files} src/vendor
    ${goCheckEnv}
    cd src
  '';
  # ADR 0023 makes console a leaf UI package: engine packages never import it.
  # go list -deps walks the whole transitive graph, so an import that reaches
  # console through another engine package is caught too, not just a direct one.
  consoleIsolationFor =
    files:
    let
      # runner/oci and runner/bwrap are adapters, not in the core source.
      # runnerGoTestShared is the structural guard, asserted below: a family's
      # go test source holds no internal/console, so a family importing it
      # fails to compile in its own runners-<name>-go-test.
      guardedPackages = pkgs.lib.concatStringsSep " " [
        "dispatch"
        "waves"
        "forge"
        "settle"
        "runner"
        "driver"
        "freshness"
      ];
      runnerConsole = fs.toList (
        fs.intersection (fs.unions runnerGoTestShared) (internalDir + "/console")
      );
    in
    assert pkgs.lib.assertMsg (
      runnerConsole == [ ]
    ) "runnerGoTestShared holds internal/console, so runner families escape launcher-console-isolation";
    pkgs.runCommand "launcher-console-isolation"
      {
        nativeBuildInputs = [ pkgs.go ];
        inherit guardedPackages;
      }
      ''
        ${corePrologue files}
        for pkg in $guardedPackages; do
          if ! deps=$(go list -deps "./internal/$pkg" 2>&1); then
            # console already imports most guarded packages, so a reverse
            # import forms a cycle — go list fails outright instead of
            # listing console as a dependency. Treat that failure itself
            # as the violation signal. But go list -deps also fails for
            # reasons unrelated to console (an unresolvable import, a
            # malformed package clause in a dependency), so only blame
            # console when the toolchain actually reports a cycle.
            if echo "$deps" | grep -q 'import cycle'; then
              echo "go list failed for internal/$pkg — likely an import cycle through internal/console, which violates ADR 0023's one-way dependency:" >&2
            else
              echo "go list failed for internal/$pkg for a reason unrelated to a console cycle (see stderr below) — not necessarily an ADR 0023 violation:" >&2
            fi
            echo "$deps" >&2
            exit 1
          fi
          if echo "$deps" | grep -qx 'spindrift.dev/launcher/internal/console'; then
            echo "internal/$pkg imports internal/console — violates ADR 0023's one-way dependency (engine packages never import console)" >&2
            exit 1
          fi
        done
        touch $out
      '';
  # The core checks, each cut from coreFiles so an adapter edit never moves one;
  # adapterRegistryChecks and backends-go-test-independent pin that.
  coreChecksFor = files: {
    launcher-go-test = launcherGoTestFor files;
    launcher-go-fmt = pkgs.runCommand "launcher-go-fmt" { nativeBuildInputs = [ pkgs.go ]; } ''
      ${gofmtCheck (coreSrc files)}
      touch $out
    '';
    launcher-go-vet = pkgs.runCommand "launcher-go-vet" { nativeBuildInputs = [ pkgs.go ]; } ''
      ${corePrologue files}
      go vet ${corePkgs}
      touch $out
    '';
    launcher-console-isolation = consoleIsolationFor files;
  };
  # Names of the core checks whose drvPath differs from the unedited tree's.
  coreChecksMovedBy =
    files:
    let
      base = coreChecksFor launcherDir;
      after = coreChecksFor files;
    in
    builtins.filter (n: base.${n}.drvPath != after.${n}.drvPath) (builtins.attrNames base);
  # The Dashboard is a separate dependency-free module (ADR 0060), so the
  # vendored-launcher GOFLAGS=-mod=vendor from goCheckEnv is reset: it has no
  # vendor/ tree. Ends with src as the working directory.
  dashboardPrologue = ''
    cp -r ${../../dashboard} src
    chmod -R +w src
    ${goCheckEnv}
    export GOFLAGS=
    cd src
  '';
in
driverGoTests
// backendGoTests
// runnerGoTests
// ecosystemGoTests
// adapterRegistryChecks {
  prefix = "drivers";
  testsFor = driverGoTestsFor;
  names = driverNames;
  independence = {
    dirFor = driverDir;
    noun = "Driver";
  };
}
// adapterRegistryChecks {
  prefix = "backends";
  testsFor = backendGoTestsFor;
  names = backendNames;
}
// adapterRegistryChecks {
  prefix = "runners";
  testsFor = runnerGoTestsFor;
  names = runnerNames;
  independence = {
    dirFor = runnerDir;
    noun = "runner family";
  };
}
// adapterRegistryChecks {
  prefix = "ecosystems";
  testsFor = ecosystemGoTestsFor;
  names = ecosystemNames;
  independence = {
    dirFor = name: ecosystemDir + "/${name}";
    noun = "ecosystem";
  };
  others = [
    {
      prefix = "drivers";
      testsFor = driverGoTestsFor;
      names = driverNames;
    }
    {
      prefix = "backends";
      testsFor = backendGoTestsFor;
      names = backendNames;
    }
    {
      prefix = "runners";
      testsFor = runnerGoTestsFor;
      names = runnerNames;
    }
  ];
}
// {
  # Every Ecosystem package directory has its go test derivation. The stub
  # case proves the check fails for a directory without one.
  ecosystems-go-test-every-package-has-derivation =
    let
      lacking = ecosystemsLackingDerivation ecosystemPackageDirs ecosystemGoTests;
    in
    assert pkgs.lib.assertMsg (
      ecosystemPackageDirs != [ ]
    ) "no Ecosystem package directory found under internal/ecosystem; the check would pass vacuously";
    assert pkgs.lib.assertMsg (lacking == [ ])
      "Ecosystem package directories without an ecosystems-<dir>-go-test derivation: ${pkgs.lib.concatStringsSep ", " lacking}";
    assert pkgs.lib.assertMsg (
      ecosystemNamesIn {
        stub = "directory";
        ${ecosystemKitDir} = "directory";
        testdata = "directory";
        "doc.go" = "regular";
      } == [ "stub" ]
    ) "ecosystemNamesIn did not reduce a directory listing to its ecosystem package names";
    assert pkgs.lib.assertMsg (
      ecosystemsLackingDerivation (ecosystemPackageDirs ++ [ "stub" ]) ecosystemGoTests == [ "stub" ]
    ) "a package directory without a derivation was not reported";
    pkgs.runCommand "ecosystems-go-test-every-package-has-derivation" { } "touch $out";

  # The kit is shared by every ecosystem: removing a file from it must move
  # every ecosystem package's derivation.
  ecosystems-go-test-kit-edit-reruns-every-package =
    let
      kitFile = builtins.head (
        builtins.attrNames (
          pkgs.lib.filterAttrs (_: t: t == "regular") (
            builtins.readDir (ecosystemDir + "/${ecosystemKitDir}")
          )
        )
      );
      after = ecosystemGoTestsFor {
        files = fs.difference launcherDir (ecosystemDir + "/${ecosystemKitDir}/${kitFile}");
        names = ecosystemNames;
      };
      unmoved = builtins.filter (
        n:
        ecosystemGoTests.${adapterGoTestAttr "ecosystems" n}.drvPath
        == after.${adapterGoTestAttr "ecosystems" n}.drvPath
      ) ecosystemNames;
    in
    assert pkgs.lib.assertMsg (
      ecosystemNames != [ ]
    ) "no Ecosystem package found; the kit check would pass vacuously";
    assert pkgs.lib.assertMsg (unmoved == [ ])
      "editing ecosystemkit/${kitFile} left these ecosystem go test derivations unchanged: ${pkgs.lib.concatStringsSep ", " unmoved}";
    pkgs.runCommand "ecosystems-go-test-kit-edit-reruns-every-package" { } "touch $out";

  dashboard-go-fmt = pkgs.runCommand "dashboard-go-fmt" { nativeBuildInputs = [ pkgs.go ]; } ''
    ${gofmtCheck ../../dashboard}
    touch $out
  '';

  dashboard-go-vet = pkgs.runCommand "dashboard-go-vet" { nativeBuildInputs = [ pkgs.go ]; } ''
    ${dashboardPrologue}
    go vet ./...
    touch $out
  '';

  dashboard-go-test =
    pkgs.runCommand "dashboard-go-test"
      {
        nativeBuildInputs = [
          pkgs.go
          pkgs.git
        ];
      }
      ''
        ${dashboardPrologue}
        go test ./...
        touch $out
      '';

  # The file set is derived from the tree, so a new .nix file can't escape the
  # gate. The quickstart golden flake.nix fixtures are included on purpose:
  # they are rendered from templates/default/flake.nix, which is checked too.
  nix-fmt = pkgs.runCommand "nix-fmt" { nativeBuildInputs = [ pkgs.nixfmt ]; } ''
    cd ${nixSrc}
    find . -type f -print0 | sort -z | xargs -0 nixfmt --check
    touch $out
  '';

  # nix-fmt must see every tracked .nix file (issue #3850). The reference side
  # uses a different primitive (listFilesRecursive + suffix test) than nixSrc
  # (fileFilter + hasExt), so one predicate edit cannot narrow both (each side
  # still names its own ../.. root literal); a git flake's source copy holds
  # only tracked files. Asserting on nixSrc, the realized input, catches a
  # narrowed root as well as a narrowed fileset.
  nix-fmt-covers-tracked =
    let
      inherit (pkgs.lib)
        assertMsg
        concatStringsSep
        filter
        hasSuffix
        removePrefix
        subtractLists
        ;
      inherit (pkgs.lib.filesystem) listFilesRecursive;
      relativeTo = base: map (p: removePrefix (toString base + "/") (toString p));
      tracked = relativeTo ../.. (filter (p: hasSuffix ".nix" (toString p)) (listFilesRecursive ../..));
      missing = subtractLists (relativeTo nixSrc (listFilesRecursive nixSrc)) tracked;
    in
    assert assertMsg (missing == [ ])
      "nix-fmt's source no longer covers every tracked .nix file; missing: ${concatStringsSep ", " missing}";
    pkgs.runCommand "nix-fmt-covers-tracked" { } "touch $out";

  # go-check-env.nix's GOMAXPROCS bound (issue #3915), and its unset/0 fallback
  # against lib/image.nix's baked nix.conf `cores` (issue #3965), and its HOME
  # redirect under $TMPDIR (issue #6009).
  go-check-env =
    let
      # Spliced inside shell double quotes: without the \\` escapes a
      # backtick would start command substitution.
      drift = "nix/checks/go-check-env.nix's fallback literal must match lib/image.nix's nix.conf \\`cores = ${imageNixCores}\\`";
    in
    pkgs.runCommand "go-check-env" { } ''
      check() {
        got=$(
          ${goCheckEnv}
          sh -c 'echo "$GOMAXPROCS"'
        )
        if [ "$got" != "$1" ]; then
          echo "NIX_BUILD_CORES=''${NIX_BUILD_CORES-unset}: expected GOMAXPROCS=$1, got $got''${2:+ -- $2}" >&2
          exit 1
        fi
      }
      (unset NIX_BUILD_CORES; check ${imageNixCores} "${drift}")
      NIX_BUILD_CORES=0 check ${imageNixCores} "${drift}"
      NIX_BUILD_CORES=2 check 2
      home=$(
        ${goCheckEnv}
        sh -c 'echo "$HOME"'
      )
      if [ -z "$TMPDIR" ]; then
        echo "TMPDIR is empty: the HOME-under-TMPDIR assertion would pass vacuously" >&2
        exit 1
      fi
      case "$home" in
        "$TMPDIR"/*) ;;
        *)
          echo "HOME=$home: nix/checks/go-check-env.nix must redirect HOME under \$TMPDIR so Go's telemetry never creates /homeless-shelter (issue #6009)" >&2
          exit 1
          ;;
      esac
      touch $out
    '';

  inherit (coreChecksFor launcherDir)
    launcher-go-test
    launcher-go-fmt
    launcher-go-vet
    launcher-console-isolation
    ;

  # The packages that link every Driver through the bundle (bundleBinaryDirs),
  # the bundle's own tests, the registry root's cross-Driver guard tests and the
  # forge package's contract guard, which needs every backend directory, and
  # the host-profile advisory guard, which needs package main's
  # route-resolution file; the whole tree is copied, so a Driver edit reruns
  # this. It also gofmts and vets those packages (not the adapters', which
  # their own go-test does), so launcher-go-fmt and launcher-go-vet need not
  # see them.
  launcher-binaries-go-test =
    let
      binaryPkgs = ". ${
        pkgs.lib.concatMapStringsSep " " (d: "./${d}/...") (bundleMains ++ bundleFixtureDirs)
      } ./internal/driver ./internal/driver/bundle/...";
    in
    pkgs.runCommand "launcher-binaries-go-test"
      {
        nativeBuildInputs = goTestInputs;
      }
      ''
        mkdir -p src/cmd
        cp -r ${launcherDir} src/cmd/launcher
        ${sideTrees}
        chmod -R +w src
        ${goTestPrologue}
        # gofmt on . would recurse the whole tree, so name each path.
        ${gofmtCheck (
          pkgs.lib.concatMapStringsSep " " (
            p: pkgs.lib.removePrefix "${toString launcherDir}/" (toString p)
          ) (bundleBinaryDirs ++ [ bundleDir ])
        )}
        go vet ${binaryPkgs}
        go test ${binaryPkgs}
        SPINDRIFT_CONTRACT_GUARD_FULL_TREE=1 go test ./internal/forge -run '^TestEveryBackendRunsItsContracts$'
        SPINDRIFT_HOSTPROFILE_GUARD_FULL_TREE=1 go test ./internal/hostprofile -run '^TestAdvisoryBoundaryHoldsOnRealTree$'
        touch $out
      '';

  # bundleMains and bundleFixtureDirs are the module-relative entries of
  # bundleBinaries and bundleFixtures in bundle_guard_test.go (its bare `module`
  # entry is the root package, which bundleBinaryDirs lists separately). A list
  # edited without the other would move a package between the core and
  # binaries derivations.
  bundle-binaries-match-guard =
    let
      inherit (pkgs.lib)
        assertMsg
        concatStringsSep
        lessThan
        sort
        ;
      guard = builtins.readFile ../../cmd/launcher/internal/driver/bundle_guard_test.go;
      # Only the two list bodies: a module + "/..." elsewhere in the file is no entry.
      blockBody =
        name:
        let
          found = builtins.filter builtins.isList (builtins.split "var ${name} = [^{]*[{]([^}]*)[}]" guard);
        in
        assert assertMsg (found != [ ]) "bundle_guard_test.go has no `var ${name} = []string{...}` block";
        builtins.head (builtins.head found);
      entries =
        body:
        map builtins.head (
          builtins.filter builtins.isList (builtins.split "module \\+ \"/([^\"]+)\"" body)
        );
      guarded = sort lessThan (
        entries (blockBody "bundleBinaries") ++ entries (blockBody "bundleFixtures")
      );
      listed = sort lessThan (bundleMains ++ bundleFixtureDirs);
      guardedCore = sort lessThan (
        map builtins.head (
          builtins.filter builtins.isList (builtins.split "\"([^\"]+)\"" (blockBody "coreBackends"))
        )
      );
    in
    assert assertMsg (guardedCore == sort lessThan coreBackendNames)
      "coreBackendNames (nix/checks/go.nix) must equal coreBackends (internal/driver/bundle_guard_test.go); go.nix has: ${concatStringsSep ", " coreBackendNames}; the guard has: ${concatStringsSep ", " guardedCore}";
    assert assertMsg (guarded == listed)
      "bundleMains ++ bundleFixtureDirs (nix/checks/go.nix) must equal the module + \"/<dir>\" entries of bundleBinaries and bundleFixtures (internal/driver/bundle_guard_test.go); go.nix has: ${concatStringsSep ", " listed}; the guard has: ${concatStringsSep ", " guarded}";
    pkgs.runCommand "bundle-binaries-match-guard" { } "touch $out";

  # Stands in for adapterRegistryChecks' independence half, which cannot hold
  # for backends: the core backends live in the core checks.
  #
  # No backend's go test source holds another backend's directory, bar the git
  # backend's for `local` and forgejo (issue #5637). A fileset check for the
  # backend-to-backend leaks, since a core backend edit still moves every
  # backend's vendor drvPath. Editing a bundled backend (issue #5648) leaves the
  # core checks' drvPaths unchanged, while editing a core backend moves every
  # core check's. Editing a runner family leaves every backend's go test
  # drvPath unchanged, as backends need only the runner root package; the
  # family's own sensitivity is asserted by runners-go-test-independent. The
  # sensitivity asserts keep it from passing vacuously.
  backends-go-test-independent =
    let
      inherit (pkgs.lib) assertMsg concatStringsSep;
      held =
        name: other:
        fs.toList (
          fs.intersection (fs.unions (backendGoTestSharedFor name)) (internalDir + "/forge/${other}")
        );
      allowed = name: other: buildsOnFor name == other;
      afterRemoving =
        dir:
        let
          file = builtins.head (
            builtins.attrNames (pkgs.lib.filterAttrs (_: t: t == "regular") (builtins.readDir dir))
          );
        in
        fs.difference launcherDir (dir + "/${file}");
      backendDir = n: internalDir + "/forge/${n}";
      movedBy = n: coreChecksMovedBy (afterRemoving (backendDir n));
      coreMoved = pkgs.lib.unique (builtins.concatMap movedBy bundledBackendNames);
      allCoreChecks = builtins.attrNames (coreChecksFor launcherDir);
      coreInert = pkgs.lib.unique (
        builtins.concatMap (
          n: builtins.filter (c: !builtins.elem c (movedBy n)) allCoreChecks
        ) coreBackendNames
      );
      backendDrvs = set: map (n: set.${adapterGoTestAttr "backends" n}.drvPath) backendNames;
      runnerMoved = builtins.filter (
        n:
        backendDrvs (backendGoTestsFor {
          files = afterRemoving (runnerDir n);
          names = backendNames;
        }) != backendDrvs backendGoTests
      ) runnerNames;
      sensitivityRows = builtins.filter (row: row ? goTestBuildsOn) backendRowList;
      leaks = concatStringsSep ", " (
        builtins.concatMap (
          name:
          builtins.concatMap (
            other:
            pkgs.lib.optional (
              other != name && !allowed name other && held name other != [ ]
            ) "${name} holds ${other}"
          ) backendNames
        ) backendNames
      );
    in
    assert assertMsg (sensitivityRows != [ ]) "no backend row sets goTestBuildsOn; the check is inert";
    assert assertMsg (builtins.all (row: held row.name row.goTestBuildsOn != [ ])
      sensitivityRows
    ) "a goTestBuildsOn backend's go test source no longer holds its base; the check is inert";
    assert assertMsg (
      coreMoved == [ ]
    ) "editing a bundled backend changed: ${concatStringsSep ", " coreMoved}";
    assert assertMsg (
      coreInert == [ ]
    ) "editing a core backend left unchanged: ${concatStringsSep ", " coreInert}; the check is inert";
    assert assertMsg (runnerMoved == [ ])
      "editing the ${concatStringsSep ", " runnerMoved} runner family changed a backend's go test derivation";
    assert assertMsg (leaks == "") "backend go test sources hold sibling backends: ${leaks}";
    pkgs.runCommand "backends-go-test-independent" { } "touch $out";

  # Seam tests (issue #4280): the integration-tagged packages run against the
  # rendered fixtures the bats harness produces, handed over via
  # SPINDRIFT_SEAM_FIXTURES_DIR since the sandbox can't build them; the
  # locked pi, its hook bridge and the baked hooks reach the piseam tests the
  # same way, via SPINDRIFT_PI_BIN, _BRIDGE and _HOOKS_DIR. The
  # other integration tests self-skip without bwrap/podman/docker. pkgs.nix is
  # on PATH so the bwrap gcroots test runs against a chroot store. The
  # package list is derived from the tree, so a new tagged file is picked up.
  #
  # Unlike the core checks it copies the whole tree (issue #5562): the fixtures'
  # run-input documents name the image built from the bundle-linking box
  # binary, so any Driver, runner family or bundled backend edit moves them
  # anyway, and several tagged packages (the root, box, driver-exec,
  # internal/forge/forgejo, internal/runner/bwrap, internal/runner/oci) lie
  # outside coreFiles.
  launcher-go-seam-test =
    pkgs.runCommand "launcher-go-seam-test"
      {
        nativeBuildInputs = [
          pkgs.go
          pkgs.git
          # The hook bridge's hooks parse their input with jq.
          pkgs.jq
          # The bwrap GC-root test drives a throwaway chroot store with it.
          pkgs.nix
        ];
        SPINDRIFT_SEAM_FIXTURES_DIR = fixtures.seamFixtures;
        SPINDRIFT_PI_BIN = "${pkgs.pi-coding-agent}/bin/pi";
        # Handed in because only cmd/launcher is copied below; the hooks carry
        # store-pinned shebangs, as the sandbox has no /usr/bin/env.
        SPINDRIFT_PI_BRIDGE = ../../agent/pi-hook-bridge.js;
        SPINDRIFT_PI_HOOKS_DIR = import ../../lib/agent-hooks.nix pkgs;
        SPINDRIFT_PI_ENV_FILES = driverRegistry.renderEnvFiles driverRegistry.entries.pi.envFiles;
      }
      ''
        mkdir -p src/cmd
        cp -r ${../../cmd/launcher} src/cmd/launcher
        # The promptassembly golden test reads the committed goldens and the raw
        # prompt templates at the paths repopath resolves from the module dir.
        mkdir -p src/tests/testdata src/templates/default
        cp -r ${../../tests/testdata/prompt-assembly-golden} src/tests/testdata/prompt-assembly-golden
        cp -r ${../../templates/default/prompts} src/templates/default/prompts
        chmod -R +w src
        ${goTestPrologue}
        files=$(grep -rl --include='*_test.go' --exclude-dir=vendor '^//go:build integration' . || true)
        if [ -z "$files" ]; then
          echo "no integration-tagged test files found" >&2
          exit 1
        fi
        # Run each package with only its own tagged test names: its untagged
        # tests need the full repo tree and already run in launcher-go-test or
        # launcher-binaries-go-test.
        # A name scraped from the sources is a heuristic, so a tagged test
        # sharing a name with an untagged one in its package would run both.
        for pkg in $(echo "$files" | xargs -n1 dirname | sort -u); do
          tests=$(echo "$files" | grep "^$pkg/[^/]*\$" | xargs grep -h '^func Test' | sed 's/^func \(Test[A-Za-z0-9_]*\).*/\1/' | paste -sd'|')
          go test -tags integration -run "^($tests)\$" "$pkg"
        done
        touch $out
      '';

  # go-check-env.nix's CGO_ENABLED=0 lets these pure-Go darwin cross-builds run
  # without a C cross-toolchain.
  launcher-cross-build =
    pkgs.runCommand "launcher-cross-build" { nativeBuildInputs = [ pkgs.go ]; }
      ''
        cp -r ${../../cmd/launcher} src
        chmod -R +w src
        cp -r ${launcherGoModules} src/vendor
        ${goCheckEnv}
        cd src
        go build -o "$TMPDIR/launcher-linux" .
        GOOS=darwin GOARCH=amd64 go build -o "$TMPDIR/launcher-darwin-amd64" .
        GOOS=darwin GOARCH=arm64 go build -o "$TMPDIR/launcher-darwin-arm64" .
        touch $out
      '';

  # ADR 0069 decision 2 (issue #5677): the core runner package holds no
  # family-specific code, and the two family packages never see each other.
  # go list -deps catches an import that arrives through a third package too;
  # -test makes a family import from only a _test.go file count as well.
  launcher-runner-family-isolation =
    pkgs.runCommand "launcher-runner-family-isolation"
      {
        nativeBuildInputs = [ pkgs.go ];
        families = pkgs.lib.concatStringsSep " " runnerNames;
      }
      ''
        cp -r ${../../cmd/launcher} src
        chmod -R +w src
        cp -r ${launcherGoModules} src/vendor
        ${goCheckEnv}
        cd src
        runnerPkg=spindrift.dev/launcher/internal/runner
        # reaches PKG FAMILY WHY: fail naming both when FAMILY is in PKG's deps.
        reaches() {
          if ! deps=$(go list -deps -test "./internal/$1" 2>&1); then
            # core importing a family forms a cycle, since each family imports
            # core; blame ADR 0069 only when the toolchain reports one.
            if echo "$deps" | grep -q 'import cycle'; then
              echo "go list failed for internal/$1 — likely an import cycle, which violates ADR 0069 decision 2 ($3):" >&2
            else
              echo "go list failed for internal/$1 for a reason unrelated to a family import (see below) — not necessarily an ADR 0069 violation:" >&2
            fi
            echo "$deps" >&2
            exit 1
          fi
          # -test lists variants as "PKG [PKG.test]", so match the path as a word.
          if echo "$deps" | grep -qE "^$runnerPkg/$2( |\$)"; then
            echo "internal/$1 imports internal/runner/$2 — violates ADR 0069 decision 2 ($3)" >&2
            exit 1
          fi
        }
        for fam in $families; do
          reaches runner "$fam" "the core runner package must not import a family package"
          for other in $families; do
            [ "$fam" = "$other" ] || reaches "runner/$fam" "$other" "a family package must not import another family"
          done
        done
        touch $out
      '';

  # The formatter must be the same store path as the nixfmt the nix-fmt check
  # runs, so how the tree is checked and how it is fixed cannot drift apart.
  formatter-is-nixfmt = pkgs.runCommand "formatter-is-nixfmt" { } ''
    test "${config.formatter}" = "${pkgs.nixfmt}"
    touch $out
  '';

  module-consumer-formatter-is-nixfmt = pkgs.runCommand "module-consumer-formatter-is-nixfmt" { } ''
    test "${consumerFormatter}" = "${pkgs.nixfmt}"
    touch $out
  '';
}
