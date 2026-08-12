#!/usr/bin/env bats
# Read-only Box puts a `gh` shim ahead of the real `gh` on PATH that rejects
# write subcommands, each naming the relay that replaces it (#2465): `gh pr
# create`, `gh pr ready`, `gh pr merge`, `gh issue comment`, `gh issue
# create`, and `gh api` with a mutating method. Reads pass through untouched;
# a read-write Box gets no shim at all.

load helper

setup() {
  setup_entrypoint_env
}

@test "read-only Box's gh shim rejects gh pr create, naming the PR-intent relay" {
  unset BOX_WRITE_ENABLED # issue #2465: read-only Box
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]

  # The shim's PATH mutation is local to the entrypoint subprocess and does not
  # survive back to this shell -- reproduce production's PATH ordering (shim in
  # front of the fake `gh` setup_fakes already put on $FAKE_BIN) by prepending
  # the same deterministic, $WORK_DIR-derived shim dir the brief specifies.
  local shim_dir
  shim_dir="$(dirname "$WORK_DIR")/readonly-gh-shim"
  [ -d "$shim_dir" ]

  PATH="$shim_dir:$PATH" run gh pr create --title "x" --body "y"
  [ "$status" -ne 0 ]
  [[ "$output" == *"SPINDRIFT_PR_INTENT"* ]] || [[ "$output" == *"PR-intent"* ]]
}

@test "read-only Box's gh shim rejects gh pr ready" {
  unset BOX_WRITE_ENABLED # issue #2465: read-only Box
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]

  local shim_dir
  shim_dir="$(dirname "$WORK_DIR")/readonly-gh-shim"
  [ -d "$shim_dir" ]

  PATH="$shim_dir:$PATH" run gh pr ready 1
  [ "$status" -ne 0 ]
  [[ "$output" == *"launcher"* ]]
  [[ "$output" == *"gh pr ready"* ]]
}

@test "read-only Box's gh shim rejects gh pr merge" {
  unset BOX_WRITE_ENABLED # issue #2465: read-only Box
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]

  local shim_dir
  shim_dir="$(dirname "$WORK_DIR")/readonly-gh-shim"
  [ -d "$shim_dir" ]

  PATH="$shim_dir:$PATH" run gh pr merge 1
  [ "$status" -ne 0 ]
  [[ "$output" == *"launcher"* ]]
  [[ "$output" == *"gh pr merge"* ]]
}

@test "read-only Box's gh shim rejects gh issue comment, naming the note= relay" {
  unset BOX_WRITE_ENABLED # issue #2465: read-only Box
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]

  local shim_dir
  shim_dir="$(dirname "$WORK_DIR")/readonly-gh-shim"
  [ -d "$shim_dir" ]

  PATH="$shim_dir:$PATH" run gh issue comment 1 --body "hi"
  [ "$status" -ne 0 ]
  [[ "$output" == *"note="* ]]
}

@test "read-only Box's gh shim rejects gh issue create, naming the issue-intent relay" {
  unset BOX_WRITE_ENABLED # issue #2465: read-only Box
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]

  local shim_dir
  shim_dir="$(dirname "$WORK_DIR")/readonly-gh-shim"
  [ -d "$shim_dir" ]

  PATH="$shim_dir:$PATH" run gh issue create --title "x" --body "y"
  [ "$status" -ne 0 ]
  [[ "$output" == *"SPINDRIFT_ISSUE_INTENT"* ]] || [[ "$output" == *"issue-intent"* ]]
}

@test "read-only Box's gh shim rejects gh api with a mutating method (-X POST)" {
  unset BOX_WRITE_ENABLED # issue #2465: read-only Box
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]

  local shim_dir
  shim_dir="$(dirname "$WORK_DIR")/readonly-gh-shim"
  [ -d "$shim_dir" ]

  PATH="$shim_dir:$PATH" run gh api -X POST repos/owner/repo/issues/1/comments
  [ "$status" -ne 0 ]
  [[ "$output" == *"gh api"* ]]
}

@test "read-only Box's gh shim rejects gh api with a mutating method (-X post, lowercase)" {
  unset BOX_WRITE_ENABLED # issue #2465: read-only Box
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]

  local shim_dir
  shim_dir="$(dirname "$WORK_DIR")/readonly-gh-shim"
  [ -d "$shim_dir" ]

  PATH="$shim_dir:$PATH" run gh api -X post repos/owner/repo/issues/1/comments
  [ "$status" -ne 0 ]
  [[ "$output" == *"gh api"* ]]
}

@test "read-only Box's gh shim rejects gh api with a mutating method (--method PATCH)" {
  unset BOX_WRITE_ENABLED # issue #2465: read-only Box
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]

  local shim_dir
  shim_dir="$(dirname "$WORK_DIR")/readonly-gh-shim"
  [ -d "$shim_dir" ]

  PATH="$shim_dir:$PATH" run gh api --method PATCH repos/owner/repo/issues/1
  [ "$status" -ne 0 ]
  [[ "$output" == *"gh api"* ]]
}

@test "read-only Box's gh shim passes through gh api with no method flag (implicit GET)" {
  unset BOX_WRITE_ENABLED # issue #2465: read-only Box
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]

  local shim_dir
  shim_dir="$(dirname "$WORK_DIR")/readonly-gh-shim"
  [ -d "$shim_dir" ]

  PATH="$shim_dir:$PATH" run gh api repos/owner/repo/issues
  [ "$status" -eq 0 ]
  grep -qF "api repos/owner/repo/issues" "$GH_LOG"
}

@test "read-only Box's gh shim passes through read subcommands untouched" {
  unset BOX_WRITE_ENABLED # issue #2465: read-only Box
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]

  local shim_dir
  shim_dir="$(dirname "$WORK_DIR")/readonly-gh-shim"
  [ -d "$shim_dir" ]

  PATH="$shim_dir:$PATH" run gh issue view 1
  [ "$status" -eq 0 ]
  grep -qF "issue view 1" "$GH_LOG"

  PATH="$shim_dir:$PATH" run gh pr view 1
  [ "$status" -eq 0 ]
  grep -qF "pr view 1" "$GH_LOG"

  PATH="$shim_dir:$PATH" run gh run view 1
  [ "$status" -eq 0 ]
  grep -qF "run view 1" "$GH_LOG"

  PATH="$shim_dir:$PATH" run gh run list
  [ "$status" -eq 0 ]
  grep -qF "run list" "$GH_LOG"
}

@test "read-write Box installs no gh shim" {
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  [ ! -d "$(dirname "$WORK_DIR")/readonly-gh-shim" ]

  run gh pr create --title "x" --body "y"
  [ "$status" -eq 0 ]
  grep -qF "pr create --title x --body y" "$GH_LOG"
}
