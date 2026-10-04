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
  nixSrc = pkgs.lib.fileset.toSource {
    root = ../..;
    fileset = pkgs.lib.fileset.fileFilter (f: f.hasExt "nix") ../..;
  };
  # Vendored modules, go env and the bwrap LookPath stub shared by the go test
  # checks. Expects the caller to have copied cmd/launcher to src/cmd/launcher.
  # Ends with src/cmd/launcher as the working directory.
  goTestPrologue = ''
    cp -r ${launcherGoModules} src/cmd/launcher/vendor
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
in
{
  launcher-go-fmt = pkgs.runCommand "launcher-go-fmt" { nativeBuildInputs = [ pkgs.go ]; } ''
    unformatted=$(gofmt -l ${../../cmd/launcher})
    if [ -n "$unformatted" ]; then
      echo "gofmt violations:" >&2
      echo "$unformatted" >&2
      exit 1
    fi
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
  # .forgejo/, templates/, agent/, README.md and lib/ are copied beside
  # cmd/launcher, mirroring the repo layout, so the tests resolve their relative
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
        cp ${../../README.md} src/README.md
        cp -r ${../../lib} src/lib
        chmod -R +w src
        ${goTestPrologue}
        go test ./...
        touch $out
      '';

  # Seam tests (issue #4280): the integration-tagged packages run against the
  # rendered fixtures the bats harness produces, handed over via
  # SPINDRIFT_SEAM_FIXTURES_DIR since nix isn't callable in the sandbox. The
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
