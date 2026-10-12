#!/usr/bin/env bash
# The single required status check's verdict (issue #5155). Reads the
# workflow's `needs` context as JSON on stdin; the arguments name the jobs the
# change kind requires. A skipped required job fails, so a run that skipped
# everything can never pass.
set -euo pipefail

if [ "$#" -eq 0 ]; then
  echo "usage: ci-ok.sh <required-job>... <needs.json" >&2
  exit 2
fi

needs=$(cat)
bad=0

while IFS=$'\t' read -r job result; do
  case $result in
    failure | cancelled)
      echo "::error::job '$job' finished $result"
      bad=1
      ;;
  esac
done < <(jq -r 'to_entries[] | [.key, .value.result] | @tsv' <<<"$needs")

for job in "$@"; do
  result=$(jq -r --arg job "$job" '.[$job].result // "missing"' <<<"$needs")
  if [ "$result" != success ]; then
    echo "::error::required job '$job' is $result, not success"
    bad=1
  fi
done

exit "$bad"
