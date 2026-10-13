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
#
# Given the diff's two revisions as `$1 $2`, a modified .go or .nix file whose
# edit is comment-only also qualifies (issue #5158): its comment-free Go token
# stream (ci-go-tokens.go) or `nix-instantiate --parse` output is unchanged,
# no changed line carries a directive or marker, and no check outside the
# docs lane reads its text (ci-comment-readers). Anything that fails to parse
# or build is code.
set -euo pipefail

script_dir=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
old_rev=${1:-}
new_rev=${2:-}

# Comments a tool reads: Go directives (`//go:build`, `//line`, `//export`,
# `//nolint:x`), linter and shellcheck suppressions, and the generated-block
# markers nix/checks splices between.
functional_re='//[a-z0-9]+:[a-z0-9]|//(line|export|extern) |\+build|nolint|shellcheck|GENERATED|DO NOT EDIT'

is_docs_path() {
  case $1 in
    docs/*) return 0 ;;
    */*) return 1 ;;
    *.md | .release-please-manifest.json) return 0 ;;
    *) return 1 ;;
  esac
}

is_comment_reader() {
  local glob
  while IFS= read -r glob; do
    case $glob in '' | '#'*) continue ;; esac
    # shellcheck disable=SC2053 # the glob is meant to match
    [[ $1 == $glob ]] && return 0
  done <"$script_dir/ci-comment-readers"
  return 1
}

work=
cleanup() { [ -z "$work" ] || rm -rf "$work"; }
trap cleanup EXIT

# Builds the helper once, in this shell: source_key runs in a subshell.
build_go_tokens() {
  [ -z "$work" ] || return 0
  work=$(mktemp -d)
  GOWORK=off GOTOOLCHAIN=local go build -o "$work/ci-go-tokens" "$script_dir/ci-go-tokens.go" >&2
}

source_key() {
  case $2 in
    *.go) git show "$1:$2" | "$work/ci-go-tokens" ;;
    # Relative paths parse to absolute ones against the cwd, the same for
    # both revisions.
    *.nix) git show "$1:$2" | nix-instantiate --store dummy:// --parse - 2>/dev/null ;;
  esac
}

is_comment_only() {
  local path=$1 old new diff
  [ -n "$old_rev" ] && [ -n "$new_rev" ] || return 1
  case $path in *.go | *.nix) ;; *) return 1 ;; esac
  ! is_comment_reader "$path" || return 1
  if [[ $path == *.go ]]; then build_go_tokens || return 1; fi
  old=$(source_key "$old_rev" "$path") || return 1
  new=$(source_key "$new_rev" "$path") || return 1
  [ "$old" = "$new" ] || return 1
  diff=$(git diff -U0 --no-color "$old_rev" "$new_rev" -- "$path") || return 1
  # One grep on a captured string: `grep -q` quitting early inside a
  # pipeline SIGPIPEs its upstream, and pipefail turns that into a miss.
  # The ---/+++ header lines are matched too, which only fails closed.
  ! grep -qE "^[-+].*($functional_re)" <<<"$diff"
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
    if [ -n "$p" ] && is_docs_path "$p"; then
      if [ "$status" != M ] && [ "$status" != T ] && [[ $p == docs/adr/* ]]; then
        echo code
        exit 0
      fi
      continue
    fi
    if [ "$status" = M ] && is_comment_only "$p"; then
      continue
    fi
    echo code
    exit 0
  done
done

if [ "$seen" -eq 0 ]; then
  echo code
else
  echo docs
fi
