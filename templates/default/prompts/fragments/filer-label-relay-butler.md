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

   When the finding's class is one the host has put on its patch
   allow-list, and only then, you may also carry a `"patch"` key: a unified
   diff of modification hunks only, no binary content, under the same
   per-field size cap as every other key. The host, never you, decides
   whether it is ever applied. Omit the key entirely otherwise.

