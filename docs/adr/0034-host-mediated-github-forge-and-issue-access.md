# `github` joins the host-mediated write model, behind an opt-in switch

## Context

ADR 0032 and ADR 0033 host-mediated `local`'s two planes — issue content and
code — for one reason specific to `local`: it is **not reachable from inside
the Box**. There is no server to call and no remote to push, so the Launcher
has to read the issue in and write the comment/branch out on the Box's
behalf. `github` never needed that treatment; it is reachable, so the Box
has always read and written it directly with its own `GH_TOKEN`.

Being *reachable* and being *safe to hand a write token* are different
properties, though, and #1914 is about the second one. Every `github`
Dispatch today hands the Box a token that can `git push`, `gh pr create`,
and `gh issue comment` on the Target repo. The only thing stopping a
prompt-injected or misbehaving Box from pushing straight to the base branch,
or merging its own PR, is a **repository ruleset** the operator configures
out-of-band — [two-actor separation](../reference.md#two-actor-separation-opt-in-hard-mode),
ADR 0016's hard mode. That guarantee lives in GitHub's config, not in
spindrift's code: it is easy to misconfigure, invisible in code review, and
load-bearing anyway.

`local`'s host-mediated model happens to also produce the property an
operator wants here — a Box that holds no capability to write at all — as a
side effect of solving unreachability. Nothing about that model is
`local`-specific in principle: a `github` Box could just as well leave its
branch as a bundle, its PR intent as a stdout block, and its comment as a
stdout block, and let the Launcher perform the writes with its *own*,
separately-scoped token, the same way it already does for `local`. The gap
is not reachability — `github` is always reachable — it's that nothing wired
the *choice* to host-mediate it anyway.

Issues #1916–#1919 built that wiring in four slices: a `BOX_FORGE_AND_ISSUE_ACCESS`
switch and its startup capability gate (#1916), host-mediated comments
(#1917), host-mediated branch hand-off via bundle relay (#1918), and
host-mediated draft-PR creation (#1919). This ADR records the decision that
ties those four slices together and closes the loop this issue (#1920)
finishes documenting.

## Decision

**Add `BOX_FORGE_AND_ISSUE_ACCESS`, a third axis orthogonal to `CODE_FORGE`
and `ISSUE_TRACKER`, and bring `github` to capability parity with `local`'s
host-mediated write model when it is set to `read-only`.**

`local` was host-mediated because it *had* to be — there was no other way to
reach it. `github` is host-mediated **only when the operator opts in** —
`read-write` (the default) leaves today's in-box-write flow byte-for-byte
unchanged, matching ADR 0032's "remote trackers... read and write them
in-box" and ADR 0033's untouched `git`/`github` merge-commit landing.
`read-only` re-derives the same three host-mediation moves ADR 0032/0033
established for `local`, this time for a reachable backend that is choosing
not to use that reachability for writes:

- **Branch → bundle.** Instead of `git push`, the Box writes its finished
  branch as a `git bundle` to the writable outbox (ADR 0033's mechanism,
  unmodified); the Launcher relays it into the real remote via
  `forge.BundleRelay`.
- **PR → intent line.** Instead of `gh pr create`, the Box emits a
  `SPINDRIFT_PR_INTENT` stdout signal (title, body; later reshaped
  single-line and nonce-guarded by this ADR's #1938 amendment); the Launcher
  opens the draft PR host-side via `forge.DraftPRCreator`, a new optional
  capability discovered by type assertion the same way `PRForge` and
  `BundleRelay` are.
- **Comment → comment line.** Instead of `gh issue comment`, the Box emits a
  `SPINDRIFT_COMMENT` stdout signal (ADR 0032's mechanism, later reshaped
  single-line and nonce-guarded by ADR 0032's #1940 amendment); the Launcher
  posts it via `forge.HostPostedCommenter` — trivially satisfied by every
  tracker's existing `Comment` method, since posting host-side needs no new
  client, only the discipline of calling it from the Launcher instead of the
  Box.

**The gate is capability, not backend name.** `checkReadOnlyCapabilityGate`
type-asserts the selected forge against `BundleRelay` (and `DraftPRCreator`
when the forge is PR-shaped) and the selected tracker against
`HostPostedCommenter`; `read-only` is refused at startup, naming the missing
seam, for any backend that doesn't implement them — never silently degraded.
This is the same optional-interface discovery pattern `PRForge`/`LandingRecorder`
already use elsewhere in the forge package, deliberately reused rather than
introducing a parallel "backend supports read-only" registry. `local`
satisfies it by construction; `github` satisfies it as of #1919's
`DraftPRCreator` implementation — the last of the three capabilities it was
missing.

**The Box's `GH_TOKEN` collapses to Contents R + Issues R + Metadata R.**
With every write host-mediated, the Box never needs `Pull requests` or
`Workflows` scope at all — see [Read-only Box
token](../reference.md#read-only-box-token) for the full table. This is
the concrete, code-enforced version of the property two-actor separation
approximates with a ruleset: a token that cannot write has no PR to open and
nothing to merge, regardless of what the prompt tells it to do.

## Two routes to the same guarantee

Two-actor separation and read-only Box now both close the same gap — "the
Box cannot unilaterally update the base branch" — by different means, and
neither depends on the other:

| | two-actor separation (ADR 0016) | read-only Box (this ADR) |
| --- | --- | --- |
| what changes | a second GitHub user + a repository ruleset | the Box's own token scope |
| where the guarantee lives | GitHub's ruleset config (out-of-band, operator-configured) | spindrift's startup capability gate (in-code, self-checking) |
| Box can open a PR? | yes — the ruleset only blocks the *base branch* | no — the token has no `Pull requests` scope |
| Box can comment? | yes, unaffected | no — the Launcher posts every comment |
| cost | a second machine account + a second secret | more host-side work per issue; requires the selected forge/tracker to implement the three capabilities above |
| enforcement point | GitHub, at ref-update time | spindrift, at token-mint time — there is nothing for the Box to bypass because it never receives the capability |

Read-only is not two-actor separation with extra steps — a Box under
read-only can still be paired with `BOX_GH_TOKEN` and a ruleset for
belt-and-suspenders, but doing so is redundant: read-only alone already
removes the capability the ruleset exists to block. An operator picks
two-actor separation when the Box still needs to author PR/comment content
under its own identity (attribution, or a forge without host-mediation
capabilities yet); an operator picks read-only for the strongest available
posture on any backend that clears the gate.

## Future trajectory

`read-write` stays the default for now — `read-only` requires the operator
to provision a second, reduced-scope token and accept more host-side Launcher
work per issue, a real cost not every deployment wants yet. Two changes are
anticipated as more backends clear the capability gate and the model proves
out operationally: `read-only` becoming the **default** (flip the knob's
default value, `read-write` stays available as an explicit opt-out), and,
further out, `read-only` becoming the **only path** (retire the in-box write
flow entirely once every supported forge/tracker combination satisfies the
gate, collapsing the axis away). Neither is this ADR's decision — both are
noted here so a future flip is read as the trajectory this ADR set up, not a
surprise reversal.

## Consequences

- `BOX_FORGE_AND_ISSUE_ACCESS=read-only` is available on `github`, not just
  `local` — the capability gate documented in ADR 0032/0033's terms now
  spans both backends that implement it.
- The Box's `GH_TOKEN` under `read-only` needs only Contents R, Issues R,
  Metadata R — a strictly smaller grant than the read-write PAT table, and
  provisionable the same way `BOX_GH_TOKEN` is under two-actor separation
  (a second token, same secret-sourcing mechanism).
- `docs/reference.md` gains the [Read-only
  Box](../reference.md#read-only-box-box_forge_and_issue_accessread-only)
  section and the [Read-only Box
  token](../reference.md#read-only-box-token) permission table; README and
  SECURITY.md cross-reference it as the second route to the two-actor
  guarantee.
- No change to `read-write` behavior, `CODE_FORGE=local`/`ISSUE_TRACKER=local`,
  or two-actor separation — all three are unaffected by this ADR and remain
  fully supported.
- A default flip and an eventual `read-only`-only posture are noted futures,
  not commitments made here.

## Amendment (issue #1938): PR-intent moves to a single nonce-guarded line

The `SPINDRIFT_PR_INTENT` signal described above as a multi-line
`_BEGIN`/`_END` stdout block never survived Claude Code's stream-json JSONL
log transport: a multi-line block collapses onto one JSON-escaped physical
line before the launcher's exact-line marker scan ever sees it (issue
#1921's dogfood failure — "no usable PR-intent line found in the box's
log"). The Box now emits it as a single line instead —
`SPINDRIFT_PR_INTENT <RUN_NONCE> <base64>` — the same shape ADR 0032's
#1940 amendment gives `SPINDRIFT_COMMENT`, and built on the same
`outcome.LastCommentLineInLog`-style scan: the launcher locates the last
line carrying the token, verifies the field immediately after it against
the run's own nonce (a line whose nonce doesn't match was written by
someone other than this run's own Box, since an untrusted issue/comment
author's text predates the nonce being minted, so it's ignored rather than
allowed to shadow an earlier genuine line), and strictly base64-decodes the
`title\n\nbody` payload that follows, rejecting any decode error outright.
Everything else in this ADR — the write-channel shape (block → stdout,
launcher applies host-side), the capability gate, the token scope — is
unchanged.

## Amendment (issue #1933): IF BLOCKED gets the same host-mediation

Issues #1917–#1919 host-mediated the three write channels above for the
happy path only — `OPEN A PULL REQUEST` and the tracker-comment steps. The
`IF BLOCKED` fallback (review never clears, CI stays red, push fails) still
rendered its push and PR-check/create steps unconditionally, so a `read-only`
Box that reached it would attempt `git push`/`gh pr view`/`gh pr create` with
a token that holds none of those capabilities.

`IF BLOCKED` now branches on the same `BOX_ACCESS_READ_WRITE`/
`BOX_ACCESS_READ_ONLY` gate the push/create steps above use: under
`read-only`, the Box writes the outbox bundle and a `SPINDRIFT_PR_INTENT`
line instead, and reports `landing=` as the branch name rather than a PR URL
it never learns. Settle's `blocked` outcome case now also relays that bundle
(and opens the draft PR, if a PR-intent line was left) the same way
`hostMediateDraftPR` does for `ready` — without this, a blocked-but-real
bundle/PR-intent the Box left behind would be silently dropped the moment the
container exits, rather than reaching the host at all.

## Amendment (issue #1951): the Box-side gate fails closed

Every write-step gate this ADR added — the tracker-comment gate (#1917), the
push-step gate (#1918), the PR-create gate (#1919), and `IF BLOCKED`'s copy
of the last two (#1933) — read `BOX_FORGE_AND_ISSUE_ACCESS` inside the Box
with a `${BOX_FORGE_AND_ISSUE_ACCESS:-read-write}` fallback. An unset,
typo'd, or forwarding-glitched value therefore fell open into the
write-capable prompt path, the opposite of what `read-only` promises.

`dispatch.buildBoxEnv` now resolves the write-enabled-vs-not decision once,
host-side, from `BOX_FORGE_AND_ISSUE_ACCESS`, and forwards it as a single
explicit positive signal, `BOX_WRITE_ENABLED`, present only when writes are
permitted. `agent/entrypoint.sh`'s precompute block gates
`BOX_ACCESS_READ_WRITE`/`BOX_ACCESS_READ_ONLY` and
`ISSUE_TRACKER_GITHUB_READWRITE`/`ISSUE_TRACKER_GITHUB_READONLY` on
`BOX_WRITE_ENABLED`'s presence instead, with no fallback default: absence —
for any reason — renders the no-write path. `BOX_FORGE_AND_ISSUE_ACCESS`
itself is still forwarded into the Box (the launcher's own
`newCodeForge`/`checkReadOnlyCapabilityGate` still read it directly), but the
Box's prompt fragments no longer branch on it.
