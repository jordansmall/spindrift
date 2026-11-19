#!/usr/bin/env bash
# Classifies the stderr of the narrowed `go mod vendor -e` (nix/checks/go.nix
# vendorFor). -e exits 0 even when a module download fails, so the exit code
# cannot gate: the one expected complaint is a trimmed-away spindrift.dev/launcher
# package, and every other non-empty line fails the build and is printed.
# An allowlist, not a denylist, so an unrecognised go message fails closed.
# Usage: go-vendor-errors.sh STDERR_FILE
set -euo pipefail

file=$1

# The absent-package complaint must name the same package twice; a prefix
# match would let a download failure on a launcher import line through.
absent='^'$'\t''spindrift\.dev/launcher/([^: ]+): no required module provides package spindrift\.dev/launcher/([^: ;]+); to add it:$'

bad=()
while IFS= read -r line || [ -n "$line" ]; do
  case $line in
  '' | 'go: downloading '* | 'go: finding module for package '*) continue ;;
  'go: '*' imports' | 'go: '*' tested by') continue ;;
  $'\t'*' imports' | $'\t'*' tested by') continue ;;
  $'\t''go get spindrift.dev/launcher/'*) continue ;;
  esac
  if [[ $line =~ $absent && ${BASH_REMATCH[1]} == "${BASH_REMATCH[2]}" ]]; then
    continue
  fi
  bad+=("$line")
done <"$file"

if [ "${#bad[@]}" -gt 0 ]; then
  echo "go mod vendor: unexpected stderr (likely a module download failure):" >&2
  printf '  %s\n' "${bad[@]}" >&2
  exit 1
fi
