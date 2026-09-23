3. File one issue per surviving finding via `gh issue create --title <title>
   --body <body> --label agent-review-finding`. Merge findings into a single
   issue only when they are the same change (e.g. the same file/function/fix)
   — never merge unrelated findings just to reduce issue count. You file with
   your own token, so the launcher never sees this intent and cannot dedup it
   host-side — end the body with one `<!-- spindrift-dedup: <site key> -->`
   line carrying step 2's site keys joined by `, `, so a later run's dedup
   search finds it.
