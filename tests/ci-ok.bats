#!/usr/bin/env bats
# Issue #5155: ci-ok.sh is the single required status check, so a wrong pass
# merges red or unchecked code.

setup() {
  CI_OK_SH="${CI_OK_SH:-$BATS_TEST_DIRNAME/../.github/scripts/ci-ok.sh}"
}

needs() {
  local -A r=([changes]=skipped [check]=skipped [check-macos]=skipped [docs]=skipped [connector]=skipped)
  local a
  for a in "$@"; do r[${a%%=*}]=${a#*=}; done
  printf '{"changes":{"result":"%s","outputs":{}},"check":{"result":"%s","outputs":{}},"check-macos":{"result":"%s","outputs":{}},"docs":{"result":"%s","outputs":{}},"connector":{"result":"%s","outputs":{}}}' \
    "${r[changes]}" "${r[check]}" "${r[check-macos]}" "${r[docs]}" "${r[connector]}"
}

@test "docs lane: heavy jobs skipped and the docs lane green passes" {
  run bash "$CI_OK_SH" changes docs < <(needs changes=success docs=success)
  [ "$status" -eq 0 ]
}

@test "code lane: heavy jobs green and the docs lane skipped passes" {
  run bash "$CI_OK_SH" changes check check-macos < <(needs changes=success check=success check-macos=success)
  [ "$status" -eq 0 ]
}

@test "code lane: a skipped check-macos fails" {
  run bash "$CI_OK_SH" changes check check-macos < <(needs changes=success check=success)
  [ "$status" -eq 1 ]
  [[ "$output" == *check-macos* ]]
}

@test "connector lane: heavy jobs skipped and docs plus connector green passes" {
  run bash "$CI_OK_SH" changes docs connector < <(needs changes=success docs=success connector=success)
  [ "$status" -eq 0 ]
}

@test "connector lane: a skipped connector job fails" {
  run bash "$CI_OK_SH" changes docs connector < <(needs changes=success docs=success)
  [ "$status" -eq 1 ]
  [[ "$output" == *connector* ]]
}

@test "code lane plus a Connector: a failed connector job fails" {
  run bash "$CI_OK_SH" changes check connector < <(needs changes=success check=success connector=failure)
  [ "$status" -eq 1 ]
  [[ "$output" == *connector* ]]
}

@test "a failed heavy job fails" {
  run bash "$CI_OK_SH" changes check check-macos < <(needs changes=success check=failure check-macos=success)
  [ "$status" -eq 1 ]
  [[ "$output" == *check* ]]
}

@test "a cancelled heavy job fails" {
  run bash "$CI_OK_SH" changes check check-macos < <(needs changes=success check=success check-macos=cancelled)
  [ "$status" -eq 1 ]
  [[ "$output" == *check-macos* ]]
}

@test "a failed docs lane fails" {
  run bash "$CI_OK_SH" changes docs < <(needs changes=success docs=failure)
  [ "$status" -eq 1 ]
  [[ "$output" == *docs* ]]
}

@test "a failed job outside the required set still fails" {
  run bash "$CI_OK_SH" changes docs < <(needs changes=success check=failure docs=success)
  [ "$status" -eq 1 ]
}

@test "a skipped required job fails, so an all-skipped run never passes" {
  run bash "$CI_OK_SH" changes docs < <(needs changes=success)
  [ "$status" -eq 1 ]
  [[ "$output" == *docs* ]]
}

@test "a failed classifier fails even though everything downstream skipped" {
  run bash "$CI_OK_SH" changes check check-macos < <(needs changes=failure)
  [ "$status" -eq 1 ]
  [[ "$output" == *changes* ]]
}

@test "a required job missing from needs fails" {
  run bash "$CI_OK_SH" changes typo < <(needs changes=success docs=success)
  [ "$status" -eq 1 ]
  [[ "$output" == *typo* ]]
}

@test "no required jobs is a usage error" {
  run bash "$CI_OK_SH" < <(needs changes=success check=success check-macos=success docs=success)
  [ "$status" -eq 2 ]
}
