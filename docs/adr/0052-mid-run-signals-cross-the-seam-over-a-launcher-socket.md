# Mid-run signals cross the seam over a launcher-owned socket

Status: proposed

On 2026-09-21 three research Dispatches (#3595, #3597, #3605) were labelled
`agent-research-failed` although every Box had printed a valid
`status=recommend` outcome line. The verdict comment never reached the
tracker. The comment travels as a `SPINDRIFT_COMMENT <nonce> <base64>` line
that the Box prints from a Bash tool call and the Launcher scrapes, after
Box exit, from the driver's stream-json log — which means the copy the
Launcher reads is the *tool result echo*, and the image bakes
`BASH_MAX_OUTPUT_LENGTH=8192` (issue #1987). Claude Code hard-cuts the
result at that length with no notice, so any verdict whose base64 exceeds
8141 characters (about 6 KB of Markdown) reaches the host as undecodable
base64. A sweep of that day's 48 research logs found roughly 30 first
emissions cut this way; the runs that succeeded did so only because the agent
happened to notice and re-emit a shorter body, against a prompt that says
"print exactly ONE such line" and names no limit. A second defect masked the
first: the shared signal scanner accepted an empty payload as verified, so
the assistant's own echoed `printf` command line, or a later `grep` naming
the marker, could win the last-verifying-wins selection over a genuine
payload. Issues #3668 and #3670 fixed the scanner and made a truncated line
fail loudly, and #3669 tells the agent the budget; none of them removes the
cap, because the cap is a property of the carrier.

The carrier is the thing to change. The three mid-run signal channels —
`SPINDRIFT_COMMENT`, `SPINDRIFT_PR_INTENT`, `SPINDRIFT_ISSUE_INTENT` — share
one shape: a body the Launcher will write somewhere with its own token, at
settle, after the Box is dead ([ADR 0041](0041-research-filing-is-host-mediated-and-relay-only.md)).
None of them needs to be in the log at all. They ride it because
[ADR 0032](0032-host-mediated-local-issue-content.md) reasoned that under
bwrap "the only way to get bytes out … is a *writable* host bind, a larger
breach than the read-only read mount", and a stdout block needed no bind.
That premise expired when the seam bundle made the outbox a writable bind on
every runner, and [ADR 0044](0044-private-registry-credentials-live-in-a-launcher-side-proxy.md)
then opened the first Box-to-Launcher *socket*, a per-Box listener the Box
talks to, with a probed TCP fallback where a unix socket cannot cross the
runtime boundary. The posture "the Box talks to a Launcher-owned listener"
is therefore already accepted, and for a heavier payload than a comment.

**Decision: the three mid-run signal channels cross the Box seam over a
second Launcher-owned listener, the signal socket, selected per Dispatch by
a new knob `BOX_SIGNAL_CARRIER` whose default keeps today's log carrier.**
The outcome line stays on the log: it is a few dozen bytes, it is the last
thing a Box says, the entrypoint already re-emits it as a bare leading line
outside the driver stream, and [ADR 0055](0055-structural-scoping-is-the-outcome-freshness-boundary-nonce-is-only-for-mid-run-signals.md)
built host-decided disposition around it. The review verdict marker is
orchestrator-internal, pass to pass, and is not a host write; it is out of
scope.

## What the socket is, and is not

The signal socket is not a general channel from the Box to the host. Its
contract is the one [ADR 0034](0034-host-mediated-github-forge-and-issue-access.md)
already states for host-mediated writes: **the Box controls the content of
a write, never its destination.** Message kinds are a closed set keyed by
route, not by a field the Box fills in — the verdict comment for *this*
Dispatch's issue, the PR intent for *this* Dispatch's branch, issue intents
filed under *this* Dispatch's provenance label. No issue number, label name,
branch, or target ever appears in a message. A request for any other route
is refused.

It also does not act. The Launcher validates each message on receipt,
answers accept or reject, holds the accepted body in memory, and performs
the write at settle exactly as it does today (ADR 0041's file → comment →
label order is unchanged). Posting immediately would hand a prompt-injected
agent unlimited comment attempts during the run and dissolve the
one-verdict-per-run invariant; buffering keeps the write a single deliberate
host act after the Box has exited. The synchronous reply is *validation*,
which is the one property no after-the-fact carrier can offer: the agent
learns at once that its verdict was too large, malformed, or of a kind this
run will never consume, instead of discovering nothing until the label
flips.

The wire is HTTP, because the listener is `http.Serve` on both transports
already. One `POST` route per kind, JSON envelope in the body:

- comment: `{body}` — repeated sends **replace**, last accepted wins, so a
  verdict can be revised;
- PR intent: `{title, body}` — explicit fields replace the "title, blank
  line, body" blob the log carrier parses today; repeated sends replace;
- issue intent: `{title, body, type}` with the closed `bug|enhancement|chore`
  enum the host already maps to labels (ADR 0041) and no labels field at all;
  repeated sends **append**, deduplicated by content hash, capped per run.

Validation is bounded and boring: the transport secret where the transport
needs one, UTF-8, non-empty, body size at the tracker's own limit (64 KiB
for GitHub), a small per-run issue-intent count, and a 409-class refusal for
a kind the current Dispatch will never consume (a comment in a work-path
run, say) rather than an accept the settle would silently drop. Accept
replies carry kind, byte count, content hash and a sequence number; reject
replies carry a status and one line of reason. Nothing in a reply names a
path, a token, or an issue. One `GET` status route returns the kinds and
hashes accepted so far, which is what the in-box marker gate (issue #1938's
resume nudge for a `ready` outcome with no PR intent) asks instead of
re-scanning the Box's own log. Every accept and reject is mirrored into the
driver log as a `spindrift_op` event with kind, size, hash and disposition,
so the post-hoc trace recipe that diagnosed this incident keeps working and
the audit trail does not regress when the payload leaves the log.

## Its own listener on shared plumbing

The signal socket is a **separate listener** from the registry proxy, not a
route on it. ADR 0044 defines the proxy as a GET/HEAD-only single-hop
forwarder whose every request is checked against a host-rooted path set
([ADR 0047](0047-registry-routes-host-rooted-enforced-by-construction.md)),
and it starts only when a routes file is configured. A POST control plane
inside it would contradict that contract, force the proxy up for Consumers
with no registry routes, and put control messages behind enforcement logic
written for package downloads. The deletion test settles it: remove the
proxy and the signal socket must still exist; remove the signal socket and
the proxy's contract must not change.

What it shares is everything below the listener: the transport probe and its
cache (the socket transport is unavailable on macOS Docker and the probe,
with issue #3466's control probe, decides unix versus TCP per runtime and
image), the socket directory with its `sun_path`-overflow fallback (issue
#3077), the socket-mount spec (generalised from exactly one socket to a
list), the crypto-random secret recipe with the same argv-hygiene under
bwrap, and the TCP fallback with the host-gateway alias. **The TCP fallback
is accepted under the same secret discipline as the proxy.** A unix-only
signal socket would make `socket` mode unusable on every macOS Docker or
Rancher Consumer and turn the eventual default flip into a breaking change
for exactly the platforms that most need parity; the proxy already made the
stronger version of this call while holding registry credentials, whereas
the signal socket only buffers text. The network modes that hard-fail the
proxy when TCP would be needed (`none`, `no-host-loopback`) hard-fail
`socket` mode the same way, at startup, capability-style like the read-only
gate — never a silent fallback.

The per-run control nonce does **not** become the socket's bearer. The code
already keeps `RUN_NONCE` and the proxy's TCP secret deliberately distinct
because they have different security roles (issue #1937); the signal socket
mints its own secret the way the proxy does. For the three channels this ADR
moves, the nonce retires: its job was to tell a line this run's Box
deliberately printed from one an untrusted author echoed into the log, and
echoed text cannot reach a socket by accident. Untrusted text can still
*persuade* a hijacked agent to send a bad verdict — nothing on any carrier
prevents that, which is why content-not-destination and buffer-not-act are
the load-bearing properties, not the nonce.

## One knob, one verb, no fallback

`BOX_SIGNAL_CARRIER` is one enum with two values, `log` (default) and
`socket`, moving all three channels together. Per-channel knobs were
rejected: no Consumer wants a mixed state, and three carriers in every
combination triples the fragment variants and the test surface for a
rollout convenience we do not need. In `log` mode nothing changes for any
existing Consumer — no listener, no mount, no probe, and the prompt
fragments render the marker-line instructions exactly as today — which is
what makes this not a breaking change at introduction. In `socket` mode a
marker line for one of the three channels that still appears in the log is
**ignored with a warning, never accepted as a fallback**: a fallback keeps
the 8192-character defect alive through the back door and invites double
delivery.

Fragment selection is a run-time gate computed in-box from the Dispatch's
environment, the same mechanism that picks the read-only research verdict
fragment today, so both variants bake into the image and the knob picks at
run time. No rebuild is needed to flip.

The in-box front is a **`driver-exec` verb only** (`driver-exec signal
comment|pr-intent|issue-intent|status`), where
[ADR 0036](0036-host-box-handoff-branching-belongs-to-driver-exec-verbs.md)
puts hand-off branching. It reads endpoint and secret from its own
environment, posts, prints the accept or reject line, and exits non-zero on
reject so the agent sees a failed command. The read-only `gh` shim was
considered as a second front that would translate `gh issue comment` and
`gh pr create` onto the verb, and rejected: the shim is generated POSIX
shell with exactly two behaviours, it is not installed at all in the default
read-write mode (including a default local run), and a flag translator in
generated shell is a fresh place for the silent partial success this ADR
exists to remove. The shim's refusal message names the verb instead, which
closes the habit gap in one step. If dogfood shows agents fighting that
refusal, translation can be layered on later without touching the contract.

## The daemon flips it by restart

The daemon (ADR 0051) runs the knob through the child environment it owns
(the daemon architecture review settled that the daemon builds each child's
environment explicitly rather than leaking its own), and changes it by
restart: `BOX_SIGNAL_CARRIER=socket nix run .#daemon`. A live toggle was
rejected. The daemon's contract is "configuration at start, restart to
change", and a hot toggle for one knob would be its first exception; if
per-wave experiments are ever wanted, a general reload-on-signal feature is
the right shape and should be argued on its own merits.

## Rollout: flip on evidence, remove on its own boundary

The default moves from `log` to `socket` on a minor version boundary once
the daemon has run both Dispatch kinds through the socket for a stated span
with no socket-attributed `agent-failed` or `agent-research-failed` — that
evidence bar is the flip trigger and belongs in the spec, not in a
conversation. The `log` value stays selectable through that minor and the
three log-carrier scanners stay in the code. Removing `log` for those
channels — and with it the nonce defense, the marker-line fragments and the
scanners — is a separate later decision on a major boundary or after a
stated deprecation window, ticketed when the flip lands. Flipping and
removing together was rejected because it gives a Consumer who has not
opted in the socket and its transport probe in one step with no way back;
never flipping was rejected because it leaves the defect class as every new
Consumer's default experience.

## Considered and rejected: the outbox file

The obvious cheaper fix was to have the agent write the verdict body to a
file in the outbox, which the Launcher already reads for the bundle and the
pass manifest. It was rejected in favour of the socket for one reason that
outweighs its lower cost: **a file gives the agent no feedback.** The
incident's shape was an agent that could not tell its comment had been cut;
a file it can write freely leaves that blindness in place and moves the
failure to the tracker's own limits. Two secondary reasons: the outbox
directories are world-writable so the Box's mapped uid can write them, which
the log is not, so a file is marginally more exposed on a multi-user host
than either the log or a socket; and a file carrier would sit beside the
log carrier as a third mechanism the eventual socket would then tear out.

## Consequences

- `socket` mode makes the transport probe run for every OCI Consumer who
  opts in, not only those with registry routes: up to four throwaway
  containers on the first Dispatch per runtime and image, cached after.
  bwrap needs no probe.
- The Box gains one more listener endpoint and secret in its environment;
  per [ADR 0046](0046-the-turn-configuration-crosses-the-box-seam-as-one-handoff-document.md)
  they join the existing handoff rather than arriving as loose variables.
- The marker-channel registry in the prompt contract gains a carrier value
  and a defense value for the socket, and the eval-time contract check
  asserts both fragment variants, so the invariant that every required
  signal has a prompt instruction holds in both modes.
- ADR 0032's stdout rationale, ADR 0034's PR-intent line, ADR 0055's
  nonce-as-sole-defense for mid-run signals, ADR 0041's log-buffered intents
  and ADR 0044's "opens no host TCP port" are each partially superseded by
  this ADR for the three channels it moves; each keeps governing the
  outcome line and the `log` carrier until removal.
