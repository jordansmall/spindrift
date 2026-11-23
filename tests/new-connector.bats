#!/usr/bin/env bats
# Issue #7421: nix/new-connector.sh writes templates/connector-tracker with the
# Connector name substituted at the two token sites and nowhere else. It runs
# against the real template and the Nix-supplied core row names.

bats_require_minimum_version 1.5.0

setup() {
  SCRIPT="${NEW_CONNECTOR_SH:-$BATS_TEST_DIRNAME/../nix/new-connector.sh}"
  export NEW_CONNECTOR_TEMPLATES="${NEW_CONNECTOR_TEMPLATES:-$BATS_TEST_DIRNAME/../templates}"
  # Under Nix, nix/checks/bats.nix sets this; derive it from the same registry otherwise.
  if [ -z "${NEW_CONNECTOR_CORE_NAMES:-}" ]; then
    NEW_CONNECTOR_CORE_NAMES="$(nix eval --raw --file "$BATS_TEST_DIRNAME/../lib/backends/default.nix" \
      --apply 'rs: toString (map (r: r.name) rs)')"
  fi
  export NEW_CONNECTOR_CORE_NAMES
  TEMPLATE="$NEW_CONNECTOR_TEMPLATES/connector-tracker"
  OUT="$BATS_TEST_TMPDIR/out"
}

# listing <dir>: sorted relative paths of everything under dir.
listing() {
  (cd "$1" && find . -mindepth 1 | LC_ALL=C sort)
}

@test "generates the template's file set with the name in paths" {
  run bash "$SCRIPT" tracker acme "$OUT"
  [ "$status" -eq 0 ]
  expected="$(listing "$TEMPLATE" | sed 's/connectorname/acme/g')"
  [ "$(listing "$OUT")" = "$expected" ]
}

@test "reversing the substitution reproduces the template byte for byte" {
  for n in acme zeta; do
    up="$(printf '%s' "$n" | tr a-z A-Z)"
    run bash "$SCRIPT" tracker "$n" "$BATS_TEST_TMPDIR/$n"
    [ "$status" -eq 0 ]
    cp -R "$BATS_TEST_TMPDIR/$n" "$BATS_TEST_TMPDIR/rev-$n"
    chmod -R u+w "$BATS_TEST_TMPDIR/rev-$n"
    find "$BATS_TEST_TMPDIR/rev-$n" -type f -exec sed -i -e "s/$n/connectorname/g" -e "s/$up/CONNECTORNAME/g" {} +
    mv "$BATS_TEST_TMPDIR/rev-$n/fragments/$n" "$BATS_TEST_TMPDIR/rev-$n/fragments/connectorname"
    run diff -r "$TEMPLATE" "$BATS_TEST_TMPDIR/rev-$n"
    [ "$status" -eq 0 ]
  done
}

@test "the generated tree holds no placeholder or example residue" {
  run bash "$SCRIPT" tracker acme "$OUT"
  [ "$status" -eq 0 ]
  run ! grep -rliE 'connectorname|example' "$OUT"
  run ! bash -c 'cd "$1" && find . | grep -iE "connectorname|example"' _ "$OUT"
}

@test "the name replaces the env-name token in upper case" {
  run bash "$SCRIPT" tracker acme "$OUT"
  [ "$status" -eq 0 ]
  grep -rq ACME "$OUT"
}

@test "generated files are writable and carry no store path" {
  run bash "$SCRIPT" tracker acme "$OUT"
  [ "$status" -eq 0 ]
  [ -z "$(find "$OUT" ! -perm -u+w)" ]
  run ! grep -r /nix/store "$OUT"
}

@test "an absent target directory is created, nested" {
  run bash "$SCRIPT" tracker acme "$OUT/a/b"
  [ "$status" -eq 0 ]
  [ -f "$OUT/a/b/flake.nix" ]
}

@test "the directory defaults to the current one" {
  mkdir "$OUT"
  cd "$OUT"
  run bash "$SCRIPT" tracker acme
  [ "$status" -eq 0 ]
  [ -f "$OUT/flake.nix" ]
}

@test "an empty existing directory is accepted" {
  mkdir "$OUT"
  run bash "$SCRIPT" tracker acme "$OUT"
  [ "$status" -eq 0 ]
}

@test "a non-empty directory is refused and left as it was" {
  mkdir "$OUT"
  echo keep >"$OUT/file"
  run bash "$SCRIPT" tracker acme "$OUT"
  [ "$status" -eq 1 ]
  [[ "$output" == *"not empty"* ]]
  [ "$(listing "$OUT")" = "./file" ]
}

@test "a directory holding only a dotfile counts as non-empty" {
  mkdir "$OUT"
  touch "$OUT/.hidden"
  run bash "$SCRIPT" tracker acme "$OUT"
  [ "$status" -eq 1 ]
  [ "$(listing "$OUT")" = "./.hidden" ]
}

@test "a target that is a file is refused" {
  echo x >"$OUT"
  run bash "$SCRIPT" tracker acme "$OUT"
  [ "$status" -eq 1 ]
  [[ "$output" == *"not a directory"* ]]
  [ "$(cat "$OUT")" = x ]
}

@test "invalid names are refused and write nothing" {
  for bad in Acme 1acme ac-me acme_x "" ac.me "ac me"; do
    run bash "$SCRIPT" tracker "$bad" "$OUT"
    [ "$status" -eq 1 ]
    [[ "$output" == *"invalid name"* ]]
    [ ! -e "$OUT" ]
  done
}

@test "an unknown kind is refused and writes nothing" {
  for kind in forge driver ""; do
    run bash "$SCRIPT" "$kind" acme "$OUT"
    [ "$status" -eq 1 ]
    [[ "$output" == *"unknown kind"* ]]
    [ ! -e "$OUT" ]
  done
}

@test "a core backend name is refused and writes nothing" {
  for core in $NEW_CONNECTOR_CORE_NAMES; do
    run bash "$SCRIPT" tracker "$core" "$OUT"
    [ "$status" -eq 1 ]
    [[ "$output" == *"core backend"* ]]
    [ ! -e "$OUT" ]
  done
}

@test "a Nix keyword or the placeholder token is refused and writes nothing" {
  # Mirrors the reserved `case` in nix/new-connector.sh (Nix keywords plus the template token).
  for bad in if then else assert with let in rec inherit or connectorname; do
    run bash "$SCRIPT" tracker "$bad" "$OUT"
    [ "$status" -eq 1 ]
    [[ "$output" == *"reserved"* ]]
    [ ! -e "$OUT" ]
  done
}

@test "a template whose go.mod lacks the SDK placeholder is refused and writes nothing" {
  mkdir "$BATS_TEST_TMPDIR/templates"
  cp -R --no-preserve=mode "$TEMPLATE" "$BATS_TEST_TMPDIR/templates/connector-tracker"
  sed -i 's/v0.0.0-00010101000000-000000000000/v1.0.0/' "$BATS_TEST_TMPDIR/templates/connector-tracker/go.mod"
  NEW_CONNECTOR_TEMPLATES="$BATS_TEST_TMPDIR/templates" run bash "$SCRIPT" tracker acme "$OUT"
  [ "$status" -eq 1 ]
  [[ "$output" == *"lacks the SDK placeholder version"* ]]
  [ ! -e "$OUT" ]
}

@test "the wrong argument count prints usage and exits 2" {
  run bash "$SCRIPT" tracker
  [ "$status" -eq 2 ]
  [[ "$output" == *usage* ]]
  run bash "$SCRIPT" tracker acme "$OUT" extra
  [ "$status" -eq 2 ]
  [ ! -e "$OUT" ]
}

@test "go.mod pins the SDK replace at the supplied pseudo-version" {
  NEW_CONNECTOR_SDK_VERSION=v0.0.0-20261123000000-0123456789ab \
    run --separate-stderr bash "$SCRIPT" tracker acme "$OUT"
  [ "$status" -eq 0 ]
  grep -qxF 'replace spindrift.dev/sdk => github.com/jordansmall/spindrift/sdk v0.0.0-20261123000000-0123456789ab' "$OUT/go.mod"
  [ -z "$stderr" ]
}

@test "without a pseudo-version go.mod keeps the placeholder and a warning is printed" {
  NEW_CONNECTOR_SDK_VERSION= run --separate-stderr bash "$SCRIPT" tracker acme "$OUT"
  [ "$status" -eq 0 ]
  grep -qF 'sdk v0.0.0-00010101000000-000000000000' "$OUT/go.mod"
  [[ "$stderr" == *"no commit to pin"* ]]
  unset NEW_CONNECTOR_SDK_VERSION
  rm -rf "$OUT"
  run --separate-stderr bash "$SCRIPT" tracker acme "$OUT"
  [[ "$stderr" == *"no commit to pin"* ]]
}
