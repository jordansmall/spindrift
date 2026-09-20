{
  pkgs,
  config,
  fixtures,
  launcherGoModules,
  ...
}:
let
  inherit (fixtures) consumerFormatter;
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

  nix-fmt = pkgs.runCommand "nix-fmt" { nativeBuildInputs = [ pkgs.nixfmt ]; } ''
    nixfmt --check \
      ${../../flake.nix} \
      ${../../lib/app-wiring-check.nix} \
      ${../../lib/builtins-compat.nix} \
      ${../../lib/default-model-fixture.nix} \
      ${../../lib/env-schema.nix} \
      ${../../lib/flakeModule.nix} \
      ${../../lib/fragment-pairs.nix} \
      ${../../lib/gh-token-intervals.nix} \
      ${../../lib/jira-status-mapping.nix} \
      ${../../lib/mkHarness.nix} \
      ${../../lib/nixpkgs-shared.nix} \
      ${../../lib/prompt-contract.nix} \
      ${../../lib/renderers.nix} \
      ${../fixtures.nix} \
      ${../../templates/default/flake.nix} \
      ${./builtins-compat.nix} \
      ${./code-review-fragment-parity.nix} \
      ${./commit-fragment-parity.nix} \
      ${./default.nix} \
      ${./bats.nix} \
      ${./changelog.nix} \
      ${./equivalence.nix} \
      ${./fragment-pairs.nix} \
      ${./gh-token-intervals.nix} \
      ${./go.nix} \
      ${./image.nix} \
      ${./jira-status-mapping.nix} \
      ${./mk-fragment-parity.nix} \
      ${./nix-checks-lore-parity.nix} \
      ${./prompts.nix} \
      ${./quickstart-golden.nix} \
      ${./schema-drift.nix} \
      ${./scout-rationale-parity.nix} \
      ${./tdd-fragment-parity.nix}
    touch $out
  '';

  # CGO_ENABLED=0 avoids needing a C toolchain: the jira forge adapter imports
  # net/http, which otherwise pulls runtime/cgo into the build and fails with
  # "gcc not found". Every derivation below sets it for the same reason.
  launcher-go-vet = pkgs.runCommand "launcher-go-vet" { nativeBuildInputs = [ pkgs.go ]; } ''
    cp -r ${../../cmd/launcher} src
    chmod -R +w src
    cp -r ${launcherGoModules} src/vendor
    export GOPROXY=off
    export GOFLAGS=-mod=vendor
    export GONOSUMCHECK='*'
    export GOMODCACHE="$TMPDIR/gomodcache"
    export GOCACHE="$TMPDIR/gocache"
    export CGO_ENABLED=0
    cd src
    go vet ./...
    touch $out
  '';

  # go test guards config-parsing regressions (#112). docs/, .github/,
  # .forgejo/, templates/, README.md and lib/ are copied beside cmd/launcher,
  # mirroring the repo layout, so the tests resolve their relative paths (#611,
  # #1985, #2038, #2507, #2566, #2741, #2743). forge's tests shell out to git;
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
        cp ${../../README.md} src/README.md
        cp -r ${../../lib} src/lib
        chmod -R +w src
        cp -r ${launcherGoModules} src/cmd/launcher/vendor
        export GOPROXY=off
        export GOFLAGS=-mod=vendor
        export GONOSUMCHECK='*'
        export GOMODCACHE="$TMPDIR/gomodcache"
        export GOCACHE="$TMPDIR/gocache"
        export CGO_ENABLED=0
        mkdir -p "$TMPDIR/fakebin"
        cat > "$TMPDIR/fakebin/bwrap" <<'EOF'
        #!/bin/sh
        echo "fakebin/bwrap: stub for LookPath only, tests never invoke it for real" >&2
        exit 1
        EOF
        chmod +x "$TMPDIR/fakebin/bwrap"
        export PATH="$TMPDIR/fakebin:$PATH"
        cd src/cmd/launcher
        go test ./...
        touch $out
      '';

  # CGO_ENABLED=0 also makes the pure-Go darwin cross-builds work without a C
  # cross-toolchain.
  launcher-cross-build =
    pkgs.runCommand "launcher-cross-build" { nativeBuildInputs = [ pkgs.go ]; }
      ''
        cp -r ${../../cmd/launcher} src
        chmod -R +w src
        cp -r ${launcherGoModules} src/vendor
        export GOPROXY=off
        export GOFLAGS=-mod=vendor
        export GONOSUMCHECK='*'
        export GOMODCACHE="$TMPDIR/gomodcache"
        export GOCACHE="$TMPDIR/gocache"
        export CGO_ENABLED=0
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
        export GOPROXY=off
        export GOFLAGS=-mod=vendor
        export GONOSUMCHECK='*'
        export GOMODCACHE="$TMPDIR/gomodcache"
        export GOCACHE="$TMPDIR/gocache"
        export CGO_ENABLED=0
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
