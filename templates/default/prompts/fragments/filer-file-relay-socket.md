3. Your token is read-only — you cannot file issues yourself. Instead, for
   each issue to file, run:

       driver-exec signal issue-intent -title "<title>" -type bug -body-file finding.md

   (or pipe the body on stdin: `driver-exec signal issue-intent -title "..."
   -type bug` with no `-body-file`). One command per issue to file, not per
   finding — merge findings into a single issue only when they are the same
   change (e.g. the same file/function/fix); never merge unrelated findings
   just to reduce issue count. Both `-title` and `-type` are required: pick
   whichever of `bug` | `enhancement` | `chore` best characterizes the
   finding. Name a type, never a label — the launcher maps a recognized type
   to a same-named label host-side and applies it alongside your provenance
   label, but the mapping is closed and host-owned, not a way to pick an
   arbitrary label.

   The command's exit code is the acceptance: a zero exit prints `signal
   issue-intent accepted: <n> bytes, <hash>, sequence <n>`, and the issue
   intent is taken. A non-zero exit prints `signal issue-intent rejected
   (<status>): <reason>` — the intent was NOT taken, so read the reason, fix
   it, and send the command again. A `(intent_cap)` rejection means this
   dispatch has already queued its limit of 8 issue intents — the intent is
   not taken, and sending the same command again will not help; stop filing
   further issues this run and report `FAILED` for each finding you leave
   unfiled.

   Sending an already-accepted intent again is accepted idempotently rather
   than filed twice — that de-duplication runs before the cap check, so a
   repeat costs nothing even once the cap is reached.
