Your Forgejo token is read-only here — you cannot comment on the issue
yourself. Print the verdict as a single line on stdout instead — the
launcher finds it by this run's nonce, decodes it, and posts it to the
issue, host-side, once you exit:

SPINDRIFT_COMMENT ${RUN_NONCE} <base64-encoded verdict comment body,
structured per below>

Base64-encode the entire verdict body (e.g. `base64 -w0`) into one unbroken
token with no embedded newlines or spaces. Print exactly ONE such line,
before the SPINDRIFT_OUTCOME line below.
