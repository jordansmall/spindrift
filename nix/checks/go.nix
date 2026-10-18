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
  driverDir = name: ../../cmd/launcher/internal/driver + "/${name}";
  adapterGoTestAttr = prefix: name: "${prefix}-${name}-go-test";
  driverGoTestAttr = adapterGoTestAttr "drivers";
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
  # `files` without the Drivers (and so their corpora), the bundle and the
  # binaries.
  coreFiles =
    files:
    fs.difference files (
      fs.unions (allDriverDirs ++ [ (launcherDir + "/internal/driver/bundle") ] ++ bundleBinaryDirs)
    );
  # The vendor tree is re-derived from a source without any Driver, bundle or
  # binary, so editing one Driver never changes another's drvPath nor the core
  # go test's (a plain launcherGoModules would, its src being the whole tree).
  # Fixed-output with the same vendorHash, so its outPath equals
  # launcherGoModules' and nothing is fetched twice. `go mod vendor -e`
  # tolerates the packages that import the absent directories. Gotcha: a Driver
  # or main importing a third-party module no other package uses changes the
  # vendored set, and the narrowed derivation then mismatches launcherVendorHash.
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
  # ADR 0068 decision 7), keyed by registry name so a new row yields one with
  # no hand-pinned attr: `${prefix}-${name}-go-test`. `pkgFor name` is the
  # adapter's package directory under cmd/launcher; `sharedFor name` the extra
  # packages its tests reach. `files` is the cmd/launcher fileset every source
  # is cut from; the independence check perturbs it. The vendor tree is
  # `vendorFor files`, so one Driver's edits never move another's drvPath;
  # coreFiles strips no backend, so a backend edit moves every backend's
  # drvPath, though not its outPath.
  # `setupFor name` is shell run after the copy for files outside cmd/launcher
  # its tests read.
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
  # every backend. Never a backend's directory. The forge and driver roots are
  # listed file by file because backend and Driver directories lie beneath
  # them; the driver root is seamtest's registry import, non-test files only.
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
      "runner"
      "seambundle"
      "seamtest"
      "testutil"
      "unixsocket"
      "usage"
    ]
    ++ rootGoFiles (internalDir + "/forge")
    ++ rootGoFiles (internalDir + "/driver");
  # `local` and forgejo build on the git backend's package as a library, not
  # on the adapter: no other backend's fileset holds a sibling backend.
  gitBackedBackends = [
    "local"
    "forgejo"
  ];
  backendGoTestSharedFor =
    name:
    backendGoTestShared
    ++ pkgs.lib.optional (builtins.elem name gitBackedBackends) (internalDir + "/forge/git");
  backendGoTestsFor =
    { files, names }:
    adapterGoTests {
      inherit files names;
      prefix = "backends";
      pkgFor = name: "internal/forge/${name}";
      sharedFor = backendGoTestSharedFor;
      # forge/github's tests read the dispatch workflow beside cmd/launcher.
      setupFor =
        name:
        pkgs.lib.optionalString (name == "github") ''
          mkdir -p src/.github/workflows
          cp ${../../.github/workflows/agent-dispatch.yml} src/.github/workflows/agent-dispatch.yml
        '';
      nativeBuildInputs = goTestInputs;
    };
  backendGoTests = backendGoTestsFor {
    files = launcherDir;
    names = backendNames;
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
  # The core run: every package that needs no Driver, bundle or main. It cuts
  # its source from coreFiles, so a Driver edit never reaches this derivation;
  # the assert fails if the copied tree still holds one. The corpus find is
  # defence in depth, for a corpus landing under internal/driver outside a
  # Driver directory.
  launcherGoTestFor =
    files:
    pkgs.runCommand "launcher-go-test" { nativeBuildInputs = goTestInputs; } ''
      mkdir -p src/cmd
      cp -r ${
        fs.toSource {
          root = launcherDir;
          fileset = coreFiles files;
        }
      } src/cmd/launcher
      ${sideTrees}
      chmod -R +w src
      for held in bundle ${pkgs.lib.concatStringsSep " " driverNames}; do
        if [ -e "src/cmd/launcher/internal/driver/$held" ]; then
          echo "launcher-go-test's source holds internal/driver/$held" >&2
          exit 1
        fi
      done
      corpora=$(find src/cmd/launcher/internal/driver -path '*/testdata/corpus')
      if [ -n "$corpora" ]; then
        echo "launcher-go-test's source holds a Driver corpus: $corpora" >&2
        exit 1
      fi
      ${goTestPrologueWith (vendorFor files)}
      module=$(go list -m)
      # The registry root's guard tests need every Driver, and run in
      # launcher-binaries-go-test.
      go test $(go list ./... | grep -vxF "$module/internal/driver")
      touch $out
    '';
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
// {
  launcher-go-fmt = pkgs.runCommand "launcher-go-fmt" { nativeBuildInputs = [ pkgs.go ]; } ''
    unformatted=$(gofmt -l ${../../cmd/launcher})
    if [ -n "$unformatted" ]; then
      echo "gofmt violations:" >&2
      echo "$unformatted" >&2
      exit 1
    fi
    touch $out
  '';

  dashboard-go-fmt = pkgs.runCommand "dashboard-go-fmt" { nativeBuildInputs = [ pkgs.go ]; } ''
    unformatted=$(gofmt -l ${../../dashboard})
    if [ -n "$unformatted" ]; then
      echo "gofmt violations:" >&2
      echo "$unformatted" >&2
      exit 1
    fi
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
  # against lib/image.nix's baked nix.conf `cores` (issue #3965).
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
      touch $out
    '';

  launcher-go-vet = pkgs.runCommand "launcher-go-vet" { nativeBuildInputs = [ pkgs.go ]; } ''
    cp -r ${../../cmd/launcher} src
    chmod -R +w src
    cp -r ${launcherGoModules} src/vendor
    ${goCheckEnv}
    cd src
    go vet ./...
    touch $out
  '';

  launcher-go-test = launcherGoTestFor launcherDir;

  # The packages that link every Driver through the bundle (bundleBinaryDirs),
  # the bundle's own tests, plus the registry root's cross-Driver guard tests;
  # the whole tree is copied, so a Driver edit reruns this.
  launcher-binaries-go-test =
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
        go test . ${
          pkgs.lib.concatMapStringsSep " " (d: "./${d}/...") (bundleMains ++ bundleFixtureDirs)
        } ./internal/driver ./internal/driver/bundle/...
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
    in
    assert assertMsg (guarded == listed)
      "bundleMains ++ bundleFixtureDirs (nix/checks/go.nix) must equal the module + \"/<dir>\" entries of bundleBinaries and bundleFixtures (internal/driver/bundle_guard_test.go); go.nix has: ${concatStringsSep ", " listed}; the guard has: ${concatStringsSep ", " guarded}";
    pkgs.runCommand "bundle-binaries-match-guard" { } "touch $out";

  # A registry row yields its per-Driver go test derivation with no other edit
  # (issue #5508). Only attr names are forced: building the stub's derivation
  # would fail on its missing package directory.
  drivers-go-test-stub-row-yields-derivation =
    let
      names = builtins.attrNames (driverGoTestsFor {
        files = ../../cmd/launcher;
        names = driverNames ++ [ "stub" ];
      });
    in
    assert pkgs.lib.assertMsg (builtins.elem "drivers-stub-go-test" names)
      "a stub registry row must yield drivers-stub-go-test, got: ${pkgs.lib.concatStringsSep ", " names}";
    pkgs.runCommand "drivers-go-test-stub-row-yields-derivation" { } "touch $out";

  # A backend registry row yields its per-backend go test derivation with no
  # other edit (issue #5637); attr names only, as for the Drivers.
  backends-go-test-stub-row-yields-derivation =
    let
      names = builtins.attrNames (backendGoTestsFor {
        files = ../../cmd/launcher;
        names = backendNames ++ [ "stub" ];
      });
    in
    assert pkgs.lib.assertMsg (builtins.elem "backends-stub-go-test" names)
      "a stub registry row must yield backends-stub-go-test, got: ${pkgs.lib.concatStringsSep ", " names}";
    pkgs.runCommand "backends-go-test-stub-row-yields-derivation" { } "touch $out";

  # Editing one Driver leaves every other Driver's go test derivation path, and
  # the core launcher-go-test's, unchanged (issues #5508, #5545): a vendor tree
  # or source shared across them would move them all. Each registry Driver in
  # turn has one file removed; the sensitivity assert keeps the check from
  # passing vacuously.
  drivers-go-test-independent =
    let
      inherit (pkgs.lib) assertMsg concatStringsSep filterAttrs;
      drv = set: n: set.${driverGoTestAttr n}.drvPath;
      independent =
        p:
        let
          file = builtins.head (
            builtins.attrNames (filterAttrs (_: t: t == "regular") (builtins.readDir (driverDir p)))
          );
          after = driverGoTestsFor {
            files = fs.difference ../../cmd/launcher (driverDir p + "/${file}");
            names = driverNames;
          };
          moved = builtins.filter (n: n != p && drv driverGoTests n != drv after n) driverNames;
          core = launcherGoTestFor (fs.difference launcherDir (driverDir p + "/${file}"));
        in
        assertMsg (drv driverGoTests p != drv after p)
          "removing ${file} from the ${p} Driver did not change its own go test derivation; the perturbation is inert"
        && assertMsg (
          moved == [ ]
        ) "editing the ${p} Driver changed the go test derivation of: ${concatStringsSep ", " moved}"
        && assertMsg (
          core.drvPath == (launcherGoTestFor launcherDir).drvPath
        ) "editing the ${p} Driver changed launcher-go-test";
    in
    assert builtins.all independent driverNames;
    pkgs.runCommand "drivers-go-test-independent" { } "touch $out";

  # No backend's go test source holds another backend's directory, bar the git
  # backend's for `local` and forgejo (issue #5637). A fileset check, not the
  # drvPath comparison the Drivers use: coreFiles strips no backend, so a
  # backend edit moves every backend's vendor drvPath. The sensitivity assert
  # keeps it from passing vacuously.
  backends-go-test-independent =
    let
      inherit (pkgs.lib) assertMsg concatStringsSep;
      held =
        name: other:
        fs.toList (
          fs.intersection (fs.unions (backendGoTestSharedFor name)) (internalDir + "/forge/${other}")
        );
      allowed = name: other: other == "git" && builtins.elem name gitBackedBackends;
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
    assert assertMsg (
      held "local" "git" != [ ]
    ) "local's go test source no longer holds the git backend; the check is inert";
    assert assertMsg (leaks == "") "backend go test sources hold sibling backends: ${leaks}";
    pkgs.runCommand "backends-go-test-independent" { } "touch $out";

  # Seam tests (issue #4280): the integration-tagged packages run against the
  # rendered fixtures the bats harness produces, handed over via
  # SPINDRIFT_SEAM_FIXTURES_DIR since nix isn't callable in the sandbox; the
  # locked pi, its hook bridge and the baked hooks reach the piseam tests the
  # same way, via SPINDRIFT_PI_BIN, _BRIDGE and _HOOKS_DIR. The
  # other integration tests self-skip without nix/bwrap/podman/docker. The
  # package list is derived from the tree, so a new tagged file is picked up.
  launcher-go-seam-test =
    pkgs.runCommand "launcher-go-seam-test"
      {
        nativeBuildInputs = [
          pkgs.go
          pkgs.git
          # The hook bridge's hooks parse their input with jq.
          pkgs.jq
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

  # ADR 0023 makes console a leaf UI package: engine packages never import it.
  # go list -deps walks the whole transitive graph, so an import that reaches
  # console through another engine package is caught too, not just a direct one.
  launcher-console-isolation =
    let
      guardedPackages = pkgs.lib.concatStringsSep " " [
        "dispatch"
        "waves"
        "forge"
        "settle"
        "runner"
        "driver"
        "freshness"
      ];
    in
    pkgs.runCommand "launcher-console-isolation"
      {
        nativeBuildInputs = [ pkgs.go ];
        inherit guardedPackages;
      }
      ''
        cp -r ${../../cmd/launcher} src
        chmod -R +w src
        cp -r ${launcherGoModules} src/vendor
        ${goCheckEnv}
        cd src
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
