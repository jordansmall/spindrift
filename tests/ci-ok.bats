#!/usr/bin/env bats
# Issue #5155: ci-ok.sh is the single required status check, so a wrong pass
# merges red or unchecked code.

setup() {
  CI_OK_SH="${CI_OK_SH:-$BATS_TEST_DIRNAME/../.github/scripts/ci-ok.sh}"
}

needs() {
  local changes=$1 check=$2 macos=$3 docs=$4
  printf '{"changes":{"result":"%s","outputs":{}},"check":{"result":"%s","outputs":{}},"check-macos":{"result":"%s","outputs":{}},"docs":{"result":"%s","outputs":{}}}' \
    "$changes" "$check" "$macos" "$docs"
}

@test "docs lane: heavy jobs skipped and the docs lane green passes" {
  run bash "$CI_OK_SH" changes docs < <(needs success skipped skipped success)
  [ "$status" -eq 0 ]
}

@test "code lane: heavy jobs green and the docs lane skipped passes" {
  run bash "$CI_OK_SH" changes check check-macos < <(needs success success success skipped)
  [ "$status" -eq 0 ]
}

@test "a failed heavy job fails" {
  run bash "$CI_OK_SH" changes check check-macos < <(needs success failure success skipped)
  [ "$status" -eq 1 ]
  [[ "$output" == *check* ]]
}

@test "a cancelled heavy job fails" {
  run bash "$CI_OK_SH" changes check check-macos < <(needs success success cancelled skipped)
  [ "$status" -eq 1 ]
  [[ "$output" == *check-macos* ]]
}

@test "a failed docs lane fails" {
  run bash "$CI_OK_SH" changes docs < <(needs success skipped skipped failure)
  [ "$status" -eq 1 ]
  [[ "$output" == *docs* ]]
}

@test "a failed job outside the required set still fails" {
  run bash "$CI_OK_SH" changes docs < <(needs success failure skipped success)
  [ "$status" -eq 1 ]
}

@test "a skipped required job fails, so an all-skipped run never passes" {
  run bash "$CI_OK_SH" changes docs < <(needs success skipped skipped skipped)
  [ "$status" -eq 1 ]
  [[ "$output" == *docs* ]]
}

@test "a failed classifier fails even though everything downstream skipped" {
  run bash "$CI_OK_SH" changes check check-macos < <(needs failure skipped skipped skipped)
  [ "$status" -eq 1 ]
  [[ "$output" == *changes* ]]
}

@test "a required job missing from needs fails" {
  run bash "$CI_OK_SH" changes typo < <(needs success skipped skipped success)
  [ "$status" -eq 1 ]
  [[ "$output" == *typo* ]]
}

@test "no required jobs is a usage error" {
  run bash "$CI_OK_SH" < <(needs success success success success)
  [ "$status" -eq 2 ]
}
