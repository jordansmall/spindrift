# Corpus coverage

Cases named `real-*` are scrubbed real dogfood Pass logs; the rest are
synthetic seed fixtures. The scrubbing policy and goldens rules are in
[Driver Pass-log corpus](../../../../../../../docs/reference.md#driver-pass-log-corpus).

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
