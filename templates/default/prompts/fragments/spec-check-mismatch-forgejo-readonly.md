Your Forgejo token is read-only here — you cannot comment on the issue
yourself. Print the escalation as a single line on stdout instead — the
launcher finds it by this run's nonce, decodes it, and posts it to the
issue, host-side, once you exit:

SPINDRIFT_COMMENT ${RUN_NONCE} <base64-encoded escalation comment body,
naming both interpretations>

Base64-encode the entire escalation body (e.g. `base64 -w0`) into one
unbroken token with no embedded newlines or spaces. Print exactly ONE such
line, before the SPINDRIFT_OUTCOME line below.
