# TASK

${CAVEMAN_STEP}One-shot sweep of the `${CHORE_NAME}` chore (ADR 0056). An
advise-only Box: no branch cut, no commits, no PR, no push, and no direct
write to the Issue Tracker. Scan the Target repo as checked out for this
Chore's kind of finding and relay each one to the Filer — the launcher
files it for a human to triage. The sweep records `${CHORE_HEAD}` as its
head; the checkout may be newer if the base branch moved since. This run
carries no tracker issue number: it is keyed by the Chore's own name,
`${CHORE_NAME}`, not an issue.

# SCOPE

Diff range since the last sweep: `${CHORE_DIFF_RANGE}` (blank on a first
sweep, or when nothing landed since the last one — review the slice below
in full either way).

Review that diff first, then sweep the rest of this run's slice: every path
below, recursively.

${CHORE_SLICE}

# CHORE

${CHORE_PROMPT}

# PROMOTION CANDIDATES

Promotion classes for this run (blank when promotion is off):

${CHORE_CLASSES}

Tag every finding with a class naming its kind: a lowercase slug of
letters, digits, and `-`, not starting with `-`, at most 40 characters. A
class outside that shape is rejected, and the finding with it.
Prefer a promotion class when one genuinely fits; never stretch a finding
to fit one just to make it a candidate.

A finding is a promotion candidate only when its class is one of the
promotion classes. For a candidate, and only a candidate, spawn a fresh
`reviewer` subagent first: hand it the finding — title, body, class, and site
keys — in the delegation message, since it has no issue, branch, or diff of
its own to read. If its final message starts `VERDICT: APPROVE`, pass the
filer its one-line reason as the finding's concurrence. Any other answer is
dissent: relay the finding without a concurrence, same as a non-candidate.
Never spawn the `reviewer` on a finding that isn't a candidate. If the
promotion classes are blank, or no `reviewer` subagent is provisioned this
run, no finding is a candidate: relay every finding, class included, with no
concurrence.

${BUTLER_PATCH_STEP}None of this — the class list, the reviewer, the concurrence — promotes
anything by itself. The launcher alone decides whether a finding is
promoted, against its own allow-list and daily budget, at settle. Pass
every finding's class to the filer regardless of whether it is a
candidate.

# FILE FINDINGS

Relay at most ${CHORE_MAX_FINDINGS} findings this run, the most important
first — the launcher may refuse or drop any beyond that.

${BUTLER_FILE_ISSUES_RELAY_STEP}${BUTLER_FILE_ISSUES_RELAY_SOCKET_STEP}Never push, never open a PR, never commit, never edit an issue, add or
remove a label, or file an issue yourself, and never write to the Ledger —
the launcher records this sweep's own done commit once you exit. Comments
on an existing issue are also out of scope: relay through the Filer only.

# OUTCOME

Once every finding is relayed (or the sweep concludes there is nothing to
relay), print exactly one line as your final output — raw plain text, not
wrapped in backticks, a code fence, or any other markdown formatting:

SPINDRIFT_OUTCOME issue=${DISPATCH_KEY} landing=none status=ready note=<n findings relayed>

This must be the literal final message — nothing after it, no prose
summary. `landing` is always `none`: this run lands nothing, on any forge.

If you cannot complete the sweep — the clone fails, the slice is
unreadable, or some other blocker stops you before a genuine conclusion —
use `blocked` as the escape hatch instead, same raw plain text requirement:

SPINDRIFT_OUTCOME issue=${DISPATCH_KEY} landing=none status=blocked note=<short reason>
