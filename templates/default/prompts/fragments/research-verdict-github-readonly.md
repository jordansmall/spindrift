Your GitHub token is read-only here — you cannot comment on the issue
yourself. Print the verdict as a single delimited block on stdout instead —
the launcher posts it to the issue, host-side, once you exit:

SPINDRIFT_COMMENT_BEGIN
<verdict comment body, structured per below>
SPINDRIFT_COMMENT_END

Print exactly ONE such block, before the SPINDRIFT_OUTCOME line below.
Never write a line reading exactly `SPINDRIFT_COMMENT_END` inside the body
itself — it would close the block early and truncate your verdict.
