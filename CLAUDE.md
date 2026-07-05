# spindrift

## Issue tracker

Issues live on GitHub (`jordansmall/spindrift`). File agent-ready issues via the
`/to-issues` skill, never ad-hoc `gh issue create`.

### Triage label lifecycle

Agent issues move through these labels (see `.github/workflows/agent-dispatch.yml`):

- `ready-for-agent` — fully specified, ready for an AFK agent to pick up. **File new
  agent-ready issues with this label.**
- `agent-trigger` — adding it to an issue fires one dispatch run; the workflow claims
  the issue by swapping `agent-trigger`/`ready-for-agent` → `agent-in-progress` up front.
- `agent-in-progress` — an AFK agent is actively working the issue.
- `agent-complete` — agent work merged and green.
- `agent-failed` — the Box exited non-zero; needs human triage, re-label to retry.

### Comment injection trust boundary

The label gates which issues get dispatched — only triage-role holders can apply
it. But once labeled, the issue body and **every comment from any GitHub user**
feed the agent as prompt input. The trust boundary is the label, not the issue or
comment author.

## Running `gh`

`gh` commands need network + the macOS keychain, which the command sandbox blocks
(TLS cert failure via trustd; token unreadable). Run `gh` **outside the sandbox**
(`dangerouslyDisableSandbox: true`) on the first attempt so a failed-then-retried
call doesn't fire a mutating action twice.
