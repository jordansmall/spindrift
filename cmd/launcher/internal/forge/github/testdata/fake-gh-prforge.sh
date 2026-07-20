#!/bin/sh
# Stateful stand-in for the gh CLI, used by prforge_contract_test.go's
# forgetest.RunPRForgeContract harness. STATE_DIR/prs/<num>/{head,base,
# prstate,checks,contexts.json} record each seeded PR's scripted state;
# STATE_DIR/branches/<branch> maps a branch back to its PR number for the
# `pr list` lookups OpenPRForBranch/PRForBranch issue.
#
# `pr merge` (without --auto) performs a genuine git merge against REMOTE,
# same as fake-gh-codeforge.sh, and flips prstate to MERGED on success — the
# one event PRState's MERGED transition depends on. `pr merge --auto` never
# touches git at all: real auto-merge only enqueues, it doesn't land
# anything itself, so this just records the call and exits 0.

pr_num() {
	printf '%s\n' "${1##*/}"
}

case "$1-$2" in
pr-list)
	shift 2
	branch="" state=""
	while [ $# -gt 0 ]; do
		case "$1" in
		--head) branch="$2"; shift 2 ;;
		--state) state="$2"; shift 2 ;;
		*) shift ;;
		esac
	done
	num=""
	[ -f "$STATE_DIR/branches/$branch" ] && num=$(cat "$STATE_DIR/branches/$branch")
	if [ -z "$num" ]; then
		printf '\n'
		exit 0
	fi
	prstate=$(cat "$STATE_DIR/prs/$num/prstate" 2>/dev/null || echo OPEN)
	if [ "$state" = "open" ] && [ "$prstate" != "OPEN" ]; then
		printf '\n'
		exit 0
	fi
	cat "$STATE_DIR/prs/$num/url"
	;;
pr-view)
	url="$3"
	num=$(pr_num "$url")
	shift 3
	fields=""
	while [ $# -gt 0 ]; do
		case "$1" in
		--json) fields="$2"; shift 2 ;;
		*) shift ;;
		esac
	done
	case "$fields" in
	headRefName,baseRefName)
		head=$(cat "$STATE_DIR/prs/$num/head")
		base=$(cat "$STATE_DIR/prs/$num/base")
		printf '%s\t%s\n' "$head" "$base"
		;;
	state)
		cat "$STATE_DIR/prs/$num/prstate" 2>/dev/null || echo OPEN
		;;
	isDraft)
		printf 'false\n'
		;;
	esac
	;;
pr-merge)
	url="$3"
	num=$(pr_num "$url")
	auto=0
	for a in "$@"; do
		[ "$a" = "--auto" ] && auto=1
	done
	if [ "$auto" = "1" ]; then
		echo 1 > "$STATE_DIR/prs/$num/automerge"
		exit 0
	fi
	head=$(cat "$STATE_DIR/prs/$num/head")
	base=$(cat "$STATE_DIR/prs/$num/base")
	work=$(mktemp -d)
	git clone "$REMOTE" "$work" >&2
	git -C "$work" checkout "$base" >&2
	if git -C "$work" merge --no-ff "origin/$head" -m "merge $head" >&2; then
		git -C "$work" push origin "HEAD:$base" >&2
		echo MERGED > "$STATE_DIR/prs/$num/prstate"
		rm -rf "$work"
		exit 0
	fi
	git -C "$work" merge --abort >&2
	rm -rf "$work"
	echo 'GraphQL: Pull Request is not mergeable (mergePullRequest)' >&2
	exit 1
	;;
pr-ready)
	exit 0
	;;
api-graphql)
	shift 2
	query="" num=""
	while [ $# -gt 0 ]; do
		case "$1" in
		query=*) query="${1#query=}" ;;
		number=*) num="${1#number=}" ;;
		esac
		shift
	done
	case "$query" in
	*autoMergeAllowed*)
		cat "$STATE_DIR/automerge_allowed" 2>/dev/null || echo false
		;;
	*"statusCheckRollup{contexts"*)
		cat "$STATE_DIR/prs/$num/contexts.json" 2>/dev/null || echo '[]'
		;;
	*"statusCheckRollup{state}"*)
		f="$STATE_DIR/prs/$num/checks"
		if [ -s "$f" ]; then
			head -n 1 "$f"
			tail -n +2 "$f" > "$f.tmp" && mv "$f.tmp" "$f"
		else
			printf '\n'
		fi
		;;
	esac
	;;
esac
