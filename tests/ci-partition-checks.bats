#!/usr/bin/env bats
# Issue #7523: .github/scripts/ci-partition-checks.sh splits checks.<system>
# into an image set and a main set by derivation closure. nix-eval-jobs and
# nix-store are PATH stubs: the former replays a fixture of eval rows, the
# latter walks a fixture drv graph (EDGES, one "drv dep" per line), so a check
# that reaches an image derivation only transitively is meaningful.

bats_require_minimum_version 1.5.0

# A realistic store path; the hash is unique per name (digits are valid base32).
drv() {
  local n
  n=$(printf '%s' "$1" | cksum | cut -d' ' -f1)
  printf '/nix/store/%032d-%s.drv' "$n" "$1"
}

# A PATH stub run by the interpreter running bats: a sandboxed build has no
# /usr/bin/env for an env shebang.
stub() { { printf "#!%s\n" "$BASH"; cat; } >"$STUBS/$1"; }

edge() { printf '%s %s\n' "$(drv "$1")" "$(drv "$2")" >>"$EDGES"; }

row() { printf '{"attr":"%s","attrPath":["%s"],"drvPath":"%s","outputs":{"out":"/nix/store/x-%s"}}\n' "$1" "$1" "$(drv "$1")" "$1" >>"$NEJ_JOBS"; }

setup() {
  CI_PARTITION_SH="${CI_PARTITION_SH:-$BATS_TEST_DIRNAME/../.github/scripts/ci-partition-checks.sh}"
  STUBS="$BATS_TEST_TMPDIR/stubs"
  mkdir -p "$STUBS"
  export NEJ_ARGV="$BATS_TEST_TMPDIR/nej.argv"
  export NEJ_JOBS="$BATS_TEST_TMPDIR/jobs.jsonl"
  export EDGES="$BATS_TEST_TMPDIR/edges"
  : >"$NEJ_JOBS"
  : >"$EDGES"
  PATH="$STUBS:$PATH"

  stub nix-eval-jobs <<'STUB'
printf '%s\n' "$*" >"$NEJ_ARGV"
cat "$NEJ_JOBS"
exit "${STUB_NEJ_STATUS:-0}"
STUB
  stub nix-store <<'STUB'
# --query --requisites: forward closure; --referrers-closure: reverse closure.
# Both include the arguments themselves, as the real nix-store does.
[ "$2" = "--referrers-closure" ] && { from=2; to=1; } || { from=1; to=2; }
shift 2
seen=$(printf '%s\n' "$@")
while :; do
  next=$(awk -v f="$from" -v t="$to" 'NR==FNR{s[$0];next} ($f in s){print $t}' <(printf '%s\n' "$seen") "$EDGES")
  new=$(printf '%s\n%s\n' "$seen" "$next" | sort -u | sed '/^$/d')
  [ "$new" = "$(printf '%s\n' "$seen" | sort -u)" ] && break
  seen=$new
done
printf '%s\n' "$seen" | sort -u
STUB
  chmod +x "$STUBS"/*

  # Checks are named for the set they belong to.
  # direct: check -> tarball -> stream script -> customisation layer.
  edge image-direct spindrift.tar.gz
  edge spindrift.tar.gz stream-spindrift
  edge stream-spindrift spindrift-customisation-layer
  # transitive: the check only reaches an image through another derivation.
  edge image-transitive harness-closure
  edge harness-closure other.tar.gz
  edge other.tar.gz stream-other
  edge stream-other other-customisation-layer
  edge plain-a coreutils
  edge plain-b coreutils
  # decoys: a fetched source tarball and a generic "stream" name, no layer.
  edge decoy-fetch foo-1.0.tar.gz
  edge decoy-stream gen-stream-helper
  for a in image-direct image-transitive plain-a plain-b decoy-fetch decoy-stream; do row "$a"; done
}

attrs() { printf '.#checks.x86_64-linux.%s\n' "$@"; }

@test "image set is exactly the checks that reach an image derivation" {
  run --separate-stderr bash "$CI_PARTITION_SH" x86_64-linux image
  [ "$status" -eq 0 ]
  [ "$output" = "$(attrs image-direct image-transitive)" ]
}

@test "main set is every other check, decoys included" {
  run --separate-stderr bash "$CI_PARTITION_SH" x86_64-linux main
  [ "$status" -eq 0 ]
  [ "$output" = "$(attrs decoy-fetch decoy-stream plain-a plain-b)" ]
}

@test "a check depending on an image only transitively lands in the image set" {
  run --separate-stderr bash "$CI_PARTITION_SH" x86_64-linux image
  [ "$status" -eq 0 ]
  [[ "$output" == *".#checks.x86_64-linux.image-transitive"* ]]
}

@test "the sets are disjoint and together cover every evaluated check" {
  bash "$CI_PARTITION_SH" x86_64-linux image >"$BATS_TEST_TMPDIR/image"
  bash "$CI_PARTITION_SH" x86_64-linux main >"$BATS_TEST_TMPDIR/main"
  [ -z "$(sort "$BATS_TEST_TMPDIR/image" "$BATS_TEST_TMPDIR/main" | uniq -d)" ]
  sort "$BATS_TEST_TMPDIR/image" "$BATS_TEST_TMPDIR/main" >"$BATS_TEST_TMPDIR/union"
  mapfile -t names < <(jq -r .attr "$NEJ_JOBS")
  attrs "${names[@]}" | sort >"$BATS_TEST_TMPDIR/all"
  [ -s "$BATS_TEST_TMPDIR/all" ]
  diff "$BATS_TEST_TMPDIR/union" "$BATS_TEST_TMPDIR/all"
}

@test "without image derivations the image set is empty and main holds every check" {
  : >"$EDGES"
  edge plain-a coreutils
  run --separate-stderr bash "$CI_PARTITION_SH" x86_64-linux image
  [ "$status" -eq 0 ]
  [ -z "$output" ]
  run --separate-stderr bash "$CI_PARTITION_SH" x86_64-linux main
  [ "$status" -eq 0 ]
  [ "$(wc -l <<<"$output")" -eq 6 ]
}

@test "an eval error row fails and names the attr" {
  printf '{"attr":"broken","attrPath":["broken"],"error":"undefined variable"}\n' >>"$NEJ_JOBS"
  run --separate-stderr bash "$CI_PARTITION_SH" x86_64-linux main
  [ "$status" -eq 1 ]
  [[ "$stderr" == *broken* ]]
  [[ "$stderr" == *"undefined variable"* ]]
  [ -z "$output" ]
}

@test "a failing nix-eval-jobs fails the script" {
  STUB_NEJ_STATUS=3 run bash "$CI_PARTITION_SH" x86_64-linux main
  [ "$status" -ne 0 ]
}

@test "nix-eval-jobs is pointed at the flake's checks for the system" {
  run bash "$CI_PARTITION_SH" aarch64-linux main github:o/r
  [ "$status" -eq 0 ]
  grep -qF -- '--flake github:o/r#checks.aarch64-linux' "$NEJ_ARGV"
  [[ "$output" == *"github:o/r#checks.aarch64-linux.plain-a"* ]]
}

@test "a backslash in the flake ref is printed verbatim" {
  run bash "$CI_PARTITION_SH" x86_64-linux main 'path:/tmp/a\tb'
  [ "$status" -eq 0 ]
  [[ "$output" == *'path:/tmp/a\tb#checks.x86_64-linux.plain-a'* ]]
}

@test "bad arguments print usage and exit 2" {
  run --separate-stderr bash "$CI_PARTITION_SH"
  [ "$status" -eq 2 ]
  [[ "$stderr" == *usage:* ]]
  run --separate-stderr bash "$CI_PARTITION_SH" x86_64-linux neither
  [ "$status" -eq 2 ]
  run --separate-stderr bash "$CI_PARTITION_SH" x86_64-linux main . extra
  [ "$status" -eq 2 ]
}
