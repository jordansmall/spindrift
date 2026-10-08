# The tuning Chore argues from a host-rendered digest of Dispatch Records

Status: proposed

## Context

ADR 0061 gives every Dispatch a durable Dispatch Record and `spindrift
stats` to read them. That shows where the money goes. It does not turn it
into changes. Someone still has to read the tables, guess the cause, and
trace it to a prompt, model, or knob. The 2026-10-07 baseline shows the
gap: about $2,723 notional over 3,594 passes, with success at ~95%+ and
cost as the lever. For example, delta-review approved 325 of its 329
verdicts for $219.

The Butler (ADR 0056) is already the idle-tier, advise-only kind that files
findings a human promotes. But every Chore so far scans code, and four
facts get in the way of a Chore that scans history instead:

- **The Box cannot see the Records.** Its mounts are the prompts, skills,
  session cache, `/repo`, `/outbox`, and launcher sockets. `.spindrift/`
  is host-only.
- **The Chore scope is a git scope.** `NextScope` returns `lastSwept..HEAD`
  plus a tree cursor, and "due" partly means "there is new code".
- **Dedup suppresses forever.** A finding's site-key terms match open *and
  closed* finding issues, and that fits code. A tuning's natural site key is a
  prompt file, so after one closed finding the tuning Chore would go quiet on
  the files that matter most.
- **Nothing measures a merge afterwards.** Every outcome ends at settle. No
  revert, follow-up, or rework signal exists. A tuning that sees only cost
  and settle outcomes drifts toward "do less".

The tuning Chore is also the first Chore whose findings change the harness's own
behaviour. The default `MERGE_GUARD_PATHS` covers agent-instruction files
and CI. It does not cover prompts, skills, fragments, the Chore catalog,
knob defaults, or the dogfood flake's model pins. A promoted tuning finding
that rewrote the reviewer prompt would merge itself once green.

## Decision

1. **The tuning Chore is a built-in Chore, scoped by Records, not by the tree.**
   It is a row in the Chore catalog, opted into through `BUTLER_CHORES`
   like the others. It reuses the Ledger, claim, budgets, filing, and
   dedup. Its Ledger cursor is the last Record ID it covered, not a
   commit. It is due when `BUTLER_EVERY[tuning]` (default 24h) has passed
   **and** at least `BUTLER_TUNING_MIN_RECORDS` (default 20) new settled
   Records exist. A root with no store is never due. Its findings concern
   the Target repo it clones, limited to what that repo controls. When the
   cause lives upstream, the finding says so in its body.

2. **The host renders the digest; the Box interprets it.** Before launch,
   the host renders a fixed digest from the root's Dispatch Records store
   and injects it as Chore input. The digest has a size cap of about 48 KB,
   and when it is over, evidence is trimmed first and aggregates never. It
   compares the **new window** (cursor..latest) against a **trailing 7-day
   baseline** and holds:
   - a summary;
   - a per-role table;
   - `--by revision|model|prompt:<role>` splits where more than one value
     exists;
   - outliers by cost, and every failed, blocked, or ambiguous Record;
   - the tuning Chore's own prior findings, with their state and, for merged
     ones, the Δ since the closing PR's revision;
   - a sample of review verdict text and fix dispositions.

   Verdict and fix text are model output shaped by issue comments. They
   pass through `promptfence`. The host computes every number. Each
   aggregate row carries its sample size `n` and a stable anchor, and rows
   below `BUTLER_TUNING_MIN_SAMPLE` (default 15) are marked thin.

3. **The snapshot stays host-private.** The rendered digest is stored in
   the Dispatch Records store, keyed by the sweep's Record ID. The Ledger
   records only `{sweep Record ID, sha256}`. A hosted forge's Ledger ref is
   pushed and readable by anyone who can read the repo, so the full digest
   never goes there.

4. **The host validates every finding against the snapshot it served.**
   A tuning `IssueIntent` carries `cites`, the digest anchors it argues
   from. At settle the host drops the finding (recorded in
   `Ledger.dropped` with a reason) when:
   - any cite is absent from the stored snapshot;
   - its target file is missing at the scanned HEAD;
   - no cited aggregate row reaches the sample floor. `evidence-gap` is
     exempt, since it argues that data is missing.

   The filed issue gets a host-appended `## Evidence` section that
   re-renders the cited rows from the stored snapshot. A human reads the
   host's numbers, not the model's transcription of them. This is ADR
   0039's rule (the host decides from evidence) applied to a finding.

5. **A closed class list that never promotes or patches.** The classes are
   `cost-waste`, `quality-regression`, `prompt-gap`, `model-fit`,
   `knob-tuning`, and `evidence-gap`. The tuning Chore row's promotion and patch
   classes are empty, and the catalog check rejects any row that sets
   them. Every self-modification is a human's call.

6. **Dedup keys on the pattern, with a cooldown.** A tuning finding's dedup
   key is `<class>:<target file>:<metric>`. Open and closed-completed
   matches suppress it. A closed-not-planned match suppresses it only for
   `BUTLER_TUNING_COOLDOWN` (default 30 days). Code Chores keep today's
   permanent suppression.

7. **Quality after merge is reverts plus churn.** For each merged work
   Record, the host derives from the Target clone:
   - whether a later commit on the base branch reverted it;
   - the share of its added lines rewritten by other commits within 14
     days.

   Both are stored as Record columns through a forward migration of the
   ADR 0061 store. They are filled once a Record matures at 14 days and
   shown by `spindrift stats`. The new-window column shows "—" until then,
   and the baseline carries the signal.

8. **Tuning provenance forces a manual merge.** Tuning findings are filed
   with `agent-tuning-finding` alongside `agent-butler-finding`. The host
   creates the label if it is missing, and doctor requires it when the
   tuning is enabled on a tracker with a label registry. A work PR whose
   closing issue wears the label downgrades to a manual merge whatever
   paths it touches, with a PR comment naming the reason. Trackers without
   labels fall back to the finding's body marker. A triage-role holder who
   removes the label has overridden the guard on purpose. Separately, the
   dogfood Consumer config adds `templates/**`, `skills/**`,
   `fragments/**`, the Chore catalog, the env schema, and `flake.nix` to
   `MERGE_GUARD_PATHS`. That catches the same behaviour changes from any
   origin. The shipped default is unchanged.

## Considered Options

- **A fifth Dispatch kind, `tuning`.** It would run the same loop under its
  own label family, settle strategy, doctor checks, and daemon selector,
  and file findings shaped like butler findings anyway. Rejected as
  ceremony.
- **A host-only `spindrift stats --suggest` with fixed heuristics.** Cheap
  and safe, but it cannot read verdict text or trace a pattern to a file.
  Kept as a possible future aid, not the tuning Chore.
- **Raw Records, or a read-only copy of the store, inside the Box.** This
  leaves the arithmetic to the model, needs truncation that drops data
  silently or a new mount plus redaction, and costs the most turns.
  Rejected for the host-rendered digest.
- **Store the snapshot in the Ledger.** It would be reproducible from any
  clone, and it would publish every Dispatch's cost, knobs, and verdict
  sample to the forge. Rejected.
- **Allow promotion of `evidence-gap`, or of every class, behind the
  existing gates.** Allowing every class would let a misread digest
  promote a prompt rewrite that is only measurable two weeks later.
  Revisit once there is a record of accepted findings against later
  churn.
- **A significance test per comparison instead of a sample floor.** This
  is statistically right, but daily windows would almost never pass it.
  The floor can be replaced later without changing the finding's shape.
- **A path guard or a provenance guard alone.** Paths miss a tuning change
  that lands in Go code. Provenance misses an equivalent change filed by
  hand. Both are kept.
- **Offline evals inside the tuning Chore**, where each finding replays its
  proposed change first. The tuning Chore would have to write the change, which
  is the patch rung under another name. Per-pass replay evals mined from
  Records get their own spec. Their results attach to guarded PRs as
  advisory evidence.

## Consequences

- A Chore can now declare a non-tree scope source and receive host-injected
  input. The Chore catalog row and the Chore input contract grow
  accordingly. Code Chores are unaffected.
- The ADR 0061 store gains two things: a snapshot table, and reverts and
  churn columns that mature after 14 days. `spindrift stats` shows them
  too.
- Butler dedup gains a per-Chore key shape and an expiry on
  closed-not-planned matches.
- The work merge gate reads a provenance label for the first time. Removing
  `agent-tuning-finding` is the documented human override.
- On dogfood, ordinary PRs that touch prompts, skills, fragments, the
  catalog, the env schema, or the flake now merge by hand. That was about
  90 of 1,400 commits on main between 2026-09-23 and 2026-10-07.
- The tuning Chore needs the host outcome, revision and prompt splits, and
  settle-time ingest from ADR 0061 before its first sweep is worth
  running.
- Dispatches launched on CI runners never reach a root's store, so the
  tuning cannot see them.
