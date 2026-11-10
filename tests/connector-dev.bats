#!/usr/bin/env bats
# Issue #6769: nix/connector-dev.sh drives the caller's nix from inside a
# Connector subflake with `core` overridden to the checkout and no lock write.
# `nix` is a PATH stub that records its cwd and argv (one arg per line); only
# the script's own decisions (resolution, flag placement) are under test.

bats_require_minimum_version 1.5.0

setup() {
  SCRIPT="${CONNECTOR_DEV_SH:-$BATS_TEST_DIRNAME/../nix/connector-dev.sh}"
  STUBS="$BATS_TEST_TMPDIR/stubs"
  ROOT="$BATS_TEST_TMPDIR/root"
  mkdir -p "$STUBS" "$ROOT/core" "$ROOT/connectors/alpha" "$ROOT/connectors/beta"
  export NIX_LOG="$BATS_TEST_TMPDIR/nix.log"
  # Unquoted heredoc so the shebang is this bash, not a /usr/bin/env lookup.
  # Like real nix, the stub writes a lock unless told not to.
  cat >"$STUBS/nix" <<STUB
#!$BASH
{ pwd -P; printf '%s\n' "\$@"; } >"$NIX_LOG"
case " \$* " in *" --no-write-lock-file "*) ;; *) echo '{"written":1}' >flake.lock ;; esac
STUB
  chmod +x "$STUBS/nix"
  export PATH="$STUBS:$PATH"
  echo '{}' >"$ROOT/core/flake.nix"
  echo '{}' >"$ROOT/connectors/alpha/flake.nix"
  echo '{"lock":1}' >"$ROOT/connectors/alpha/flake.lock"
  echo '{}' >"$ROOT/connectors/beta/flake.nix"
  git -C "$ROOT" init -q
  ROOT="$(cd "$ROOT" && pwd -P)"
  OVERRIDE="--override-input
core
git+file://$ROOT?dir=core
--no-write-lock-file"
}

# assert_nix_call <dir> <argv...>: the script succeeded and nix saw exactly this
# cwd and argv; the token OVERRIDE stands for the four wrapper-injected words.
assert_nix_call() {
  [ "$status" -eq 0 ]
  local expected="$1" a
  shift
  for a in "$@"; do
    if [ "$a" = OVERRIDE ]; then a="$OVERRIDE"; fi
    expected+=$'\n'"$a"
  done
  [ "$(cat "$NIX_LOG")" = "$expected" ]
}

@test "bare name runs nix in the Connector dir with core overridden" {
  cd "$ROOT"
  run bash "$SCRIPT" alpha flake check -L
  assert_nix_call "$ROOT/connectors/alpha" flake check -L OVERRIDE
}

@test "a directory argument resolves as a path" {
  cd "$ROOT/core"
  run bash "$SCRIPT" ../connectors/beta build .#x
  assert_nix_call "$ROOT/connectors/beta" build .#x OVERRIDE
}

@test "a bare name is never a cwd-relative path" {
  cd "$ROOT"
  run -1 bash "$SCRIPT" core flake check
  [[ "$output" == *"no Connector 'core'"* ]]
  [ ! -e "$NIX_LOG" ]
}

@test "program args after -- stay after it" {
  cd "$ROOT"
  run bash "$SCRIPT" alpha run .#x -- a b
  assert_nix_call "$ROOT/connectors/alpha" run .#x OVERRIDE -- a b
}

@test "-c keeps its command words after the override" {
  cd "$ROOT"
  run bash "$SCRIPT" alpha develop -c bash -c 'make check'
  assert_nix_call "$ROOT/connectors/alpha" develop OVERRIDE -c bash -c 'make check'
}

@test "--command keeps its command words after the override" {
  cd "$ROOT"
  run bash "$SCRIPT" alpha shell .#x --command echo hi
  assert_nix_call "$ROOT/connectors/alpha" shell .#x OVERRIDE --command echo hi
}

@test "a -c after -- is a program arg; the override goes in once, before --" {
  cd "$ROOT"
  run bash "$SCRIPT" alpha run .#x -- -c y
  assert_nix_call "$ROOT/connectors/alpha" run .#x OVERRIDE -- -c y
}

@test "a caller's own core override comes first, so the wrapper's wins" {
  cd "$ROOT"
  run bash "$SCRIPT" alpha flake check --override-input core github:x/y
  assert_nix_call "$ROOT/connectors/alpha" flake check --override-input core github:x/y OVERRIDE
}

@test "the lock file is untouched and a missing lock stays missing" {
  cd "$ROOT"
  cp connectors/alpha/flake.lock "$BATS_TEST_TMPDIR/lock.before"
  run bash "$SCRIPT" alpha flake check
  [ "$status" -eq 0 ]
  cmp connectors/alpha/flake.lock "$BATS_TEST_TMPDIR/lock.before"
  run bash "$SCRIPT" beta flake check
  [ "$status" -eq 0 ]
  [ ! -e connectors/beta/flake.lock ]
}

@test "an unknown Connector fails naming the available ones" {
  cd "$ROOT"
  run -1 bash "$SCRIPT" nope flake check
  [[ "$output" == *alpha* ]]
  [[ "$output" == *beta* ]]
  [[ "$output" == *"pass a Connector directory"* ]]
  [ ! -e "$NIX_LOG" ]
}

@test "no nix args is a usage error" {
  cd "$ROOT"
  run -2 bash "$SCRIPT" alpha
  [[ "$output" == *usage* ]]
  [ ! -e "$NIX_LOG" ]
}

@test "outside a spindrift checkout fails" {
  rm "$ROOT/core/flake.nix"
  cd "$ROOT"
  run -1 bash "$SCRIPT" alpha flake check
  [[ "$output" == *"spindrift checkout"* ]]
  [ ! -e "$NIX_LOG" ]
}
