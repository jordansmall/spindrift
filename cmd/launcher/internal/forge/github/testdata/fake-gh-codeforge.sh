#!/bin/sh
# Stateful stand-in for the gh CLI, used by codeforge_contract_test.go's
# forgetest.RunCodeForgeContract harness. REMOTE names the bare git repo
# backing every seeded PR; STATE_DIR/prs/<num>/{head,base} record each PR's
# branch names so `pr view`/`pr merge` can look them up.
#
# `pr merge` performs a genuine git merge against REMOTE rather than
# returning a scripted verdict, and caches the outcome in
# STATE_DIR/prs/<num>/mergeable so the follow-up `api graphql` mergeable
# query (execClient.classifyMergeFailure) reports the same verdict Merge
# itself just discovered, never an independently scripted guess. Rebase
# needs no case here at all — the real adapter only uses `repo clone` and
# `pr view` from this script; the checkout/rebase/force-push themselves run
# straight through the real git binary (exec_pr.go's Rebase).

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
	# --no-ff, not real gh's --rebase merge strategy: the contract only
	# needs a real landing/conflict outcome to observe, and --no-ff
	# reaches both just as genuinely as replaying commits would.
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
esac
