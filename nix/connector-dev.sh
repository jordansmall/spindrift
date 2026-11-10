#!/usr/bin/env bash
# Issue #6769: build or check a Connector subflake against the LOCAL core tree.
# Runs the caller's nix from inside the Connector dir with `core` overridden to
# this checkout and the lock never written, so the Connector's pinned core
# input is never fetched and its flake.lock stays as it was.
#
#   connector-dev <connector> <nix-args...>
#
# <connector> is a path if it contains `/` or is `.`/`..`; anything else is a
# bare name resolved to connectors/<name>, never a cwd-relative directory.
# The override flags follow the user's args, or precede the first `--`/`-c`/
# `--command`: nix hands everything after those to the program, so a trailing
# override would reach `bash -c` as its $0 and positionals. The scan matches
# any word equal to one of those, an option's value included (`--option foo
# -c`), so such a value splits from its option.
set -euo pipefail

if [ "$#" -lt 2 ]; then
  echo "usage: connector-dev <connector> <nix-args...>" >&2
  exit 2
fi
connector="$1"
shift

root="$(git rev-parse --show-toplevel 2>/dev/null)" || root=
if [ -z "$root" ] || [ ! -f "$root/core/flake.nix" ]; then
  echo "connector-dev: run from a spindrift checkout (no core/flake.nix found)" >&2
  exit 1
fi

case "$connector" in
  */* | . | ..) dir="$connector" ;;
  *) dir="$root/connectors/$connector" ;;
esac
if [ -d "$dir" ]; then
  dir="$(cd "$dir" && pwd -P)"
fi
if [ ! -f "$dir/flake.nix" ]; then
  available=()
  for f in "$root"/connectors/*/flake.nix; do
    [ -f "$f" ] && available+=("$(basename "$(dirname "$f")")")
  done
  echo "connector-dev: no Connector '$connector' (no flake.nix at $dir)" >&2
  echo "available Connectors: ${available[*]:-none}" >&2
  echo "pass a Connector directory (containing /) to use one outside connectors/" >&2
  exit 1
fi

override=(--override-input core "git+file://$root?dir=core" --no-write-lock-file)
args=()
injected=
for a in "$@"; do
  if [ -z "$injected" ] && { [ "$a" = "--" ] || [ "$a" = "-c" ] || [ "$a" = "--command" ]; }; then
    args+=("${override[@]}")
    injected=1
  fi
  args+=("$a")
done
[ -n "$injected" ] || args+=("${override[@]}")

cd "$dir"
exec nix "${args[@]}"
