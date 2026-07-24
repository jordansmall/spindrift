Print exactly one line as your final output — raw plain text, not wrapped in
backticks, a code fence, or any other markdown formatting:

SPINDRIFT_OUTCOME issue=${ISSUE_NUMBER} landing=${BRANCH} status=ready note=<short reason> nonce=${RUN_NONCE}

landing is your branch name here, not a PR URL — your token is read-only, so
you never open the PR and never learn its URL. The launcher opens the draft
PR host-side from your PR-intent line above and records the real PR URL as
this seam's landing itself.
