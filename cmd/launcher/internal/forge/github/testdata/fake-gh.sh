#!/bin/sh
# Stateful stand-in for the gh CLI behind the native label writes.
# Invocations share a STATE_DIR/issues/<num>/ tree rather than each returning a
# scripted response, so every call sees the previous calls' writes.
DIR="$STATE_DIR/issues"

# The trailing "." keeps a final newline alive through awk and the command
# substitution; tab and CR come from printf because POSIX sed has no \t escape.
json_escape() {
	tab=$(printf '\t')
	cr=$(printf '\r')
	out=$(printf '%s.' "$1" | sed -e 's/\\/\\\\/g' -e 's/"/\\"/g' -e "s/$tab/\\\\t/g" -e "s/$cr/\\\\r/g" | awk 'NR > 1 { printf "\\n" } { printf "%s", $0 }')
	printf '%s' "${out%.}"
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
issue-view)
	printf '{"labels":%s}' "$(labels_json "$3")"
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
esac
