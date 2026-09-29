# The butler lands bounded patches through the host

Status: proposed

## Context

[ADR 0056](0056-the-butler-is-an-issueless-dispatch-kind-with-its-state-in-git.md)
made the butler an advise-only Dispatch kind: one read-only Box scans a
Chore and relays findings to the Filer; it never pushes code and never
touches the Code Forge. It added one opt-in exception to "a finding never
carries a dispatch label": a finding may be filed already
`ready-for-agent` when its class is on a host-side allow-list, host
limits hold, and an in-Box reviewer concurred. The allow-list is the
trust gate; the reviewer is a quality filter only, because it reads the
same repo content as the scanner.

Promotion still buys a second Box to type one line. Issue #3857 is the
shape: a fully specified docs finding — drop an ADR range that had gone
stale — drew two research passes and then a full work dispatch queue,
each a Box boot, for a one-line deletion. The `docs-drift` Chore will
keep producing findings of exactly this class, and the cheapest thing
the butler can do with them today is hand them to a work agent.

ADR 0056 rejected "a writable Box that opens PRs for cheap fixes"
because it mixes research-shaped and work-shaped settle in one run and
raises the Box's trust bar. Both reasons stand. Neither applies to the
**host** landing a change the Box only proposed, if the host can bound
that change without trusting the Box, and if the change then passes the
same merge gates every PR passes.

## Decision

**For allow-listed patch classes, the butler Box attaches a unified diff
to its finding, and the host — never the Box — applies it, pushes a
branch, opens a PR, and hands that PR to the existing work merge gate.**
This is a second rung above promotion, off by default, with its own
budget.

**The Box's role does not change.** It produces the diff in the
throwaway clone it already has, against the commit it scanned, only for
classes the host told it may patch, and relays it as one more field on
the finding. It still never commits, pushes, or touches the Code Forge.
The signal socket's rule that content is never destination holds: a
diff names no branch, label, or target.

**The host applies only what it can bound without trusting the Box.**
Before anything touches git, all of these must hold, and any failure
falls through to the ordinary promote decision, never to an error:

1. the finding's class is on a separate, host-side *patch* allow-list
   for the Chore, which must be a subset of the promotion allow-list;
2. every path in the diff matches a host-side path allow-list whose
   default admits documentation and denies every file an agent reads as
   prompt input (CLAUDE.md, CONTEXT.md, CONTRIBUTING.md, AGENTS.md,
   `docs/adr/`, skills, prompt templates and fragments, workflows);
3. the diff is modification hunks only — no add, delete, rename, mode
   change, or binary — within a file cap and a changed-line cap;
4. every diff path is one of the finding's own site keys, so the
   reviewer's "confined" rubric is enforced mechanically;
5. the diff applies cleanly to the scanned commit and to the current
   base head;
6. the reviewer, who saw the diff, concurred; and
7. the day's patch budget has room.

**The finding is still filed first.** The issue carries
`agent-butler-finding` and a new provenance label, `agent-butler-patch`,
and never a dispatch label. (Amended by issue #4074: unless the push or
PR create then fails and the finding falls back to promotion, which
adds the dispatch label to the already-filed issue; that fallback needs
a tracker that can add labels after filing — `github` or `forgejo` — and
that names the same backend as the Code Forge, so `Closes #N` and the
agent branch both land in that backend's own issue namespace; the rung
stays off on a `local` tracker or a mismatched forge/tracker pairing.)
The host commits the diff with its own identity onto the finding's
agent branch, pushes with the launcher credential, and opens a draft
PR that closes the finding and shows the reviewer's concurrence and a
visible "patched by the butler" note. The PR URL is recorded in the
Ledger, and the Chore's claim is released, before CI is polled, so a
crash while waiting never strands the claim.

**The PR then obeys every merge policy already set.** It enters the work
merge gate through its existing entry point — CI to green, the Merge
guard, then `MERGE_MODE` — with fix passes forced to zero. Under
`manual` a human merges. On red the PR stays draft with the usual
failure comment and the finding stays open under its two labels for a
human to relabel `ready-for-agent` or close; `agent-failed` is never
applied. On merge the finding is closed and marked `agent-complete`
like any landed work PR.

**Trust posture.** The class and path allow-lists, the caps, the
confinement check, and the apply check are host-owned and mechanical:
model output cannot widen them. The reviewer remains a quality filter.
The merge gate is unchanged, so a Consumer whose policy is `manual`
still has a human between a butler patch and `main`, and one whose
policy is `immediate` has chosen that for every PR, not this one.

## Considered options

- **Writable Box that opens PRs for cheap fixes.** Rejected in ADR 0056
  and still rejected: it raises the Box's trust bar and mixes two settle
  shapes. This ADR keeps the Box advise-only and moves the write to the
  host.
- **Git bundle through the outbox relay.** Reuses the read-only work
  Box's path verbatim. Rejected: a bundle is opaque to bounding until
  unpacked, the outbox is keyed by issue number, and it detaches the
  change from the finding that justifies it.
- **PR only, no finding issue.** Less tracker noise. Rejected: needs a
  new branch scheme, a dedup source that reads PRs, and leaves no trail
  when the PR is rejected.
- **Reuse the promotion allow-list.** One knob. Rejected: "worth a work
  agent" and "worth applying without one" are different bars, and
  promoting a bugs class must not silently enable patching it.
- **Forge auto-merge and walk away.** Frees the slot at once. Rejected:
  bypasses the Merge guard and `MERGE_MODE`, and a red PR sits with
  nobody told.
- **Fix passes on a red patch PR.** Parity with work PRs. Rejected: the
  first red would turn a free patch into the work dispatch the rung
  exists to skip.
- **Extend the rung to the Filer's review findings.** Would have caught
  #3857 directly. Deferred: the Filer runs inside a work Box that is
  already pushing code, so the trust story and settle path differ.

## Consequences

- The host gains a second write to the Target repo beyond the Ledger
  refs: a branch under `refs/heads/` carrying a commit the host built
  from a host-validated diff. The github and forgejo adapters gain
  host-credentialed push-branch and draft-PR creation regardless of Box
  access mode.
- The finding wire type gains an optional `patch` field; the Runner's
  single decision gains a Patch variant evaluated before Promote; the
  Room and the Ledger gain a patches dimension.
- CLAUDE.md's triage lifecycle gains `agent-butler-patch` beside
  `agent-butler-finding`, with the same "never a dispatch label" rule and
  two documented end states.
- A Daemon butler slot is held while a patch PR's CI is polled. That is
  idle time by construction, and a bounded docs change should be quick
  through CI; if it is not, the fix is a smaller CI path for docs-only
  PRs, not a change to this rung.
- This rung lands on spec #3985's shapes (one wire type, one `decide`,
  one Room, a Tree adapter) and is blocked by those four tickets. It is
  specified in issue #4001.
