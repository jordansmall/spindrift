3. Your token is read-only — you cannot file issues yourself. Instead, for
   each issue to file, send it in one call, the body on stdin — never write
   the body to a file in one call and send it in the next:

```sh
driver-exec signal issue-intent -title '<title>' -type bug -dedup 'path/to/file.go:Symbol' <<'SPINDRIFT_SIGNAL_EOF'
<issue body>
SPINDRIFT_SIGNAL_EOF
```

   The closing `SPINDRIFT_SIGNAL_EOF` must sit alone at column one — an
   indented copy never ends the heredoc.

   One command per issue to file, not per finding — merge findings into a
   single issue only when they are the same change (e.g. the same
   file/function/fix); never merge unrelated findings just to reduce issue
   count.

   The flags are exactly `-title`, `-type`, `-dedup` (repeatable),
   and `-body-file` (optional fallback for a body already in
   a file; without it the body is read from stdin). There is no
   `-body` flag — the body goes on stdin or via `-body-file`, never
   inline. Both `-title` and `-type` are required: pick whichever of `bug`,
   `enhancement`, `chore` best characterizes the finding. Name a type,
   never a label — the launcher maps a recognized type to a same-named
   label host-side and applies it alongside your provenance label, but
   the mapping is closed and host-owned, not a way to pick an arbitrary
   label. `-dedup` carries this finding's site key (step 2) so the launcher
   can dedup future findings against this issue even after your title or
   wording changes (a line key from step 2 still goes stale once the code
   around it moves) — repeat the flag, once per site the finding spans,
   and always pass it.

   A title with backticks needs single quotes, not double: inside double
   quotes a backtick runs as a command substitution, e.g.
   ``-title 'fix(x): handle `nil` input'``. A title containing a single
   quote needs `'\''` in its place (closing the quote, an escaped quote,
   reopening it).

   Run the command bare — never through `tail`, `head`, or `2>&1 |`. The
   command's exit code is the acceptance: a zero exit prints `signal
   issue-intent accepted: <n> bytes, <hash>, sequence <n>`, and the issue
   intent is taken. A non-zero exit prints `signal issue-intent rejected
   (<status>): <reason>` — the intent was NOT taken, so read the reason, fix
   it, and send the command again. A pipe replaces the exit code with the
   last piped command's own, so it looks like acceptance even on rejection.
   A `(intent_cap)` rejection means this dispatch has already queued its
   limit of 8 issue intents — the intent is not taken, and sending the same
   command again will not help; stop filing further issues this run and
   report `FAILED` for each finding you leave unfiled.

   Sending an already-accepted intent again is accepted idempotently rather
   than filed twice — that de-duplication runs before the cap check, so a
   repeat costs nothing even once the cap is reached.
