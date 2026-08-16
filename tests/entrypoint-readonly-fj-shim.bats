#!/usr/bin/env bats
# Read-only Box puts an `fj` (forgejo-cli) shim ahead of the real `fj` on
# PATH that rejects write subcommands (issue #2509 flipped `fj`'s five
# forbiddenMarkers rows from enforce="prompt-only" to "command-shim",
# alongside the pre-existing `gh` rows): `fj pr create`, `fj pr ready`, `fj
# pr merge`, `fj issue comment`, and `fj issue create`. Reads pass through
# untouched; a read-write Box gets no shim at all. Mirrors
# entrypoint-readonly-gh-shim.bats's structure -- both `gh` and `fj` shims
# install into the same $HOME-derived shim dir (agent/entrypoint.sh's
# install_readonly_guards installs every command-shim row the registry names
# in one driver-exec readonly-guards pass, regardless of argv0), so this
# suite reuses that same directory name rather than inventing its own.
#
# The real `fj` binary (forgejo-cli) is baked into the image only for a
# forgejo-backend Consumer (lib/image.nix's forgejoBackend knob) -- unlike
# `gh`, tests/helper.bash's setup_fakes puts no fake `fj` on $FAKE_BIN by
# default, so every test here installs its own minimal fake first (mirrors
# tests/entrypoint-clone.bats's per-test fj fake), letting
# `driver-exec readonly-guards`'s real-binary resolution succeed the same
# way it would on a forgejo-backend Box.

load helper

setup() {
  setup_entrypoint_env
  # A minimal fake fj: records every invocation to $FJ_LOG and exits 0,
  # exactly enough for driver-exec readonly-guards to resolve a real "fj"
  # binary to shim in front of, and for a passthrough assertion to prove the
  # shim's exec-through reaches it.
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

  # install_readonly_guards installs every command-shim row in one pass, so
  # both argv0 groups land under the same $HOME-derived directory.
  [ -d "$HOME/.spindrift/readonly-gh-shim" ]
  [ -x "$HOME/.spindrift/readonly-gh-shim/fj" ]
}

@test "read-only Box's fj shim rejects fj pr create" {
  unset BOX_WRITE_ENABLED # issue #2465/#2509: read-only Box
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]

  # The shim's PATH mutation is local to the entrypoint subprocess and does
  # not survive back to this shell -- reproduce production's PATH ordering
  # by prepending the same deterministic shim dir the entrypoint installs
  # to, mirroring entrypoint-readonly-gh-shim.bats.
  local shim_dir
  shim_dir="$HOME/.spindrift/readonly-gh-shim"
  [ -d "$shim_dir" ]

  # The rejection's wording lives in lib/prompt-contract.nix's
  # forbiddenMarkers registry (issue #2509) -- assert the stable "fj pr
  # create" substring the row's own message always names, not its full
  # prose, which the registry is free to reword.
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
