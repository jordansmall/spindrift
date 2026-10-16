# Corpus coverage

Cases named `real-*` are real dogfood Pass logs, scrubbed with
`internal/driver/corpus/scrub` and reviewed by hand (issue #5181). Scrubbing
removes emails, host home paths and the commit author name, and blanks tool
output, file bodies, heredoc bodies in commands, thinking text and subagent
task text. The rest are the synthetic seed fixtures (issue #5180). Goldens
for the real cases were generated from the pre-canonical readers, before any
Driver switched to the shared readers (issue #4944).

## claude

| Case | Covers | Source log |
| --- | --- | --- |
| `real-normal` | a run that settled with an outcome | `issue-5246.log` |
| `real-fix-pass-subagents` | review, fix and land passes with Task subagents (role attribution) | `issue-4940.log` |
| `real-no-outcome` | a pass that ended without an outcome line | `issue-5263.log` |
| `real-conflict-resolve-error` | a conflict-resolve pass ending in an `is_error` result | `issue-5009-conflict-resolve.log` |
| `real-resumed-prior-run` | a run later resumed under a new run | `issue-4994.log.prior-run.2` |
| `real-rate-limit-recovered` | a rate limit the run got past, so it classifies terminal | `issue-4423.log.prior-run.1` |

Missing:

- A rate-limited, overloaded or network failure that classifies **transient**.
  Real logs of each exist (`issue-3840.log.prior-run.1`, `issue-1950.log.4`,
  `issue-5115-fix-1.log`), but they have not been added yet.
- A Box killed mid-pass with no final result event. No log of one was
  identified.

## opencode

The dogfood Daemon has only ever run the `claude` Driver, so the real
`opencode` logs come from a scratch Consumer on GitHub Copilot models, with a
local issue tracker and a local Code Forge, working a toy Python project.

| Case | Covers | Source log |
| --- | --- | --- |
| `real-normal` | a merged run: an implement pass with no outcome line, a review pass, then a land pass that reached the outcome | `issue-longest-line.log` |
| `real-box-failed` | a Box that failed its env guards before `opencode` started, so the log has no Driver events | `issue-longest-line.log.prior-run.1` |
| `real-single-pass` | a merged run whose implement pass reached the outcome itself | `issue-stdin.log` |
| `real-review-block-ignored` | a review pass that printed `VERDICT: BLOCK`, captured before #5332, so the host read it as no verdict and the land pass merged it | `issue-missing-file.log` |
| `real-blocked` | an implement pass that stopped with `status=blocked` on a contradictory spec | `issue-word-rules.log` |

`real-review-block-ignored` renders under the reviewer role so its verdict
golden pins the real `BLOCK` line. The log holds the whole run, so its
implement and land lines carry `[reviewer]` too.

Missing:

- A fix pass. Until #5332 the `opencode` Transcript dropped the
  `[reviewer]` prefix, so the host never saw a review verdict and never ran
  one. Every log here predates that fix; capture one from a fresh dispatch.
- A resumed session. Until #5349 the `opencode` Driver did not resume
  sessions; capture a corrective resume from a fresh dispatch.
- A worker subagent. Until #5331 the `opencode` Box never enabled one.
- A Box killed mid-pass and a transient failure. None happened in the
  captured runs; the synthetic cases cover a rate-limit reset and a
  recovered error (#5353).
- A whole run with no outcome. Five of the seven runs ended their implement
  pass without one, but the land pass recovered every time.
