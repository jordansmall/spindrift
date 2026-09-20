#!/usr/bin/env bash
# Forgejo REST issue close for the .forgejo/workflows control plane (issue
# #1967). Forgejo issues carry no `state_reason`, so there is no "not planned"
# to send — the close is plain, and the `agent-research-reject` label left on
# the issue is what records why it was closed.
set -euo pipefail

: "${CLOSE_BASE_URL:?}" "${CLOSE_TOKEN:?}" "${CLOSE_REPO:?}" "${CLOSE_ISSUE:?}"

api="${CLOSE_BASE_URL%/}/api/v1/repos/${CLOSE_REPO}"
auth="Authorization: token ${CLOSE_TOKEN}"

# Idempotence lives here rather than in the workflow's `if:` guard: the GitHub
# mirror can read `github.event.issue.state` off the webhook payload, but the
# Forgejo payload is not guaranteed to carry it. Re-labeling an already-closed
# issue is a no-op, never a reopen-and-reclose.
state=$(curl -fsS -H "$auth" "${api}/issues/${CLOSE_ISSUE}" | jq -r '.state')
if [ "$state" != "open" ]; then
  echo "issue ${CLOSE_ISSUE} is already ${state} — nothing to close"
  exit 0
fi

curl -fsS -X PATCH -H "$auth" -H "Content-Type: application/json" \
  "${api}/issues/${CLOSE_ISSUE}" -d '{"state": "closed"}' >/dev/null
