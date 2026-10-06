#!/bin/sh
# Stateful stand-in for the gh CLI that forgetest.RunTrackerContract runs.
# Invocations share a STATE_DIR/issues/<num>/ tree rather than each returning a
# scripted response, so every call sees the previous calls' writes.
DIR="$STATE_DIR/issues"

json_escape() {
	printf '%s' "$1" | sed 's/\\/\\\\/g; s/"/\\"/g' | tr '\n' ' '
}

ordered_nums() {
	# Issue directory names are always plain digits, because the test creates
	# them, so ls's word-splitting risk does not apply.
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
	printf '['
	first=1
	for num in $(ordered_nums); do
		if [ -n "$label" ]; then
			labf="$DIR/$num/labels"
			if [ ! -f "$labf" ] || ! grep -qxF "$label" "$labf"; then
				continue
			fi
		fi
		[ $first -eq 0 ] && printf ','
		first=0
		title=$(cat "$DIR/$num/title" 2>/dev/null)
		printf '{"number":%s,"title":"%s","labels":%s}' "$num" "$(json_escape "$title")" "$(labels_json "$num")"
	done
	printf ']'
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
	*comments*)
		# The harness writes the native {"comments":[...]} document, so the wire
		# shape stays decided on the Go side rather than re-encoded here.
		if [ -f "$DIR/$num/comments" ]; then
			cat "$DIR/$num/comments"
		else
			printf '{"comments":[]}'
		fi
		;;
	*)
		printf '{"labels":%s}' "$(labels_json "$num")"
		;;
	esac
	;;
issue-edit)
	num="$3"
	shift 3
	add=""; removes=""
	while [ $# -gt 0 ]; do
		case "$1" in
		--add-label) add="$2"; shift 2 ;;
		--remove-label) removes="$removes
$2"; shift 2 ;;
		*) shift ;;
		esac
	done
	tmp="$DIR/$num/labels.tmp"
	: > "$tmp"
	if [ -f "$DIR/$num/labels" ]; then
		while IFS= read -r l; do
			[ -z "$l" ] && continue
			if printf '%s\n' "$removes" | grep -qxF "$l"; then
				continue
			fi
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
	*/issues\?*)
		# Conditional page read behind forge.DemandCounter: the open issues
		# carrying the labels= query value, with an Etag over the body.
		query=${path#*\?}
		case "$query" in
		*sort=updated*) ;;
		*) echo "fake gh: issues page must sort by update: $path" >&2; exit 1 ;;
		esac
		case "$query" in
		*per_page=100*) ;;
		*) echo "fake gh: issues page must be 100 per page: $path" >&2; exit 1 ;;
		esac
		want=$(printf '%s' "$query" | tr '&' '\n' | sed -n 's/^labels=//p' | sed 's/%20/ /g; s/+/ /g')
		if [ -n "$FAKE_GH_ISSUES_FAIL_STATUS" ]; then
			echo "$path - $FAKE_GH_ISSUES_FAIL_STATUS" >> "$STATE_DIR/api.log"
			printf 'HTTP/2.0 %s Error\r\n\r\n{"message":"boom"}' "$FAKE_GH_ISSUES_FAIL_STATUS"
			echo "gh: HTTP $FAKE_GH_ISSUES_FAIL_STATUS" >&2
			exit 1
		fi
		inm="-"
		shift 2
		while [ $# -gt 0 ]; do
			case "$1" in
			-H)
				case "$2" in
				If-None-Match:*) inm=${2#If-None-Match: } ;;
				esac
				shift 2
				;;
			*) shift ;;
			esac
		done
		body=$(
			printf '['
			first=1
			for n in $(ordered_nums); do
				labf="$DIR/$n/labels"
				if [ ! -f "$labf" ] || ! grep -qxF "$want" "$labf"; then
					continue
				fi
				[ $first -eq 0 ] && printf ','
				first=0
				title=$(cat "$DIR/$n/title" 2>/dev/null)
				printf '{"number":%s,"title":"%s","labels":%s}' "$n" "$(json_escape "$title")" "$(labels_json "$n")"
			done
			printf ']'
		)
		etag="\"$(printf '%s' "$body" | cksum | tr -d ' ')\""
		if [ "$inm" = "$etag" ]; then
			echo "$path $inm 304" >> "$STATE_DIR/api.log"
			printf 'HTTP/2.0 304 Not Modified\r\nEtag: %s\r\n\r\n' "$etag"
			echo "gh: HTTP 304" >&2
			exit 1
		fi
		echo "$path $inm 200" >> "$STATE_DIR/api.log"
		printf 'HTTP/2.0 200 OK\r\nEtag: %s\r\n\r\n%s' "$etag" "$body"
		;;
	esac
	;;
esac
