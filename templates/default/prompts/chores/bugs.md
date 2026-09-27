Look for latent bugs in the scoped code: correctness mistakes, wrong error
handling (swallowed, mismatched, or missing), races and other concurrency
hazards, off-by-one and boundary errors, and resource leaks (an unclosed
file, connection, goroutine, or lock that outlives its scope).

A finding must be concrete and cited with file:line, and worth a human's
time to read and fix. Prefer filing nothing over a speculative or
stylistic finding — this is not a style or readability sweep, and a vague
"this could maybe be a problem" finding costs a human more to triage than
it saves.
