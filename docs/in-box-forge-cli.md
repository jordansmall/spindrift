# In-Box forge CLI uses

This page inventories every way a shipped prompt or fragment tells the agent
in the Box to use a forge CLI: `gh`, `fj`, a `curl` against a forge API, or
the generic "read it via the forge" instruction. The Box is meant to hold no
forge credential (issue #6757), and nothing an agent relies on should be taken
away blind, so each use is recorded here with the access modes it applies in
and what will replace it.

[`nix/checks/in-box-forge-cli-allowlist.nix`](../nix/checks/in-box-forge-cli-allowlist.nix)
pins the set shrink-only. A template under `templates/default/` that starts
instructing a forge CLI and is not on the allowlist fails the check, and an
allowlisted template that stops instructing one fails it too, until its entry
is removed. A negation ("do NOT run `gh pr merge`") does not count as an
instruction. The check also fails when the Inventory table and the allowlist
name different fragments, so remove the row here and the allowlist entry
together when a use goes away. Never add one.

Harness-owned skills under `templates/default/skills/` carry no forge CLI
instruction today. `prompts/fix-prompt.md` and the `*-outbox*` and
`outcome-ready-means-*` fragments mention `gh`, but only to forbid it. The
runtime backstop for a prohibited command is the shim and git-hook guards in
`cmd/launcher/internal/readonlyguards`. Dogfood-only skills (root `skills/`,
wired via `nix/dogfood-skills.nix`) are outside the check's corpus: they are
this repo's own dev skills, not shipped to the Boxes of other repos.

## Inventory

Empty. Issue #6901 removed the `read-write` Box access mode, and with it the
last fragments that told the agent to use a forge CLI: the in-Box issue
comment, research verdict, landing report and the Filer's direct label and
file steps. Every write now has a host-mediated form: the PR intent, the
issue intent, the outcome line's `note=`, and the research verdict relay (see
[Read-only Box](reference.md#read-only-box-box_forge_and_issue_accessread-only)).
Fragment paths would be listed as the Box sees them, under
`templates/default/prompts/fragments/` or a Connector's `fragments/` folder,
one row per use, with the `github/` and `forgejo/` variants sharing a row.

## Reachable under `read-only`

A `read-only` Box reaches no in-box read use: #6900 ("A read-only Box holds
no forge credential") drove that set to empty. The fix pass reads
CI failure detail from the launcher-injected `CI FAILURE` section
(`ci-failure.md`), which carries each failing job's log tail, so
`fix-ci-read` instructs no forge call.

Parent and linked issue reads have already moved: `issue-read.md`,
`research-issue-read.md` and `scout-issue-read.md` name `driver-exec signal
issue <ref>` (issue #6894), a Signal-socket verb the host answers from the
Dispatch's tracker, and no forge CLI. They render only when the host built an
issue reader for a `socket`-carrier dispatch and forwarded
`BOX_SIGNAL_ISSUE_READ` (issue #6924); otherwise, and always under the `log`
carrier, which has no socket, the `*-unreadable` sibling renders instead, and
`local` keeps its own folder fragment.

The Filer's dedup has moved the same way: `github/filer-dedup.md`,
`forgejo/filer-dedup.md` and `jira/filer-dedup.md` run `driver-exec signal
issue-search` (issues #6895, #6924) wherever the host built a searcher and
forwarded `BOX_SIGNAL_ISSUE_SEARCH`. A tracker without one, and any tracker
under `log`, render `filer-dedup-unwritable.md`, which runs no search:
host-side dedup over relayed intents matches by exact site key and
overlapping line range only, never by subject, so subject-level suppression
is lost there. `local` keeps its own folder fragment.
