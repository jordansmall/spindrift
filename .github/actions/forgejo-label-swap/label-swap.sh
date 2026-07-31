#!/usr/bin/env bash
# Forgejo REST label swap: optionally post a comment, then add and remove a set
# of labels on one issue. The .forgejo/workflows control-plane set (issue #1967)
# calls this for the up-front claim, the dispatch "release if blocked" undo, and
# the recover "park if nothing to recover" swap. No `fj`/`gh` label verb is
# guaranteed on a self-hosted runner, and a Forgejo token cannot authenticate
# against api.github.com, so every label mutation goes through the Forgejo REST
# API here. Inputs arrive as SWAP_* environment variables set by action.yml.
set -euo pipefail

: "${SWAP_BASE_URL:?}" "${SWAP_TOKEN:?}" "${SWAP_REPO:?}" "${SWAP_ISSUE:?}"
add_labels="${SWAP_ADD_LABELS:-}"
remove_labels="${SWAP_REMOVE_LABELS:-}"
comment="${SWAP_COMMENT:-}"

api="${SWAP_BASE_URL%/}/api/v1/repos/${SWAP_REPO}"
auth="Authorization: token ${SWAP_TOKEN}"

# Optional comment first, so the human-facing note lands even if a later label
# call fails (e.g. a target label is absent from the repo).
if [ -n "$comment" ]; then
  curl -fsS -X POST -H "$auth" -H "Content-Type: application/json" \
    "${api}/issues/${SWAP_ISSUE}/comments" \
    -d "$(jq -n --arg b "$comment" '{body: $b}')" >/dev/null
fi

# Page through every repo label — the default page size can hide labels past the
# first page in a repo with many of them — and build one combined name->id list.
labels='[]'
page=1
while :; do
  chunk=$(curl -fsS -H "$auth" "${api}/labels?page=${page}&limit=50")
  n=$(printf '%s' "$chunk" | jq 'length')
  [ "$n" -eq 0 ] && break
  labels=$(jq -s 'add' <(printf '%s' "$labels") <(printf '%s' "$chunk"))
  [ "$n" -lt 50 ] && break
  page=$((page + 1))
done

label_id() {
  printf '%s' "$labels" | jq -r --arg n "$1" '.[] | select(.name == $n) | .id' | head -n1
}

# Add: every named label must exist on the repo (the claim/park targets are
# lifecycle labels `spindrift doctor` provisions), so an unresolved id is a hard
# error rather than a silent skip.
read -ra add_arr <<<"$add_labels"
add_ids=()
for name in ${add_arr[@]+"${add_arr[@]}"}; do
  id=$(label_id "$name")
  if [ -z "$id" ]; then
    echo "::error::label not found on ${SWAP_REPO}: ${name} (run 'spindrift doctor' to create the lifecycle labels)" >&2
    exit 1
  fi
  add_ids+=("$id")
done
if [ "${#add_ids[@]}" -gt 0 ]; then
  ids=$(
    IFS=,
    printf '%s' "${add_ids[*]}"
  )
  curl -fsS -X POST -H "$auth" -H "Content-Type: application/json" \
    "${api}/issues/${SWAP_ISSUE}/labels" -d "{\"labels\": [${ids}]}" >/dev/null
fi

# Remove: a label absent from the repo has nothing to detach, so skip an
# unresolved id instead of failing the step — an unpaired DELETE on an empty id
# would hit .../labels/ and 404, killing the workflow.
read -ra rm_arr <<<"$remove_labels"
for name in ${rm_arr[@]+"${rm_arr[@]}"}; do
  id=$(label_id "$name")
  [ -z "$id" ] && continue
  curl -fsS -X DELETE -H "$auth" "${api}/issues/${SWAP_ISSUE}/labels/${id}" >/dev/null
done
