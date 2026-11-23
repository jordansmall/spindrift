#!/usr/bin/env bash
# Scaffold a Connector from a template under templates/connector-<kind>.
#
#   new-connector <kind> <name> [<dir>]
#
# <dir> defaults to the current directory and is created if absent; it must be
# empty. The template is itself a valid Connector named by the lowercase token
# below, with the uppercase form for env names, so generation is substitution
# and nothing else. The Nix wrapper exports NEW_CONNECTOR_TEMPLATES (the
# directory holding the templates), NEW_CONNECTOR_CORE_NAMES (core backend
# row names) and NEW_CONNECTOR_CORE_DRIVER_NAMES (core Driver row names), both
# space separated, and NEW_CONNECTOR_SDK_VERSION (the pseudo-version
# go.mod pins the SDK at; empty for an uncommitted build).
set -euo pipefail

token=connectorname
token_upper=CONNECTORNAME

die() {
  echo "new-connector: $*" >&2
  exit 1
}

if [ "$#" -lt 2 ] || [ "$#" -gt 3 ]; then
  echo "usage: new-connector <kind> <name> [<dir>]" >&2
  exit 2
fi
kind="$1"
name="$2"
dir="${3:-.}"

# One arm per kind; a forge template is another arm and directory.
# connectorChecks keys its per-row path on a tracker's name against the backend
# rows and on a driver's against the Driver rows, so each kind refuses only its
# own list.
case "$kind" in
  tracker)
    template="$NEW_CONNECTOR_TEMPLATES/connector-tracker"
    core_names="$NEW_CONNECTOR_CORE_NAMES"
    core_what="backend"
    ;;
  driver)
    template="$NEW_CONNECTOR_TEMPLATES/connector-driver"
    core_names="$NEW_CONNECTOR_CORE_DRIVER_NAMES"
    core_what="Driver"
    ;;
  *) die "unknown kind '$kind' (known: tracker, driver)" ;;
esac

[[ "$name" =~ ^[a-z][a-z0-9]*$ ]] ||
  die "invalid name '$name': use lowercase letters and digits, starting with a letter"

# The name lands in Nix attr paths (spindrift.connectors.<name>.knobs), where
# a keyword does not parse; the token would be substituted into itself.
# tests/new-connector.bats mirrors this list.
case "$name" in
  if | then | else | assert | with | let | in | rec | inherit | or | "$token")
    die "name '$name' is reserved; pick another"
    ;;
esac

# A core row name routes connectorChecks down its per-row path, not the
# generic conformance a new Connector starts green on.
for core in $core_names; do
  [ "$name" != "$core" ] || die "name '$name' is a core $core_what; pick another"
done

if [ -e "$dir" ] && [ ! -d "$dir" ]; then
  die "$dir exists and is not a directory"
fi
if [ -d "$dir" ] && [ -n "$(ls -A "$dir")" ]; then
  die "$dir is not empty"
fi

# Stage first so a failure part-way leaves the target untouched.
stage="$(mktemp -d)"
trap 'rm -rf "$stage"' EXIT
# The store copy is read-only; drop the mode bits and take the caller's umask.
cp -R --no-preserve=mode,ownership "$template/." "$stage"

upper="${name^^}"
while IFS= read -r -d '' file; do
  sed -i -e "s/$token/$name/g" -e "s/$token_upper/$upper/g" "$file"
done < <(find "$stage" -type f -print0)
# Deepest first, so a renamed parent never invalidates a path still to visit.
while IFS= read -r -d '' path; do
  mv "$path" "$(dirname "$path")/$(basename "$path" | sed "s/$token/$name/g")"
done < <(find "$stage" -depth -name "*$token*" -print0)

# The template's SDK replace carries a placeholder version; only a build from a
# commit knows the real one.
placeholder=v0.0.0-00010101000000-000000000000
grep -qF "$placeholder" "$stage/go.mod" ||
  die "template go.mod lacks the SDK placeholder version $placeholder"
if [ -n "${NEW_CONNECTOR_SDK_VERSION:-}" ]; then
  sed -i "s/$placeholder/$NEW_CONNECTOR_SDK_VERSION/" "$stage/go.mod"
else
  echo "new-connector: go.mod's SDK replace has no commit to pin (unreleased or dirty build); set its version ($placeholder) by hand" >&2
fi

mkdir -p "$dir"
cp -R "$stage/." "$dir"
echo "new-connector: wrote $kind Connector '$name' to $dir"
