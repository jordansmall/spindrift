#!/usr/bin/env bats
# Go module binding (ADR 0044, issue #2857 slice 2): phase_go_binding points
# Go's own module-fetch tooling (GOPROXY/GOPRIVATE/GONOPROXY/GOSUMDB/
# GONOSUMDB, all plain env vars Go reads directly) at
# phase_registry_proxy_forwarder's local Forwarder
# (entrypoint-registry-proxy-forwarder.bats), closing off GOPRIVATE's default
# GONOPROXY side effect -- the silent fetch-straight-from-the-internet bypass
# -- while leaving GOPRIVATE's default GONOSUMDB side effect (the leak
# prevention we want) untouched.

load helper

setup() {
  setup_entrypoint_env
}

teardown() {
  [ -n "${_test_socat_pid:-}" ] && kill "$_test_socat_pid" 2>/dev/null
  # phase_registry_proxy_forwarder backgrounds its own socat TCP listener
  # (detached, closed fds) on REGISTRY_PROXY_FORWARDER_PORT -- its PID is
  # never visible to this test, so a leaked one from an earlier run can only
  # be reaped by matching the port it bound, not by tracking a PID.
  [ -n "${REGISTRY_PROXY_FORWARDER_PORT:-}" ] &&
    pkill -f "TCP-LISTEN:${REGISTRY_PROXY_FORWARDER_PORT}," 2>/dev/null
  true
}

# Bounded poll for the stand-in socat's UNIX-LISTEN socket file to actually
# exist -- copied from entrypoint-registry-proxy-forwarder.bats.
_wait_for_socket() {
  local _path="$1" _tries=0
  while [ "$_tries" -lt 50 ]; do
    [ -S "$_path" ] && return 0
    sleep 0.1
    _tries=$((_tries + 1))
  done
  return 1
}

# Builds $BATS_TEST_TMPDIR/go_binding_harness.sh from $ENTRYPOINT with its
# trailing `main "$@"` call stripped (mirrors entrypoint-pr-intent-nudge.bats'
# `sed '$d'` harness pattern), so a test can call configure_env plus the two
# phases directly and observe the resulting env vars -- phase_go_binding's
# effect is process environment, not a file on disk, so a full `run bash
# "$ENTRYPOINT"` (as the sibling cargo suites use) can't observe it from the
# outside.
_go_binding_harness() {
  local harness="$BATS_TEST_TMPDIR/go_binding_harness.sh"
  sed '$d' "$ENTRYPOINT" >"$harness"
  {
    echo 'configure_env'
    echo 'phase_registry_proxy_forwarder'
    echo 'phase_go_binding'
    echo 'echo "GOPROXY=[${GOPROXY:-unset}]"'
    echo 'echo "GONOPROXY=[${GONOPROXY:-unset}]"'
    echo 'echo "GOPRIVATE=[${GOPRIVATE:-unset}]"'
    echo 'echo "GOSUMDB=[${GOSUMDB:-unset}]"'
    echo 'echo "GONOSUMDB=[${GONOSUMDB:-unset}]"'
    echo 'echo "GOTOOLCHAIN=[${GOTOOLCHAIN:-unset}]"'
    # go env asks the actual Go toolchain to resolve GONOPROXY the way
    # cfg.envOr("GONOPROXY", GOPRIVATE) really does -- inspecting the raw
    # shell var can't tell an empty-string GONOPROXY (which envOr treats as
    # unset, falling back to GOPRIVATE) apart from a truly neutralizing one
    # (the documented "none" sentinel, see `go help private`).
    echo 'echo "GONOPROXY_RESOLVED=[$(go env GONOPROXY)]"'
    echo 'echo "GOTOOLCHAIN_RESOLVED=[$(go env GOTOOLCHAIN)]"'
  } >>"$harness"
  echo "$harness"
}

@test "socket present: GOPROXY is bound to the local Forwarder (issue #2857)" {
  export REGISTRY_PROXY_SOCKET_PATH="$BATS_TEST_TMPDIR/registry-proxy.sock"
  export REGISTRY_PROXY_FORWARDER_PORT=27185

  socat "UNIX-LISTEN:$REGISTRY_PROXY_SOCKET_PATH,fork,reuseaddr" EXEC:true &
  _test_socat_pid=$!
  _wait_for_socket "$REGISTRY_PROXY_SOCKET_PATH"

  run bash "$(_go_binding_harness)"
  [ "$status" -eq 0 ]
  [[ "$output" == *"GOPROXY=[http://127.0.0.1:${REGISTRY_PROXY_FORWARDER_PORT}]"* ]]

  # No trailing ,direct fallback -- that's the public-proxy silent-bypass
  # path this binding exists to close off.
  [[ "$output" != *",direct"* ]]

  [[ "$output" == *"go bound to it via GOPROXY"* ]]

  # Neither GONOPROXY/GOPRIVATE, GOSUMDB, nor GOTOOLCHAIN carried a
  # pre-existing value in this test, so none of the three override warnings
  # below has anything to warn about.
  [[ "$output" != *"overriding pre-existing GONOPROXY"* ]]
  [[ "$output" != *"overriding explicit GOSUMDB"* ]]
  [[ "$output" != *"overriding GOTOOLCHAIN"* ]]
}

@test "GONOPROXY resolves to none via go env even when GOPRIVATE is already set (issue #2857)" {
  export REGISTRY_PROXY_SOCKET_PATH="$BATS_TEST_TMPDIR/registry-proxy.sock"
  export REGISTRY_PROXY_FORWARDER_PORT=27185
  export GOPRIVATE="corp.example/*"

  socat "UNIX-LISTEN:$REGISTRY_PROXY_SOCKET_PATH,fork,reuseaddr" EXEC:true &
  _test_socat_pid=$!
  _wait_for_socket "$REGISTRY_PROXY_SOCKET_PATH"

  run bash "$(_go_binding_harness)"
  [ "$status" -eq 0 ]
  # Asserting the raw shell var's text can't distinguish a merely
  # empty-string GONOPROXY (which Go's own cfg.envOr("GONOPROXY", GOPRIVATE)
  # treats identically to unset, silently falling back to GOPRIVATE) from a
  # genuinely neutralizing one -- ask the actual Go toolchain instead.
  [[ "$output" == *"GONOPROXY_RESOLVED=[none]"* ]]
  [[ "$output" == *"GOPRIVATE=[corp.example/*]"* ]]

  # GOPRIVATE was already set, so forcing GONOPROXY=none neutralizes its
  # GONOPROXY-defaulting side effect -- that's a real override, not a no-op.
  [[ "$output" == *"==> WARNING: overriding pre-existing GONOPROXY/GOPRIVATE with GONOPROXY=none"* ]]
}

@test "WARNING logged when a pre-existing GONOPROXY value is overridden with none (issue #2857)" {
  export REGISTRY_PROXY_SOCKET_PATH="$BATS_TEST_TMPDIR/registry-proxy.sock"
  export REGISTRY_PROXY_FORWARDER_PORT=27185
  export GONOPROXY="corp.example/*"

  socat "UNIX-LISTEN:$REGISTRY_PROXY_SOCKET_PATH,fork,reuseaddr" EXEC:true &
  _test_socat_pid=$!
  _wait_for_socket "$REGISTRY_PROXY_SOCKET_PATH"

  run bash "$(_go_binding_harness)"
  [ "$status" -eq 0 ]
  [[ "$output" == *"GONOPROXY_RESOLVED=[none]"* ]]
  [[ "$output" == *"==> WARNING: overriding pre-existing GONOPROXY/GOPRIVATE with GONOPROXY=none"* ]]
}

@test "GOSUMDB defaults to off when unset (issue #2857)" {
  export REGISTRY_PROXY_SOCKET_PATH="$BATS_TEST_TMPDIR/registry-proxy.sock"
  export REGISTRY_PROXY_FORWARDER_PORT=27185

  socat "UNIX-LISTEN:$REGISTRY_PROXY_SOCKET_PATH,fork,reuseaddr" EXEC:true &
  _test_socat_pid=$!
  _wait_for_socket "$REGISTRY_PROXY_SOCKET_PATH"

  run bash "$(_go_binding_harness)"
  [ "$status" -eq 0 ]
  [[ "$output" == *"GOSUMDB=[off]"* ]]

  # No pre-existing GOSUMDB to override, so no GOSUMDB warning is expected.
  [[ "$output" != *"overriding explicit GOSUMDB"* ]]
}

@test "GOSUMDB is forced off when set without a GOPRIVATE/GONOSUMDB exemption (issue #2857)" {
  export REGISTRY_PROXY_SOCKET_PATH="$BATS_TEST_TMPDIR/registry-proxy.sock"
  export REGISTRY_PROXY_FORWARDER_PORT=27185
  export GOSUMDB="sum.mycorp.example"

  socat "UNIX-LISTEN:$REGISTRY_PROXY_SOCKET_PATH,fork,reuseaddr" EXEC:true &
  _test_socat_pid=$!
  _wait_for_socket "$REGISTRY_PROXY_SOCKET_PATH"

  run bash "$(_go_binding_harness)"
  [ "$status" -eq 0 ]
  # No GOPRIVATE/GONOSUMDB exemption declared, so this single-upstream
  # Forwarder has no way to know which paths behind an explicit GOSUMDB are
  # actually private -- force it off rather than risk leaking a private path
  # to that checksum database.
  [[ "$output" == *"GOSUMDB=[off]"* ]]

  # An explicit repo-set GOSUMDB really is being overridden here, unlike the
  # unset-GOSUMDB default case above -- that's worth a WARNING.
  [[ "$output" == *"==> WARNING: overriding explicit GOSUMDB=sum.mycorp.example with GOSUMDB=off"* ]]
}

@test "GOSUMDB is left alone when GONOSUMDB exempts paths alongside an explicit GOSUMDB (issue #2857)" {
  export REGISTRY_PROXY_SOCKET_PATH="$BATS_TEST_TMPDIR/registry-proxy.sock"
  export REGISTRY_PROXY_FORWARDER_PORT=27185
  export GONOSUMDB="corp.example/*"
  export GOSUMDB="sum.mycorp.example"

  socat "UNIX-LISTEN:$REGISTRY_PROXY_SOCKET_PATH,fork,reuseaddr" EXEC:true &
  _test_socat_pid=$!
  _wait_for_socket "$REGISTRY_PROXY_SOCKET_PATH"

  run bash "$(_go_binding_harness)"
  [ "$status" -eq 0 ]
  # Repo declared its own exemption via GONOSUMDB, so its explicit GOSUMDB
  # choice is honored rather than overridden.
  [[ "$output" == *"GOSUMDB=[sum.mycorp.example]"* ]]

  # GOSUMDB was never actually touched in this branch, so no WARNING either.
  [[ "$output" != *"overriding explicit GOSUMDB"* ]]

  # GOSUMDB=[sum.mycorp.example] alone can't distinguish "phase_go_binding ran
  # and left it alone" from "phase_go_binding never ran at all" (the test
  # itself exported that value) -- assert the phase actually bound GOPROXY
  # too, proving it did run.
  [[ "$output" == *"go bound to it via GOPROXY"* ]]
}

@test "GONOSUMDB is left alone when already set, and GOSUMDB is not forced in that case (issue #2857)" {
  export REGISTRY_PROXY_SOCKET_PATH="$BATS_TEST_TMPDIR/registry-proxy.sock"
  export REGISTRY_PROXY_FORWARDER_PORT=27185
  export GONOSUMDB="corp.example/*"

  socat "UNIX-LISTEN:$REGISTRY_PROXY_SOCKET_PATH,fork,reuseaddr" EXEC:true &
  _test_socat_pid=$!
  _wait_for_socket "$REGISTRY_PROXY_SOCKET_PATH"

  run bash "$(_go_binding_harness)"
  [ "$status" -eq 0 ]
  [[ "$output" == *"GONOSUMDB=[corp.example/*]"* ]]
  [[ "$output" == *"GOSUMDB=[unset]"* ]]

  # Both assertions above hold trivially even if phase_go_binding never ran
  # (the test itself exported GONOSUMDB, and GOSUMDB was never set) -- assert
  # the phase actually bound GOPROXY too, proving it did run.
  [[ "$output" == *"go bound to it via GOPROXY"* ]]
}

@test "GOTOOLCHAIN is pinned to local, skipping the forced-sumdb toolchain-download path (issue #2857)" {
  export REGISTRY_PROXY_SOCKET_PATH="$BATS_TEST_TMPDIR/registry-proxy.sock"
  export REGISTRY_PROXY_FORWARDER_PORT=27185

  socat "UNIX-LISTEN:$REGISTRY_PROXY_SOCKET_PATH,fork,reuseaddr" EXEC:true &
  _test_socat_pid=$!
  _wait_for_socket "$REGISTRY_PROXY_SOCKET_PATH"

  run bash "$(_go_binding_harness)"
  [ "$status" -eq 0 ]
  # useSumDB forces a checksum-database lookup for golang.org/toolchain
  # "even if GOSUMDB=off" whenever GOTOOLCHAIN=auto (the default) picks a
  # newer toolchain than the one baked into this Box -- pinning
  # GOTOOLCHAIN=local is what actually prevents that lookup, not GOSUMDB.
  [[ "$output" == *"GOTOOLCHAIN=[local]"* ]]
  [[ "$output" == *"GOTOOLCHAIN_RESOLVED=[local]"* ]]

  # GOTOOLCHAIN was unset, so pinning it to local isn't overriding anything
  # -- no WARNING expected.
  [[ "$output" != *"overriding GOTOOLCHAIN"* ]]
}

@test "WARNING logged when a pre-existing non-local GOTOOLCHAIN is overridden with local (issue #2857)" {
  export REGISTRY_PROXY_SOCKET_PATH="$BATS_TEST_TMPDIR/registry-proxy.sock"
  export REGISTRY_PROXY_FORWARDER_PORT=27185
  export GOTOOLCHAIN="go1.21.0"

  socat "UNIX-LISTEN:$REGISTRY_PROXY_SOCKET_PATH,fork,reuseaddr" EXEC:true &
  _test_socat_pid=$!
  _wait_for_socket "$REGISTRY_PROXY_SOCKET_PATH"

  run bash "$(_go_binding_harness)"
  [ "$status" -eq 0 ]
  [[ "$output" == *"GOTOOLCHAIN=[local]"* ]]
  [[ "$output" == *"==> WARNING: overriding GOTOOLCHAIN=go1.21.0 with GOTOOLCHAIN=local"* ]]
}

@test "socket present but Forwarder never ready: phase_go_binding no-ops (issue #2857)" {
  export REGISTRY_PROXY_SOCKET_PATH="$BATS_TEST_TMPDIR/registry-proxy.sock"
  export REGISTRY_PROXY_FORWARDER_PORT=27186

  # The stand-in socket only needs to exist for phase_go_binding's own
  # `[ -S "$REGISTRY_PROXY_SOCKET_PATH" ]` check to pass -- it's created here,
  # with the outer shell's real socat, before socat is hidden below. Nothing
  # needs to connect to it: with socat hidden from the harness's PATH,
  # phase_registry_proxy_forwarder's `command -v socat` guard fires first and
  # returns early, before the readiness poll that would otherwise dial it.
  socat "UNIX-LISTEN:$REGISTRY_PROXY_SOCKET_PATH,fork,reuseaddr" EXEC:true &
  _test_socat_pid=$!
  _wait_for_socket "$REGISTRY_PROXY_SOCKET_PATH"

  # Hide every PATH directory that holds a socat binary from the harness
  # subprocess, not a fresh minimal PATH -- the harness still needs
  # go/bash/coreutils on PATH for its own `go env` calls and to execute at
  # all. This makes phase_registry_proxy_forwarder's `command -v socat` guard
  # fail as its very first check, so it warns and returns before ever
  # setting _registry_proxy_forwarder_ready -- the one path none of the
  # tests above exercise, since they all leave a real socat on PATH and let
  # the Forwarder actually start. Filtering every match, not just the first
  # `command -v socat` hit, matters here: this sandbox's own PATH carries
  # socat in more than one directory (e.g. a nix store path and /bin), so
  # stripping only the first would leave the guard's `command -v` still
  # resolving it via the second.
  # IFS=: is scoped to this command substitution's own subshell, not the
  # test body, so it never leaks into the `run bash "$(_go_binding_harness)"`
  # call below.
  local _path_without_socat
  _path_without_socat="$(
    IFS=:
    local _out="" _p
    for _p in $PATH; do
      [ -x "$_p/socat" ] && continue
      _out="${_out:+$_out:}$_p"
    done
    printf '%s' "$_out"
  )"

  PATH="$_path_without_socat" run bash "$(_go_binding_harness)"
  [ "$status" -eq 0 ]

  [[ "$output" == *"socat is not on PATH"* ]]
  [[ "$output" == *"GOPROXY=[unset]"* ]]
  [[ "$output" == *"GOSUMDB=[unset]"* ]]
}

@test "socket absent: phase is a silent no-op (issue #2857)" {
  export REGISTRY_PROXY_SOCKET_PATH="$BATS_TEST_TMPDIR/registry-proxy.sock"
  export REGISTRY_PROXY_FORWARDER_PORT=27185

  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]

  [[ "$output" != *"go bound to it via GOPROXY"* ]]
}

# Unlike every test above, which drives the harness to inspect env vars that
# die with the subshell, this exercises main()'s own `phase_go_binding` call
# line via a full `run bash "$ENTRYPOINT"` (mirrors
# entrypoint-registry-proxy-forwarder.bats' equivalent test) -- the phase's
# success log line is the only externally observable effect once the process
# exits, so it's the one signal that can catch main() itself failing to wire
# the phase in.
@test "socket present: entrypoint's own main() invokes phase_go_binding (issue #2857)" {
  export REGISTRY_PROXY_SOCKET_PATH="$BATS_TEST_TMPDIR/registry-proxy.sock"
  export REGISTRY_PROXY_FORWARDER_PORT=27187

  socat "UNIX-LISTEN:$REGISTRY_PROXY_SOCKET_PATH,fork,reuseaddr" EXEC:true &
  _test_socat_pid=$!
  _wait_for_socket "$REGISTRY_PROXY_SOCKET_PATH"

  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]

  [[ "$output" == *"go bound to it via GOPROXY"* ]]
}
