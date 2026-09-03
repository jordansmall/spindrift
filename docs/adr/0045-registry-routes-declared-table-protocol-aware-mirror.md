# Registry routes: a declared table, a protocol-aware mirror, discovered configuration

Supersedes ADR 0044. The containment model it established is kept whole; the
configuration surface, the proxy's response policy, and the Box handoff are
redesigned on the evidence its five amendments accumulated.

## Context

ADR 0044 shipped private-registry support as a launcher-side Registry proxy:
the credential lives in the launcher, the Box gets an unauthenticated channel
authenticated on its behalf, clients are pointed at that channel by
configuration rather than interception, and an in-tree rewrite tagged
`skip-worktree` keeps the plumbing out of the repository. Those properties
held. Everything around them accumulated corrections faster than the document
could absorb them — five amendments, each retracting part of an earlier
claim — and bringing up one real Artifactory Consumer surfaced the pattern
behind the churn:

- **The model was scalar where reality is a table.** One upstream URL, one
  credential, one hardcoded auth scheme. The bare-origin restriction on the
  upstream URL existed only because host-substitution needed it; the path
  allowlist was structurally unable to match a registry served under a base
  path; a second registry host was inexpressible.
- **Configuration was five interdependent env knobs** whose constraints lived
  in prose (`_FILE_FORMAT` only with `_FILE`; `_CREDENTIAL_CARGO_REGISTRY_NAME`
  only with one format), one of them named after a single ecosystem, all of
  them required to agree with what the Target repo's own config files already
  declare — with nothing checking the agreement.
- **Config-layer binding redirects only the requests a client derives from
  config.** URLs embedded in *responses* — a cargo sparse index's `dl`, an npm
  packument's `dist.tarball` — leak to upstream uncredentialed. For a
  registry with no anonymous read this is fatal: the motivating Consumer
  resolved 311 packages through the proxy and then failed every crate
  download with a 401. The npm variant of the same gap was patched with a
  `ModifyResponse` hook once (issue #2854) and reverted; the cargo variant
  (issue #3129) arrived with a Consumer bleeding on it. Fighting this
  per-emergency was the wrong altitude.
- **The Box handoff sprawled.** "Where the proxy is" traveled as four loose
  scalars (socket path, TCP host, TCP port, secret) over 10–13 hops on two
  parallel channels, with struct fields only tests read, a 40-line transport
  gate duplicated across both verb modes, and a Forwarder port declared in
  three hand-synced places.

## Decision

### One declaration: the route table

A Consumer declares registries in a **routes file** — TOML, named by a single
knob, `REGISTRY_PROXY_ROUTES_FILE`. Each entry is a **Registry route**:

```toml
[[routes]]
match-host = "artifactory.example.com"
upstream-base-url = "https://artifactory.example.com/artifactory"
auth-scheme = "bearer"                       # bearer | basic | header:<Name>; default bearer
credential = { netrc = "~/.netrc" }
# enforce-allowlist = true                   # optional, default false
# cargo-registries = ["example-remote"]      # optional; the Target repo's [registries.NAME] entries this route serves
```

Credential and upstream are bound in the same record — the property the 0044
route-table amendment identified as load-bearing: the Box may select a route,
but there is no Box-reachable way to pair credential A with host B. The
`upstream-base-url` may carry a path; the bare-origin launch gate is retired
because the constraint it enforced is now structural (see the mirror, below).

The five `REGISTRY_PROXY_*` scalar knobs are **deleted, not deprecated**. A
set knob fails the launch gate with an error that names the routes file and
shows the equivalent stanza. Keeping a second, shallower way to say the same
thing would mean testing and documenting both surfaces forever.

The routes file is a runtime input, like the upstream URL it replaces:
private hostnames stay out of the world-readable nix store by construction.
It carries credential *references*, never values.

Parsing the file takes a real TOML dependency — the launcher's first non-TUI
dependency, accepted deliberately: it also replaces the hand-rolled TOML
extractor `cmd/launcher/internal/credresolver/cargocredentials.go` carried, and the hand-rolled parsing this
feature accreted (~440 lines across three files) was itself a finding.

### One question: the credential resolver

Credential ingress becomes a **resolver** with one interface — *the
credential for host H* — and sources as adapters behind it:

| source | shape | note |
| --- | --- | --- |
| `env` | variable name | value read at startup, then unset (unchanged from 0044) |
| `file` | path | whole contents are the credential (unchanged) |
| `netrc` | path | keyed by host — one file answers N routes |
| `cargo-credentials` | path + registry name | cargo's own `credentials.toml` |
| `npmrc` | path | `//host/:_authToken=` lines, keyed by host |
| `gradle-properties` | path + key | JVM shops' `~/.gradle/gradle.properties` |
| `exec` | argv | run it, stdout is the credential — the credential-helper pattern |

`exec` is the escape hatch that caps this table: any secret manager
(1Password, vault, pass) is one command line, so no further format extractor
is ever load-bearing. Resolution happens host-side at launcher startup;
`fromEnv` values are unset from ambient immediately after, exactly as 0044
decided. Nothing here changes what the Box can read: still no credential, on
any path.

### Discovery writes the file; doctor keeps it honest

The Target repo already declares which registries it uses — in
`.cargo/config.toml`, `.npmrc`, `.yarnrc.yml`, `pnpm-workspace.yaml`, gradle
build files. **`spindrift registry discover`** runs the same parse the
in-tree rewrite already performs, pointed the other way: it extracts hosts,
upstream base URLs (the committed index URL carries the base path the
operator previously had to transcribe), and cargo registry names; matches
credentials against the well-known host-keyed stores above; probes the auth
scheme from the registry's `WWW-Authenticate` answer; and writes the routes
file. References only — discovery never copies a credential value anywhere.

The file remains the operator-blessed source of truth. `spindrift doctor`
re-runs discovery in check mode and reports **drift** — "the repo names
host X; no route covers it" — and the in-Box scan emits the same advisory at
dispatch (the *feature on but not covered* register issue #3082
established). Discovery is **never** a runtime behavior fed from inside the
Box: if the launcher minted routes for whatever hosts the in-Box scan
reported, an Agent that edits `.cargo/config.toml` mid-run could steer a
real credential toward any host the operator's stores happen to hold. Routes
are created at setup time, by the operator, or not at all.

Setup is therefore one command and a glance at the file; maintenance is
reacting to a doctor warning.

### The proxy is a protocol-aware read-only mirror

The proxy's promise deepens from *relays your request* to **everything the
client fetches next stays on the credentialed path**. It remains a read-only
mirror — `GET`/`HEAD` only, credential attached per route on the outbound
leg only, never carried across a redirect — and gains a **shape-keyed
response-rewrite table**: for an enumerated set of exactly-identified request
shapes, the proxy rewrites URLs embedded in the response body that name the
route's own `match-host`, re-pointing them at the Forwarder.

v1 has one row: a cargo sparse index's `config.json`, whose `dl` field is
host-swapped (path preserved, route prefix inserted) so crate downloads
travel the same credentialed path the index does. A `dl` naming any other
host — a CDN, a mirror — is left untouched: there is no credential for that
host, and rewriting it would make the proxy an open relay. npm's packument
`dist.tarball` is the same class and becomes the second row when a Consumer
needs it; adding a row is additive and does not reopen proxy policy.

This deliberately reverses 0044's "no body rewriting" closure, on the
evidence 0044 itself demanded: issue #2854's reverted npm hook failed
because it inspected every response and guessed (wrong media-type match, a
`HEAD` crash, a path double-join); shape-keying removes the guessing, and
the route record's base path removes the join ambiguity, so the defect class
is excluded by construction rather than fixed. Issue #3129 carries the
fatal-evidence case.

### The allowlist becomes matchable, and enforcement becomes opt-in

Requests arrive at the proxy carrying a route prefix; stripped of the prefix
and relativized against the route's base path, they finally have the shape
the allowlist patterns were written for — so the patterns now carry signal
for Artifactory-style deployments instead of being structurally inert.

The default posture stays **advisory** (logged, relayed): the patterns are
still not provably complete, and a false denial presents to the Agent as a
registry outage. A route may declare `enforce-allowlist = true`, turning
out-of-pattern requests into a 403 whose body names the policy. This is a
tightening-only knob — the inverse of the read-only rule, which forbids
knobs that loosen. An operator whose registry host also fronts non-registry
paths now has a real bound on what the Box can reach with their credential,
instead of an advisory one. This tolerance has a sharp edge on a cargo route:
the Forwarder re-points a cargo registry's `dl` field at this proxy (see
above), but that rewritten download path is deliberately outside the derived
allowlist, so `enforce-allowlist = true` on a cargo route 403s the crate
downloads it's routing, not just stray non-registry traffic.

The advisory logging is kept per route (issue #3176), named by prefix in
every line: one route's upstream matching the allowlist says nothing about
another route's, since each names a distinct upstream that may or may not be
root-served, so a build wiring several routes gets one suppression
lifecycle per route rather than one shared across the whole proxy. Within a
route, what a suppressed run's final summary counts is distinct paths, not
requests, excluding the one path already named by the first, fully logged
miss — a build looping over that same one out-of-pattern path a thousand
times produces only that one detailed line and no summary at all; the
summary covers just the further distinct paths beyond it.

### One handoff: the manifest

Everything the Box needs to know about the proxy crosses in **one JSON
document**, the env var `REGISTRY_PROXY_MANIFEST`:

```json
{
  "endpoint": "unix:///registry-proxy.sock",
  "routes": [
    { "prefix": "artifactory-example-com",
      "upstreamHost": "artifactory.example.com",
      "cargoRegistries": ["example-remote"] }
  ]
}
```

`prefix` is a slug of the route's match host (every character outside
`[a-z0-9]` mapped to `-`); a table-order collision between two routes'
slugs gets a `-2`, `-3`, ... suffix. An `r<index>` fallback exists for an
empty slug, but it is an internal guard only — the routes-file parser
rejects an empty `match-host`, so no operator input reaches it.

`endpoint` is a typed value — `unix://<path>` or `tcp://<host>:<port>` —
minted once by the launcher from the transport probe 0044's #3111 amendment
introduced (the probe itself is unchanged). The TCP secret stays a separate
env var in the runner's secrets set, kept off container argv. Env is the one
channel that works on every transport: the TCP fallback exists precisely for
runtimes where host filesystem sharing is broken or absent, so a mounted
manifest file cannot be the carrier.

Consequences of the single handoff:

- `Runner.RegistryProxyTransport()` returns the endpoint (or a typed error),
  not a four-tuple of mutually-constrained scalars; the runner fake scripts
  one value and cannot be set incoherent.
- The verb parses the manifest once; both verb modes share one
  transport-and-readiness gate, deleting the duplicated 40-line gate and the
  double Forwarder probe/spawn.
- The Forwarder port stops being declared in three hand-synced places; the
  verb owns it.
- `RegistryProxyLocation`'s test-only fields go, along with the entrypoint's
  per-value flag plumbing.

The cargo placeholder mechanism (0044's #3053 amendment) is unchanged in
kind and becomes per-route: `CARGO_REGISTRIES_<NAME>_TOKEN` placeholders are
derived from the manifest's `cargoRegistries`.

### One table: the ecosystem package

Per-toolchain knowledge concentrates in a new package,
`cmd/launcher/internal/ecosystem`: one row per ecosystem carrying lockfile
names, allowlist patterns, in-tree config paths, and the env-export /
home-config / placeholder render functions as row values. `registryproxy`
imports it for patterns; `bindregistry` imports it for the rest; neither
imports the other. This replaces the three partial tables and four
hand-written renderer functions the implementation grew, retires the one
`if ecosystem == "cargo"` branch in the verb, and lets diagnostic messages
derive their ecosystem list from the table instead of nine hand-maintained
strings. Adding an ecosystem is one row plus its tests.

## What 0044 established and this ADR keeps

- The credential lives in the launcher; the Box holds none, on any
  transport. Read-and-unset at startup. Nothing secret is ever evaluable at
  the flake layer or reaches the nix store.
- The Box is a confused deputy by construction: it may *use* the channel,
  never *read* the secret, and — via the same-record binding — never
  *redirect* a credential to a host it wasn't declared for.
- Read-only is an invariant, not a knob: `GET`/`HEAD` gated ahead of the
  credential-attaching hook on both transports; no loosening knob exists.
- Redirects are relayed, never followed with the credential attached.
- Binding is configuration, not interception; the in-tree rewrite stays a
  textual host substitution tagged `skip-worktree`, reverted around
  harness-driven git, invisible to `git status` and unstageable.
- Transport is probed, not assumed; the TCP fallback carries a per-run
  secret checked ahead of everything; `no-host-loopback` plus an incapable
  socket fails loudly before any Box starts.

## Consequences

- **Breaking for existing configurations.** The scalar knobs error at launch
  with the migration stanza. Accepted while the Consumer population is
  effectively one.
- **A TOML dependency enters the launcher.** Paid once, and it deletes more
  parsing than it adds.
- **The proxy now inspects an enumerated set of response bodies.** The
  rewrite table is proxy policy; every row added is a policy change and gets
  reviewed as one. Rows are shape-keyed — no content sniffing — and a
  response matching no row is relayed byte-for-byte.
- **Discovery reads the operator's credential stores** (to match, never to
  copy). Its output is inspectable before anything uses it.
- **The residual from 0044 narrows but does not close**: a response URL
  naming a genuinely foreign host still leaves the proxy uncredentialed, by
  design.

## Delivery

Three specs: **A** — route table, resolver, discovery, manifest (the
declaration-to-Box data flow); **B** — the protocol-aware mirror, blocked by
A; **C** — the ecosystem package, blocked by A, independent of B.

## Amendment (issue #3145): the `credential` key is optional

The Decision's example stanza writes `credential = { netrc = "~/.netrc" }`
uncommented, alongside `match-host`, `upstream-base-url`, and `auth-scheme` —
next to `enforce-allowlist` and `cargo-registries`, which the same example
marks `# optional`. That reads as `credential` being required. It is not:
`registryroutes.Parse` treats a route that omits the `credential` key
entirely as `credresolver.Config{}`, the resolver's own documented
unauthenticated pass-through, same as `REGISTRY_PROXY_UPSTREAM_URL` alone
was under the retired scalar knobs (0044). A route that does write a
`credential` table still must name exactly one source key — a
present-but-empty `credential = {}` is a config error, not a shorthand for
"none" — so omission, not an empty table, is how a Consumer declares a
pass-through route.

## Amendment (issue #3201): `cargo-registries` restricts a replacement, it no longer keys a rewrite

The Decision's example stanza and "What 0044 established and this ADR
keeps" both describe `cargo-registries` and the in-tree rewrite as this ADR
found them: `cargo-registries` names the Target repo's own `[registries.NAME]`
entries a route serves, and cargo was one more row bound by the same
"textual host substitution tagged `skip-worktree`" every ecosystem used. The
field's *meaning* is unchanged by this amendment — it still names which of
the repo's declared registries belong to this route — but cargo's *binding
mechanism* is no longer the rewrite this document otherwise still describes
correctly for npm, yarn, and pnpm. Cargo now binds by **source replacement**:
a stanza rendered into the Box's own `$CARGO_HOME/config.toml` after clone,
keyed off the repo's tracked `.cargo/config.toml` left exactly as the repo
wrote it (see the ADR 0044 #3201 amendment for the mechanism and why —
lockfile identity, not this document's containment model, which is
untouched).

That relocation changes what `cargo-registries` *does*, not what it *names*.
Before this amendment, a route's cargo registries existed only implicitly —
whichever names the in-tree rewrite happened to touch on the Target repo's
already-rewritten config, with `cargo-registries` narrowing the placeholder
derivation the #3053 amendment introduced. Now the field gates a real
decision at replacement-planning time: a route with a non-empty
`cargo-registries` gets a `[source.…]` replacement stanza only for the
names it lists, and a repo-declared registry that matches the route's
`upstream-base-url` host but is absent from the list produces a warning
naming both the registry and the route rather than a silent stanza. A route
that omits `cargo-registries` falls back to scanning every `[registries.NAME]`
table in the repo's **un-rewritten** config for one whose `index` host
matches the route — not, as before this amendment, a rewritten one, since
there is no longer a rewritten copy to scan. The one line this ADR's Decision
states about `upstream-base-url` — that it may carry a path — binds here too:
the spike behind this amendment (#3200) confirmed against real cargo 1.97.0
that the URL a route's replacement stanza carries into `[source.…]` must be
the registry's sparse index root, the same shape `index` already takes in the
repo's own config, not merely a matching origin.

The line above under "What 0044 established and this ADR keeps" describing
the cargo placeholder as "unchanged in kind" and "derived from the
manifest's `cargoRegistries`" is superseded for cargo specifically by this
amendment: the placeholder now names the replacement source
(`spindrift-registry-proxy` or `spindrift-registry-proxy-<prefix>`), not a
registry from `cargoRegistries` directly, because cargo binds credential
lookups to the replacement source once a source is replaced. `cargoRegistries`
still flows through the manifest unchanged — only what the Box does with it
downstream, once the Target repo is on disk, moved.
