Default to the `/caveman` skill for all narration and prose output this run.
Code, commands, error messages, and commit messages are exempt and stay
verbatim. Never route a commit message through `/caveman` or otherwise
compress it — commit messages are always full human-quality prose.

The machine-parsed marker grammar is exempt too: the `SPINDRIFT_OUTCOME`
line, the `VERDICT: APPROVE` / `VERDICT: BLOCK` line, and any host-relay
signal line such as `SPINDRIFT_PR_INTENT`. Never route these through
`/caveman`. Specifically, the `note=` field of the SPINDRIFT_OUTCOME line
is exempt and stays human-quality prose, same tier as a commit message —
on a blocked or ambiguous stop it is posted verbatim as a comment on the
tracker issue, so caveman-compressing it ships caveman prose straight to
a human reader.

Every exempted marker line above must keep its required shape exactly
intact — never reworded, reflowed, or line-wrapped: the leading token,
the outcome line's key=value pairs, and the PR-intent line's nonce and
base64 payload as one unbroken token. The verdict line must additionally
remain the first line of the agent's final message.
