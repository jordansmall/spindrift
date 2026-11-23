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

# Issue #7421: a Connector template is no Connector itself (its name is a
# placeholder), so connector-dev generates it under two names with new-connector
# and runs the same nix args in each generated dir. new-connector is the real
# script on a fixture template; nix is the recording stub, which fails in a dir
# whose flake.nix holds $NIX_FAIL_NAME.
template_setup() {
  export NEW_CONNECTOR_TEMPLATES="$ROOT/templates"
  export NEW_CONNECTOR_CORE_NAMES="github forgejo"
  mkdir -p "$ROOT/templates/connector-tracker"
  echo '{ description = "connectorname"; }' >"$ROOT/templates/connector-tracker/flake.nix"
  echo "replace sdk => ./sdk v0.0.0-00010101000000-000000000000" >"$ROOT/templates/connector-tracker/go.mod"
  cat >"$STUBS/new-connector" <<STUB
#!$BASH
exec bash "${NEW_CONNECTOR_SH:-$BATS_TEST_DIRNAME/../nix/new-connector.sh}" "\$@"
STUB
  chmod +x "$STUBS/new-connector"
  cat >"$STUBS/nix" <<STUB
#!$BASH
{ pwd -P; printf '%s\n' "\$@"; echo ---; } >>"$NIX_LOG.calls"
[ -z "\${NIX_FAIL_NAME:-}" ] || ! grep -q "\$NIX_FAIL_NAME" flake.nix
STUB
}

# calls: the cwds of the recorded nix calls, in order.
calls() {
  awk 'BEGIN { first = 1 } first { print; first = 0 } /^---$/ { first = 1 }' "$NIX_LOG.calls"
}

@test "a template runs the nix args in two generated Connectors with distinct names" {
  template_setup
  cd "$ROOT"
  run bash "$SCRIPT" templates/connector-tracker flake check -L
  [ "$status" -eq 0 ]
  [ "$(grep -c '^---$' "$NIX_LOG.calls")" -eq 2 ]
  mapfile -t dirs < <(calls)
  [ "${#dirs[@]}" -eq 2 ]
  [ "${dirs[0]}" != "${dirs[1]}" ]
  # Both calls carry the same args with the override injected.
  [ "$(grep -c '^--override-input$' "$NIX_LOG.calls")" -eq 2 ]
  [ "$(grep -c "^git+file://$ROOT?dir=core\$" "$NIX_LOG.calls")" -eq 2 ]
  [ "$(grep -c '^--no-write-lock-file$' "$NIX_LOG.calls")" -eq 2 ]
  # The generated dirs are gone once connector-dev exits.
  [ ! -e "${dirs[0]}" ]
  [ ! -e "${dirs[1]}" ]
  # The template itself is never the nix cwd.
  [ "${dirs[0]}" != "$ROOT/templates/connector-tracker" ]
}

@test "a template's two generated flakes carry different names" {
  template_setup
  # Capture each flake.nix as the nix stub sees it.
  cat >"$STUBS/nix" <<STUB
#!$BASH
cat flake.nix >>"$NIX_LOG.flakes"
STUB
  cd "$ROOT"
  run bash "$SCRIPT" templates/connector-tracker flake check
  [ "$status" -eq 0 ]
  [ "$(sort "$NIX_LOG.flakes" | uniq | wc -l)" -eq 2 ]
  [[ "$(cat "$NIX_LOG.flakes")" != *connectorname* ]]
  grep -q 'acme' "$NIX_LOG.flakes"
  grep -q 'zeta' "$NIX_LOG.flakes"
}

@test "a template fails when either generated Connector fails, running both" {
  template_setup
  cd "$ROOT"
  for n in acme zeta; do
    rm -f "$NIX_LOG.calls"
    NIX_FAIL_NAME=$n run bash "$SCRIPT" templates/connector-tracker flake check
    [ "$status" -ne 0 ]
    [ "$(grep -c '^---$' "$NIX_LOG.calls")" -eq 2 ]
  done
}

@test "a template path given relatively is recognised as a template" {
  template_setup
  cd "$ROOT/core"
  run bash "$SCRIPT" ../templates/connector-tracker flake check
  [ "$status" -eq 0 ]
  [ "$(grep -c '^---$' "$NIX_LOG.calls")" -eq 2 ]
}

@test "an ordinary Connector is not generated" {
  template_setup
  cd "$ROOT"
  run bash "$SCRIPT" alpha flake check
  [ "$status" -eq 0 ]
  [ "$(grep -c '^---$' "$NIX_LOG.calls")" -eq 1 ]
  [ "$(head -n1 "$NIX_LOG.calls")" = "$ROOT/connectors/alpha" ]
}
