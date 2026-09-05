# Registry routes are host-rooted and enforced by construction, not base-path-scoped and advisory

Supersedes [ADR 0045](0045-registry-routes-declared-table-protocol-aware-mirror.md).
The containment model it established — credential in the launcher, Box
holds none, read-only mirror, binding by configuration, routes created at
setup time by the operator — is kept whole. What this ADR replaces is the
serving model underneath it: the route table stops being keyed to one base
path per host and the allowlist stops being an advisory knob a route can
leave off. Spec #3253 carries the full grilling record and the
implementation plan; this document is the decision record.

## Context

A 2026-09-04 field run brought up a real Artifactory Consumer and produced
three failures in sequence, all traceable to one modeling error in ADR
0045's route table:

- The route's `upstream-base-url` had to be hand-corrected from a bare host
  to a full index URL before anything worked, because the field's only
  home for "where does this registry live" was one URL an operator had to
  transcribe correctly by hand.
- The repo's two cargo registries — an internal repo and a crates.io
  remote — shared one host but could not share the one route that host
  permits, and silently folded down onto a single index, making the
  crates.io dependencies unresolvable with no error at the point of
  failure.
- After both of those were fixed by hand, crate downloads still failed,
  because Artifactory's download endpoint is a *sibling* of the index
  path rather than something nested under it, and the Forwarder's
  under-base-path rewrite refuses to touch a URL that isn't under the
  route's own base path:

  > `registryproxy: cargo config.json: dl "…/v1/crates" is not under the
  > route's own upstream base path, left unchanged`

  left unrewritten, that `dl` field pointed the network-less Box straight
  at the real registry, which it cannot reach.

All three trace to the same root cause. ADR 0045 modeled a route as *one
base path under one host*: match a host, scope everything to a path
prefix under it, and treat anything the client fetches outside that
prefix as foreign. That shape assumes a registry's own endpoints nest
under one path and that one host serves at most one registry a route
cares about. Neither held for the Consumer that actually showed up: one
host served two independent registries at unrelated paths, and a single
registry's own download endpoint lived beside its index rather than under
it. A model built around "under this path" cannot express "same host,
different registry" or "same host, different endpoint" — it can only
narrow or widen one prefix, and every fix available inside that shape
made the next failure in the list worse, not better. The fix is not a
better prefix; it's rooting the route at the host and deriving what is
in-policy from the Target repo's own committed configuration instead of
from an operator-transcribed path.

The containment grilling that followed settled four core decisions, in
this order: enforce-by-construction over advisory or scoped-credential
postures; host-side snapshot derivation over in-box relay or static
transcription; same-host-only `dl` pinning over adjacency rules or
operator confirmation; additive `allow` over an off switch or no
recourse — hard-enforce was chosen even before the `allow` refinement
existed to soften it.

## Decision

### Enforcement is unconditional, not advisory or scoped-credential

The Forwarder forwards a request only if its path is in the enforced set;
everything else is answered 403 before any upstream dial, with no
credential attached. There is no advisory mode and no `enforce = false`.
The `enforce-allowlist` route key and the advisory allowlist-miss logging
machinery ADR 0045 introduced are retired outright; the 403 body follows
the existing policy-naming shape, naming the refusing policy and the
derived set.

Two alternatives were argued and rejected. **Advisory-by-default**, ADR
0045's own posture, was rejected because the field run is exactly what an
advisory default produces: not one of its three failures announced itself
where it happened. The bare-host base URL resolved nothing, the
two-registry fold-down left the crates.io dependencies unresolvable with
no error at the point of failure, and the sibling `dl` was logged "left
unchanged" and then handed to the Box, which aimed it at the real
registry rather than being refused early and loudly. A false denial's
cost — presenting as a registry outage to an Agent mid-build — no longer
outweighs a leak's cost now that the field run has shown a real Consumer
leaking: the pass-through leg forwarded cargo's placeholder token to
Artifactory (see [The Forwarder always strips the inbound Authorization
header](#the-forwarder-always-strips-the-inbound-authorization-header)),
the benign version of exactly the seam a real credential would travel.
**Scoped-credential postures** — minting a credential that is itself
restricted to the allowed paths, so an out-of-policy request fails at the
upstream rather than at the Forwarder — were rejected because that
restriction lives in a system the launcher doesn't control (the upstream
registry's own credential scoping, where it exists at all) and does
nothing about a response-body-derived URL like `dl`: the failure that
mattered most in the field run was never about the credential being too
powerful, it was about the Forwarder not knowing a path was in-policy at
all. Enforcement has to live at the one point that sees every request,
which is the Forwarder itself.

### The enforced set is derived host-side, pre-Box

The launcher scans the Target repo's committed registry configuration at
the ref it dispatches, and hands the Forwarder the derived path-set
before the Box starts. The in-Box re-render — the textual rewrite ADR
0045 already performs for npm, yarn, and pnpm, and the source-replacement
stanza it performs for cargo — continues to do client binding only and
contributes nothing to policy. A mid-run Agent edit to the repo's
registry configuration cannot change what the Forwarder enforces,
because the Forwarder never looks at the Box's copy of that
configuration again after dispatch.

Two alternatives were rejected. **In-box relay** — deriving the enforced
set from whatever the Box's own scan reports at request time — was
rejected for the reason ADR 0045 already gave for discovery: an Agent
that edits `.cargo/config.toml` mid-run could steer policy toward
whatever host its edit names, turning the enforcement mechanism into
something the very thing it's meant to contain can widen. **Static
transcription** — an operator hand-copying the paths a registry uses
into the route once, the way `upstream-base-url` had to be
hand-corrected in the field run — was rejected because it reproduces the
first failure exactly: a human transcribing a path by hand is the
mechanism that produced the bare-host error the field run started with.
Deriving the set from the repo's own committed config at the dispatched
ref removes the transcription step and ties the policy to the same
source of truth the client bindings already read.

### The cargo `dl` base is learned from upstream, same-host pinned

On rewriting a `config.json` response, the Forwarder records the `dl`
base it observes and adds its subtree to the enforced set — if, and only
if, the `dl` URL's host matches the route's own matched host. A
cross-host `dl` is refused: logged, not added, and left unrewritten so
the failure is loud at fetch time rather than silent. Learned entries
are per derived index base, so multiple registries sharing one host each
accumulate their own `dl` subtree independently — the composition the
base-path model could not express, since the field run's second failure
(two registries on one host) and its third (a `dl` sibling to the index)
have to hold at once.

Two alternatives were rejected. **Adjacency rules** — treating any path
"near" the index base, by some heuristic distance, as in-policy — were
rejected because "near" has no principled definition: the field run's
own `dl` endpoint is a sibling of the index path, not a descendant of
it, and any adjacency heuristic loose enough to admit a sibling is loose
enough to admit paths that were never the registry's at all. **Operator
confirmation** — pausing to ask a human before adding a learned `dl`
subtree — was rejected because it reintroduces a human in a path that is
supposed to run unattended at dispatch time and during an agent's build,
and because the information needed to judge the request correctly — is
this `dl` host the same host the route already trusts — is exactly the
same-host check the Forwarder can already make itself. Same-host pinning
answers both alternatives without adding either a heuristic or a human.

### Gap recourse is an additive `allow` key, never an off switch

A route may declare extra path patterns that extend the derived
enforced set. The key is empty in the happy path — nothing needs to be
declared for a registry whose committed configuration already says
everything the Forwarder needs — and is validated like every other route
field.

Two alternatives were rejected. **An off switch** — a route-level knob
that disables enforcement entirely, mirroring the `enforce-allowlist`
key this ADR retires — was rejected because it reopens exactly the
enforce-by-construction decision settled above: a route with the switch
flipped is indistinguishable, at request time, from the ADR 0045 world
the field run failed in. **No recourse at all** — accepting that some registries
serve paths a committed config can never name and leaving those
Consumers with no path forward but disabling the feature — was rejected
as an availability regression the derivation model shouldn't impose:
the enforced-set derivation is a strong default, not a claim of
completeness, and an operator who knows their registry serves an
additional path deserves a way to say so without giving up enforcement
everywhere else. `allow` is additive only — it can widen the derived
set, never narrow or disable it. That one-directional shape is what it
shares with `enforce-allowlist`: each moves policy one way only, and
neither can switch enforcement off. The direction differs — 0045's key
could only tighten, `allow` only loosens — because enforcement is no
longer the opt-in corner it was under 0045 but the model's default,
so the recourse a Consumer needs from it points outward, not inward.

### The route table surface: `upstream-base-url` retired, `upstream-origin` and a go path arrive

`upstream-base-url` is retired. A routes file that still declares it is
rejected at the launch gate with a remedy naming the migration and
printing the equivalent new stanza — the same retired-scalar-knob
precedent ADR 0045 used when it deleted the five `REGISTRY_PROXY_*` env
knobs. In its place, a route matches a host, not a base path:

```toml
[[routes]]
match-host = "artifactory.example.com"
credential = { cargo-credentials = "…", registry-name = "artifactory" }
# optional: auth-scheme, upstream-origin, go path declaration, allow
```

An optional `upstream-origin` key covers the two things a committed
config can't always supply on its own: scheme and port, and a host that
serves only ecosystems without any committed config for the launcher to
scan (nothing in `.cargo/config.toml`, `.npmrc`, or the like names it).
Where a committed config is present, the derivation reads the base path
directly from it — the committed index URL already carries whatever path
an operator previously had to transcribe by hand — so `upstream-origin`
stays optional rather than becoming a second required field that could
drift from the config it should agree with.

Go gets an explicit declaration for the same reason `upstream-origin` is
optional rather than derived: GOPROXY has no in-repo declaration to scan.
A route may declare the go path directly; absent it, go is simply not
bound through that route. The declared path joins the enforced set as
operator-owned policy, the one part of the enforced set that is not
derived from the repo's own configuration, because for go there is
nothing in the repo to derive it from.

### The response-rewrite closure holds without a base path

ADR 0045 credited part of the response-rewrite defect class's exclusion
to the route record's base path: shape-keying removed the guessing, and
the base path removed the join ambiguity, so together they excluded the
defect class by construction rather than merely fixing it. This ADR
removes that base path, so it owes an argument for why the closure still
holds.

It holds because the ambiguity the base path was closing is closed a
different way, more cheaply, by the same host-rooted shape this ADR
already argues for elsewhere. Client bindings embed full upstream paths
under the route prefix: the cargo replacement source URL becomes the
Forwarder origin plus prefix plus the registry's full index path, the
rewritten `dl` points at prefix plus the full `dl` path, and go — or any
other env-bound ecosystem — rides the same shape. The Forwarder's side of
the mapping is symmetric: a prefixed request maps to the upstream origin
plus the verbatim remaining path. There is no join step left at all — no
base path to relativize a request against, and so no ambiguity a wrong
relativization could introduce. What 0045 bought by keeping paths short
and joining them against a base, this ADR buys more cheaply by keeping
paths long and never joining them.

The `config.json` rewrite row survives the same way. It no longer
matches by relativizing a request path against a route's base path; it
keys on the exact derived index bases the host-side derivation already
enumerates, so a row matches `<index-base>/config.json` exactly rather
than guessing by suffix or media type. The exactness 0045 located in the
base path moves onto the derived index bases instead — the shape-keying
half of 0045's closure is untouched, and the join-ambiguity half is
retired by having no join left to be ambiguous about.

### Client bindings and Cargo.lock purity are untouched

Cargo continues to bind through source-replacement stanzas — the
mechanism the ADR 0044 #3201 amendment settled and the ADR 0045 #3201
amendment carried forward — whose upstream `registry` values are the
repo's real URLs, never a Forwarder-local one. Cargo.lock therefore
continues to record the real upstream URL, not a proxy-local address, and
the settle-time lockfile tripwire that guards against a "the real thing"
regression remains the acceptance criterion it already was. None of the
host-rooted route table changes touch this:
they change what the Forwarder enforces and how a route names its
upstream, not how the client-side binding is rendered.

The repo-claimed-source-name collision fix (issue #3248) remains valid
and composes unchanged with the host-rooted route table: it still reuses
the repo's own source name where one already exists, now simply pointed
at the full-path local URL a host-rooted route resolves to, rather than
one carrying a hand-transcribed base path.

### The Forwarder always strips the inbound Authorization header

Before attaching the route's own credential — or forwarding
unauthenticated, for a route that declares none — the Forwarder strips
whatever `Authorization` header the client sent inbound. This closes a
placeholder-leak the field run also surfaced: a pass-through leg
forwarded cargo's own placeholder token to Artifactory untouched,
producing a misleading "Invalid basic authentication token" 401 that
pointed the wrong direction during debugging. The strip is unconditional
and route-independent — it costs nothing on a route that already
supplies its own credential, and it closes the one channel by which a
client-side placeholder or stale token could ever reach a real upstream.

## What 0045 established and this ADR keeps

- The credential lives in the launcher; the Box holds none, on any
  transport. Read-and-unset at startup. Nothing secret is ever evaluable
  at the flake layer or reaches the nix store.
- The Box is a confused deputy by construction: it may *use* the
  channel, never *read* the secret, and — via the same-record binding —
  never *redirect* a credential to a host it wasn't declared for.
- Read-only is an invariant, not a knob: `GET`/`HEAD` gated ahead of the
  credential-attaching hook on both transports; redirects are relayed,
  never followed with the credential attached.
- Binding is configuration, not interception: the textual rewrite for
  npm, yarn, and pnpm, and the source-replacement stanza for cargo,
  are unchanged by this ADR.
- Routes are created at setup time, by the operator, never minted at
  runtime from whatever the in-Box scan happens to report — this ADR's
  host-side derivation strengthens that boundary rather than crossing
  it: the enforced set is derived from the dispatch snapshot before the
  Box exists, not from anything the Box reports back.
- Transport is probed, not assumed; the TCP fallback still carries a
  per-run secret checked ahead of everything, and `no-host-loopback`
  plus an incapable socket still fails loudly before any Box starts.
  None of the host-rooted route table changes touch how the manifest
  reaches the Box, only what the route table inside it says.
- Matching is still one route per host: a routes file declaring two
  routes for the same `match-host` remains a config error, not a way
  to widen coverage. Host-rooting looks like it invites the opposite —
  removing the base path also removes the thing that used to
  distinguish two routes sharing a host — but the multiplicity the
  field run needed moves into the derived set instead of the route
  table: the host-side derivation enumerates every registry the Target
  repo's committed configuration declares on the matched host, each
  contributing its own derived index base and its own same-host-pinned
  `dl` subtree — the optional `cargo-registries` key only narrows that
  enumeration when a route names it, per the #3201 amendment — so one
  host serving two registries is one route with two index bases, not
  two routes.
- The credential resolver, discovery, the manifest handoff, and the
  ecosystem package are all unchanged in kind; the route table fields
  they read simply gain a host-rooted shape in place of a base-path one.

## Consequences

- **Breaking for existing routes files.** Any route still declaring
  `upstream-base-url` or `enforce-allowlist` errors at the launch gate
  with the migration stanza, following the precedent ADR 0045 itself
  set for the scalar knobs it retired.
- **A Consumer that relied on the advisory default now gets a hard 403**
  for any request outside the derived-plus-`allow` set. This is the
  intended tightening — the field run is the evidence that the advisory
  default let a real failure travel all the way to a broken build
  instead of stopping at the Forwarder — but it means a Consumer whose
  registry serves paths the derivation and `allow` don't yet cover needs
  to add an `allow` entry rather than relying on advisory pass-through.
- **The enforced set now depends on the dispatch-time snapshot, not the
  Box's live state.** A registry configuration change made mid-run has
  no effect on enforcement until the next dispatch — the same latency
  the discovery-at-setup-time model in ADR 0045 already accepted,
  extended to the new derivation.
- **The Authorization-strip is unconditional.** A route that intended to
  forward a client's own inbound Authorization header for some reason
  cannot: there is no such route today, and the strip is judged a closed
  channel worth keeping closed rather than a hypothetical feature worth
  preserving.
- **Cargo.lock purity and source-replacement binding are unaffected**;
  this ADR narrows the residual ADR 0045 already named — a response URL
  naming a genuinely foreign host still leaves the proxy uncredentialed,
  by design — without touching how the client itself is bound.

## Delivery

The implementation lands as children of spec #3253, independent of this
document: the route table and launch-gate migration for
`upstream-base-url`/`enforce-allowlist`, the host-side derivation and its
snapshot plumbing, the `dl` same-host learning and the `allow` key, the
go path declaration, and the Authorization-strip. A `MIGRATING.md` entry
accompanies that delivery, following the same retired-scalar-knob
precedent ADR 0045's own migration entries used.
