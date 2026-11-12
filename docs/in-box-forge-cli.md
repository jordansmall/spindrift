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

Fragment paths are under `templates/default/prompts/fragments/`. `github/` and
`forgejo/` variants of one use share a row. The access column comes from each
row's gate in `lib/fragments.nix` (and the gate's definition in
`cmd/launcher/internal/promptassembly/`).

| intent | fragment(s) | rendered by | access modes | r/w | planned replacement |
| ------ | ----------- | ----------- | ------------ | --- | ------------------- |
| search existing issues before filing a finding | `github/filer-dedup.md`, `forgejo/filer-dedup.md` | Filer pass (work, research, butler) | both | read | Signal-socket verb |
| open the draft pull request | `github/open-pr-create.md`, `forgejo/open-pr-create.md` | work, implement pass | `read-write` | write | removed along with `read-write` mode |
| check for an existing pull request and open a draft one when blocked | `github/if-blocked-pr.md`, `forgejo/if-blocked-pr.md` | work, implement pass | `read-write` | write | removed along with `read-write` mode |
| comment on the issue with what is done and what remains when blocked | `github/issue-blocked-comment.md`, `forgejo/issue-blocked-comment.md` | work, implement pass | `read-write`, on a tracker with an in-Box client | write | removed along with `read-write` mode |
| post the research verdict comment | `github/research-verdict.md`, `forgejo/research-verdict.md` | research | `read-write`, without a Filer, on a tracker with an in-Box client | write | removed along with `read-write` mode |
| report the posted verdict comment's URL as the outcome's `landing` | `github/research-outcome-landing.md`, `forgejo/research-outcome-landing.md` | research | `read-write`, without a Filer, on a tracker with an in-Box client | write | removed along with `read-write` mode |
| ensure the finding label exists before filing | `github/filer-direct-label.md`, `forgejo/filer-direct-label.md` | Filer pass (work only) | `read-write`, on a tracker with an in-Box client | write | removed along with `read-write` mode |
| file a finding as an issue directly | `github/filer-direct-file.md`, `forgejo/filer-direct-file.md` | Filer pass (work only) | `read-write`, on a tracker with an in-Box client | write | removed along with `read-write` mode |

Every write use already has a host-mediated form under `read-only`: the PR
intent, the issue intent, the outcome line's `note=`, and the research verdict
relay (see [Read-only Box](reference.md#read-only-box-box_forge_and_issue_accessread-only)).
The write rows above are therefore reachable only in a `read-write` Box, and
disappear with that mode. A research Box with the Filer provisioned relays its
verdict even in `read-write`, and so does a Box on a tracker with no in-Box
client (jira), which is why the research rows carry the Filer condition and
every issue-side write row carries the tracker one.

## Reachable under `read-only`

A `read-only` Box still reaches one in-box read use, not gated on access
mode:

- The Filer's dedup searches: `github/filer-dedup.md`,
  `forgejo/filer-dedup.md`. Gated on the tracker being writable, in either
  access mode and whichever way the Filer files.

This is the set that #6900 ("A read-only Box holds no forge credential")
drives to empty. The fix pass already reads CI failure detail from the
launcher-injected `CI FAILURE` section (`ci-failure.md`), which carries each
failing job's log tail, so `fix-ci-read` instructs no forge call. The planned
replacement for the rest:

- The Filer's dedup becomes a Signal-socket search verb. Host-side dedup over
  relayed intents is not a substitute: it matches by exact site key and
  overlapping line range only, never by subject, which is why the dedup search
  exists.

Parent and linked issue reads have already moved: `issue-read.md`,
`research-issue-read.md` and `scout-issue-read.md` name `driver-exec signal
issue <ref>` (issue #6894), a Signal-socket verb the host answers from the
Dispatch's tracker, and no forge CLI. They render for every tracker with a
host-side issue read (github, forgejo, jira) under the `socket` carrier; under
the `log` carrier, which has no socket, the `*-unreadable` sibling renders
instead, and `local` keeps its own folder fragment.
