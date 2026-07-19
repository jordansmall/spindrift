# TASK

Research GitHub issue #${ISSUE_NUMBER}: ${ISSUE_TITLE}

Fresh clone of the Target repo, no branch cut, no commits. This is a research
dispatch (ADR 0022): explore the repo, judge the issue's relevance, and post
one verdict comment. Advise-only — never edit the issue body, never touch a
label, never close the issue, never promote it to dispatchable. A human acts
on your verdict; the launcher owns every lifecycle transition.

# CONTEXT

Read first (run these yourself):

- `gh issue view ${ISSUE_NUMBER} --comments` — the issue plus any parent/linked
  issue or PRD it references (pull those in too).
- Any prior research comment already on the issue (look for the
  `<!-- spindrift-research -->` marker used below) — read it before
  researching again so a re-run doesn't repeat prior findings.

# EXPLORE

Explore the actual repo — grep, read the relevant files, check existing
tests — before rendering a verdict; the issue text alone is not enough.

When the issue claims a bug and the repo's own tests make it cheap, attempt a
repro (run the existing suite, or write and run a throwaway repro script).
Skip the repro when it would be expensive to set up — encouraged, not
mandated.

# VERDICT

Render exactly one of three verdicts:

- `recommend` — relevant, now enriched with real context; promote it.
- `reject` — false positive, not worth doing, or a duplicate. Name the
  duplicate issue by number in your rationale; duplicate is a reason under
  `reject`, not a separate verdict.
- `unclear` — relevance can't be determined without a human's answer.

# POST THE VERDICT

Post exactly ONE comment on the issue (`gh issue comment ${ISSUE_NUMBER}
--body "..."`), structured in this order:

1. **Verdict** — `recommend` / `reject` / `unclear`, plus a one-line rationale.
2. **Context for a worker** — code pointers (file:line), related issues/PRs,
   repro notes, sharpened acceptance criteria.
3. **Open questions** — mandatory when the verdict is `unclear`: the concrete
   questions only a human can answer. Omit this section for
   `recommend`/`reject`.

Carry the machine marker `<!-- spindrift-research -->` in the comment body so
a later research pass or tooling can find it. Always post a NEW comment —
never edit a predecessor research comment, even on a re-run.

Never edit the issue body, never add or remove a label, never close the
issue, never promote it to dispatchable. Comments only.

# OUTCOME

Once the comment is posted, print exactly one line as your final output —
raw plain text, not wrapped in backticks, a code fence, or any other
markdown formatting:

SPINDRIFT_OUTCOME issue=${ISSUE_NUMBER} landing=<verdict-comment-url> status=<recommend|reject|unclear> note=<one-line rationale>

This must be the literal final message — nothing after it, no prose summary.
`landing` is the URL of the comment you just posted (`gh issue comment`
prints it). `status` carries the verdict, not a work-style ready/blocked.

If you cannot reach a verdict, or the comment cannot be posted, use `blocked`
as the escape hatch instead — same raw plain text requirement:

SPINDRIFT_OUTCOME issue=${ISSUE_NUMBER} landing=none status=blocked note=<short reason>
