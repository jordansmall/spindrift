#!/usr/bin/env bats
# Issue #5861: eval-profile-top.sh renders a folded-stacks Nix eval profile as
# a "where did eval time go" Markdown report.

setup() {
  EVAL_PROFILE_TOP_SH="${EVAL_PROFILE_TOP_SH:-$BATS_TEST_DIRNAME/../.github/scripts/eval-profile-top.sh}"
  FIXTURE="$BATS_TEST_DIRNAME/testdata/eval-profile-top/profile.folded"
}

report() {
  run bash "$EVAL_PROFILE_TOP_SH" "$FIXTURE" /fake/root
  [ "$status" -eq 0 ]
}

@test "no arguments prints usage to stderr and exits 2" {
  run bash "$EVAL_PROFILE_TOP_SH"
  [ "$status" -eq 2 ]
  [[ "$output" == *usage:* ]]
}

@test "more than two arguments is a usage error" {
  run bash "$EVAL_PROFILE_TOP_SH" "$FIXTURE" /fake/root extra
  [ "$status" -eq 2 ]
  [[ "$output" == *usage:* ]]
}

@test "a missing input prints a clear message and exits 0" {
  run bash "$EVAL_PROFILE_TOP_SH" "$BATS_TEST_TMPDIR/absent.folded"
  [ "$status" -eq 0 ]
  [ "$output" = "No eval profile samples: $BATS_TEST_TMPDIR/absent.folded is missing or has no samples." ]
}

@test "an empty input prints a clear message and exits 0" {
  : >"$BATS_TEST_TMPDIR/empty.folded"
  run bash "$EVAL_PROFILE_TOP_SH" "$BATS_TEST_TMPDIR/empty.folded"
  [ "$status" -eq 0 ]
  [ "$output" = "No eval profile samples: $BATS_TEST_TMPDIR/empty.folded is missing or has no samples." ]
}

@test "a blank-only input counts as empty" {
  printf '\n\n' >"$BATS_TEST_TMPDIR/blank.folded"
  run bash "$EVAL_PROFILE_TOP_SH" "$BATS_TEST_TMPDIR/blank.folded"
  [ "$status" -eq 0 ]
  [[ "$output" == "No eval profile samples:"* ]]
}

@test "a line whose last field is not a count gets the same no-samples message" {
  printf 'x9\n' >"$BATS_TEST_TMPDIR/x9.folded"
  run bash "$EVAL_PROFILE_TOP_SH" "$BATS_TEST_TMPDIR/x9.folded"
  [ "$status" -eq 0 ]
  [ "$output" = "No eval profile samples: $BATS_TEST_TMPDIR/x9.folded is missing or has no samples." ]
}

@test "CRLF line endings do not hide the counts" {
  printf '/r/a.nix:3:1 4\r\n' >"$BATS_TEST_TMPDIR/crlf.folded"
  run bash "$EVAL_PROFILE_TOP_SH" "$BATS_TEST_TMPDIR/crlf.folded" /r
  [ "$status" -eq 0 ]
  [[ "$output" == *'| 1 | `a.nix:3` | 4 | 100.0% |'* ]]
}

@test "an empty leaf frame still ranks by its count" {
  printf '/r/a.nix:1:1 1\n/r/b.nix:2:2; 9\n' >"$BATS_TEST_TMPDIR/empty-leaf.folded"
  run bash "$EVAL_PROFILE_TOP_SH" "$BATS_TEST_TMPDIR/empty-leaf.folded" /r
  [ "$status" -eq 0 ]
  top=$(grep -E '^\| [0-9]+ \|' <<<"$output")
  [[ "$(sed -n 1p <<<"$top")" == '| 1 | `` | 9 | 90.0% |' ]]
}

@test "unlocked github refs keep the repo name, not the ref" {
  printf '«github:o/r/main»/lib/a.nix:3:1 4\n' >"$BATS_TEST_TMPDIR/ref.folded"
  run bash "$EVAL_PROFILE_TOP_SH" "$BATS_TEST_TMPDIR/ref.folded"
  [ "$status" -eq 0 ]
  [[ "$output" == *'`r/lib/a.nix:3`'* ]]
}

@test "path: store blocks drop the store hash" {
  printf '«path:/nix/store/0123456789abcdfghijklmnpqrsvwxyz-source?narHash=x»/a.nix:3:1 4\n' >"$BATS_TEST_TMPDIR/pathblk.folded"
  run bash "$EVAL_PROFILE_TOP_SH" "$BATS_TEST_TMPDIR/pathblk.folded"
  [ "$status" -eq 0 ]
  [[ "$output" == *'`source/a.nix:3`'* ]]
  [[ "$output" != *0123456789* ]]
}

@test "string and stdin pseudo-files keep their guillemets" {
  printf '«string»:1:1 3\n«stdin»:2:1 2\n' >"$BATS_TEST_TMPDIR/pseudo.folded"
  run bash "$EVAL_PROFILE_TOP_SH" "$BATS_TEST_TMPDIR/pseudo.folded"
  [ "$status" -eq 0 ]
  [[ "$output" == *'`«string»:1` | 3 |'* ]]
  [[ "$output" == *'| `«stdin»` | 2 |'* ]]
}

@test "reports the total over every stack line" {
  report
  [[ "$output" == *"Total samples: 39"* ]]
}

@test "only the leaf frame takes the self sample" {
  report
  # flake.nix:44:5 appears mid-stack with 9 samples below it, and as a leaf once.
  [[ "$output" == *'`flake.nix:44` | 1 | 2.6% |'* ]]
}

@test "two columns on one line aggregate into one row" {
  report
  [[ "$output" == *'| 1 | `nixpkgs/lib/attrsets.nix:1620` | 9 | 23.1% |'* ]]
}

@test "rows rank by count descending with ties broken by key" {
  report
  top=$(grep -E '^\| [0-9]+ \|' <<<"$output")
  [[ "$(sed -n 2p <<<"$top")" == '| 2 | `nixpkgs.lib/lib/modules.nix:404 (primop seq)` | 6 | 15.4% |' ]]
  [[ "$(sed -n 3p <<<"$top")" == '| 3 | `nix/checks/default.nix:87` | 2 | 5.1% |' ]]
  [[ "$(sed -n 4p <<<"$top")" == '| 4 | `nix/checks/default.nix:87 (primop import)` | 2 | 5.1% |' ]]
}

@test "the first table is cut at 15 rows" {
  report
  [ "$(grep -cE '^\| [0-9]+ \|' <<<"$output")" -eq 15 ]
  [[ "$output" != *'lib/f9.nix'* ]]
}

@test "a primop leaf keeps its own row beside the plain frame on that line" {
  report
  [[ "$output" == *'`nix/checks/default.nix:87 (primop import)` | 2 |'* ]]
  [[ "$output" == *'`nix/checks/default.nix:87` | 2 |'* ]]
}

@test "flake-input blocks shorten to the input name" {
  report
  [[ "$output" == *'`flakes-internal/call-flake.nix:5` | 1 |'* ]]
  [[ "$output" == *'`nixpkgs.lib/lib/modules.nix:404'* ]]
  [[ "$output" != *narHash* ]]
  [[ "$output" != *«github* && "$output" != *«flakes* ]]
}

@test "store paths lose their store hash" {
  report
  [[ "$output" == *'`source/pkgs/top-level/all-packages.nix:10` | 2 |'* ]]
  [[ "$output" != */nix/store* ]]
}

@test "repo frames lose the repo root" {
  report
  [[ "$output" == *'`flake.nix:44`'* ]]
  [[ "$output" != */fake/root* ]]
}

@test "a frame with no position keeps its raw text" {
  report
  [[ "$output" == *'`«none»:0` | 2 |'* ]]
}

@test "the per-file table sums rows, primop rows counting toward their file" {
  report
  files=${output#*'## Self samples by file'}
  [[ "$files" == *'| `nixpkgs/lib/attrsets.nix` | 9 | 23.1% |'* ]]
  [[ "$files" == *'| `nixpkgs.lib/lib/modules.nix` | 8 | 20.5% |'* ]]
  [[ "$files" == *'| `nix/checks/default.nix` | 4 | 10.3% |'* ]]
}

@test "the repo root defaults to the working directory" {
  mkdir "$BATS_TEST_TMPDIR/repo"
  printf '%s/a.nix:3:1 4\n' "$BATS_TEST_TMPDIR/repo" >"$BATS_TEST_TMPDIR/p.folded"
  cd "$BATS_TEST_TMPDIR/repo"
  run bash "$EVAL_PROFILE_TOP_SH" "$BATS_TEST_TMPDIR/p.folded"
  [ "$status" -eq 0 ]
  [[ "$output" == *'| 1 | `a.nix:3` | 4 | 100.0% |'* ]]
}
