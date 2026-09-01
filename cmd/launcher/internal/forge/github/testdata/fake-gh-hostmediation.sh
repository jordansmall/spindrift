#!/bin/sh
# Stateful stand-in for the gh CLI, used by hostmediation_contract_test.go's
# forgetest.RunHostMediationContract harness. REMOTE names the bare git repo
# `gh repo clone` clones from (RelayBundle's real push target);
# STATE_DIR/comments/<num> records posted comment bodies and
# STATE_DIR/issues/<num> records filed issues (title, then body, then one label
# per remaining line) for the contract to read back. Magic values force a
# failure: PR head "fail-head", issue number "fail-comment", issue title
# "fail-issue". PR head "agent/issue-2407-adopt" makes pr-create fail with gh's
# own already-exists stderr (relay.go's CreateDraftPR matches "already exists")
# and lets pr-list/pr-view resolve that head's already-open PR -- the
# DraftPRCreationAdoptsExisting scenario.

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
	if [ "$head" = "agent/issue-2407-adopt" ]; then
		printf 'a pull request for branch "agent/issue-2407-adopt" into branch "main" already exists: https://github.com/owner/repo/pull/2407\n' >&2
		exit 1
	fi
	printf 'https://github.com/owner/repo/pull/999\n'
	;;
pr-list)
	printf 'https://github.com/owner/repo/pull/2407\n'
	;;
pr-view)
	url="$3"
	if [ "$url" = "https://github.com/owner/repo/pull/2407" ]; then
		# The adopted PR from the "agent/issue-2407-adopt" scenario: github's
		# own CreateDraftPR always runs `gh pr create --draft`, so a leftover
		# adopted PR is always a draft too.
		printf 'true\n'
	else
		printf 'false\n'
	fi
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
issue-create)
	shift 2
	title=""
	body=""
	labels=""
	while [ $# -gt 0 ]; do
		case "$1" in
		--title) title="$2"; shift 2 ;;
		--body) body="$2"; shift 2 ;;
		--label) labels="$labels$2
"; shift 2 ;;
		*) shift ;;
		esac
	done
	if [ "$title" = "fail-issue" ]; then
		printf 'could not create issue\n' >&2
		exit 1
	fi
	num=1000
	mkdir -p "$STATE_DIR/issues"
	{
		printf '%s\n' "$title"
		printf '%s\n' "$body"
		printf '%s' "$labels"
	} > "$STATE_DIR/issues/$num"
	printf 'https://github.com/owner/repo/issues/%s\n' "$num"
	;;
esac
