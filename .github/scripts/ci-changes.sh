#!/usr/bin/env bash
# Classifies a PR diff for CI (issue #5155). Reads `git diff --name-status -M`
# on stdin and prints `docs` when every path is on the docs allowlist, else
# `code`. A `docs` verdict skips CI's heavy matrix, so anything unrecognised
# falls to `code`.
#
# The allowlist is docs/**, root *.md, and the release-please manifest.
# Prompt templates and skills are prompt inputs and never qualify. Adding,
# deleting, renaming, or copying an ADR counts as code: checks resolve ADRs
# by filename, outside the docs lane's reach.
set -euo pipefail

is_docs_path() {
  case $1 in
    docs/*) return 0 ;;
    */*) return 1 ;;
    *.md | .release-please-manifest.json) return 0 ;;
    *) return 1 ;;
  esac
}

seen=0
while IFS=$'\t' read -r status path new_path; do
  [ -n "$status" ] || continue
  seen=1
  case $status in
    M | T | A | D) paths=("$path") ;;
    R[0-9]* | C[0-9]*) paths=("$path" "${new_path:-}") ;;
    *) echo code; exit 0 ;;
  esac
  for p in "${paths[@]}"; do
    if [ -z "$p" ] || ! is_docs_path "$p"; then
      echo code
      exit 0
    fi
    if [ "$status" != M ] && [ "$status" != T ] && [[ $p == docs/adr/* ]]; then
      echo code
      exit 0
    fi
  done
done

if [ "$seen" -eq 0 ]; then
  echo code
else
  echo docs
fi
