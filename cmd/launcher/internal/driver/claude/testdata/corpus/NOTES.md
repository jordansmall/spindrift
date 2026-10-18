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
| `real-conflict-resolve-error` | a conflict-resolve pass with a failed tool call that still ends in a successful result | `issue-5009-conflict-resolve.log` |
| `real-resumed-prior-run` | a run later resumed under a new run | `issue-4994.log.prior-run.2` |
| `real-rate-limit-recovered` | a research run whose assistant text and tool inputs quote 429/overloaded markers but classifies terminal | `issue-4423.log.prior-run.1` |

Missing:

- A rate-limited, overloaded or network failure that classifies **transient**.
  Real logs of each exist (`issue-3840.log.prior-run.1`, `issue-1950.log.4`,
  `issue-5115-fix-1.log`), but they have not been added yet.
- A pass whose final `result` event carries `is_error:true`.
- A real rate limit that the run recovered from, so it still classifies
  terminal: a `rate_limit_event` whose status is not `allowed`, or an API
  rate-limit error.
- A Box killed mid-pass with no final result event. No log of one was
  identified.
