# Go env shared by every check that builds the vendored launcher tree -- a
# shell snippet spliced in via `goCheckEnv`, not a check; go.nix's
# `go-check-env` check tests it.
# CGO_ENABLED=0 avoids needing a C toolchain: net/http otherwise pulls
# runtime/cgo into the build and fails with "gcc not found". GOMAXPROCS bounds
# Go by NIX_BUILD_CORES, which Go never reads itself: an unsandboxed build sees
# every host CPU, and the Box's cgroup pids limit counts each Go process's
# threads (issue #3915). Unset and 0 fall back to lib/image.nix's nix.conf
# `cores`; go.nix's `go-check-env` check fails if the two drift (issue #3965).
# HOME moves under $TMPDIR because Go writes telemetry to $HOME/.config/go
# (GOTELEMETRY=off does not stop it) and an unsandboxed build's default
# /homeless-shelter must stay absent or Nix's HOME guard fails every later
# build (issue #6009).
# NIX_CONF_DIR pins any real `nix` a check runs: an unsandboxed Box build reads
# /etc/nix/nix.conf, which a sandboxed CI builder lacks, so the same call would
# pass in one and fail in the other (issue #7083).
''
  export GOPROXY=off
  export GOFLAGS=-mod=vendor
  export GONOSUMCHECK='*'
  export GOMODCACHE="$TMPDIR/gomodcache"
  export GOCACHE="$TMPDIR/gocache"
  export HOME="$TMPDIR/home"
  mkdir -p "$TMPDIR/nix-conf"
  echo "experimental-features = nix-command flakes" >"$TMPDIR/nix-conf/nix.conf"
  export NIX_CONF_DIR="$TMPDIR/nix-conf"
  export NIX_USER_CONF_FILES=
  export CGO_ENABLED=0
  export GOMAXPROCS="''${NIX_BUILD_CORES:-0}"
  [ "$GOMAXPROCS" != 0 ] || GOMAXPROCS=4
''
