# Private-registry credentials live in a launcher-side proxy, never in the Box

> **Superseded by [ADR 0045](0045-registry-routes-declared-table-protocol-aware-mirror.md).**
> The containment model this ADR established — credential in the launcher,
> unauthenticated channel authenticated on the Box's behalf, read-only
> mirror, binding by configuration — is kept whole there. What 0045 replaces
> is everything this document's five amendments were straining against: the
> scalar upstream/credential model becomes a declared route table, the five
> env knobs become a routes file written by discovery, the no-body-rewrite
> closure is reversed into a shape-keyed response-rewrite table, and the
> four-scalar Box handoff becomes one manifest. This document remains the
> record of the decisions and evidence that got there.

## Context

A Target repo whose Project toolchain resolves dependencies through a private
registry needs a credential the Agent must never see. The two constraints are
in direct tension: the credential has to reach the thing doing the fetching
(`cargo`, `npm`, ...), and that thing is a subprocess the Agent itself spawns
inside the Box.

The existing credential machinery does not transfer. `agent/env-credential-scrub.sh`
protects `ANTHROPIC_API_KEY` and `CLAUDE_CODE_OAUTH_TOKEN` by rewriting every
Bash call to `unset` them before the Agent's command runs, and by denying every
`/proc/<pid>/environ` read outright. That works precisely because *no subprocess
legitimately needs those variables*. A registry credential inverts the case: the
subprocess is the one party that does need it. The scrub cannot be reused, and
its own header documents two vectors a rewrite could not close even for the
easier problem — a denylist is not a foundation for "no exceptions".

Two hard constraints frame the space:

- **The credential must never be readable by the Agent.** The Instruction
  surface and the comment-injection trust boundary (`CLAUDE.md`) already assume
  a prompt-injected Agent with arbitrary code execution as its own uid. A
  credential inside the Box is a credential that Agent can obtain.
- **The credential must never reach the nix store.** The store is
  world-readable and its paths can be pushed to a cache.

The motivating case makes the problem sharper than "authenticate to one private
registry". Under an Artifactory-style universal proxy, *every* dependency —
public ones included — resolves through the authenticated host. An Agent Box
without the credential resolves nothing at all: not a private crate, but
`serde`. That kills every design in which the credential is absent by default
and acquired on escalation, because escalation stops being rare and becomes
ordinary work.

Three families were considered and rejected:

- **Materialize the secret in the Box, defend with hooks.** Env var or rendered
  `.npmrc`, guarded by PreToolUse denies and log redaction. Cheapest, reuses
  existing machinery, and cannot honor "no exceptions" — it is a denylist whose
  security equals the completeness of its enumeration.
- **Prefetch into a warm cache, with a Driver-free Fetcher Box holding the
  credential.** Structurally sound, and it survives the universal-proxy case
  only by firing on every dependency the Agent adds. It also puts
  attacker-influenceable manifests next to the credential: `cargo fetch` runs
  no build scripts, but `npm install` runs `postinstall` by default and
  `pip download` executes `setup.py` for sdists, so the Agent chooses what code
  executes beside the secret. Rejected as v1; the mechanism remains available if
  a genuinely offline Box is ever wanted.
- **TLS interception with a per-run ephemeral CA.** The only design needing
  *zero* per-toolchain knowledge: `/etc/hosts` or `HTTPS_PROXY` routes the
  client to a proxy that terminates TLS, injects the header, and re-originates
  upstream. It violates neither hard constraint — the CA private key stays
  host-side. Rejected for v1 on reviewability, not correctness: a MITM is a hard
  sell in environments that ban it outright, and the alternative's cost turned
  out to be smaller than feared.

## Decision

**The credential lives in the launcher process. The Box gets an unauthenticated
channel that is authenticated on its behalf.**

- **Ingress carries names, never values.** The Consumer flake declares
  `fromFile` or `fromEnv` — a path or a variable name. Nothing secret is
  evaluable, so nothing secret can reach the store *by construction* rather than
  by discipline. The upstream URL is a runtime input for the same reason: a
  private registry hostname is not a credential, but it is not something to bake
  into a world-readable store path either.

- **The launcher reads the credential at startup and immediately unsets it.**
  At the time this ADR was written, `resolvedRunEnv` (`internal/runner/bwrap.go`)
  forwarded the launcher's entire ambient environment into a bwrap Box minus two
  hardcoded names (issue #2859 later inverted this to a schema-driven
  allowlist). Without this read-and-unset step, a `fromEnv` credential was
  inherited by the Agent Box automatically, and the Agent could read it with
  `echo` while the proxy behaved perfectly. Reading and unsetting at startup
  removes it from ambient under both runtimes and from the launcher's own
  `/proc/self/environ`.

- **The Box reaches the proxy over a per-Box unix socket.** Mounted through the
  existing `Mounts` plumbing, with a small in-Box forwarder presenting it as a
  local TCP endpoint because package managers need a URL, not a socket. This
  opens no host TCP port, behaves identically under bwrap and every OCI runtime,
  and composes with `networkMode=no-host-loopback` rather than contradicting it.

- **The client is pointed at the proxy by configuration, not interception.** Env
  overrides where they suffice; for in-tree config that would otherwise take
  precedence, a textual substitution of the upstream host followed by
  `git update-index --skip-worktree`, so the change is invisible to `git status`
  and can never be staged. The Agent sees ordinary config that happens to name a
  local endpoint, and needs no credential because the proxy supplies it upstream.

- **The proxy is a read-only mirror.** `GET`/`HEAD` only; the credential is
  attached only for the configured upstream host; and it is **never forwarded
  across a redirect** — registries routinely answer downloads with a `302` to a
  CDN, and a proxy that follows one with the header attached hands the token to
  whatever host the redirect names. The `302` is returned to the client, which
  fetches the target directly and unauthenticated. A path allowlist is derived
  from configuration and *logged* rather than enforced in v1: derivation is
  automatic and self-maintaining, but not provably complete, and a false denial
  presents as a registry outage.

**v1 ships `cargo`, `go`, and `npm` in the path-allowlist table**
(`cmd/launcher/internal/registryproxy/allowlist.go`). Other ecosystems are
additive table entries, filed as their own tickets, added when something
needs one — `gradle`'s own Binding (below) needs no table entry at all, for
the same reason cargo's own download endpoint is excluded from its patterns:
the artifact base path is registry-specific, not a fixed shape.

> **Update.** `npm` landed as a third v1 table entry (issue #2854), with
> zero changes to ingress, containment, or proxy policy — confirming the
> "additive table entry" claim above unmodified. An earlier draft of this
> Binding gave the proxy a `ModifyResponse` hook that rewrote npm's
> `dist.tarball` URL in a packument response, specifically to route the
> follow-up tarball fetch back through the proxy; that hook was reverted
> after review found it was itself the proxy-policy change this ticket's
> own acceptance criteria forbade, on top of several correctness bugs of
> its own (wrong media-type match against npm's actual `Accept`, a
> path-prefixed-upstream double-join, and a `HEAD`-request crash). npm's
> client (`pacote`) still fetches that tarball URL verbatim rather than
> deriving it from the configured registry host, so the request leaves the
> proxy and reaches upstream directly, unauthenticated — the same class of
> gap this ADR already accepts for cargo's own download endpoint, above
> ("registries routinely answer downloads with a `302` to a CDN... The
> `302` is returned to the client, which fetches the target directly and
> unauthenticated"). Reported, not worked around, per the ticket's own
> instruction.

> **Update.** `yarn` needed no table changes at all (issue #2856) — a
> stronger version of npm's own "additive table entry" claim above, since
> yarn adds nothing to the table rather than one more entry to it. Yarn
> classic rode npm's existing table entry and Binding
> (`npm_config_registry`, the in-tree `.npmrc` rewrite) completely verbatim:
> neither mechanism checks anything npm-specific, so the only change landed
> was a characterization test proving it. Yarn berry needed its own Binding
> — `YARN_NPM_REGISTRY_SERVER` (yarn berry's own env-var override,
> mirroring npm's env half) for its single default `npmRegistryServer` key,
> plus a textual `.yarnrc.yml` rewrite (mirroring npm's in-tree half) for
> the per-scope `npmScopes` entries no env var reaches — but explicitly no
> new table entry, since both yarn lineages resolve packages through the
> identical npm-compatible registry protocol paths (packument, tarball,
> scoped `@scope/name`) the existing `npm` entry
> (`allowlist.go:98-112`/`allowlist.go:129-132`) already allows: the table
> matches on URL path shape, not on which client sent the request. The more
> significant finding is that the textual-substitution mechanism itself
> turned out to be format-agnostic in practice, not just in the "textual,
> not a parser" framing above: `.yarnrc.yml` is YAML, not npm's INI-like
> `.npmrc`, and the same plain two-pass https/http substitution npm's
> Binding already uses — no YAML parsing involved — left the file valid
> YAML that yarn berry parsed correctly, resolving both the rewritten
> default registry and the rewritten per-scope registry hosts (verified
> against real yarn-berry 4.14.1).

> **Update.** `pnpm` (issue #2855) needed no new path-allowlist table entry —
> `allowlist.go`'s existing `npm` entry already matches pnpm's requests,
> since pnpm's client fetches from the same registry protocol with the same
> URL shapes as npm's client. The per-scope in-tree `.npmrc` rewrite
> (`phase_npm_intree_binding_apply`) needed no changes either: it is a
> content-driven textual rewrite, not npm-specific parsing, so a pnpm
> project's `.npmrc` (default or per-scope `@scope:registry=`) is covered
> identically — proven by `tests/entrypoint-pnpm-intree-binding.bats` with
> zero production diff. Two genuine divergences did surface. First: pnpm
> dropped support for generic `npm_config_*` environment variables and reads
> only `pnpm_config_*` (e.g. `pnpm_config_registry`) for this purpose now
> (per pnpm's own docs, pnpm.io/configuring), so
> `phase_registry_proxy_bindings`'s existing `npm_config_registry` export
> gained a same-shape additive `pnpm_config_registry` export line next to it
> — not a new table entry or a new Binding phase. Second: pnpm 11.23.0+ can
> also pin a scope to a private registry via a `registries:` block in
> `pnpm-workspace.yaml` (pnpm.io/registries), a file `.npmrc`'s rewrite never
> touches — a project using only that mechanism would have resolved straight
> to the real upstream host, uncredentialed. Unlike the env-var divergence,
> this one is a distinct committed file with its own git-tracked/skip-
> worktree/revert-around-rebase lifecycle, so it got its own phase pair,
> `phase_pnpm_workspace_intree_binding_apply` /
> `pnpm_workspace_intree_binding_revert`, mirroring
> `phase_npm_intree_binding_apply` exactly (same plain-sed textual rewrite,
> not YAML-aware parsing) rather than folding into the `.npmrc` phase.
> Confirms the additive per-ecosystem table design holds for the
> path-allowlist half, and that a second package-manager client can still
> need its own in-tree rewrite phase when it introduces a new config file,
> not just a new env var.

> **Update.** Issue #2930 gave `gradle`, `yarn`, and `pnpm` each an explicit
> row in the shared `bindings` table
> (`cmd/launcher/internal/registryproxy/allowlist.go`) after all, softening
> the "no table entry at all" / "no new table entry" claims made above — but
> not the path-allowlist policy those claims were really about. Each new row
> carries `lockfileGlobs` for a second table consumer,
> `cmd/launcher/internal/bindregistry`, a toolchain-nudge classifier that
> reads the table's ecosystem/lockfile shape through a new `Ecosystems()`
> accessor, entirely independent of the `patterns` field the path allowlist
> itself matches on. The allowlist-derivation claims stand exactly as
> written: gradle's row still carries a nil `patterns` (no allowlist
> derivable, same reasoning as cargo's excluded download endpoint above),
> and yarn's and pnpm's rows still share npm's exact `patterns` value
> verbatim (no new allowlist pattern, same npm-compatible-protocol reasoning
> above) — only the table itself gained rows, to serve a second consumer the
> table's shape happened to already fit.

> **Update.** Issue #2933 deleted the last of the per-ecosystem bash in-tree
> phases (`phase_npm_intree_binding_apply`,
> `phase_yarn_berry_intree_binding_apply`,
> `phase_pnpm_workspace_intree_binding_apply` /
> `pnpm_workspace_intree_binding_revert`, and their bats suites
> `tests/entrypoint-npm-intree-binding.bats`,
> `tests/entrypoint-yarn-berry-intree-binding.bats`,
> `tests/entrypoint-pnpm-intree-binding.bats`) and replaced them with three
> more rows in the same `InTreeBindings()` table issue #2932 already built
> for cargo (`cmd/launcher/internal/bindregistry/intreebinding.go`): npm's
> `.npmrc`, yarn berry's `.yarnrc.yml`, and pnpm's `pnpm-workspace.yaml`, all
> driven by the one `ApplyInTreeBinding`/`RevertInTreeBinding` engine and the
> `driver-exec bind-registry --intree-action` verb. No per-ecosystem Go code
> was needed — the engine already did plain host-substring substitution, not
> npm-specific parsing — so this closes the "still bash-only" gap the
> updates above left open for npm/yarn/pnpm's in-tree rewrites specifically
> (their path-allowlist/bindings-table entries had already moved, per the
> updates above; only the in-tree config rewrite itself remained bash).

## Consequences

- **The binding mechanism is a swappable last mile, not an architecture.**
  Ingress, unset-at-startup, socket, forwarder, and proxy policy are identical
  whether the client is bound by config or by TLS interception. If the
  per-ecosystem table becomes painful — the JVM is the likeliest trigger, since
  Gradle and Maven ignore `HTTPS_PROXY` and want `-Dhttps.proxyHost` — MITM can
  be added later as an additional binding mode, on evidence, without disturbing
  anything above.

- **Gradle turned out not to need the JVM MITM the Consequences above
  speculated about (issue #2858).** A home-level Gradle init script
  (`$GRADLE_USER_HOME/init.d/*.gradle`) can point all dependency and plugin
  resolution at the Forwarder with no in-tree rewrite at all. This was
  verified by interactively running a real Gradle 8.14.4 against a local
  stand-in HTTP server outside this repo's own toolchain — no JDK/gradle
  dependency is added here, so a Go golden test
  (`cmd/launcher/internal/bindregistry/gradlebinding_test.go`,
  `TestGradleInitScript_ExactContent`) covers the generated init script's
  shape, and `tests/bind-registry-parity.bats` covers the entrypoint's
  control flow: the call site is one ecosystem-agnostic verb invocation
  covering every table row, so pinning it end-to-end for the cargo row
  (that `driver-exec bind-registry` reached from the entrypoint really
  rewrites config and reaches the Driver's child, not a bash
  reimplementation) proves the same seam gradle's row goes through, not an
  actual Gradle invocation; that the init script itself gets written to
  `$GRADLE_USER_HOME/init.d/` is covered separately, at the Go level, by
  `cmd/launcher/driver-exec/bindregistry_cmd_test.go`'s
  `TestRunBindRegistryWithDeps_*GradleInitScript*` cases, which assert the
  file on disk — `TestGradleInitScript_ExactContent` only compares
  `GradleInitScript`'s returned string to a literal and never touches a
  filesystem.
  `-Dhttps.proxyHost`/`JAVA_TOOL_OPTIONS` were
  confirmed the wrong mechanism, since a forward proxy cannot inject a header
  into an HTTPS CONNECT tunnel and the Forwarder is a reverse proxy standing
  in as the origin, not a forward proxy. The init script uses two redirect
  forms. A one-shot clear-then-add is correct only where the override
  installs *after* the thing it competes with has already finished declaring
  repositories — Gradle 7+'s centralized `dependencyResolutionManagement`
  and plain per-project repositories both qualify, since both are overridden
  from a lifecycle callback (`gradle.settingsEvaluated`/
  `gradle.projectsEvaluated`) that fires only once the competing declaration
  is done. Everywhere else, a one-shot clear-then-add was found (by the same
  interactive testing, issue #2858 review findings 1/1b) to have an
  append-after-clear escape: a project's own
  `buildscript { repositories { mavenCentral() } }` block, or a
  `buildscript { }` block written directly in `settings.gradle`, runs
  *after* the override and silently appends the real upstream back in, so
  resolution falls through to it on any Forwarder 404 — the same is true of
  an *explicit* `pluginManagement { repositories { } }` block in the
  settings script itself, previously documented here as an unclosable gap.
  The fix is a persistent form: install the Forwarder repository once, then
  register a `repos.all { }` listener that removes any other repository the
  container gains afterward, forever — `RepositoryHandler.all(Action)`
  fires its action immediately for every repository already present AND
  again for every repository added later, so the listener keeps winning
  instead of losing to whatever declaration runs last, closing the
  previously-documented gap along with it. Buildscript classpath resolution
  completes *before* `gradle.projectsEvaluated` ever fires, so only a plain
  top-level `allprojects { buildscript { ... } }` (run immediately, not
  deferred to a lifecycle callback, installing the listener before that
  project's own build script body runs) catches it; a settings-level
  `plugins { }` block (which ships by default in `gradle init`-generated
  `settings.gradle.kts`) and any `buildscript { }` block written directly in
  `settings.gradle` both resolve *during* settings-script evaluation, before
  `gradle.settingsEvaluated` ever fires, so both need the persistent form
  installed from `gradle.beforeSettings` — the one hook early enough to win
  before the settings script body itself runs. `gradle.settingsEvaluated`
  still re-applies a one-shot `pluginManagement.repositories` override
  afterwards, but it is guarded on a `spindriftPluginManagementManaged` flag
  set only once `gradle.beforeSettings`' persistent redirects have installed,
  and runs *only* when that flag is false — i.e. only as the Gradle <6.0
  fallback, where `gradle.beforeSettings` itself requires 6.0+ and is
  wrapped in a `try`/`catch` (issue #2858 review finding 3) rather than
  thrown unguarded. An earlier revision ran this one-shot override
  unconditionally, on the theory that it was merely redundant once the
  persistent listener had installed; in fact, on Gradle 6.0+ it was
  destructive — clear-then-add re-adds an unnamed repository the listener,
  already attached to that same container, immediately strips (its name
  never became `'spindrift'`), leaving `pluginManagement.repositories` empty
  and every `plugins { }` block unresolvable on every Gradle 6.0+ build
  (issue #2858 review round 4). The guard above is the fix. The same
  `try`/`catch` guard wraps `allowInsecureProtocol`, also
  6.0+, everywhere the init script sets it. Plain project repositories need
  `gradle.projectsEvaluated`, not `allprojects { repositories.clear();
  ... }`, which runs *before* the project's own build script and so is
  silently undone once that script re-declares the real repository; Gradle
  7+'s centralized `dependencyResolutionManagement` needs its own
  `gradle.settingsEvaluated` override, applied *instead of* (not alongside)
  the plain per-project override once `RepositoriesMode` is anything but
  `PREFER_PROJECT` — a project enforcing `FAIL_ON_PROJECT_REPOS` rejects the
  per-project override itself as a forbidden project-level repository; and
  `dependencyResolutionManagement`/`repositoriesMode` themselves require
  Gradle 6.8+, so that one override is wrapped in its own `try`/`catch`,
  falling back to the per-project override on older Gradle instead of
  throwing and killing every build in the Box. This is evidence against
  reopening MITM here, and it narrows the case for MITM to ecosystems that
  actually resist config-layer binding, not merely ones that ignore
  `HTTPS_PROXY`.

- **A per-toolchain table is accepted, deliberately.** Roughly five fields per
  ecosystem: home-config path, render template, env override, and in-tree
  filenames. This is the cost of not intercepting. Registry-config spellings are
  close to frozen in practice, so this accretes rather than churns.

- **`skip-worktree` collides with harness-driven git.** `Rebase` is part of the
  Code Forge contract, and rebase and checkout error on `skip-worktree` paths.
  The rewrite must be reverted around harness-driven git operations.

- **The Agent retains use of the channel it cannot read.** It can reach the
  upstream through the proxy. `GET`/`HEAD` plus the host restriction bound this
  to reading dependencies it was going to build anyway, but the residual is real
  and is the same residual every design in this space has.

- **`resolvedRunEnv`'s denylist was a latent leak beyond this feature.** Removing
  two names from an inherited environment protected exactly two secrets. Read-and-
  unset fixes this credential; inverting the denylist to a schema-driven
  allowlist was the structural fix, tracked separately (issue #2859).

## Amendment (issue #3053): the Box holds no credential, but its toolchain still expects one

The Decision above says the Agent "needs no credential because the proxy
supplies it upstream." That is true of the *transport* and false of the
*client's own credential machinery*, and the gap is not hypothetical: a Target
repo whose committed `.cargo/config.toml` declares

```toml
[registries.example-remote]
index = "sparse+https://registry.example.com/artifactory/api/cargo/crates/index/"
credential-provider = "cargo:token"
```

makes cargo perform a credential lookup **before it opens a socket**. Finding
nothing, it aborts client-side with `no token found for 'example-remote'`. The
proxy is never contacted, so the launcher-side credential is never consulted,
and the run fails with an empty log because nothing ever reached the network.
The same abort occurs with no `credential-provider` line at all whenever the
registry's sparse index answers with `"auth-required": true`, since the proxy
relays that response body verbatim.

The in-tree substitution (`intreebinding.go`) rewrites the *host* and leaves
`credential-provider` untouched — correctly, on its own terms: it is a
find-and-replace over one upstream host string, not a config transformer. The
omission is that nothing else picks the obligation up.

**The client-side check is satisfied with a placeholder, never with a
credential.** The Box's channel to the Forwarder is unauthenticated by design;
a client demanding a credential for that hop is asserting a false requirement
inherited from a config file written for developer laptops. The fix is to
answer it with a value that is worthless by construction:

- The binding step emits `CARGO_REGISTRIES_<NAME>_TOKEN` for every registry
  whose config it rewrote, through the `EnvExport` channel bindings mode
  already renders and `entrypoint.sh` already sources. No new seam, and
  specifically not `BOX_ENV_VARS`, which is generated from the schema
  (`renderBoxEnvVarsList`) and is not an operator-facing passthrough.
- The value is a fixed, self-documenting non-secret, so a leaked log line is
  visibly harmless and an Agent that exfiltrates it has exfiltrated nothing.
- `cargo:token` reads exactly that variable, so the committed
  `credential-provider` line needs no edit — the Target repo stays valid for
  developers.
- The placeholder rides only the Box→Forwarder hop. The proxy's Rewrite hook
  does `Header.Set("Authorization", …)`, which *replaces* rather than appends,
  so the real credential is what reaches upstream. Cargo permits tokens over
  plain HTTP to a loopback address, so the transport needs no change.

**This stays a Binding change, not a proxy-policy change.** Stripping
`"auth-required"` from a relayed sparse index — or rewriting its `dl` field to
point back at the Forwarder — would close more of the gap, and is deliberately
not done here. The npm Binding (issue #2854) already tried a `ModifyResponse`
body rewrite for the structurally identical `dist.tarball` case and reverted
it: review found the hook was itself the proxy-policy change the work forbade,
carrying a wrong media-type match, a `HEAD`-request crash, and a
path-prefixed-upstream double-join. Reopening body rewriting is an ADR-level
decision with its own evidence, not a follow-on ticket.

Consequently, where a registry's index names an absolute `dl` host, cargo's
crate downloads leave the Forwarder and reach upstream directly and
unauthenticated. That is the same accepted gap this ADR already records for
cargo's own download endpoint and for npm's `dist.tarball` — narrowed by this
amendment, not closed.

**A distinction this ADR has been eliding.** "The credential never reaches the
Box" reads stronger than what is guaranteed. What holds is that the Box never
*holds* the credential. What does not hold is that the Box lacks *access*: any
process in the Box — including the Agent — can address the Forwarder and have
real credentials attached on its behalf. The Box is a confused deputy by
construction, and that is the feature, not a defect in it. The bound on what it
can reach is the path allowlist, which this ADR ships **logged rather than
enforced** on the reasoning that derivation is not provably complete and a
false denial presents as a registry outage. Both halves should be stated
together: containment of the *credential* is structural; containment of the
*access* is currently advisory. An operator whose registry host also fronts
non-registry paths should read the second half as the live one.

## Amendment (issue #3089): one credential becomes a credential route table

Bringing up a real Artifactory Consumer surfaced three questions this ADR had
left scalar, and they resolve into one shape rather than three: where the
credential is *sourced* from, what happens when there is more than one of
them, and whether the read-only claim is an invariant or a convention.

**The credential file is read host-side, never mounted.** Ingress today is
`fromEnv` or `fromFile`, and `fromFile` expects the file's entire contents to
*be* the credential — a single line. An operator who already holds the token in
`~/.cargo/credentials.toml` or `~/.netrc` has no way to point at it. The
obvious move, mounting that file into the Box read-only, is rejected for
exactly the reason the "materialize the secret in the Box" family was rejected
in the Context above: read access is the entire risk, and a read-only mount
grants read access. It would also solve one ecosystem while leaving the rest on
the proxy path, producing two credential mechanisms where the point was to have
one.

Sources instead gain a *selector*, and the launcher does the extraction
host-side. Two extractors cover the field: netrc, and a TOML path
(`registries.<name>.token`). netrc is the preferred primary — it is keyed by
host, which is the same key the routing model below needs, and it already holds
N hosts in one file, so it answers the multiplicity question without a second
mechanism. The Box learns nothing new from any of this: the placeholder
(issue #3053, above) already answers whatever local check a client makes, and
the real credential never leaves the launcher.

**Credential and upstream become one record, not two knobs.** The model is
scalar in three places at once — one `REGISTRY_PROXY_UPSTREAM_URL`, one
`RegistryProxyCredential`, and a hardcoded `Bearer` in `New`'s Rewrite hook. An
operator with two registry hosts and two tokens has no way to express it.

The generalization is a route table — `{matchHost, upstreamBaseURL,
credentialSource, authScheme}` — with the launcher assigning each route a path
prefix on the Forwarder (`http://127.0.0.1:<port>/<route>/`). One socket, one
port, one Forwarder, N upstreams, disambiguated by prefix.

The load-bearing property is that credential and upstream are bound in the
*same record*. The Box selects a route by naming its prefix, but selecting
route A sends credential A to upstream A; there is no Box-reachable way to pair
credential A with host B. The rejected alternative — a credential map consulted
at request time, after the Box has named a target — would hand the Box exactly
that pairing. This keeps the concession the Decision already makes ("the Box is
a confused deputy by construction") bounded where it is: the Box may *use* the
channel, but it may not *redirect* a credential.

`authScheme` is a per-route enum (`bearer`, `basic`, `header:<Name>`) rather
than the hardcoded `Bearer`, because reads against a private registry
authenticate too, and `Bearer` is not universal — Maven and Gradle repositories
commonly want Basic, and JFrog deployments commonly want their own header.

**Deferred, with triggers.** The table is not built yet. The motivating
deployment is one host serving two registry *names* (`artifactory` and
`artifactory-remote` under one Artifactory), which the scalar model already
serves: the multiplicity lives in cargo's config, not in the upstreams.
Building the table for it now would be speculative.

Deferring is safe because the migration is additive rather than a rename.
Today's Forwarder URL carries no prefix, and that becomes the default route.
`ApplyInTreeBinding` already takes `(upstreamHost, localURL)` per call, so N
routes is a loop over the existing signature. `ParseCargoRegistryNames` keys off
`localURL` and works per route unmodified. Build the table when a second
upstream *host* appears, or when one host needs two distinct credentials.

**A constraint of the scalar model, previously implicit.** Because the in-tree
substitution replaces only `https://<host>`, the rewritten request keeps the
registry's full upstream path. `REGISTRY_PROXY_UPSTREAM_URL` must therefore name
a bare origin: if it carries a path, `SetURL`'s join prepends that path to a
request that already contains it, and every proxied request reaches upstream
with the path doubled. The launch gate now rejects a pathful upstream before
any Box starts (issue #3084, `validateRegistryProxyUpstreamURL`), so the
failure surfaces as a configuration error rather than as registry 404s. The
route model still makes the constraint structural instead of enforced —
`upstreamBaseURL` and the substitution key become separate fields required to
agree, rather than one field required to be pathless and checked for it.

**The path allowlist is inert for path-prefixed registries.** This sharpens the
"logged rather than enforced" caveat above into something stronger than it
reads. The patterns anchor at the registry root — `^/config\.json$`,
`^/3/[A-Za-z0-9_-]/[A-Za-z0-9_-]{3}$` (`allowlist.go:29-33`) — which is correct for a registry
served at an origin root, and never matches one served under a path. Under
Artifactory, cargo's index request arrives as
`/artifactory/api/cargo/cargo-crates-remote/index/config.json` and matches
nothing at all, so every request logs `path outside derived allowlist`
(`registryproxy.go:95-96`).

Today the consequence is log noise rather than denial, because the allowlist is
advisory. But for an entire class of deployment it is not merely unenforced —
it is non-matching, so the log carries no signal either, and the "bound on what
the Box can reach" the Decision describes is absent rather than soft. A route
that knows its own base path is what restores it: the allowlist can then match
the path *relative to* the registry root, which is the shape those patterns
were written for. That is a second, independent reason to build the route
table, and it is why the trigger above should not be read as purely a
convenience threshold.

> **Update.** Issue #3087 changed the log-noise consequence described above:
> `registryproxy.go`'s handler now logs the first out-of-allowlist miss in
> full and suppresses the count of any further misses until (or unless) some
> request finally matches the allowlist -- proving the deployment is
> root-served after all -- at which point the suppressed count flushes as a
> single summary line and per-request logging resumes for every miss after
> that. `Proxy.Close` flushes the same summary line at proxy teardown for the
> case where the allowlist never matches at all during the run. So for the
> path-prefixed shape described here, one run now produces one detailed line
> plus at most one summary line, not one line per request. The claim above --
> "every request logs `path outside derived allowlist`" (`registryproxy.go:95-96`)
> -- no longer holds; it described the log-noise symptom this issue fixed, not
> current behavior. The GET/HEAD-only gate the invariant below cites has also
> moved, to `registryproxy.go:100-103` (previously `:80-83`).
>
> **Update.** Issue #3176 keyed the above per route, named by prefix in every
> line: a proxy configured with several routes (ADR 0045) now tracks each
> route's "has anything matched the allowlist yet" and suppressed-miss state
> independently, so one route's upstream turning out to be root-served no
> longer flushes or silences another route's still-suppressing misses.
> `Proxy.Close` walks the route table and flushes every route that
> accumulated any state, in that table's order, so a multi-route teardown's
> summaries come out stably rather than in Go's randomized map order. The
> same issue also changed what a summary line counts: distinct out-of-pattern
> paths rather than requests, and excludes the path already named in that
> first full line -- so a build hammering the same unmatched path repeatedly
> produces only that one detailed line and no summary at all; the summary
> covers just the *further* distinct paths beyond it.

**Read-only is an invariant, not a knob.** Publishing to a registry is out of
scope for the Agent, and the gate enforcing it is already structural rather
than conventional: `GET`/`HEAD` only, checked at `registryproxy.go:80-83`
*before* `rp.ServeHTTP`. A write is therefore refused and never reaches the
`Rewrite` hook that attaches the credential, so a `cargo publish` from the Box
cannot leak the token upstream even in a rejected request. A publish that
bypasses the Forwarder entirely cannot authenticate either, since the only
token the Box holds is the non-secret placeholder.

No knob is added to relax this. Read-only is currently a property of the
design; a switch would demote it to a configuration mistake available to be
made.

What this process cannot enforce is the credential's own capability. The 405
governs what the proxy will forward, never what the token could do if it
escaped the launcher by some other route. Operators should issue a read-only
registry token: with publishing out of scope for the Agent it costs nothing,
and it is the only half of the guarantee that holds outside this process. The
405 itself now names the policy in its body, rather than reading as a bare
transport fault.

## Amendment (issue #3110): the socket transport is unavailable on macOS

Bringing up the same Artifactory Consumer on macOS found the proxy completely
inert: no rewrite, no placeholder, and cargo failing with `no token found`. The
cause is not configuration, and no operator setting fixes it. The unix socket
this ADR chose as its transport does not cross the boundary between a macOS
host and a Linux VM.

**The socket never reaches the Box.** Every VM-backed runtime on macOS shares
host files through a filesystem-sharing layer, and none of the available layers
represent an `AF_UNIX` inode. Measured directly, with a real socket bound on the
host:

| Mount | Host | Container |
| --- | --- | --- |
| `-v /tmp/p.sock:/p.sock` | socket | empty **directory** |
| `-v /tmp/dir:/dir` | socket | **absent entirely** |

Both shapes fail, so no choice of mount path helps. The measurement above was
taken under Rancher Desktop on Apple's Virtualization.framework with virtiofs —
the most capable sharing configuration macOS offers — which means there is no
better setting to move to. That virtiofs device is Apple's, not the reference
`virtiofsd`: it shares *directories*, and special files are not part of what it
carries. Docker Desktop and `podman machine` under `applehv` drive the same
device and inherit the same limitation, so switching runtimes is not a remedy.
Linux hosts and the bwrap runner are unaffected, having no sharing layer at all.

**Every layer that could have caught this passes.** The host half is genuinely
correct, which is precisely why nothing complained. `runOnce` aborts the whole
dispatch if the proxy cannot bind (`box.go:241`), so a Box that starts at all
proves a real socket exists. `candidateSocketMount` stats that source and
requires `os.ModeSocket` before emitting a mount spec (`mount.go:95-96`), so the
mount is issued against a verified socket. `spindrift doctor` resolves the
credential and validates the upstream URL, but inspects only host-side state.
The projection is the one fact none of them observe, and it is the one that is
wrong.

In the Box, `isMountedSocket` stats the target and finds a directory, so the
in-tree verb returns zero immediately (`bindregistry_cmd.go:349-351`). The
config keeps its real upstream, the `ApplyApplied` branch never runs, no
`CARGO_REGISTRIES_<NAME>_TOKEN` placeholder is exported, and the first evidence
that reaches an operator is cargo's client-side `no token found`, six steps
downstream and pointing at a credential problem that does not exist.

**One silence was correct; the other is a defect.** The verb must stay quiet
when no proxy is configured, because the entrypoint passes the socket flag on
every dispatch — issue #3082 settled that distinction as *feature off* (say
nothing) versus *feature on but not working* (say why), and applied it to the
per-row apply reasons. The socket gate above it was left silent in both cases.
With `REGISTRY_PROXY_UPSTREAM_URL` set, an unusable socket is a configured
feature that is not working, and it must say so.

**Detection is separable from transport, and worth more.** The check is exactly
the experiment that diagnosed this: bind a socket, mount it into a throwaway
container, and confirm the guest sees a socket *and* can connect to it. That
belongs in `spindrift doctor`, and it is correct regardless of which transport
ships, because the answer depends on the operator's VM configuration rather than
on anything spindrift can infer from the platform. A macOS check alone would be
both too broad and too narrow — it would condemn a working Linux VM
configuration and miss a Linux host with an exotic runtime.

The probe must test connectability, not merely projection. The two fail
independently: a faithful passthrough layer could present the inode while the
guest kernel has no endpoint behind it, and that outcome is worse than the one
measured here. `isMountedSocket` would pass, the Forwarder would start, the
rewrite would apply, and requests would fail at connect time — a confusing
failure in place of a clean skip.

> **Update.** The amendment below (issue #3111) shipped this check, and placed
> it differently than this paragraph proposed: it runs in the launcher as a
> live per-Dispatch probe (`Runner.RegistryProxyTransport`), not in `spindrift
> doctor`, so the verdict is a fact about the run actually starting rather than
> about the operator's machine at setup time. The two-part shape argued for
> here survived intact — `probeRegistrySocketVisible` for projection and
> `probeRegistrySocketConnect` for connectability — and so did the reasoning
> against a platform constant, which that amendment restates as its own
> rejection of `GOOS == "darwin"`. A `doctor` check remains worth having for
> the setup-time diagnosis this paragraph wanted; it is no longer the only
> thing standing between an operator and a silent skip.

**A TCP fallback is not a like-for-like substitute.** The Decision above chose a
unix socket partly because its access control is free: filesystem permissions
mean only something able to open that path can reach a proxy that attaches a
real credential. A loopback port has no equivalent. Any local process on the
operator's machine could reach it, and the proxy would authenticate that
request upstream exactly as it authenticates the Box's. A macOS transport must
therefore carry its own per-run authentication — a secret the launcher mints,
passes to the Box, and requires on every forwarded request. That is an
expansion of this ADR's threat model rather than a transport detail, which is
why it is recorded here and not left to an implementation ticket.

The read-only invariant is unaffected: the `GET`/`HEAD` gate runs before the
`Rewrite` hook regardless of how a request arrived, so a write is still refused
without ever reaching the credential.

**Rejected: declare macOS unsupported and document it.** This is coherent, and
strictly cheaper. It is rejected because it converts a platform spindrift
already runs on into one where a documented feature silently does nothing, and
because the detection work above is required either way — an operator told
"macOS is unsupported" still deserves to be told so by `doctor` at setup rather
than by cargo mid-run. Documenting the limitation is a step on the way to the
transport, not an alternative to it.

**Rejected: require the QEMU backend with the reference `virtiofsd`.** A genuine
passthrough daemon might carry the inode where Apple's device does not, and this
was not measured. It is rejected regardless: it trades native virtualization for
emulation on every dispatch, and it would stake the feature on a configuration
chosen for no other reason, still without resolving whether connect succeeds.

## Amendment (issue #3111): a loopback-TCP fallback when the socket cannot cross

The Decision above treats "reach the proxy over a per-Box unix socket" as a
constant. It is not: the socket is a host path bind-mounted into the guest,
and not every configured runtime honors that mount as a live connectable
endpoint. A remote-context docker/podman talks to a daemon on a different
host entirely, so there is no local path to bind-mount from. Some VM-backed
runtimes share files into the guest through a passthrough layer that can
project a socket's *inode* — `stat` sees a socket-mode file — without wiring
a kernel endpoint behind it, so a client that connects gets ECONNREFUSED (or
hangs) even though the mount looks fine. `GOOS == "darwin"` was considered
and rejected as the test for this: the real answer turns on the operator's
own choice of runtime and VM/mount backend, which a compile-time constant
cannot see.

So capability is decided by a live probe, run once per Dispatch against the
actually-configured runtime, rather than assumed from the platform. The
launcher binds a throwaway unix socket on the host, launches a disposable
container under the configured `docker`/`podman` binary with that socket
bind-mounted in, and runs `driver-exec probe-registry-socket` inside it. The
guest-side check is deliberately two-part: `probeRegistrySocketVisible`
confirms the path exists and is socket-mode, and `probeRegistrySocketConnect`
confirms a connection actually completes — a stat-only check would call the
passthrough-inode case above "capable" when it is exactly the broken case
this probe exists to catch. `RegistryProxyTransport`
(`cmd/launcher/internal/runner/oci.go`) reads the probe container's exit code
as the verdict (0 capable, 1 incapable) and treats anything else — a wedged
daemon, a probe container that fails to start — as an infrastructure error
rather than a silent downgrade to TCP. The bwrap adapter carries none of this
machinery: it runs the sandbox directly on the host, with no VM or remote
daemon between the launcher and the guest for the socket to fail to cross, so
its own `RegistryProxyTransport` is a trivial stub that always reports
socket-capable.

When the probe finds the socket incapable, the Box instead gets a TCP
endpoint: the launcher binds `registryproxy.Proxy.ListenAndServeTCP` on
`0.0.0.0:0` and the Box reaches it over `--add-host <host>:host-gateway`,
resolving to whichever hostname the configured runtime uses for its own
host-loopback gateway (`host.containers.internal` for podman,
`host.docker.internal` for docker and nerdctl). An earlier version of this
amendment bound `127.0.0.1:0` and simply trusted that route — review caught
that a plain Linux docker bridge resolves `host-gateway` to the bridge IP
(e.g. `172.17.0.1`), not loopback, so nothing was listening on the address
the guest actually dialled, and a remote-context daemon (a different
physical machine) has no route back to the launcher host at all regardless
of bind address. `RegistryProxyTransport` now runs a second, independent
live sub-probe (`probeRegistryTCPReachable`) whenever the first probe finds
the socket incapable and `networkMode` doesn't already deny host-loopback: a
second disposable container, wired with the same `--add-host` the real Box
would get, runs `driver-exec probe-registry-tcp` against a throwaway
listener on the launcher's own `0.0.0.0:0` bind. Exit 0 confirms the route is
live and `RegistryProxyTransport` reports the TCP transport as usable; exit 1
(or any other outcome — timeout, exec failure) is a hard error, the same
tone as the existing `networkMode`-denies-host-loopback branch, rather than
a silent downgrade to a Box that can never reach its registry proxy. This is
what makes AC 1 ("a Box on a runtime that cannot carry a unix socket reaches
the Registry proxy successfully") an assertion the probe actually proves,
including for the remote-context case: there the sub-probe's own container
fails to dial back, so the Dispatch errors loudly before any Box starts,
rather than silently falling through to the public registry.

**A loopback port needs its own gate, because a socket's came for free.** A
unix socket's access control is filesystem permissions on its path — nothing
else on the host can open it without also being able to read that path. A
loopback TCP port has no equivalent: any local process can connect to it,
which is exactly the vector this ADR already treats as adversarial (`agent/
env-credential-scrub.sh`'s framing, "an Agent Box with arbitrary code
execution as its own uid"). So the TCP transport carries a per-run secret,
minted fresh by `newRegistryProxyTCPSecret` (`dispatch/box.go`, 16
`crypto/rand` bytes, hex-encoded) and required on every request via the
`registryproxy.TCPSecretHeader` header. `ListenAndServeTCP` checks it with
`crypto/subtle.ConstantTimeCompare`, not `!=` — a short-circuiting equality
check leaks the secret to the same local adversary a byte at a time, through
response-time variance, which is precisely the class of attack the socket's
filesystem permissions made moot. The check runs in front of both the
GET/HEAD gate and the credential-attaching `Rewrite` hook, so a request that
fails it never causes upstream to be dialled and never puts the real
registry credential at risk. `ListenAndServeTCP` also refuses to start at all
on an empty secret, rather than silently gating on an always-matching empty
string — fail closed, not fall open.

**The secret reaches the Box the same way the launcher's other bearer tokens
do.** `REGISTRY_PROXY_TCP_SECRET` is a bearer-token-shaped credential, so it
joins `GH_TOKEN`/`CLAUDE_CODE_OAUTH_TOKEN`/`ANTHROPIC_API_KEY` in
`bwrapSecrets` (`cmd/launcher/internal/runner/bwrap.go`), the set of
`box.Env` keys kept off a container CLI's argv — `docker`/`podman run`'s
argv, unlike an unshared socket, is visible via `ps`/`/proc` to any local
user for the container's entire lifetime. The OCI adapter's `ociRunEnv`
carries the actual value in the `docker`/`podman` subprocess's own
environment, and the render loop that builds `run` passes a bare `-e KEY`
(name only, no `=value`) for any key in `bwrapSecrets`, which tells
docker/podman to forward the value from its own process environment rather
than from a literal argv assignment.

### The three original claims, corrected

- **"Opens no host TCP port."** No longer unconditionally true. The launcher
  opens a loopback-only TCP port, gated by the per-run secret above, but only
  when the live probe finds the configured runtime cannot carry the socket
  across, and only for that one Dispatch. A runtime the probe finds capable —
  which includes every bwrap run, and every OCI run against a local daemon
  with a working bind mount — still gets the unix socket exactly as
  originally decided, and no port is opened at all.

- **"Behaves identically under bwrap and every OCI runtime."** No longer
  true, and no longer the goal. Transport selection is now a per-run,
  per-runtime decision made by `Runner.RegistryProxyTransport()`: bwrap's
  implementation is a constant, since it has no VM/remote-daemon boundary to
  fail across, while the OCI adapter's implementation genuinely probes. An
  OCI Box's registry traffic can now cross a real TCP hop that a bwrap Box's
  traffic never does — the two runtimes were never going to be able to
  behave identically once one of them could be pointed at a remote daemon at
  all, and pretending otherwise would have meant a silent failure instead of
  a fallback.

- **"Composes with `networkMode=no-host-loopback` rather than
  contradicting it."** This is the claim review found actually false in the
  naive implementation, not merely dated: a Box running under
  `NETWORK_MODE=no-host-loopback` has asked, by policy, for the host-loopback
  route the TCP fallback needs — under podman's pasta the route is genuinely
  blocked, so the Box would silently lose all registry access with no
  diagnostic; under docker, where `no-host-loopback` renders as plain
  `bridge` (documented elsewhere in `oci.go` as "an inert-but-correct render,
  not a functional guarantee"), the fallback would instead wire a working
  host-loopback route the operator's own network mode explicitly asked to
  deny. Composing the two knobs silently was wrong in both directions. The
  fix makes them mutually exclusive by construction instead:
  `RegistryProxyTransport` checks `deniesHostLoopback(networkMode)` — true
  for `no-host-loopback` and for `none`, which denies the route by having no
  network at all — and when the probe has also found the socket incapable,
  returns an error rather than a usable TCP host. A Dispatch configured with
  a registry proxy in that specific combination now fails loudly, before any
  Box starts, instead of degrading into one of the two silent failures above.

**What is unchanged.** The real registry credential still never reaches the
Box on either transport — the same `Rewrite`-hook `Header.Set` mechanism
attaches it only on the launcher-to-upstream hop, regardless of which
transport the Box-to-launcher hop uses. `GET`/`HEAD`-only is enforced
identically on both: on TCP the secret check runs in front of it, never
behind, but the method gate itself is the same `Handler` code path either
way, so a write is refused on either transport before it can reach the
`Rewrite` hook at all. And the
capability probe keeps this ADR's existing engineering discipline around
untested infrastructure paths: `RegistryProxyTransport` is exercised through
injectable seams (`probeSocketDir`, the `exec.CommandContext` call, the
timeout var), and no test in this repo starts a real container to exercise
it — the same posture the netrc/TOML extractor discussion elsewhere in this
file takes toward host-side parsing it would rather not fake with a live
service.
