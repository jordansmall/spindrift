`status=ready` = your branch is written to the outbox and
`driver-exec signal pr-intent` exited zero (`signal pr-intent accepted: ...`),
so your PR intent is queued — not that a PR already exists. The launcher
relays your branch, opens the draft PR from that intent, then takes it from
there as OPEN A PULL REQUEST above describes. If the command never exited
zero, even after you fixed each rejection's reason and sent it again, no
intent is queued: print `status=blocked` instead, with a `note=` naming the
rejection. You never run `gh pr create`, `gh pr ready`,
`gh issue edit ... --add-label ${COMPLETE_LABEL}`, or `gh pr merge` — your
token cannot.
