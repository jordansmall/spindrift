# Go env shared by every check that builds the vendored launcher tree -- a
# shell snippet spliced in via `goCheckEnv`, not a check; go.nix's
# `go-check-env` check tests it.
# CGO_ENABLED=0 avoids needing a C toolchain: net/http otherwise pulls
# runtime/cgo into the build and fails with "gcc not found". GOMAXPROCS bounds
# Go by NIX_BUILD_CORES, which Go never reads itself: an unsandboxed build sees
# every host CPU, and the Box's cgroup pids limit counts each Go process's
# threads (issue #3915). Unset and 0 fall back to 4, matching lib/image.nix's
# nix.conf `cores`.
''
  export GOPROXY=off
  export GOFLAGS=-mod=vendor
  export GONOSUMCHECK='*'
  export GOMODCACHE="$TMPDIR/gomodcache"
  export GOCACHE="$TMPDIR/gocache"
  export CGO_ENABLED=0
  export GOMAXPROCS="''${NIX_BUILD_CORES:-0}"
  [ "$GOMAXPROCS" != 0 ] || GOMAXPROCS=4
''
