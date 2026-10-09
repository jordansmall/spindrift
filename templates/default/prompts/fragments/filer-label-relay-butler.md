1. Your token is read-only — you cannot create labels yourself. The launcher
   applies the `agent-butler-finding` label itself when it files each issue
   host-side; skip this step.

   Carry the finding's class, and its concurrence when you were handed one,
   verbatim into the filing call — never invent either. On the log carrier,
   add `"class"` and (when present) `"concurrence"` keys to the
   `SPINDRIFT_ISSUE_INTENT` JSON payload; on the socket carrier, add
   `-class '<class>'` and (when present) `-concurrence '<text>'` to the
   `driver-exec signal issue-intent` call. Omit the concurrence entirely
   when you were not handed one. On the socket carrier, if `driver-exec
   signal issue-intent` rejects the class as off the class list, do not
   pick a class yourself — this overrides the base step's "fix it and send
   again": report the rejection, with the valid classes it names, back as
   your result.

   When you were handed the finding's cited digest anchors and metric slug,
   carry them verbatim too. On the log carrier, add a `"cites"` list (one
   entry per anchor) and a `"metric"` string to the `SPINDRIFT_ISSUE_INTENT`
   JSON payload; on the socket carrier, add one `-cite '<anchor>'` per anchor
   and `-metric '<slug>'` to the `driver-exec signal issue-intent` call. If
   the call rejects the finding for a missing cite or metric, report the
   rejection back as your result. Omit both when you were not handed them.

   When you were handed a patch alongside the finding, carry it verbatim
   into the filing call — never invent or edit one. On the log carrier,
   add a `"patch"` key to the `SPINDRIFT_ISSUE_INTENT` JSON payload: a
   unified diff of modification hunks only, no binary content, under the
   same per-field size cap as every other key. On the socket carrier,
   write the diff to a file and add `-patch-file <file>` to the
   `driver-exec signal issue-intent` call. The host, never you, decides
   whether it is ever applied. Omit the patch entirely when you were not
   handed one.

