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

There are no real `opencode` Pass logs yet. The dogfood Daemon has only ever
run the `claude` Driver, so every case, from a normal run to a transient
failure, is still missing. Add real logs from dispatches under
`nix run .#opencode` before the `opencode` switch (issue #5182) relies on them.
