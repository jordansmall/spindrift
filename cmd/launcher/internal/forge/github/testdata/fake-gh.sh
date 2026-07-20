#!/bin/sh
# Stateful stand-in for the gh CLI, used by contract_test.go's
# forgetest.RunTrackerContract harness. Reads and writes a
# STATE_DIR/issues/<num>/ tree instead of returning a single scripted
# response, so successive gh invocations across a contract run see each
# other's effects.
DIR="$STATE_DIR/issues"

json_escape() {
	printf '%s' "$1" | sed 's/\\/\\\\/g; s/"/\\"/g' | tr '\n' ' '
}

# ordered_nums lists issue directory names ascending. Names are always
# plain digits (test-controlled), so ls's word-splitting risk (SC2012)
# doesn't apply here.
ordered_nums() {
	# shellcheck disable=SC2012
	ls "$DIR" 2>/dev/null | sort -n
}

labels_json() {
	f="$DIR/$1/labels"
	first=1
	printf '['
	if [ -f "$f" ]; then
		while IFS= read -r l; do
			[ -z "$l" ] && continue
			[ $first -eq 0 ] && printf ','
			first=0
			printf '{"name":"%s"}' "$(json_escape "$l")"
		done < "$f"
	fi
	printf ']'
}

cmd1="$1"; cmd2="$2"

case "$cmd1-$cmd2" in
issue-list)
	shift 2
	label=""
	while [ $# -gt 0 ]; do
		case "$1" in
		--label) label="$2"; shift 2 ;;
		*) shift ;;
		esac
	done
	if [ -n "$label" ]; then
		for num in $(ordered_nums); do
			labf="$DIR/$num/labels"
			if [ -f "$labf" ] && grep -qxF "$label" "$labf"; then
				title=$(cat "$DIR/$num/title" 2>/dev/null)
				printf '%s\t%s\n' "$num" "$title"
			fi
		done
	else
		printf '['
		first=1
		for num in $(ordered_nums); do
			[ $first -eq 0 ] && printf ','
			first=0
			title=$(cat "$DIR/$num/title" 2>/dev/null)
			printf '{"number":%s,"title":"%s","labels":%s}' "$num" "$(json_escape "$title")" "$(labels_json "$num")"
		done
		printf ']'
	fi
	;;
issue-view)
	num="$3"
	shift 3
	fields=""
	while [ $# -gt 0 ]; do
		case "$1" in
		--json) fields="$2"; shift 2 ;;
		*) shift ;;
		esac
	done
	title=$(cat "$DIR/$num/title" 2>/dev/null)
	body=$(cat "$DIR/$num/body" 2>/dev/null)
	case "$fields" in
	*body*)
		printf '{"number":%s,"title":"%s","body":"%s","state":"OPEN","labels":%s}' "$num" "$(json_escape "$title")" "$(json_escape "$body")" "$(labels_json "$num")"
		;;
	*)
		printf '{"labels":%s}' "$(labels_json "$num")"
		;;
	esac
	;;
issue-edit)
	num="$3"
	shift 3
	add=""; remove=""
	while [ $# -gt 0 ]; do
		case "$1" in
		--add-label) add="$2"; shift 2 ;;
		--remove-label) remove="$2"; shift 2 ;;
		*) shift ;;
		esac
	done
	tmp="$DIR/$num/labels.tmp"
	: > "$tmp"
	if [ -f "$DIR/$num/labels" ]; then
		while IFS= read -r l; do
			[ -z "$l" ] && continue
			[ "$l" = "$remove" ] && continue
			echo "$l" >> "$tmp"
		done < "$DIR/$num/labels"
	fi
	[ -n "$add" ] && echo "$add" >> "$tmp"
	mv "$tmp" "$DIR/$num/labels"
	;;
api-*)
	path="$2"
	case "$path" in
	*/dependencies/blocked_by)
		rest=${path#*/issues/}
		num=${rest%%/dependencies*}
		if [ -f "$DIR/$num/fail_native" ]; then
			echo "simulated native lookup failure" >&2
			exit 1
		fi
		if [ -f "$DIR/$num/deps" ]; then
			cat "$DIR/$num/deps"
		fi
		;;
	esac
	;;
esac
