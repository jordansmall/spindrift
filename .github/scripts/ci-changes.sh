#!/usr/bin/env bash
# Classifies a PR diff for CI (issue #5155). Reads `git diff --name-status -M`
# on stdin and prints `docs` when every path is on the docs allowlist,
# `connector` when the rest are all Connector paths, else `code`. A `docs`
# or `connector` verdict skips CI's heavy matrix, so anything unrecognised falls
# to `code`.
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
#
# A Connector path is connectors/<name>/**, <name> matching [a-z0-9][a-z0-9_-]*,
# and connectors/<name>/flake.nix exists at the new revision. Git's quoted form
# of a path ("connectors/...") counts too. Without revisions nothing
# qualifies. So a deleted or renamed-away Connector is core, and the full
# matrix checks what still references it. A Connector path is judged before
# the docs and comment-only exemptions, so no edit under it is ever docs: the
# connector-escape guard reads its Nix text. Any other path under connectors/
# is core, hence code. Connector and docs paths only give `connector`; one
# core path gives `code`.
#
# `--connectors OLD NEW` instead prints the sorted, unique Connector names any
# path in the diff touches (both sides of a rename or copy) as a JSON array,
# core paths or not. The name regex makes plain quoting safe, so no jq.
set -euo pipefail

script_dir=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
mode=classify
if [ "${1:-}" = --connectors ]; then
  mode=connectors
  shift
fi
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

# Sets connector_name for a Connector path; fails for any other path. Sets a
# global, not stdout, so the per-name flake.nix lookup cache survives.
declare -A has_flake=()
connector_of() {
  [ -n "$new_rev" ] || return 1
  [[ $1 =~ ^\"?connectors/([a-z0-9][a-z0-9_-]*)/. ]] || return 1
  connector_name=${BASH_REMATCH[1]}
  if [ -z "${has_flake[$connector_name]:-}" ]; then
    if git cat-file -e "$new_rev:connectors/$connector_name/flake.nix" 2>/dev/null; then
      has_flake[$connector_name]=1
    else
      has_flake[$connector_name]=0
    fi
  fi
  [ "${has_flake[$connector_name]}" = 1 ]
}

# Sets paths to the paths a name-status line touches; fails on an unknown
# status letter.
diff_paths() {
  case $1 in
    M | T | A | D) paths=("$2") ;;
    R[0-9]* | C[0-9]*) paths=("$2" "${3:-}") ;;
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

if [ "$mode" = connectors ]; then
  names=()
  while IFS=$'\t' read -r status path new_path; do
    diff_paths "$status" "$path" "${new_path:-}" || continue
    for p in "${paths[@]}"; do
      if connector_of "$p"; then names+=("$connector_name"); fi
    done
  done
  out=
  if [ "${#names[@]}" -gt 0 ]; then
    while IFS= read -r name; do out+=${out:+,}\"$name\"; done < <(printf '%s\n' "${names[@]}" | sort -u)
  fi
  echo "[$out]"
  exit 0
fi

seen=0
connector_seen=0
while IFS=$'\t' read -r status path new_path; do
  [ -n "$status" ] || continue
  seen=1
  if ! diff_paths "$status" "$path" "${new_path:-}"; then
    echo code
    exit 0
  fi
  for p in "${paths[@]}"; do
    if connector_of "$p"; then
      connector_seen=1
      continue
    fi
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
elif [ "$connector_seen" -eq 1 ]; then
  echo connector
else
  echo docs
fi
