#!/bin/sh
# This script is a stateful stand-in for the gh CLI that
# forgetest.RunCodeForgeContract drives. REMOTE is the bare git repo behind
# every seeded PR, and STATE_DIR/prs/<num> holds that PR's branch names.

# `pr merge` runs a real git merge and caches the outcome in
# prs/<num>/mergeable so the follow-up `api graphql` query from
# execClient.classifyMergeFailure reports the verdict Merge found, not a
# separate guess. Rebase needs no case: the rebase runs through the real git
# binary (exec_pr.go's Rebase).

pr_num() {
	printf '%s\n' "${1##*/}"
}

case "$1-$2" in
auth-status)
	exit 0
	;;
repo-view)
	case "$3" in
	*does-not-exist*)
		printf 'not found\n' >&2
		exit 1
		;;
	esac
	printf '%s' "$3"
	;;
repo-clone)
	dir="$4"
	git clone "$REMOTE" "$dir" >&2
	;;
pr-view)
	num=$(pr_num "$3")
	head=$(cat "$STATE_DIR/prs/$num/head")
	base=$(cat "$STATE_DIR/prs/$num/base")
	printf '%s\t%s\n' "$head" "$base"
	;;
pr-merge)
	num=$(pr_num "$3")
	head=$(cat "$STATE_DIR/prs/$num/head")
	base=$(cat "$STATE_DIR/prs/$num/base")
	work=$(mktemp -d)
	git clone "$REMOTE" "$work" >&2
	git -C "$work" checkout "$base" >&2
	# --no-ff, not real gh's --rebase strategy: the contract only needs a
	# genuine landing or conflict outcome, and --no-ff reaches both.
	if git -C "$work" merge --no-ff "origin/$head" -m "merge $head" >&2; then
		git -C "$work" push origin "HEAD:$base" >&2
		echo MERGEABLE > "$STATE_DIR/prs/$num/mergeable"
		rm -rf "$work"
		exit 0
	fi
	git -C "$work" merge --abort >&2
	echo CONFLICTING > "$STATE_DIR/prs/$num/mergeable"
	rm -rf "$work"
	echo 'GraphQL: Pull Request is not mergeable (mergePullRequest)' >&2
	exit 1
	;;
api-graphql)
	shift 2
	num=""
	while [ $# -gt 0 ]; do
		case "$1" in
		number=*) num="${1#number=}" ;;
		esac
		shift
	done
	cat "$STATE_DIR/prs/$num/mergeable" 2>/dev/null
	;;
pr-create)
	shift 2
	head=""
	base=""
	while [ $# -gt 0 ]; do
		case "$1" in
		--head)
			head="$2"
			shift 2
			;;
		--base)
			base="$2"
			shift 2
			;;
		*)
			shift
			;;
		esac
	done
	if [ "$head" = "fail-head" ]; then
		printf 'could not create pull request\n' >&2
		exit 1
	fi
	# head is a branch name (e.g. "agent/issue-1919"), not a PR URL, so pr_num
	# does not apply; derive the number from head's trailing "-<num>" suffix.
	num=${head##*-}
	mkdir -p "$STATE_DIR/prs/$num"
	printf '%s' "$head" >"$STATE_DIR/prs/$num/head"
	printf '%s' "$base" >"$STATE_DIR/prs/$num/base"
	printf 'https://github.com/owner/repo/pull/%s\n' "$num"
	;;
esac
