# Branch hygiene and exit-code contract for the dogfood loop. The loop resets to
# the base branch before `git pull --ff-only` because a host left on a feature
# branch has no upstream to fast-forward. The launcher's exit code drives
# termination, not a separate gh probe: exit 2 (queue empty) breaks the loop
# cleanly; any other non-zero exit aborts with an error.

load helper

# Replaces the fake nix with one that exits $1 on `nix run .# -- $2` calls ($2 is
# the dispatch kind, defaulting to "dispatch") and exits 0 on every other nix call.
_install_exit_code_nix() {
  local code="$1"
  local kind="${2:-dispatch}"
  local shebang
  shebang="$(head -n1 "$FAKE_BIN/nix")"
  {
    printf '%s\n' "$shebang"
    cat <<EOF
: "\${NIX_LOG:?NIX_LOG must point at a log file}"
printf '%s\n' "\$*" >>"\$NIX_LOG"
if printf '%s ' "\$@" | grep -q -- '-- $kind'; then
  exit $code
fi
exit 0
EOF
  } >"$FAKE_BIN/nix.tmp"
  mv "$FAKE_BIN/nix.tmp" "$FAKE_BIN/nix"
  chmod +x "$FAKE_BIN/nix"
}

# Exits each code arg in order on successive `nix run .# -- $kind` calls, re-exiting
# the last code once the list runs out, and exits 0 on every other nix call. $kind
# defaults to "dispatch"; override it with a trailing non-numeric arg, e.g.
# `_install_sequence_exit_nix 4 2 research`.
_install_sequence_exit_nix() {
  local args=("$@")
  local kind="dispatch"
  if [ "${#args[@]}" -gt 0 ] && [[ ! "${args[-1]}" =~ ^-?[0-9]+$ ]]; then
    kind="${args[-1]}"
    unset 'args[-1]'
  fi
  local codes=("${args[@]}")
  local counter="$BATS_TEST_TMPDIR/dispatch-call-count"
  echo 0 >"$counter"
  local shebang
  shebang="$(head -n1 "$FAKE_BIN/nix")"
  {
    printf '%s\n' "$shebang"
    cat <<EOF
: "\${NIX_LOG:?NIX_LOG must point at a log file}"
printf '%s\n' "\$*" >>"\$NIX_LOG"
if printf '%s ' "\$@" | grep -q -- '-- $kind'; then
  codes=(${codes[*]})
  n=\$(cat "$counter")
  last=\$(( \${#codes[@]} - 1 ))
  [ "\$n" -gt "\$last" ] && n="\$last"
  echo \$((n + 1)) >"$counter"
  exit "\${codes[\$n]}"
fi
exit 0
EOF
  } >"$FAKE_BIN/nix.tmp"
  mv "$FAKE_BIN/nix.tmp" "$FAKE_BIN/nix"
  chmod +x "$FAKE_BIN/nix"
}

# Fakes `uname -s` to report $1 so DOGFOOD_RUNTIME tests pass whichever OS bats
# runs on (#2672 review finding). Reuses the fake nix's already-rewritten shebang
# rather than a hardcoded one, because the nix sandboxed build has no /usr/bin/env.
_fake_uname() {
  local os="$1"
  local shebang
  shebang="$(head -n1 "$FAKE_BIN/nix")"
  {
    printf '%s\n' "$shebang"
    printf 'echo %s\n' "$os"
  } >"$FAKE_BIN/uname"
  chmod +x "$FAKE_BIN/uname"
}

setup() {
  setup_fakes

  export HOME="$BATS_TEST_TMPDIR/home"
  mkdir -p "$HOME"
  git config --global init.defaultBranch main
  git config --global user.name "Dogfood Test"
  git config --global user.email "dogfood@example.com"

  local remote="$BATS_TEST_TMPDIR/remote.git"
  git init --bare -q "$remote"

  export WORK="$BATS_TEST_TMPDIR/work"
  git clone -q "$remote" "$WORK"
  # The nix check injects DOGFOOD_SH because it copies only tests/ into the
  # sandbox; locally fall back to the repo-root script beside tests/.
  cp "${DOGFOOD_SH:-$BATS_TEST_DIRNAME/../dogfood.sh}" "$WORK/dogfood.sh"
  printf 'harness.env\n' >"$WORK/.gitignore"
  printf 'REPO_SLUG=owner/repo\n' >"$WORK/harness.env"
  git -C "$WORK" add dogfood.sh .gitignore
  git -C "$WORK" commit -q -m "seed"
  git -C "$WORK" push -q origin HEAD:main
  git -C "$WORK" branch --set-upstream-to=origin/main main

  # Land the host on a feature branch with no upstream, the state that broke a
  # bare `git pull --ff-only`.
  git -C "$WORK" checkout -q -b feat/leftover

  # Default nix exits 2 on `nix run .# -- dispatch` so tests terminate after one cycle.
  _install_exit_code_nix 2
  export DETACHED_PID_FILE="$BATS_TEST_TMPDIR/detached.pid"
}

# The Ctrl-C test's detached child is deliberately spared by `abort`, so the
# suite owns its lifetime. KILL, not TERM: a TERM would trip the child's own
# marker trap and outlive the test that asserts that marker's absence.
teardown() {
  if [ -f "$DETACHED_PID_FILE" ]; then
    kill -KILL "$(cat "$DETACHED_PID_FILE")" 2>/dev/null
  fi
  true
}

@test "dogfood resets to the base branch before pulling" {
  run env BASE_BRANCH=main bash "$WORK/dogfood.sh"
  [ "$status" -eq 0 ]
  [ "$(git -C "$WORK" rev-parse --abbrev-ref HEAD)" = "main" ]
}

@test "dogfood exits cleanly when launcher exits 2 (queue empty)" {
  _install_exit_code_nix 2
  run env BASE_BRANCH=main bash "$WORK/dogfood.sh"
  [ "$status" -eq 0 ]
  [[ "$output" == *"done"* ]]
}

@test "dogfood aborts when launcher exits non-zero (not 2)" {
  _install_exit_code_nix 1
  run bash -c "BASE_BRANCH=main bash '$WORK/dogfood.sh' 2>&1"
  [ "$status" -ne 0 ]
  printf '%s\n' "$output" | grep -q "launcher failed"
}

@test "dogfood does not pin --max-jobs 1" {
  run env BASE_BRANCH=main bash "$WORK/dogfood.sh"
  [ "$status" -eq 0 ]
  ! grep -q -- '--max-jobs 1 ' "$NIX_LOG"
}

@test "dogfood pulls, rebuilds, and re-invokes when launcher exits 4 (image stale)" {
  _install_sequence_exit_nix 4 2
  run env BASE_BRANCH=main bash "$WORK/dogfood.sh"
  [ "$status" -eq 0 ]
  [[ "$output" == *"image stale"* ]]
  [ "$(grep -c -- '-- dispatch' "$NIX_LOG")" -eq 2 ]
}

@test "dogfood halts cleanly with a diagnostic when launcher exits 5 (host-tainted)" {
  _install_exit_code_nix 5
  run env BASE_BRANCH=main bash "$WORK/dogfood.sh"
  [ "$status" -eq 0 ]
  [[ "$output" == *"non-converging (host-tainted)"* ]]
  # The loop halts on exit 5 instead of rebuilding and retrying the way exit 4
  # does, so it calls dispatch exactly once.
  [ "$(grep -c -- '-- dispatch' "$NIX_LOG")" -eq 1 ]
}

@test "dogfood stops cleanly when launcher exits 7 (signalled stop)" {
  _install_exit_code_nix 7
  run env BASE_BRANCH=main bash "$WORK/dogfood.sh"
  [ "$status" -eq 0 ]
  [[ "$output" == *"stopped on request"* ]]
  # The loop stops on exit 7 instead of rebuilding and retrying the way exit 4
  # does, so it calls dispatch exactly once.
  [ "$(grep -c -- '-- dispatch' "$NIX_LOG")" -eq 1 ]
}

@test "dogfood forwards SIGTERM to the in-flight launcher on stop request" {
  # Reproduces the operator gesture end to end: dogfood-stop sends USR1 to the
  # loop's own pid (read from the pid file, same as the devShell alias would),
  # and the loop must forward SIGTERM to the backgrounded launcher rather than
  # only latching stop_requested for later.
  local marker="$BATS_TEST_TMPDIR/forwarded"
  local shebang
  shebang="$(head -n1 "$FAKE_BIN/nix")"
  {
    printf '%s\n' "$shebang"
    cat <<EOF
: "\${NIX_LOG:?NIX_LOG must point at a log file}"
printf '%s\n' "\$*" >>"\$NIX_LOG"
if printf '%s ' "\$@" | grep -q -- '-- dispatch'; then
  trap 'printf forwarded >"$marker"; exit 7' TERM
  pid=\$(cat .spindrift/dogfood.pid 2>/dev/null)
  kill -USR1 "\$pid"
  for _ in \$(seq 1 50); do
    sleep 0.05
  done
  exit 9
fi
exit 0
EOF
  } >"$FAKE_BIN/nix.tmp"
  mv "$FAKE_BIN/nix.tmp" "$FAKE_BIN/nix"
  chmod +x "$FAKE_BIN/nix"

  run env BASE_BRANCH=main bash "$WORK/dogfood.sh"
  [ "$status" -eq 0 ]
  [ -f "$marker" ]
  [[ "$output" == *"stopped on request"* ]]
  [ "$(grep -c -- '-- dispatch' "$NIX_LOG")" -eq 1 ]
}

@test "dogfood does not forward SIGTERM when continuous dispatch is opted out" {
  # With CONTINUOUS_DISPATCH= the launcher never installs its stop handler
  # (installStopSignal, cmd/launcher/main.go), so a forwarded SIGTERM would
  # take Go's default disposition and kill it mid-wave. The operator gesture
  # must instead leave the launcher alone to finish the wave it is draining.
  # The fake stands in for that disposition by dying 143 on a TERM it should
  # never receive.
  local marker="$BATS_TEST_TMPDIR/signalled"
  local shebang
  shebang="$(head -n1 "$FAKE_BIN/nix")"
  {
    printf '%s\n' "$shebang"
    cat <<EOF
: "\${NIX_LOG:?NIX_LOG must point at a log file}"
printf '%s\n' "\$*" >>"\$NIX_LOG"
if printf '%s ' "\$@" | grep -q -- '-- dispatch'; then
  trap 'printf signalled >"$marker"; exit 143' TERM
  pid=\$(cat .spindrift/dogfood.pid 2>/dev/null)
  kill -USR1 "\$pid"
  for _ in \$(seq 1 10); do
    sleep 0.05
  done
  exit 0
fi
exit 0
EOF
  } >"$FAKE_BIN/nix.tmp"
  mv "$FAKE_BIN/nix.tmp" "$FAKE_BIN/nix"
  chmod +x "$FAKE_BIN/nix"

  run env BASE_BRANCH=main CONTINUOUS_DISPATCH= bash "$WORK/dogfood.sh"
  [ "$status" -eq 0 ]
  [ ! -f "$marker" ]
  [[ "$output" == *"graceful stop"* ]]
  [ "$(grep -c -- '-- dispatch' "$NIX_LOG")" -eq 1 ]
}

@test "dogfood's Ctrl-C abort reaches the launcher's same-group children" {
  # A foreground Ctrl-C used to reach the launcher's children as well as the
  # launcher — and it has to, since `podman run` is started without `--rm`
  # (cmd/launcher/internal/runner/oci.go), so a client left alive leaves a container
  # behind. Backgrounding the launcher took that away twice over: the signal
  # is aimed at one pid, and bash's job-control-off SIG_IGN for SIGINT is
  # inherited tree-wide. `abort` restores the blast radius by TERMing the
  # descendants that share this loop's process group, while sparing one in a
  # group of its own (NixRealizer's Setpgid'd background `nix build`,
  # docs/reference.md). The fake plays all three parts, and stands in for the
  # terminal by signalling the loop's own pid, read from the pid file.
  local launcher_marker="$BATS_TEST_TMPDIR/launcher-hup"
  local grouped_marker="$BATS_TEST_TMPDIR/grouped-termed"
  local detached_marker="$BATS_TEST_TMPDIR/detached-termed"
  local shebang
  shebang="$(head -n1 "$FAKE_BIN/nix")"
  {
    printf '%s\n' "$shebang"
    cat <<EOF
: "\${NIX_LOG:?NIX_LOG must point at a log file}"
printf '%s\n' "\$*" >>"\$NIX_LOG"
if printf '%s ' "\$@" | grep -q -- '-- dispatch'; then
  trap 'printf hup >"$launcher_marker"; exit 130' HUP
  # Inherits this fake's process group, like a podman client: must be TERMed.
  # The redirects keep it off the stdout \`run\` reads to EOF, and off bats's
  # own TAP fd 3, either of which a lingering child would hold open.
  (
    trap 'printf termed >"$grouped_marker"; exit 0' TERM
    while :; do sleep 0.05; done
  ) </dev/null >/dev/null 2>&1 3>&- &
  # \`set -m\` gives this one a process group of its own: must be spared.
  set -m
  (
    trap 'printf termed >"$detached_marker"; exit 0' TERM
    while :; do sleep 0.05; done
  ) </dev/null >/dev/null 2>&1 3>&- &
  printf '%s\n' \$! >"$DETACHED_PID_FILE"
  set +m
  kill -INT "\$(cat .spindrift/dogfood.pid)"
  while :; do sleep 0.05; done
fi
exit 0
EOF
  } >"$FAKE_BIN/nix.tmp"
  mv "$FAKE_BIN/nix.tmp" "$FAKE_BIN/nix"
  chmod +x "$FAKE_BIN/nix"

  run env BASE_BRANCH=main bash "$WORK/dogfood.sh"
  [ "$status" -eq 130 ]
  # The loop exits without waiting for anything, so every marker lands after
  # `run` returns. Wait out the two that should land before judging the one
  # that should not, or the negative assertion would only mean "too early".
  local i
  for ((i = 0; i < 200; i++)); do
    [ -f "$launcher_marker" ] && [ -f "$grouped_marker" ] && break
    sleep 0.05
  done
  [ -f "$launcher_marker" ]
  [ -f "$grouped_marker" ]
  sleep 1
  [ ! -f "$detached_marker" ]
}

@test "dogfood leaves the launcher's stdin attached to the operator's terminal" {
  # SPINDRIFT_GH_TOKEN_CMD unlock prompts only reach the operator while the
  # launcher's stdin is the real terminal (isInteractiveTTY, cmd/launcher/flags.go),
  # so backgrounding must not silently swap it for /dev/null. Reading from a
  # regular file (below) only proves the explicit `<&0` redirect; a file can
  # never raise SIGTTIN the way a real pty read would, so it cannot by itself
  # prove the launcher stays out of a background process group. The pgid
  # comparison is what guards that: without job control the launcher shares
  # the loop's own (foreground) process group, so a vault-unlock prompt's tty
  # read is a legal foreground read rather than one that raises SIGTTIN.
  local marker="$BATS_TEST_TMPDIR/launcher-stdin"
  local pgid_marker="$BATS_TEST_TMPDIR/launcher-pgid-verdict"
  local stdin_file="$BATS_TEST_TMPDIR/operator-stdin"
  printf 'operator-typed-this\n' >"$stdin_file"
  local shebang
  shebang="$(head -n1 "$FAKE_BIN/nix")"
  {
    printf '%s\n' "$shebang"
    cat <<EOF
: "\${NIX_LOG:?NIX_LOG must point at a log file}"
printf '%s\n' "\$*" >>"\$NIX_LOG"
if printf '%s ' "\$@" | grep -q -- '-- dispatch'; then
  loop_pgid=\$(ps -o pgid= -p "\$(cat .spindrift/dogfood.pid)" | tr -d '[:space:]')
  own_pgid=\$(ps -o pgid= -p \$\$ | tr -d '[:space:]')
  if [ "\$own_pgid" = "\$loop_pgid" ]; then
    printf 'same' >"$pgid_marker"
  else
    printf 'different' >"$pgid_marker"
  fi
  if IFS= read -r line; then
    printf '%s' "\$line" >"$marker"
  else
    printf 'EOF-ON-DEV-NULL' >"$marker"
  fi
  exit 2
fi
exit 0
EOF
  } >"$FAKE_BIN/nix.tmp"
  mv "$FAKE_BIN/nix.tmp" "$FAKE_BIN/nix"
  chmod +x "$FAKE_BIN/nix"

  run env BASE_BRANCH=main bash "$WORK/dogfood.sh" <"$stdin_file"
  [ "$status" -eq 0 ]
  [ "$(cat "$marker")" = "operator-typed-this" ]
  [ "$(cat "$pgid_marker")" = "same" ]
}

@test "dogfood latches stop_requested and breaks the loop when a stop signal arrives between iterations" {
  # Regression guard: a launcher that ignores the forwarded TERM (or a signal
  # that lands after it already exited on its own) must still stop the loop
  # via the stop_requested backstop rather than spinning to another wave.
  local shebang
  shebang="$(head -n1 "$FAKE_BIN/nix")"
  {
    printf '%s\n' "$shebang"
    cat <<'EOF'
: "${NIX_LOG:?NIX_LOG must point at a log file}"
printf '%s\n' "$*" >>"$NIX_LOG"
if printf '%s ' "$@" | grep -q -- '-- dispatch'; then
  trap '' TERM
  kill -USR1 "$PPID"
  sleep 0.05
  exit 0
fi
exit 0
EOF
  } >"$FAKE_BIN/nix.tmp"
  mv "$FAKE_BIN/nix.tmp" "$FAKE_BIN/nix"
  chmod +x "$FAKE_BIN/nix"

  run env BASE_BRANCH=main bash "$WORK/dogfood.sh"
  [ "$status" -eq 0 ]
  [[ "$output" == *"graceful stop"* ]]
  [ "$(grep -c -- '-- dispatch' "$NIX_LOG")" -eq 1 ]
}

@test "dogfood terminates cleanly with triage message when launcher exits 3" {
  _install_exit_code_nix 3
  run env BASE_BRANCH=main bash "$WORK/dogfood.sh"
  [ "$status" -eq 0 ]
  [[ "$output" == *"none are dispatchable"* ]]
}

@test "dogfood pulls, rebuilds, and retries once when exit 3's pull advances HEAD" {
  local remote_url
  remote_url="$(git -C "$WORK" remote get-url origin)"
  local other="$BATS_TEST_TMPDIR/other"
  git clone -q "$remote_url" "$other"
  git -C "$other" config user.name "Other Agent"
  git -C "$other" config user.email "other@example.com"

  local counter="$BATS_TEST_TMPDIR/dispatch-call-count"
  echo 0 >"$counter"
  local shebang
  shebang="$(head -n1 "$FAKE_BIN/nix")"
  {
    printf '%s\n' "$shebang"
    cat <<EOF
: "\${NIX_LOG:?NIX_LOG must point at a log file}"
printf '%s\n' "\$*" >>"\$NIX_LOG"
if printf '%s ' "\$@" | grep -q -- '-- dispatch'; then
  n=\$(cat "$counter")
  echo \$((n + 1)) >"$counter"
  if [ "\$n" -eq 0 ]; then
    echo landed >"$other/landed.txt"
    git -C "$other" add landed.txt
    git -C "$other" commit -q -m landed
    git -C "$other" push -q origin HEAD:main
    exit 3
  fi
  exit 2
fi
exit 0
EOF
  } >"$FAKE_BIN/nix.tmp"
  mv "$FAKE_BIN/nix.tmp" "$FAKE_BIN/nix"
  chmod +x "$FAKE_BIN/nix"

  run env BASE_BRANCH=main bash "$WORK/dogfood.sh"
  [ "$status" -eq 0 ]
  [ "$(grep -c -- '-- dispatch' "$NIX_LOG")" -eq 2 ]
  [ -f "$WORK/landed.txt" ]
}

@test "dogfood passes --max-jobs defaulting to MAX_PARALLEL" {
  run env BASE_BRANCH=main MAX_PARALLEL=5 bash "$WORK/dogfood.sh"
  [ "$status" -eq 0 ]
  grep -q -- '--max-jobs 5' "$NIX_LOG"
}

@test "dogfood passes --continuous-dispatch" {
  run env BASE_BRANCH=main bash "$WORK/dogfood.sh"
  [ "$status" -eq 0 ]
  grep -q -- '--continuous-dispatch=1' "$NIX_LOG"
}

@test "dogfood runs dispatch by default" {
  run timeout 15 env BASE_BRANCH=main bash "$WORK/dogfood.sh"
  [ "$status" -eq 0 ]
  grep -q -- '-- dispatch' "$NIX_LOG"
  ! grep -q -- '-- research' "$NIX_LOG"
}

@test "DOGFOOD_KIND=research runs research instead of dispatch" {
  _install_exit_code_nix 2 research

  run timeout 15 env BASE_BRANCH=main DOGFOOD_KIND=research bash "$WORK/dogfood.sh"
  [ "$status" -eq 0 ]
  grep -q -- '-- research' "$NIX_LOG"
  ! grep -q -- '-- dispatch' "$NIX_LOG"
}

@test "dogfood pulls, rebuilds, and re-invokes under DOGFOOD_KIND=research" {
  _install_sequence_exit_nix 4 2 research
  run timeout 15 env BASE_BRANCH=main DOGFOOD_KIND=research bash "$WORK/dogfood.sh"
  [ "$status" -eq 0 ]
  [[ "$output" == *"image stale"* ]]
  [ "$(grep -c -- '-- research' "$NIX_LOG")" -eq 2 ]
}

@test "DOGFOOD_RUNTIME unset targets the default (podman) flake app" {
  run timeout 15 env BASE_BRANCH=main bash "$WORK/dogfood.sh"
  [ "$status" -eq 0 ]
  grep -qF -- 'run .# --' "$NIX_LOG"
  ! grep -qF -- '.#dogfood-bwrap' "$NIX_LOG"
}

@test "DOGFOOD_RUNTIME=podman explicitly targets the default flake app" {
  run timeout 15 env BASE_BRANCH=main DOGFOOD_RUNTIME=podman bash "$WORK/dogfood.sh"
  [ "$status" -eq 0 ]
  grep -qF -- 'run .# --' "$NIX_LOG"
  ! grep -qF -- '.#dogfood-bwrap' "$NIX_LOG"
}

@test "DOGFOOD_RUNTIME=bwrap targets the dogfood-bwrap flake app" {
  # Fake Linux so dogfood.sh does not reject DOGFOOD_RUNTIME=bwrap when this
  # test runs on a real macOS dev host (#2672 review finding).
  _fake_uname Linux
  run timeout 15 env BASE_BRANCH=main DOGFOOD_RUNTIME=bwrap bash "$WORK/dogfood.sh"
  [ "$status" -eq 0 ]
  grep -qF -- 'run .#dogfood-bwrap --' "$NIX_LOG"
  ! grep -qF -- 'run .# --' "$NIX_LOG"
}

@test "DOGFOOD_RUNTIME=bogus fails fast before any nix run" {
  run env BASE_BRANCH=main DOGFOOD_RUNTIME=bogus bash "$WORK/dogfood.sh"
  [ "$status" -eq 1 ]
  [[ "$output" == *"DOGFOOD_RUNTIME must be 'podman' or 'bwrap', got: bogus"* ]]
  # setup_fakes truncates NIX_LOG, so an empty log proves the script exited
  # before its first `nix run` call, which comes well after this check.
  [ ! -s "$NIX_LOG" ]
}

@test "DOGFOOD_RUNTIME=bwrap fails fast on a faked-Darwin host before any nix run" {
  # bubblewrap is Linux-only, so this preflight must reject bwrap on macOS with a
  # clear message instead of failing opaquely inside the launcher (#2672 review
  # finding).
  _fake_uname Darwin

  run env BASE_BRANCH=main DOGFOOD_RUNTIME=bwrap bash "$WORK/dogfood.sh"
  [ "$status" -eq 1 ]
  [[ "$output" == *"DOGFOOD_RUNTIME=bwrap requires Linux"* ]]
  # Empty NIX_LOG proves the script exited before its first `nix run` call,
  # same proof pattern as the bogus-value test above.
  [ ! -s "$NIX_LOG" ]
}

@test "dogfood aborts when podman machine RAM is below MEMORY_LIMIT" {
  export FAKE_PODMAN_MACHINE_MEMORY_MIB=2048
  run env BASE_BRANCH=main MEMORY_LIMIT=4g bash "$WORK/dogfood.sh"
  [ "$status" -ne 0 ]
  [[ "$output" == *"2048"* ]]
  [[ "$output" == *"4g"* ]]
  [[ "$output" == *"podman machine set --memory"* ]]
}

@test "dogfood skips the podman memory preflight when DOGFOOD_RUNTIME=bwrap" {
  # Same RAM and MEMORY_LIMIT as the case above that aborts under the default
  # podman runtime: bwrap is daemonless and has no VM, so this preflight must not
  # gate it (#2672). Fake Linux for the same reason as the bwrap-app test above.
  _fake_uname Linux
  export FAKE_PODMAN_MACHINE_MEMORY_MIB=2048
  run env BASE_BRANCH=main MEMORY_LIMIT=4g DOGFOOD_RUNTIME=bwrap bash "$WORK/dogfood.sh"
  [ "$status" -eq 0 ]
  [[ "$output" != *"podman machine set --memory"* ]]
}

@test "dogfood proceeds when podman machine RAM meets MEMORY_LIMIT" {
  export FAKE_PODMAN_MACHINE_MEMORY_MIB=4608
  run env BASE_BRANCH=main MEMORY_LIMIT=4g MAX_PARALLEL=1 bash "$WORK/dogfood.sh"
  [ "$status" -eq 0 ]
  [[ "$output" != *"podman machine set --memory"* ]]
}

@test "dogfood aborts when podman machine RAM fits one container but not MAX_PARALLEL of them" {
  export FAKE_PODMAN_MACHINE_MEMORY_MIB=8192
  run env BASE_BRANCH=main MEMORY_LIMIT=4g MAX_PARALLEL=3 bash "$WORK/dogfood.sh"
  [ "$status" -ne 0 ]
  [[ "$output" == *"8192"* ]]
  [[ "$output" == *"MAX_PARALLEL=3"* ]]
  [[ "$output" == *"lower MAX_PARALLEL"* ]]
}

@test "dogfood proceeds when podman machine RAM meets MEMORY_LIMIT times MAX_PARALLEL" {
  export FAKE_PODMAN_MACHINE_MEMORY_MIB=16384
  run env BASE_BRANCH=main MEMORY_LIMIT=4g MAX_PARALLEL=3 bash "$WORK/dogfood.sh"
  [ "$status" -eq 0 ]
  [[ "$output" != *"podman machine set --memory"* ]]
}

@test "dogfood skips the memory preflight when no podman machine exists" {
  run env BASE_BRANCH=main MEMORY_LIMIT=4g bash "$WORK/dogfood.sh"
  [ "$status" -eq 0 ]
  [[ "$output" != *"podman machine set --memory"* ]]
}

@test "dogfood skips the memory preflight when MEMORY_LIMIT is explicitly disabled" {
  export FAKE_PODMAN_MACHINE_MEMORY_MIB=2048
  run env BASE_BRANCH=main MEMORY_LIMIT= bash "$WORK/dogfood.sh"
  [ "$status" -eq 0 ]
  [[ "$output" != *"podman machine set --memory"* ]]
}

@test "dogfood defaults MEMORY_LIMIT to 5g when unset" {
  export FAKE_PODMAN_MACHINE_MEMORY_MIB=4096
  run env BASE_BRANCH=main MAX_PARALLEL=1 bash "$WORK/dogfood.sh"
  [ "$status" -ne 0 ]
  [[ "$output" == *"MEMORY_LIMIT=5g"* ]]
}

@test "dogfood aborts with a clear message when MAX_PARALLEL is non-numeric" {
  run env BASE_BRANCH=main MAX_PARALLEL=garbage bash "$WORK/dogfood.sh"
  [ "$status" -ne 0 ]
  [[ "$output" == *"MAX_PARALLEL"* ]]
  [[ "$output" == *"non-negative integer"* ]]
  [[ "$output" != *"unbound variable"* ]]
  [[ "$output" != *"arithmetic syntax error"* ]]
}

@test "dogfood aborts with a clear message when MAX_PARALLEL has a leading zero" {
  run env BASE_BRANCH=main MAX_PARALLEL=08 bash "$WORK/dogfood.sh"
  [ "$status" -ne 0 ]
  [[ "$output" == *"MAX_PARALLEL"* ]]
  [[ "$output" != *"unbound variable"* ]]
  [[ "$output" != *"value too great for base"* ]]
}

@test "dogfood treats empty MAX_PARALLEL same as unset, defaulting to 3" {
  run env BASE_BRANCH=main MAX_PARALLEL="" bash "$WORK/dogfood.sh"
  [ "$status" -eq 0 ]
  grep -q -- '--max-jobs 3' "$NIX_LOG"
}

@test "dogfood aborts with a clear message when MAX_PARALLEL is a decimal" {
  run env BASE_BRANCH=main MAX_PARALLEL=3.5 bash "$WORK/dogfood.sh"
  [ "$status" -ne 0 ]
  [[ "$output" == *"MAX_PARALLEL"* ]]
  [[ "$output" != *"unbound variable"* ]]
  [[ "$output" != *"arithmetic syntax error"* ]]
}

@test "dogfood aborts with a clear message when MAX_PARALLEL is negative" {
  run env BASE_BRANCH=main MAX_PARALLEL=-1 bash "$WORK/dogfood.sh"
  [ "$status" -ne 0 ]
  [[ "$output" == *"MAX_PARALLEL"* ]]
  [[ "$output" != *"unbound variable"* ]]
  [[ "$output" != *"arithmetic syntax error"* ]]
}

@test "dogfood writes the pidfile under .spindrift/ and cleans it up on exit" {
  local probe="$BATS_TEST_TMPDIR/pidfile-probe"
  local shebang
  shebang="$(head -n1 "$FAKE_BIN/nix")"
  {
    printf '%s\n' "$shebang"
    cat <<EOF
: "\${NIX_LOG:?NIX_LOG must point at a log file}"
printf '%s\n' "\$*" >>"\$NIX_LOG"
if printf '%s ' "\$@" | grep -q -- '-- dispatch'; then
  if [ -f "$WORK/.spindrift/dogfood.pid" ]; then
    echo spindrift=yes >>"$probe"
  else
    echo spindrift=no >>"$probe"
  fi
  if [ -f "$WORK/.dogfood.pid" ]; then
    echo top=yes >>"$probe"
  else
    echo top=no >>"$probe"
  fi
  exit 2
fi
exit 0
EOF
  } >"$FAKE_BIN/nix.tmp"
  mv "$FAKE_BIN/nix.tmp" "$FAKE_BIN/nix"
  chmod +x "$FAKE_BIN/nix"

  run env BASE_BRANCH=main bash "$WORK/dogfood.sh"
  [ "$status" -eq 0 ]
  grep -q "^spindrift=yes$" "$probe"
  grep -q "^top=no$" "$probe"
  [ ! -f "$WORK/.spindrift/dogfood.pid" ]
  [ ! -f "$WORK/.dogfood.pid" ]
}

