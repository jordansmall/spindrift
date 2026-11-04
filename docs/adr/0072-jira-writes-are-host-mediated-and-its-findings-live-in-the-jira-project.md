# Jira writes are host-mediated, and its findings live in the Jira project

Status: proposed

## Context

ADR 0013 made the Issue Tracker and the Code Forge independent seams, so
a Jira project can pair with a GitHub (or Forgejo) repo. ADR 0034 then let
a Box run with no write token (`BOX_FORGE_AND_ISSUE_ACCESS=read-only`),
with the Launcher performing every write. A backend may run read-only only
when its registry row says it can do this host-side: bundle relay and
draft-PR create on the forge axis, host-posted comments and issue filing
on the tracker axis.

Jira already meets most of that bar without saying so. `JIRA_TOKEN` is
host-only (`boxEnv = false`). The Box has no Jira client in either access
mode, so the row sets `inBoxUnwritableTracker`. Research verdicts and
blocked-run notes already relay through the Launcher (issues #5245,
#5263). The adapter has `Comment`, but not `PostIssue`, so the row omits
`hostPostingCapable`, and read-only is refused on Jira.

The missing filer does more damage than the refusal does:

- **Findings leak to the wrong tracker.** A read-write work run with the
  Filer on takes the GitHub write arm and runs `gh issue create`, so its
  findings land in the GitHub repo's issues. Jira users don't watch that
  list. The host-side dedup index, which reads the Jira backlog, never
  sees them.
- **Relayed findings vanish.** A butler Box always relays findings, and a
  research run on Jira is forced onto the relay. Settle's filing step
  returns nothing for a tracker that cannot file, so those findings are
  dropped without a word (issue #6343).
- **The Filer searches the wrong tracker.** Its prompt's dedup step runs
  `gh issue list --search` against GitHub to look for Jira duplicates.

Three more facts shape the decision:

- **Raw Markdown.** Everything spindrift posts to Jira today goes out as
  Markdown on the v2 API, which Jira renders as wiki markup. Headings and
  links show literally, and so do the hidden HTML-comment markers.
  `<!-- spindrift-dedup: ... -->` ends every filed finding.
  `<!-- spindrift-research -->` opens every research verdict.
- **A conflated registry bit.** `LabelRegistry` answers two questions:
  can labels be listed and created ahead of time, and do issues wear
  labels? Jira is false on the first and true on the second. The tuning
  merge hold reads the bit as the second question. On a tracker without a
  registry it also holds on the `chore=tuning` marker term, which no
  human can remove. That defeats the documented override on Jira, which
  is to remove `agent-tuning-finding`.
- **Labels as a fallback lifecycle.** `JIRA_STATUS_MAPPING` already moves
  work through native workflow statuses. Labels are only the fallback for
  an unmapped state or an unavailable transition. But reads and the claim
  check always OR the labels in. `Ambiguous` cannot be mapped at all.
  Every transition is posted bare, so a workflow whose Done transition
  requires a Resolution fails with a 400, which is not the unavailable
  case and does not fall back. A team running a kanban board gets
  labels it never asked for, and closes no cards.

## Decision

1. **Findings live in the Jira project.** The Jira adapter implements
   `PostIssue`. On Jira, the Filer always relays to the host, whatever
   the access mode, as research verdicts already do. Every finding then
   lands in the same backlog the dedup index reads. Jira's row gains
   `hostPostingCapable`, so read-only becomes legal on Jira with a forge
   that can already run read-only (github, forgejo). Read-write and
   read-only then behave the same on Jira's tracker axis.
2. **One `JIRA_ISSUE_TYPE` knob, default `Task`.** Every filed finding
   gets that type. Provenance stays in labels. `doctor` checks the type
   against the project's create metadata and warns about any required
   field spindrift cannot fill.
3. **The adapter converts Markdown to Jira wiki markup.** This covers
   every body it sends, `Comment` and `PostIssue` alike, over the v2 API
   on both Cloud and Server/Data Center. The converter handles the subset
   spindrift emits: headings, lists, emphasis, inline and fenced code,
   links and simple tables. Anything else passes through as text. ADF
   (v3, Cloud only) is not used.
4. **Machine markers live in Jira entity properties.** Wiki markup has no
   comment syntax, so markers cannot stay hidden in the text. `PostIssue`
   moves the dedup marker line into an issue property in the create
   request itself. `Comment` moves the research marker into a comment
   property. On reads (`Issue`, searches, `Comments`), the adapter writes
   each marker back as its usual HTML-comment line. Settle's parser and
   the Box's prior-research check see unchanged text. Humans never see the
   markers and cannot damage them by editing. The marker grammar stays
   settle's; the adapter imports it rather than copying it.
5. **`IssuesWearLabels` is split from `LabelRegistry`.** `LabelRegistry`
   keeps its meaning for `doctor`: labels are registered objects. The new
   row bit means issues carry the labels applied to them. It is true for
   github, forgejo and jira, and false for local. The merge-by-hand hold
   and the marker substitute key on the new bit. On Jira the finding label
   alone holds a merge, and removing it is the override, as on GitHub.
6. **`JIRA_LIFECYCLE=hybrid|status`, default `hybrid`.** `hybrid` is
   today's behaviour. `status` drops the label fallback from the work
   lifecycle:
   - Reads and the claim check match the mapped status only.
   - Eval-time validation and the launcher's startup check both require
     every work state to be mapped.
   - When a mapped transition is unavailable, the adapter returns an error
     naming the source status, the target status and the transitions on
     offer. Settle posts that error as a comment on the issue, so a card
     that didn't move says why, and the Record settles with its own
     reason.
   - `doctor` checks that each mapped status exists in the project's
     workflow.
7. **The mapping gains an `ambiguous` key, and only the human-triage
   terminals may share a status.** `failed` and `ambiguous` may map to
   one status, such as a Blocked column. No other two states may share a
   status, so a ready card can never read as claimed. Nothing in the
   dispatch loop reads the two apart: the Record keeps the reason, and an
   ambiguous run posts its questions as a comment. There is no
   `recoverable` key. Settle reaches `Recoverable` only on a forge that
   relays bundles and has no PR forge, and no forge Jira pairs with is
   both.
8. **A mapping value is a status name or a `{status, resolution}`
   object.** The object form sends `fields.resolution` with the
   transition. Before posting, the adapter reads the transition's field
   metadata (`expand=transitions.fields`). If a required field is still
   unset, it fails with an error naming the field, without posting. This
   holds in both lifecycle modes. Plain strings keep working unchanged.
9. **On Jira the Filer runs no in-Box tracker search.** It takes the
   local tracker's dedup fragment shape. The Box cannot reach Jira, and
   `gh` would search the wrong tracker. The host's site-key dedup over
   the relayed intents is the whole check.

Out of scope:

- research states stay on labels in both lifecycle modes (ADR 0022,
  issue #4919), since an issue can sit in both lifecycles at once;
- Jira gets no Actions trigger;
- linked Jira issues are not injected host-side (issue #6344 fixes the
  wording only);
- Jira gets no `IssueCloser`, so the `complete` mapping closes the card
  (issue #4025);
- Jira gets no `AddLabels` or butler patch rung, since that rung needs
  the tracker and the forge on one backend;
- Jira gets no ADF;
- Jira comments get no new trust handling (issue #382).

## Considered Options

- **File findings in the Code Forge's issues.** Rejected. It keeps
  findings in a list Jira teams don't watch. It leaves dedup reading one
  tracker while filing goes to another, so no match is ever found. It
  also leaves read-only refused.
- **A per-kind or per-class issue type.** Deferred. Labels already carry
  provenance. If a project needs a map, one later extends the same knob
  to accept a JSON object.
- **Post raw Markdown, or ADF on Cloud.** Raw Markdown leaves every body
  cluttered and every marker visible. ADF would mean a second converter
  and a v3 write path beside the v2 reads, which isn't worth it for
  machine-written text.
- **Markers as a muted visible footer.** This was the fallback if entity
  properties proved unusable. It is atomic and simple. But it is visible,
  and anyone editing the text can mangle it.
- **Flip Jira's `LabelRegistry` to true.** Rejected. `doctor` would
  require labels that cannot exist before their first use, which is the
  failure behind issue #5460.
- **Derive status-only mode from a complete mapping.** Rejected. Adding
  the last mapping key would silently change how reads behave.
- **Keep one fallback label in status mode,** applying `agent-failed`
  when a transition is refused. Rejected. It brings back the label
  semantics status mode exists to remove. A comment is the signal people
  watching a kanban board actually see.
- **Pick a Resolution automatically.** Rejected. A guessed value can mark
  landed work "Won't Fix" on an oddly named scheme.
- **Inject a backlog snapshot into the Filer prompt,** or run fuzzy
  host-side text search. Deferred, and rejected. The snapshot is the
  upgrade if duplicates of human-filed issues show up. Fuzzy search
  without judgement would suppress real findings, which is worse than
  filing a duplicate.

## Consequences

- With `PostIssue` on Jira, the capability-keyed guards from issue #6343
  stop firing on Jira, so #6343 lands first.
- A Jira finding no longer appears in the GitHub repo's issues, and an
  existing Jira deployment's Filer switches trackers with this change.
- Entity properties on create and in search results are confirmed only
  against Cloud's API reference. Server/Data Center support is an open
  risk to check when Jira is tested by hand. If it fails there, the muted
  footer is the fallback, still inside the adapter.
- Status mode trades the fallback's guarantee of forward progress for a
  board free of labels. A refused transition now leaves a card where it
  is, with a comment, for a human to move.
- Jira stays documented as experimental until it has run against a real
  project.
