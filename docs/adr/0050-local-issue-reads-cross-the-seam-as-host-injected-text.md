# Local issue reads cross the seam as host-injected text, not a mount

## Context

[ADR 0032](0032-host-mediated-local-issue-content.md) gave `local`'s
issue-plane read a read-only mount: the Launcher RO bind-mounts
`LOCAL_ISSUES_DIR` into the Box at `/issues`, and the agent reads
`/issues/${ISSUE_NUMBER}.md` and follows its own `## Blocked by`/`parent`
links from the folder. That ADR considered and rejected forwarding the body
(and a linked-issue bundle) via an env var instead, on two stated grounds:
the forwarding plumbing didn't exist yet, and a forwarded bundle could only
give "no linked-issue enrichment beyond one hop" without becoming a much
bigger undertaking than the mount's "free" transitive folder walk. The mount
became, in its own words, "the single, named exception" to the Box's
zero-shared-host-filesystem rule.

Both grounds have since lapsed. Issue #3445 built the forwarding plumbing
ADR 0032 said didn't exist: the Launcher now resolves the subject issue's
body (plus a last-10-comment snapshot, for trackers that have comments)
host-side and injects it into the Box as an `ISSUE_TEXT` env var, rendered
into a `# ISSUE TEXT` prompt section by `promptassembly` — every tracker's
`*-prompt.md` fragment now reads the subject issue this way, `local`
included, rather than fetching it itself. Issue #3469 then closed the
one-hop gap directly: the Launcher walks a local issue's `## Blocked by` and
`parent:` references *transitively*, the same enrichment the `/issues`
folder walk used to give the agent, and renders the whole chain into the
same `ISSUE_TEXT` section as a `## Linked issues` block. With host-side
injection now doing everything the mount did — subject body, and the full
linked-issue chain, not just one hop of it — the mount has nothing left to
provide that injection doesn't already cover, and it becomes exactly the
kind of exception the zero-shared-host-filesystem rule exists to keep to a
minimum: one that no longer earns its keep.

## Decision

**Local issue reads cross the seam as host-injected text. The `/issues`
mount is retired; the Box never sees a bind of `LOCAL_ISSUES_DIR` again.**
This is ADR 0032's own rejected "forward the body via an env var" option,
minus the one-hop ceiling that used to be the reason not to take it — #3445
and #3469 together are what changed between then and now, not a change of
mind about the mount's security properties (see ADR 0032's own Security
section, which this ADR does not revisit).

- **Shape.** `LinkedIssueLister` is a new optional Issue Tracker surface,
  discovered by type assertion the same way `CommentLister` already is:
  `LocalTracker` implements it, and it returns plain data — a reference, the
  relation it was reached by (`blocked-by` or `parent`), the depth it was
  found at, either a resolved issue or a read error, or an unresolved
  marker for a reference that doesn't name a local issue at all.
  `forge.IssueText` alone renders, orders, and budgets that data; a remote
  tracker (`github`, `forgejo`, `jira`) implements no such surface, so its
  output is byte-identical to before this feature existed.
- **Links followed.** `## Blocked by` slugs are followed transitively, and
  so is each linked issue's own `## Blocked by` list. A `parent:`
  reference is followed, and its own links walked in turn, only when its
  raw value exactly matches a local issue slug — a `parent:` pointing at a
  URL or a Jira key is not walked, it is recorded as unresolved instead.
  Cycles, and a back-link to the subject issue itself, terminate the walk
  rather than looping; an issue reachable by more than one path is rendered
  once, at whichever depth reaches it first.
- **Rendering.** The subject issue's body renders first, exactly as before;
  a `## Linked issues` section follows it when the walk finds anything.
  Each entry is a heading naming the linked issue's slug and title and the
  relation it was reached by relative to the issue that linked it (for
  example "blocked-by of seam-b", "parent of seam-b"), one status line
  built only from frontmatter the local adapter already records — open or
  closed, `landing`, `abandoned` — never anything derived, and then the
  linked issue's full body, including its own `## Comments` section if it
  has one. Raw frontmatter is never dumped into the rendered text. A
  reference that doesn't resolve to a file, and an entry that failed to
  read or parse, are both listed by slug with the reason — never silently
  dropped, so the agent can tell a missing link from an absent one.
- **Budget.** One 64KB budget (`forge.maxIssueTextBytes`) covers the whole
  `ISSUE_TEXT` value, subject and linked issues together, not a separate
  allowance per section. The subject body is admitted first, with its
  existing tail-truncation behavior kicking in only if the subject alone
  already exceeds the cap; what's left is spent on direct blockers, then
  the parent, then transitive links in depth order. An entry that doesn't
  fit is omitted whole, by slug, under an "Omitted for size" list — never
  cut mid-body, since a half-rendered issue is worse than a clearly-marked
  absence — while a smaller, lower-priority entry that does still fit is
  still admitted rather than losing its slot to the one that didn't.
- **Failure contract.** The subject issue is load-bearing: a read failure
  there fails the dispatch outright, is not retried, and surfaces as
  `agent-failed`, because every prompt fragment unconditionally tells the
  agent its body is in `ISSUE_TEXT` and gives it no fallback to fetch the
  tracker itself. A linked issue is not load-bearing the same way: a read
  or parse failure there degrades to an unresolved entry in the rendered
  text and never fails the dispatch that would otherwise have proceeded on
  the subject alone.
- **`ISSUE_TEXT` reaches the Box as an env var, not a prompt-fragment
  argument.** It lands as `$ISSUE_TEXT` exactly the way `ISSUE_NUMBER` and
  `ISSUE_TITLE` already did, through the same per-dispatch env map every
  other injected box value travels through — no `*-prompt.md` fragment
  builds a command line out of it.
- **`ISSUE_TEXT` is to reach the runner process's own environment, never the
  runner's command line — pending, tracked by #3470.** A container's or
  sandbox's argv is world-readable through `ps` and `/proc/<pid>/cmdline` for
  the Box's whole lifetime, and ADR 0032 kept local issue content off the
  host-Box seam's readable-by-anyone surface on purpose; this ADR's
  injection makes that property *more* load-bearing than it was under the
  mount, since the whole linked-issue chain — not just the subject body —
  now rides the one `ISSUE_TEXT` value. The decision was taken in #3468's
  grilling alongside the rest of this ADR's contract, but it has not
  landed: today `ISSUE_TEXT` is not a member of either runner's small
  named-credential env-only set (`bwrapSecrets` in
  `cmd/launcher/internal/runner/bwrap.go` — `GH_TOKEN`,
  `CLAUDE_CODE_OAUTH_TOKEN`, `ANTHROPIC_API_KEY`, `OPENCODE_AUTH_CONTENT`,
  `REGISTRY_PROXY_TCP_SECRET`, `FORGEJO_TOKEN`), so it rides the ordinary
  per-dispatch `box.Env` loop onto the runner's command line like any other
  non-secret value: `--setenv ISSUE_TEXT …` under bwrap (that `box.Env` loop
  in `cmd/launcher/internal/runner/bwrap.go`, gated only by
  `!bwrapSecrets[k]`) and bare `-e ISSUE_TEXT=…` under OCI (the same loop in
  `cmd/launcher/internal/runner/oci.go`, where the same `bwrapSecrets[k]`
  check gates the argv-free `-e KEY` form vs. the OCI `-e KEY=VALUE` form).
  Closing #3470 moves `ISSUE_TEXT` into that set, so it instead ships to the
  sandboxed process through `resolvedRunEnv`
  (`cmd/launcher/internal/runner/bwrap.go`) or `ociRunEnv`
  (`cmd/launcher/internal/runner/oci.go`) the same way the named
  credentials already do.

## Considered Options

- **Leave the `/issues` mount in place and let it fall out of use on its
  own** — the fragments could simply stop reading it once injection covers
  everything the mount does. Rejected: an unread mount is not a neutral
  no-op, it is a live exception to zero-shared-host-filesystem that keeps
  costing runtime plumbing (`MountParams`, `absLocalIssuesDir`, the
  bwrap/OCI bind logic, the tests asserting its shape) for zero remaining
  benefit, and a stale mount is exactly the kind of thing a future change
  might start depending on again by accident rather than by decision.
- **Keep the mount as a fallback alongside the injection**, so a bug in the
  new host-side walk has a folder to fall back on. Rejected: this leaves
  two read paths for the same content, one of them untested and unused in
  the normal case, and the mount's exception to zero-shared-host-filesystem
  survives for a fallback nobody exercises — if the injection path has a
  bug, the fix is to fix the injection path, not to keep a second one on
  standby.
- **Re-name the issues directory into the prompt fragments again** (a
  literal path the fragment tells the agent to read, rather than a
  rendered section) as a smaller change than retiring the mount outright.
  Rejected: this was #3468's own framing of the band-aid it explicitly
  chose not to take — it leaves the host/Box split exactly where it was,
  keeps the exception on the books, and buys nothing the full retirement
  doesn't already buy at the same cost.

## Consequences

- **ADR 0032's read half is superseded by this ADR** (see the banner on
  that ADR); its write half — the Box never touches the tracker, emits its
  comment on stdout, and the Launcher posts it — is unchanged and this ADR
  does not revisit it.
- The `/issues` mount, its `MountParams` fields, and the host-side
  `absLocalIssuesDir` resolution are removed from both runners identically
  (issue #3471). `LOCAL_ISSUES_DIR` itself is not retired — it is still how
  the Launcher finds a local issue's file host-side to build `ISSUE_TEXT`
  and to drive every lifecycle-plane operation (`reconcile`, `settle`,
  `CloseIssue`) that already ran host-side under ADR 0032. The
  `InBoxUnreachableTracker` signal is unchanged too; it still drives
  host-side reconcile and the fully-local derivation, independent of
  whichever content-plane mechanism is in use.
- **This retires the issue-plane exception only.** Under `CODE_FORGE=local`
  the read-only Accumulation repo mount and the writable outbox
  ([ADR 0033](0033-host-mediated-local-code-forge.md)) remain documented
  exceptions to zero-shared-host-filesystem — this ADR does not touch the
  code plane, and zero-shared-host-filesystem is not restored as a whole
  claim by this change.
- `gitlab`/`bitbucket`/`jira` continue to land on the remote path (in-box
  client, read and write) as their adapters arrive; `LinkedIssueLister` is
  an opt-in surface, not a requirement any of them need to implement.
