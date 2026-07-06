# No `internal/lifecycle` module — label transitions stay as call-site one-liners

Issue #139 proposed an `internal/lifecycle` package that would own a validated
transition table for the label lifecycle (`ready-for-agent` → `agent-in-progress`
→ `agent-failed`/closed). The issue deferred the decision until #136 (self-heal),
#85 (transient classification), and #86 (bounded retry) had landed, then asked
for a deletion test: enumerate every `swapLabel` call site and decide whether the
set has grown into a real state machine scattered across callers.

That test was run against the post-#85/#86 codebase (2026-07-06). Six call sites:

| site | from | to | function |
|---|---|---|---|
| `main.go:289` | `label` (ready-for-agent) | `inProgressLabel` | `claimIssue` |
| `main.go:407` | `inProgressLabel` | `completeLabel` | `mergeWhenGreen` |
| `main.go:469` | `inProgressLabel` | `failedLabel` | `selfHeal` (fix-passes exhausted) |
| `main.go:497` | `inProgressLabel` | `failedLabel` | `verifyMerged` (PR not merged) |
| `main.go:1148` | `inProgressLabel` | `failedLabel` | `fanOut` (runWithRetry false) |
| `main.go:1183` | `label` | `failedLabel` | `dispatchWaves` (blocker-skip) |

The logical shape is unchanged from the pre-#136 baseline: one start transition,
one success terminal, four failure convergences. #136, #85, and #86 added
scheduling complexity — a fix-pass retry loop, transient/terminal classification,
hold-until-reset — but introduced **no new label states**. An issue stays
`agent-in-progress` throughout multiple fix passes and holds; the LABEL surface
at the GitHub boundary is still just three values.

CONTEXT.md already says `_Avoid_: status, queue, state machine` for this concept
area. A lifecycle module adds an indirection layer over one-liners that are each
already in a focused function, and still can't prevent an accidental call with
swapped arguments — the actual defect mode. Without new label states or invariants
worth enforcing there is nothing for the module to do except add ceremony.

## Considered Options

- **Build `internal/lifecycle` with a transition table** — expressive for a real
  state machine, but the table reduces to six rows with no cycles and no
  intermediate states; a Go map from `(from, to)` pairs to booleans buys nothing
  over reading the call sites. Rejected.
- **Add a transition validator to the forge fake** — `fake.SwapCall` already
  records every invocation; adding an assertion that `(from, to)` is in a
  hard-coded allow-list would catch bugs in tests. This is the lightest form of
  the proposal, but the existing tests already assert the specific labels on every
  path; the validator would duplicate that coverage. Rejected.
- **Close as won't-do, write this ADR** — records the reasoning so future
  architecture reviews don't re-suggest the module without additional evidence.
  Adopted.

## Consequences

No code changes. Revisit if the label surface grows: if a new dispatch state
(e.g. `agent-hold`, `agent-fix-pass`) is introduced as a GitHub label visible to
operators, the deletion test would read differently and a module may then be
warranted.
