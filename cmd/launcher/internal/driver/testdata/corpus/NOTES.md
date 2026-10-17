# Corpus coverage

Cases named `real-*` are real dogfood Pass logs, scrubbed with
`internal/driver/corpus/scrub` and reviewed by hand (issue #5181). Scrubbing
removes emails, host home paths and the commit author name, and blanks tool
output, file bodies, heredoc bodies in commands, thinking text and subagent
task text. The rest are the synthetic seed fixtures (issue #5180). Goldens
for the real cases were generated from the pre-canonical readers, before any
Driver switched to the shared readers (issue #4944). The opencode transcript
goldens, and the `BlockLine` in its verdict goldens, were regenerated when the
opencode Transcript began rendering tool calls and results (issue #5357).

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
local issue tracker and a local Code Forge, working a toy Python project. The
real cases carry `bash` and `read` tool calls, so they cover the tool-call and
tool-result lines.

| Case | Covers | Source log |
| --- | --- | --- |
| `real-normal` | a merged run: an implement pass with no outcome line, a review pass, then a land pass that reached the outcome | `issue-longest-line.log` |
| `real-box-failed` | a Box that failed its env guards before `opencode` started, so the log has no Driver events | `issue-longest-line.log.prior-run.1` |
| `real-single-pass` | a merged run whose implement pass reached the outcome itself | `issue-stdin.log` |
| `real-review-block-ignored` | a review pass that printed `VERDICT: BLOCK`, captured before #5332, so the host read it as no verdict and the land pass merged it | `issue-missing-file.log` |
| `real-blocked` | an implement pass that stopped with `status=blocked` on a contradictory spec | `issue-word-rules.log` |
| `real-review-approved` | an approving review the host read as a verdict, then a land pass | `issue-top-words.log` |
| `real-fix-pass` | a blocking review, then a fix pass that reached the outcome | `issue-encoding.log` |
| `real-fix-rounds-delta-review` | two blocking reviews and fix passes, an approving review, a land pass, then a delta review of the land pass's edits | `issue-output-format.log` |
| `real-driver-crash` | `opencode` failing to open its database on every pass, so no pass emitted a Driver event or an outcome and the run settled box-failed | `issue-encoding.log.prior-run.1` |

`real-review-block-ignored`, `real-review-approved` and `real-fix-pass`
render under the reviewer role so their verdict goldens pin the real verdict
line. Each log holds the whole run, so its other passes' lines carry
`[reviewer]` too.

The last four cases were captured after #5331, #5332 and #5348 landed.
`real-driver-crash` comes from the #5349 session cache regression (#5387).

`subagent-task-fixture` is synthetic. It covers a `task` call to the reviewer
subagent whose result renders tagged `[reviewer]`, the form the legacy-loop
verdict scan reads.

Missing:

- A resumed session. No pass in the captured runs needed a corrective
  resume.
- A worker subagent and a Filer finding. Both were enabled, but no pass
  called a subagent. Every review left non-blocking findings, but each land
  pass fixed the cheap ones and dropped the rest without filing, or a fix
  pass reached the outcome and no land pass ran. With no subagent call, no
  real log has a `task` tool call yet, so the synthetic
  `subagent-task-fixture` stands in; a real one would still not show the
  verdict, because the scrubber blanks tool output.
- A Box killed mid-pass and a transient failure. None happened in the
  captured runs; the synthetic cases cover a rate-limit reset and a
  recovered error (#5353).
