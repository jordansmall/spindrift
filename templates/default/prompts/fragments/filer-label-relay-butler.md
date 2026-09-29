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

   When you were handed a patch alongside the finding, carry it verbatim
   into the filing call — never invent or edit one. On the log carrier,
   add a `"patch"` key to the `SPINDRIFT_ISSUE_INTENT` JSON payload: a
   unified diff of modification hunks only, no binary content, under the
   same per-field size cap as every other key. On the socket carrier,
   write the diff to a file and add `-patch-file <file>` to the
   `driver-exec signal issue-intent` call. The host, never you, decides
   whether it is ever applied. Omit the patch entirely when you were not
   handed one.

