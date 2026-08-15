# Research filing is host-mediated: the Filer relays, the launcher writes

A research Dispatch (ADR 0022) is advise-only — its prompt says "Comments
only," no filing step exists in any research prompt, and even an off-script
attempt dead-ends: under a read-only Box the `gh` shim blocks direct
`gh issue create`, and relayed `SPINDRIFT_ISSUE_INTENT` lines are decoded by
the dispatch scanner but silently dropped, because `fileIssueIntents` is a
method on the work-path settle that `ResearchSettle` never calls. Yet the
survey-style use ("evaluate this module; file bug/feature/chore issues for
what you find") is real and currently structurally impossible. This ADR gives
research exactly one new power — filing issues — while keeping every other
advise-only guarantee, under one governing shape: **in a research run the Box
never writes to the Issue Tracker at all; the Filer relays intents and the
verdict comment, and the launcher performs every write at settle.**

The decision set:

- **Advise-only for code is untouched.** Findings that imply code changes
  ("remove this module," "prune these methods") are *filed as issues*; the
  human-promotes → `ready-for-agent` → work-path chain executes them.
  Research still never opens a PR, watches CI, or merges.

- **The researcher delegates to the Filer; the Filer is relay-only in
  research.** The research prompts gain the filing step (rendered only when
  the Filer is provisioned), and the write-path gate for research is simply
  `kind == research && Filer provisioned → relay` — **no orchestrator
  condition**, unlike the work path's `!BoxWriteEnabled && orchestratorEnabled`
  relay gate, which is a work-path rule research does not inherit. This holds
  in every mode: CI read-write, CI read-only, local, dogfood. A read-write
  research Box therefore *stops* writing the tracker even though its token
  (Issues RW) would allow it — the advise-only story is structural, not
  token-dependent.

- **Filing is orthogonal to the verdict, and the one comment enumerates it.**
  Any verdict (`recommend`/`reject`/`unclear`) may carry 0..N filings. When
  the Filer is provisioned, the verdict comment always relays too
  (`SPINDRIFT_COMMENT`); at settle the launcher files the intents first, then
  posts the single verdict comment with a "Filed issues" section carrying the
  real URLs, then applies the verdict label. One comment, correct links,
  single writer.

- **Provenance label: `agent-research-finding`.** Same contract as
  `agent-review-finding` (never a dispatch label; a human promotes), created
  advisorily by `doctor` like the rest of the research family. The relay's
  hard-coded `agent-review-finding` label becomes a parameter keyed on the
  calling settle.

- **Types are a closed enum, mapped host-side.** The intent payload gains an
  optional `type` field limited to `bug` | `enhancement` | `chore`; the
  launcher owns the type→label mapping (labels ensure-created best-effort)
  and ignores anything outside the enum. The Box names a *type*, never a
  label, so dispatch labels stay structurally unwritable by the Box.

- **Filing failures are non-fatal.** If the researcher reached a verdict, the
  run concludes with that verdict; a finding whose filing failed appears
  inline in the verdict comment (title + summary) so nothing is lost.
  `agent-research-failed` keeps meaning "no verdict was produced," never "a
  side effect hiccuped." Every filed issue backlinks its source research
  issue.

- **Provisioning the Filer is the only switch.** Roster model presence — the
  existing idiom — enables research filing; no new knob. Comment-only research
  is had by not provisioning the Filer.

- **Self-contained research (ADR 0022 amendment two) files too.** The filing
  step is authored once and inject-shared into both research prompts via the
  prompt-contract registry (warn-severity marker rows, per the registry's
  zero-false-positive rule), and the contract asserts a rendered research
  prompt never contains the direct-file fragments — today's silent dead-end
  becomes a build-time impossibility.

- **Verdict vocabulary is unchanged.** A survey ticket concludes `reject`
  ("nothing here to promote; findings filed; close me") or a consumer-defined
  verdict via the configurable `RESEARCH_VERDICTS` knob. **Revisit trigger**:
  if real usage shows consumers independently inventing a `surveyed`-style
  verdict, add a default verdict then — not before, since a `filed` verdict
  would re-couple filing to the verdict, which this ADR deliberately keeps
  orthogonal.

## Considered Options

- **Who writes: researcher direct-files / Filer direct-files / Filer relays,
  launcher writes.** Direct filing by the researcher duplicates the Filer's
  judgment and dedup discipline into a second prompt. Direct filing by the
  Filer works only on a read-write Box, resurrects the read-only dead-end,
  and gives research two divergent write paths. Chosen: relay-only — the
  launcher already posts the research comment host-side in read-only/local
  modes, so this makes one flow universal, keeps the Box tracker-write-free,
  and lets settle order writes (file → comment-with-links → label).

- **Issue links in the comment: Box posts with titles only / host posts a
  second links comment / host posts the one comment after filing.** Titles
  only leaves the auditable conclusion with dangling references; a second
  comment breaks the one-research-comment contract. Chosen: the host posts
  the single comment last, links included.

- **Labels: free-form from the Box / none / closed type enum.** Free-form
  hands the Box a label pen across spindrift's trust boundary — a confused or
  injected Box could name `ready-for-agent`. No types loses the machine-
  readable triage the survey use case wants. Chosen: closed enum with the
  host owning the mapping.

- **Enablement: Filer provisioning alone / a separate research-filing knob /
  per-ticket opt-in.** A second knob has no named consumer and doubles the
  documentation/contract surface; ticket-phrasing-as-gate is fragile. Chosen:
  provisioning alone, matching the work path's idiom; a knob can be
  retrofitted behind the same gate if a real need appears.

## Consequences

- `ResearchSettle` learns to file issue intents (the parameterized
  `fileIssueIntents`), in order: file → post verdict comment with links →
  apply verdict label. The work-path settle is unchanged except for the label
  parameter.
- `doctor`'s advisory research label set gains `agent-research-finding`.
- The prompt-contract registry gains inject rows for the filing step in both
  research kinds, warn-severity relay-marker validation, and a reject-class
  assert that research prompts never render `FILE_ISSUES_DIRECT` fragments.
- With the Filer provisioned, a read-write research Box's tracker writes drop
  to zero — an observable behavior change for read-write consumers, and a
  deliberate one.
- The work-path Filer can adopt the `type` enum for free (shared intent
  grammar), but nothing requires it to.
