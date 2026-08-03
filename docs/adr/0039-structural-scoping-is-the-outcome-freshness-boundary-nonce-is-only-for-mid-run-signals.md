# Structural scoping is `SPINDRIFT_OUTCOME`'s freshness boundary; the nonce is retained only for the mid-run signal channels

## Context

The per-run control nonce (`RUN_NONCE`, issues #1937/#1939) was introduced as
a *uniform* anti-replay defense across every control-signal line the Box
writes to stdout: the launcher gates a token-bearing line on the run's own
freshly-minted nonce, so an untrusted issue/comment author's pre-authored
echo — written before the nonce existed — cannot carry it and cannot shadow
the Box's genuine line via last-wins. ADR 0032 (#1940, `SPINDRIFT_COMMENT`)
and ADR 0034 (#1938, `SPINDRIFT_PR_INTENT`) record the nonce-guarded
single-line grammar for the signal channels; CONTEXT.md's Outcome-line entry
records it for `SPINDRIFT_OUTCOME`.

The uniform framing hides a real asymmetry. The launcher actually parses two
families of control-signal line by two different mechanisms, and the nonce
does a different job in each:

- **`SPINDRIFT_OUTCOME`** is defended by *structure*, twice over, before the
  nonce is ever consulted. In-box, the per-driver extractor
  `_driver_extract_outcome` jq-scopes to the agent's *own final message*
  (claude `select(.type=="result") | .result`; opencode
  `select(.type=="text") | .part.text`), greps the bare `^SPINDRIFT_OUTCOME`
  line, `tail -1`, and re-emits exactly one **bare, leading** line. Host-side,
  `outcome.LastInLog`'s primary tier requires the token to *lead* the line
  (`stripToken(TrimSpace(line))`). Untrusted corpus reaches the log only as a
  `tool_result` — a different stream-json event type — or buried mid-JSON in
  the raw teed transcript: wrong event type for the extractor, non-leading for
  the host scan. It is excluded by structure independently of any nonce. The
  nonce is a third layer whose only non-vacuous job is disambiguating *several*
  bare-leading outcome lines inside the one final message — which the
  extractor's `tail -1` already resolves.

- **`SPINDRIFT_COMMENT` / `SPINDRIFT_PR_INTENT` / `SPINDRIFT_ISSUE_INTENT`**
  have no in-box extractor and no leading-line requirement. `parseSignalLine`
  finds the token *anywhere* in a line and is deliberately mid-JSON-tolerant
  (`base64AlphabetPrefix` strips trailing JSON escaping off a payload butted
  against a `"}`), because these lines are emitted **mid-run**, not in the
  terminal `result` event, and survive only as a token embedded in one
  JSON-escaped physical NDJSON line — the very stream-json flattening that
  drove the #1938/#1940 move to a single nonce-guarded line. For these, the
  `nonce=` field is the **sole** structural discriminator between a genuine
  line and a corpus echo. Remove it and a pre-authored comment echo sitting in
  a raw `gh issue view` `tool_result` line parses as a valid forged signal.

So the nonce is redundant for one channel and load-bearing for three, and the
reason is intrinsic: the outcome line can be structurally located (final
event, leading line); the mid-run signals cannot, so the nonce substitutes for
the structure they can't have.

## Decision

1. **Retire the nonce for `SPINDRIFT_OUTCOME`.** Its freshness boundary is
   structural scoping — the in-box `.result` / `.part.text` extraction plus the
   host leading-line requirement. Drop the `nonce=` field from the outcome
   grammar and its explanatory paragraph in the prompts, the host
   `LastInLog` nonce gate and its `skipped`-without-nonce warning, and the
   synthetic backstop's `nonce=` append. The extractor keeps `tail -1` as the
   within-final-message tiebreak (unchanged behavior).

2. **Keep the nonce unchanged for the three signal channels.** It is their
   sole replay defense. `RUN_NONCE` plumbing, the prompt fragments carrying
   `SPINDRIFT_COMMENT <RUN_NONCE> …` etc., their nix parity checks, and the
   in-box PR-intent nudge gate all stay exactly as they are.

## What the nonce proves — and what it does not

The nonce proves **freshness only**: a line carrying it was minted after the
run started, so it is not pre-authored corpus. It does *not* prove the agent
is trustworthy — the model is shown `RUN_NONCE` in cleartext and a
prompt-injected agent will stamp a forged line for free. That residual gap —
a compromised agent emitting a genuine-looking outcome line with
attacker-chosen `landing`/`status` — is not an outcome-parsing problem and is
**accepted** here; it is bounded by the Box's read-only posture, token scope,
and the Merge guard (ADR 0016), not by any freshness tag on the line.
Retiring `SPINDRIFT_OUTCOME`'s nonce loses nothing against this gap, because
the nonce never closed it — it only ever proved the line wasn't stale corpus,
which structure already proves for this channel.

## Consequences

- `SPINDRIFT_OUTCOME`'s silent-drop-if-the-agent-forgot-the-nonce liveness
  failure mode disappears. The synthetic backstop, self-report, and
  `skipped`-flag machinery narrow to genuine driver-crash / no-outcome cases;
  the read-only self-report recovery path (issue #2223) is unaffected — it
  keys on `synthetic=true`, never on the nonce.
- The outcome prompt shrinks: one fewer host secret the model must remember to
  echo on its most important line.
- The two parsing families now use *visibly* different mechanisms. **This ADR
  is the record that the difference is intentional.** A future architecture
  review must not, "for consistency," either re-add the outcome nonce or strip
  the signal-channel nonce: the first is redundant, the second removes the only
  defense three channels have.
- This narrows, but does not contradict, the nonce framing in ADR 0032 (#1940)
  and ADR 0034 (#1938): the nonce-guarded single-line grammar remains correct
  and load-bearing for the signal channels those ADRs introduced.
