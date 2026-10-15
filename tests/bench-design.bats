#!/usr/bin/env bats
# bench/design.sh: the model/effort experiment design behind bench/arms and
# nix/ab-arms.nix. The balance properties are asserted on a freshly generated
# design.tsv, so a formula edit that breaks them fails here, not in a costly
# benchmark run. Model IDs are the design's own factor levels, not the
# default model: leave them literal when the default model is bumped.

design() { bash "${BENCH_DESIGN_SH:-$BATS_TEST_DIRNAME/../bench/design.sh}" "$@"; }

setup() {
  T="$BATS_TEST_TMPDIR"
  OUT="$T/out"
  design --root "$OUT"
  TSV="$OUT/bench/design.tsv"
  ARMS="$OUT/bench/arms"
  NIX="$OUT/nix/ab-arms.nix"
}

# Rows without the header.
rows() { tail -n +2 "$TSV"; }

# Prints "<cells> <min> <max>" for the cross of TSV columns $1 and $2.
cross() {
  rows | awk -F'\t' -v a="$1" -v b="$2" '
    { n[$a SUBSEP $b]++; la[$a]; lb[$b] }
    END {
      min = -1; max = 0; cells = 0
      for (x in la) for (y in lb) {
        c = n[x SUBSEP y] + 0; cells++
        if (min < 0 || c < min) min = c
        if (c > max) max = c
      }
      print cells, min, max
    }'
}

# Asserts every level pair of columns $1 x $2 occurs equally often.
assert_balanced() {
  local cells min max
  read -r cells min max < <(cross "$1" "$2")
  [ "$min" -gt 0 ] && [ "$min" -eq "$max" ] || {
    echo "columns $1 x $2: $cells cells, min=$min max=$max" >&2
    return 1
  }
}

@test "design.tsv has the header and 72 unique arms" {
  [ "$(head -1 "$TSV")" = "$(printf 'arm\tcoord_model\tcoord_effort\tscout_model\tscout_effort\tworker_model\tworker_effort\treviewer_model\treviewer_effort\timage')" ]
  [ "$(rows | wc -l)" -eq 72 ]
  [ "$(rows | cut -f1 | sort -u | wc -l)" -eq 72 ]
}

@test "arm and image names encode their columns" {
  bad=$(rows | awk -F'\t' '
    function m(id) { return id == "claude-opus-5-5" ? "o" : id == "claude-sonnet-5-5" ? "s" : id == "claude-haiku-5-5" ? "h" : "?" }
    function e(w) { return w == "low" ? "l" : w == "medium" ? "m" : w == "high" ? "h" : w == "xhigh" ? "x" : "?" }
    {
      img = "s" m($4) e($5) "-w" m($6) e($7) "-r" m($8) e($9)
      arm = "c" m($2) e($3) "-" img
      if ($1 != arm || $10 != img || arm ~ /\?/) print
    }')
  [ -z "$bad" ] || { echo "$bad" >&2; false; }
}

@test "rows follow the documented construction" {
  # c=s=w=r=b=0: k=0, every effort at its first level.
  grep -q "^com-sol-wol-rom	" "$TSV"
  # c=1 s=2 w=1 r=1 b=1: k=7, CE=1 WE=0 RE=2 SE=1.
  grep -q "^csh-shm-wsl-rsx	" "$TSV"
}

@test "model factors form a full factorial, each cell twice with every effort differing" {
  [ "$(rows | cut -f2,4,6,8 | sort -u | wc -l)" -eq 36 ]
  bad=$(rows | awk -F'\t' '
    { k = $2 SUBSEP $4 SUBSEP $6 SUBSEP $8; n[k]++; e[k, n[k]] = $3 SUBSEP $5 SUBSEP $7 SUBSEP $9 }
    END {
      for (k in n) {
        if (n[k] != 2) { print "count", n[k]; continue }
        split(e[k, 1], a, SUBSEP); split(e[k, 2], b, SUBSEP)
        for (i = 1; i <= 4; i++) if (a[i] == b[i]) print "shared effort", i
      }
    }')
  [ -z "$bad" ] || { echo "$bad" >&2; false; }
}

@test "every effort factor is balanced against every model factor" {
  # Includes the same-role crosses: 2x3 coord, 4x5 scout, 6x7 worker, 8x9 reviewer.
  for e in 3 5 7 9; do
    for m in 2 4 6 8; do
      assert_balanced "$m" "$e"
    done
  done
}

@test "effort pairs are balanced, CE x RE within 1.5x" {
  assert_balanced 3 5
  assert_balanced 3 7
  assert_balanced 5 7
  assert_balanced 5 9
  assert_balanced 7 9
  read -r cells min max < <(cross 3 9)
  [ "$min" -gt 0 ]
  [ $((2 * max)) -le $((3 * min)) ] || { echo "CE x RE: min=$min max=$max" >&2; false; }
}

@test "arms file keeps its comment header and lists every arm in design order" {
  grep -q '^# bench.sh arms: <name> <flake app> \[dispatch flags...\]' "$ARMS"
  [ "$(grep -vc '^#' "$ARMS")" -eq 72 ]
  diff <(grep -v '^#' "$ARMS") <(rows | awk -F'\t' '{ print $1, ".#ab-" $10, "--model", $2, "--effort", $3 }')
}

@test "ab-arms.nix has one entry per distinct image, review-axis tracking the reviewer" {
  head -1 "$NIX" | grep -q '^#'
  grep -q 'bench/design.sh' "$NIX"
  # Flatten each entry to the TSV's image-column shape.
  got=$(awk '
    /^  "[^"]+" = \{$/ { split($0, q, "\""); img = q[2]; next }
    /^    (models|efforts) = \{$/ { sec = $1; next }
    /^      / {
      key = $1; gsub(/"/, "", key)
      val = $3; gsub(/[";]/, "", val)
      v[img, sec, key] = val; imgs[img]
    }
    END {
      for (i in imgs) {
        if (v[i, "models", "review-axis"] != v[i, "models", "reviewer"] ||
            v[i, "efforts", "review-axis"] != v[i, "efforts", "reviewer"]) print "review-axis drift", i
        print v[i, "models", "scout"], v[i, "efforts", "scout"], v[i, "models", "worker"], v[i, "efforts", "worker"], v[i, "models", "reviewer"], v[i, "efforts", "reviewer"], i
      }
    }' "$NIX" | sort)
  want=$(rows | awk -F'\t' '{ print $4, $5, $6, $7, $8, $9, $10 }' | sort -u)
  diff <(echo "$want") <(echo "$got")
  [ "$(echo "$got" | wc -l)" -eq "$(rows | cut -f10 | sort -u | wc -l)" ]
}

@test "every arm's app names an ab-arms.nix entry" {
  missing=$(grep -v '^#' "$ARMS" | awk '{ sub(/^\.#ab-/, "", $2); print $2 }' | sort -u |
    while read -r img; do grep -q "^  \"$img\" = {$" "$NIX" || echo "$img"; done)
  [ -z "$missing" ] || { echo "no entry: $missing" >&2; false; }
}

@test "generation is deterministic" {
  design --root "$T/again"
  diff -r "$OUT" "$T/again"
}

@test "--check passes on fresh output" {
  run design --root "$OUT" --check
  [ "$status" -eq 0 ]
}

@test "--check fails and names the file that drifted" {
  echo "stray 1" >>"$ARMS"
  run design --root "$OUT" --check
  [ "$status" -ne 0 ]
  [[ "$output" == *bench/arms* ]]
  [[ "$output" == *stray* ]]
}

@test "--check fails when a file is missing" {
  rm "$NIX"
  run design --root "$OUT" --check
  [ "$status" -ne 0 ]
  [[ "$output" == *nix/ab-arms.nix* ]]
}

@test "the committed design is up to date" {
  run design --root "${BENCH_DESIGN_ROOT:-$BATS_TEST_DIRNAME/..}" --check
  echo "$output"
  [ "$status" -eq 0 ]
}
