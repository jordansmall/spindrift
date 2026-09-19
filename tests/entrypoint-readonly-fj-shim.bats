#!/usr/bin/env bats
# A read-only Box shims `fj` ahead of the real one on PATH to reject write
# subcommands (issue #2509). The `gh` and `fj` shims share one $HOME-derived
# dir, because install_readonly_guards installs every command-shim row in a
# single readonly-guards pass regardless of argv0.

# tests/helper.bash's setup_fakes puts no fake `fj` on $FAKE_BIN, because
# lib/image.nix bakes the real forgejo-cli in only for a forgejo-backend
# Consumer. Every test installs its own fake first so that
# `driver-exec readonly-guards`'s real-binary resolution succeeds.

load helper

setup() {
  setup_entrypoint_env
  # driver-exec readonly-guards needs a real "fj" binary to shim in front of,
  # and the log lets a passthrough test prove the shim execs through to it.
  FJ_LOG="$BATS_TEST_TMPDIR/fj.log"
  export FJ_LOG
  : >"$FJ_LOG"
  {
    printf '#!%s\n' "$(command -v bash)"
    echo 'printf '"'"'%s\n'"'"' "$*" >>"$FJ_LOG"'
    echo 'exit 0'
  } >"$FAKE_BIN/fj"
  chmod +x "$FAKE_BIN/fj"
}

@test "read-only Box installs the fj shim in the same shim dir as gh" {
  unset BOX_WRITE_ENABLED # issue #2465/#2509: read-only Box
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]

  # One pass installs every command-shim row, so fj lands in the gh-named dir.
  [ -d "$HOME/.spindrift/readonly-gh-shim" ]
  [ -x "$HOME/.spindrift/readonly-gh-shim/fj" ]
}

@test "read-only Box's fj shim rejects fj pr create" {
  unset BOX_WRITE_ENABLED # issue #2465/#2509: read-only Box
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]

  # The entrypoint mutates PATH only inside its own subprocess, so reproduce
  # production's PATH ordering by prepending the same shim dir here.
  local shim_dir
  shim_dir="$HOME/.spindrift/readonly-gh-shim"
  [ -d "$shim_dir" ]

  # Assert only the stable "fj pr create" substring: the full wording lives in
  # lib/prompt-contract.nix's forbiddenMarkers registry (issue #2509), which
  # is free to reword it.
  PATH="$shim_dir:$PATH" run fj pr create --title "x" --body "y"
  [ "$status" -ne 0 ]
  [[ "$output" == *"fj pr create"* ]]
}

@test "read-only Box's fj shim rejects fj pr ready" {
  unset BOX_WRITE_ENABLED # issue #2465/#2509: read-only Box
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]

  local shim_dir
  shim_dir="$HOME/.spindrift/readonly-gh-shim"
  [ -d "$shim_dir" ]

  PATH="$shim_dir:$PATH" run fj pr ready 1
  [ "$status" -ne 0 ]
  [[ "$output" == *"fj pr ready"* ]]
}

@test "read-only Box's fj shim rejects fj pr merge" {
  unset BOX_WRITE_ENABLED # issue #2465/#2509: read-only Box
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]

  local shim_dir
  shim_dir="$HOME/.spindrift/readonly-gh-shim"
  [ -d "$shim_dir" ]

  PATH="$shim_dir:$PATH" run fj pr merge 1
  [ "$status" -ne 0 ]
  [[ "$output" == *"fj pr merge"* ]]
}

@test "read-only Box's fj shim rejects fj issue comment" {
  unset BOX_WRITE_ENABLED # issue #2465/#2509: read-only Box
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]

  local shim_dir
  shim_dir="$HOME/.spindrift/readonly-gh-shim"
  [ -d "$shim_dir" ]

  PATH="$shim_dir:$PATH" run fj issue comment 1 --body "hi"
  [ "$status" -ne 0 ]
  [[ "$output" == *"fj issue comment"* ]]
}

@test "read-only Box's fj shim rejects fj issue create" {
  unset BOX_WRITE_ENABLED # issue #2465/#2509: read-only Box
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]

  local shim_dir
  shim_dir="$HOME/.spindrift/readonly-gh-shim"
  [ -d "$shim_dir" ]

  PATH="$shim_dir:$PATH" run fj issue create --title "x" --body "y"
  [ "$status" -ne 0 ]
  [[ "$output" == *"fj issue create"* ]]
}

@test "read-only Box's fj shim passes through unguarded subcommands untouched" {
  unset BOX_WRITE_ENABLED # issue #2465/#2509: read-only Box
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]

  local shim_dir
  shim_dir="$HOME/.spindrift/readonly-gh-shim"
  [ -d "$shim_dir" ]

  PATH="$shim_dir:$PATH" run fj pr list
  [ "$status" -eq 0 ]
  grep -qF "pr list" "$FJ_LOG"

  PATH="$shim_dir:$PATH" run fj issue view 1
  [ "$status" -eq 0 ]
  grep -qF "issue view 1" "$FJ_LOG"
}

@test "read-write Box installs no fj shim" {
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  [ ! -e "$HOME/.spindrift/readonly-gh-shim" ]

  run fj pr create --title "x" --body "y"
  [ "$status" -eq 0 ]
  grep -qF "pr create --title x --body y" "$FJ_LOG"
}
