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
   per finding. The payload may also carry an optional `"type"` key, one of
   `bug` | `enhancement` | `chore` — pick whichever best characterizes the
   finding to tag it with what kind of work it is (e.g.
   `printf '%s' '{"title":"...","body":"...","type":"bug"}' | base64 -w0`).
   Name a type, never a label: the launcher maps a recognized type to a
   same-named label host-side and applies it alongside your provenance
   label, but the mapping is closed and host-owned, not a way to pick an
   arbitrary label. `type` is optional — omit the key entirely when it
   doesn't apply, don't pass an empty string — and an absent or
   unrecognized type still files the issue, just untyped, never rejected.
