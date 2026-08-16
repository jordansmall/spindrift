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

@test "read-only Box installs the gh shim under \$HOME, never \$WORK_DIR's parent" {
  unset BOX_WRITE_ENABLED # issue #2465: read-only Box
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]

  [ -d "$HOME/.spindrift/readonly-gh-shim" ]
  [ -x "$HOME/.spindrift/readonly-gh-shim/gh" ]

  # Regression guard: production WORK_DIR is /work, so a $WORK_DIR-derived
  # location resolves to `/` -- root-owned, and the Box runs as uid 1000
  # (lib/image.nix), so the install `mkdir` fails and `set -e` kills the Box
  # mid-clone. This suite's own $WORK_DIR sits in a writable tmpdir, which is
  # exactly what hid the failure, so assert the parent stays untouched rather
  # than trusting the tmpdir to fail the way `/` does.
  [ ! -e "$(dirname "$WORK_DIR")/readonly-gh-shim" ]
}

@test "read-only Box installs no fj shim alongside the gh shim" {
  unset BOX_WRITE_ENABLED # issue #2465: read-only Box
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]

  # lib/prompt-contract.nix's five fj rows stay enforce=="prompt-only"
  # (issue #2509 Finding 1): driver-exec's readonly-guards verb can render a
  # real fj shim, but install_readonly_guards's own gating condition
  # (BOX_HOST_MEDIATED_REMOTE or BOX_OUTBOX_RELAY_CAPABLE) never fires for a
  # real forgejo-backend dispatch, so no fj shim ever reaches production --
  # this suite's own env (CODE_FORGE-default, github) is the one combination
  # that can never co-occur with an fj binary in the first place. Assert the
  # gh shim install this suite already exercises never installs an fj
  # artifact alongside it, even though the registry's command-shim rows are
  # read and grouped generically by argv0.
  [ -d "$HOME/.spindrift/readonly-gh-shim" ]
  [ ! -e "$HOME/.spindrift/readonly-gh-shim/fj" ]
}

@test "read-only Box's gh shim rejects gh pr create, naming the PR-intent relay" {
  unset BOX_WRITE_ENABLED # issue #2465: read-only Box
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]

  # The shim's PATH mutation is local to the entrypoint subprocess and does not
  # survive back to this shell -- reproduce production's PATH ordering (shim in
  # front of the fake `gh` setup_fakes already put on $FAKE_BIN) by prepending
  # the same deterministic, $HOME-derived shim dir the entrypoint installs to.
  local shim_dir
  shim_dir="$HOME/.spindrift/readonly-gh-shim"
  [ -d "$shim_dir" ]

  # The rejection's wording lives in lib/prompt-contract.nix's
  # forbiddenMarkers registry (issue #2509), rendered verbatim into the
  # installed shim by driver-exec readonly-guards -- assert the stable "gh
  # pr create" substring the row's own message always names, plus the
  # relay-naming text (SPINDRIFT_PR_INTENT) that distinguishes this row from
  # a bare boilerplate rejection.
  PATH="$shim_dir:$PATH" run gh pr create --title "x" --body "y"
  [ "$status" -ne 0 ]
  [[ "$output" == *"gh pr create"* ]]
  [[ "$output" == *"SPINDRIFT_PR_INTENT"* ]]
}

@test "read-only Box's gh shim rejects gh pr ready" {
  unset BOX_WRITE_ENABLED # issue #2465: read-only Box
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]

  local shim_dir
  shim_dir="$HOME/.spindrift/readonly-gh-shim"
  [ -d "$shim_dir" ]

  PATH="$shim_dir:$PATH" run gh pr ready 1
  [ "$status" -ne 0 ]
  [[ "$output" == *"gh pr ready"* ]]
  [[ "$output" == *"launcher"* ]]
}

@test "read-only Box's gh shim rejects gh pr merge" {
  unset BOX_WRITE_ENABLED # issue #2465: read-only Box
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]

  local shim_dir
  shim_dir="$HOME/.spindrift/readonly-gh-shim"
  [ -d "$shim_dir" ]

  PATH="$shim_dir:$PATH" run gh pr merge 1
  [ "$status" -ne 0 ]
  [[ "$output" == *"gh pr merge"* ]]
  [[ "$output" == *"launcher"* ]]
}

@test "read-only Box's gh shim rejects gh issue comment, naming the note= relay" {
  unset BOX_WRITE_ENABLED # issue #2465: read-only Box
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]

  local shim_dir
  shim_dir="$HOME/.spindrift/readonly-gh-shim"
  [ -d "$shim_dir" ]

  PATH="$shim_dir:$PATH" run gh issue comment 1 --body "hi"
  [ "$status" -ne 0 ]
  [[ "$output" == *"gh issue comment"* ]]
  [[ "$output" == *"note="* ]]
}

@test "read-only Box's gh shim rejects gh issue create, naming the issue-intent relay" {
  unset BOX_WRITE_ENABLED # issue #2465: read-only Box
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]

  local shim_dir
  shim_dir="$HOME/.spindrift/readonly-gh-shim"
  [ -d "$shim_dir" ]

  PATH="$shim_dir:$PATH" run gh issue create --title "x" --body "y"
  [ "$status" -ne 0 ]
  [[ "$output" == *"gh issue create"* ]]
  [[ "$output" == *"SPINDRIFT_ISSUE_INTENT"* ]]
}

@test "read-only Box's gh shim rejects gh api with a mutating method (-X POST)" {
  unset BOX_WRITE_ENABLED # issue #2465: read-only Box
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]

  local shim_dir
  shim_dir="$HOME/.spindrift/readonly-gh-shim"
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
  shim_dir="$HOME/.spindrift/readonly-gh-shim"
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
  shim_dir="$HOME/.spindrift/readonly-gh-shim"
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
  shim_dir="$HOME/.spindrift/readonly-gh-shim"
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
  shim_dir="$HOME/.spindrift/readonly-gh-shim"
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
  [ ! -e "$HOME/.spindrift/readonly-gh-shim" ]

  run gh pr create --title "x" --body "y"
  [ "$status" -eq 0 ]
  grep -qF "pr create --title x --body y" "$GH_LOG"
}
