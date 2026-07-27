3. Your token is read-only — you cannot file issues yourself. Instead print
   one `SPINDRIFT_ISSUE_INTENT ${RUN_NONCE} <base64-payload>` line per issue
   to file — the launcher finds each line by this run's nonce, decodes it,
   and files the issue host-side once you exit. Build each payload by
   JSON-encoding `{"title": <title>, "body": <body>}` and base64-encoding the
   result into one unbroken token with no embedded newlines or spaces (e.g.
   `printf '%s' '{"title":"...","body":"..."}' | base64 -w0`). Merge findings
   into a single issue only when they are the same change (e.g. the same
   file/function/fix) — never merge unrelated findings just to reduce issue
   count; that means one `SPINDRIFT_ISSUE_INTENT` line per issue to file, not
   per finding.
