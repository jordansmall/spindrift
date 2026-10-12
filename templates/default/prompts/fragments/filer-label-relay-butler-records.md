   When you were handed the finding's cited digest anchors and metric slug,
   carry them verbatim too. On the log carrier, add a `"cites"` list (one
   entry per anchor) and a `"metric"` string to the `SPINDRIFT_ISSUE_INTENT`
   JSON payload; on the socket carrier, add one `-cite '<anchor>'` per anchor
   and `-metric '<slug>'` to the `driver-exec signal issue-intent` call. If
   the call rejects the finding for a missing cite or metric, report the
   rejection back as your result. Omit both when you were not handed them.

