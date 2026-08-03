#!/bin/sh
# Stateful stand-in for the gh CLI, used by hostmediation_contract_test.go's
# forgetest.RunHostMediationContract harness. REMOTE names the bare git repo
# `gh repo clone` clones from (RelayBundle's real push target); STATE_DIR/
# comments/<num> records posted comment bodies for CommentPosted to read
# back. A magic PR head "fail-head" and a magic issue number "fail-comment"
# make their respective calls fail, mirroring fake-gh-codeforge.sh's own
# "fail-head" convention for pr-create.

case "$1-$2" in
repo-clone)
	dir="$4"
	git clone "$REMOTE" "$dir" >&2
	;;
pr-create)
	shift 2
	head=""
	while [ $# -gt 0 ]; do
		case "$1" in
		--head) head="$2"; shift 2 ;;
		*) shift ;;
		esac
	done
	if [ "$head" = "fail-head" ]; then
		printf 'could not create pull request\n' >&2
		exit 1
	fi
	printf 'https://github.com/owner/repo/pull/999\n'
	;;
issue-comment)
	num="$3"
	if [ "$num" = "fail-comment" ]; then
		printf 'could not create comment\n' >&2
		exit 1
	fi
	shift 3
	body=""
	while [ $# -gt 0 ]; do
		case "$1" in
		--body) body="$2"; shift 2 ;;
		*) shift ;;
		esac
	done
	mkdir -p "$STATE_DIR/comments"
	printf '%s\n' "$body" >> "$STATE_DIR/comments/$num"
	;;
esac
