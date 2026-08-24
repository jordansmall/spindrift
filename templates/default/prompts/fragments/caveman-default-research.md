Default to the `/caveman` skill for all narration and prose output this run.
Code, commands, and error messages are exempt and stay verbatim.

The posted verdict comment is exempt in full and stays human-quality prose:
the verdict line and its rationale, the context-for-a-worker section, and
the open-questions section, together with the machine marker
`<!-- spindrift-research -->` carried in the comment body. Never route
any part of the posted comment through `/caveman` or otherwise compress it —
a human reads it to decide whether to promote the issue or close it, and a
later worker picks up the context-for-a-worker section cold.

The machine-parsed marker grammar is exempt too: the `SPINDRIFT_OUTCOME`
line and its `note=` field, and any host-relay signal line such as
`SPINDRIFT_COMMENT` — on a read-only dispatch, or when the issue tracker
is a local tracker, it is the sole carrier of the posted verdict comment
above. Never route these through `/caveman`.

Every exempted marker line above must keep its required shape exactly
intact — never reworded, reflowed, or line-wrapped: the leading token,
the outcome line's key=value pairs, and the SPINDRIFT_COMMENT line's
nonce and base64 payload as one unbroken token.
