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
  driverDir = name: ../../cmd/launcher/internal/driver + "/${name}";
  driverGoTestAttr = name: "drivers-${name}-go-test";
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
  # One go test derivation per Driver (ADR 0066 decision 5), keyed by registry
  # name so a new row yields one with no hand-pinned attr. `files` is the
  # cmd/launcher fileset every source is cut from; the independence check
  # perturbs it.
  #
  # The vendor tree is re-derived from a source without any Driver directory,
  # so editing one Driver never changes another's drvPath (a plain
  # launcherGoModules would, its src being the whole tree). Fixed-output with
  # the same vendorHash, so its outPath equals launcherGoModules' and nothing
  # is fetched twice. `go mod vendor -e` tolerates the packages that import the
  # absent Driver directories. Gotcha: a Driver importing a third-party module
  # no other package uses changes the vendored set, and the narrowed derivation
  # then mismatches launcherVendorHash.
  driverGoTestsFor =
    {
      files,
      entries,
    }:
    let
      allDriverDirs = fs.unions (map driverDir driverNames);
      vendor = launcherGoModules.overrideAttrs (_: {
        src = fs.toSource {
          root = ../../cmd/launcher;
          fileset = fs.difference files allDriverDirs;
        };
        preBuild = "goModVendorFlags=(-e)";
      });
    in
    pkgs.lib.listToAttrs (
      map (name: {
        name = driverGoTestAttr name;
        value =
          let
            src = fs.toSource {
              root = ../../cmd/launcher;
              fileset = fs.intersection files (
                fs.unions (
                  [
                    ../../cmd/launcher/go.mod
                    ../../cmd/launcher/go.sum
                    (driverDir name)
                  ]
                  ++ driverGoTestShared
                )
              );
            };
          in
          pkgs.runCommand (driverGoTestAttr name) { nativeBuildInputs = [ pkgs.go ]; } ''
            mkdir -p src/cmd
            cp -r ${src} src/cmd/launcher
            chmod -R +w src
            ${goTestPrologueWith vendor}
            go test ./internal/driver/${name}/...
            touch $out
          '';
      }) (builtins.attrNames entries)
    );
  driverGoTests = driverGoTestsFor {
    files = ../../cmd/launcher;
    inherit (driverRegistry) entries;
  };
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

  # go test guards config-parsing regressions (#112). docs/, .github/,
  # .forgejo/, templates/, agent/, README.md (if present) and lib/ are copied
  # beside cmd/launcher, mirroring the repo layout, so the tests resolve their relative
  # paths (#611, #1985, #2038, #2507, #2566, #2741, #2743, #3988). forge's tests shell out to git;
  # the RUNTIME=bwrap tests LookPath "bwrap" (#2441), and bubblewrap is Linux-only.
  launcher-go-test =
    pkgs.runCommand "launcher-go-test"
      {
        nativeBuildInputs = [
          pkgs.go
          pkgs.git
        ];
      }
      ''
        mkdir -p src/cmd
        cp -r ${../../cmd/launcher} src/cmd/launcher
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
        chmod -R +w src
        ${goTestPrologue}
        module=$(go list -m)
        listed=$(go list ./...)
        # Driver packages run in their own drivers-<name>-go-test derivations.
        go test $(printf '%s\n' "$listed" | grep -vE "^$module/internal/driver/(${pkgs.lib.concatStringsSep "|" driverNames})(/|\$)")
        touch $out
      '';

  # A registry row yields its per-Driver go test derivation with no other edit
  # (issue #5508). Only attr names are forced: building the stub's derivation
  # would fail on its missing package directory.
  drivers-go-test-stub-row-yields-derivation =
    let
      names = builtins.attrNames (driverGoTestsFor {
        files = ../../cmd/launcher;
        entries = driverRegistry.entries // {
          stub = { };
        };
      });
    in
    assert pkgs.lib.assertMsg (builtins.elem "drivers-stub-go-test" names)
      "a stub registry row must yield drivers-stub-go-test, got: ${pkgs.lib.concatStringsSep ", " names}";
    pkgs.runCommand "drivers-go-test-stub-row-yields-derivation" { } "touch $out";

  # Editing one Driver leaves every other Driver's go test derivation path
  # unchanged (issue #5508): a vendor tree or source shared across Drivers
  # would move them all. Each registry Driver in turn has one file removed; the
  # sensitivity assert keeps the check from passing vacuously.
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
            inherit (driverRegistry) entries;
          };
          moved = builtins.filter (n: n != p && drv driverGoTests n != drv after n) driverNames;
        in
        assertMsg (drv driverGoTests p != drv after p)
          "removing ${file} from the ${p} Driver did not change its own go test derivation; the perturbation is inert"
        && assertMsg (
          moved == [ ]
        ) "editing the ${p} Driver changed the go test derivation of: ${concatStringsSep ", " moved}";
    in
    assert builtins.all independent driverNames;
    pkgs.runCommand "drivers-go-test-independent" { } "touch $out";

  # Seam tests (issue #4280): the integration-tagged packages run against the
  # rendered fixtures the bats harness produces, handed over via
  # SPINDRIFT_SEAM_FIXTURES_DIR since nix isn't callable in the sandbox; the
  # locked pi reaches the piseam tests the same way, via SPINDRIFT_PI_BIN. The
  # other integration tests self-skip without nix/bwrap/podman/docker. The
  # package list is derived from the tree, so a new tagged file is picked up.
  launcher-go-seam-test =
    pkgs.runCommand "launcher-go-seam-test"
      {
        nativeBuildInputs = [
          pkgs.go
          pkgs.git
        ];
        SPINDRIFT_SEAM_FIXTURES_DIR = fixtures.seamFixtures;
        SPINDRIFT_PI_BIN = "${pkgs.pi-coding-agent}/bin/pi";
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
        # tests need the full repo tree and already run in launcher-go-test.
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
