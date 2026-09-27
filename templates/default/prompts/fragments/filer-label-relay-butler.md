1. Your token is read-only — you cannot create labels yourself. The launcher
   applies the `agent-butler-finding` label itself when it files each issue
   host-side; skip this step.

   Carry the finding's class, and its concurrence when you were handed one,
   verbatim into the filing call — never invent either. On the log carrier,
   add `"class"` and (when present) `"concurrence"` keys to the
   `SPINDRIFT_ISSUE_INTENT` JSON payload; on the socket carrier, add
   `-class '<class>'` and (when present) `-concurrence '<text>'` to the
   `driver-exec signal issue-intent` call. Omit the concurrence entirely
   when you were not handed one.

