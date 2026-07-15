# Go toolchain gates (gofmt, vet, test, cross-build) plus the nixfmt
# formatting gate and formatter-identity checks that ride alongside them.
{
  pkgs,
  config,
  fixtures,
  ...
}:
let
  inherit (fixtures) consumerFormatter;

  # The vendored module tree for cmd/launcher's external deps
  # (charmbracelet/bubbletea, issue #784) — GOPROXY=off below means these
  # hand-rolled go build/vet/test checks need it on disk instead of
  # reaching the network. vendorHash must match lib/mkHarness.nix's
  # launcherBin/driverExecBin; update both together.
  launcherGoModules =
    (pkgs.buildGoModule {
      pname = "spindrift-launcher-modules";
      version = "0";
      src = ../../cmd/launcher;
      vendorHash = "sha256-pz95WwGNc065UWJspokZ4heMGKWh8Bsi+5O+UmCAtqA=";
    }).goModules;
in
{
  # gofmt -l must exit cleanly — any output means unformatted files.
  launcher-go-fmt = pkgs.runCommand "launcher-go-fmt" { nativeBuildInputs = [ pkgs.go ]; } ''
    unformatted=$(gofmt -l ${../../cmd/launcher})
    if [ -n "$unformatted" ]; then
      echo "gofmt violations:" >&2
      echo "$unformatted" >&2
      exit 1
    fi
    touch $out
  '';

  # nixfmt --check must exit cleanly — any output means unformatted files.
  nix-fmt = pkgs.runCommand "nix-fmt" { nativeBuildInputs = [ pkgs.nixfmt ]; } ''
    nixfmt --check \
      ${../../flake.nix} \
      ${../../lib/env-schema.nix} \
      ${../../lib/flakeModule.nix} \
      ${../../lib/mkHarness.nix} \
      ${../fixtures.nix} \
      ${../../templates/default/flake.nix} \
      ${./default.nix} \
      ${./bats.nix} \
      ${./changelog.nix} \
      ${./equivalence.nix} \
      ${./go.nix} \
      ${./image.nix} \
      ${./prompts.nix} \
      ${./schema-drift.nix}
    touch $out
  '';

  # go vet catches suspicious constructs at analysis time.
  # CGO_ENABLED=0 avoids needing a C toolchain: the jira forge adapter
  # imports net/http, which otherwise pulls runtime/cgo into the build
  # and fails with "gcc not found" (matches launcher-cross-build, which
  # already builds the real binary this way).
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

  # go test must stay green: unit tests catch config-parsing bugs
  # before they reach the binary (see issue #112, 9494fc1-class).
  # forge's tests shell out to git (TestGitForcePush_CapturesStderr), so
  # git must be on PATH in the sandbox alongside go. CGO_ENABLED=0 for
  # the same reason as launcher-go-vet above.
  # docs/ is copied alongside cmd/launcher, mirroring the repo layout,
  # so TestReferenceDocLabelSnippetMatchesTriageDefaults can resolve its
  # ../../docs/reference.md path (#611).
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
        chmod -R +w src
        cp -r ${launcherGoModules} src/cmd/launcher/vendor
        export GOPROXY=off
        export GOFLAGS=-mod=vendor
        export GONOSUMCHECK='*'
        export GOMODCACHE="$TMPDIR/gomodcache"
        export GOCACHE="$TMPDIR/gocache"
        export CGO_ENABLED=0
        cd src/cmd/launcher
        go test ./...
        touch $out
      '';

  # Cross-build: launcher must compile for linux and darwin. Native
  # (x86_64-linux on CI) plus explicit darwin cross-targets.
  # CGO_ENABLED=0 makes pure-Go cross-compilation work without
  # a C cross-toolchain.
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

  # formatter output must be the same store path as the pinned pkgs.nixfmt
  # used by the nix-fmt check — no drift between "how it's checked" and
  # "how it's fixed".
  formatter-is-nixfmt = pkgs.runCommand "formatter-is-nixfmt" { } ''
    test "${config.formatter}" = "${pkgs.nixfmt}"
    touch $out
  '';

  # flakeModule consumers receive the same formatter via perSystem.
  module-consumer-formatter-is-nixfmt = pkgs.runCommand "module-consumer-formatter-is-nixfmt" { } ''
    test "${consumerFormatter}" = "${pkgs.nixfmt}"
    touch $out
  '';
}
