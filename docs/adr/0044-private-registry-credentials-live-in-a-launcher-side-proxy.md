# Private-registry credentials live in a launcher-side proxy, never in the Box

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

## Amendment: one credential becomes a credential route table

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
with the path doubled. Nothing states this today, and the failure presents as
registry 404s rather than as a configuration error. The route model makes it
structural instead of implicit — `upstreamBaseURL` and the substitution key are
separate fields required to agree, rather than one field silently required to
be pathless.

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
and it is the only half of the guarantee that holds outside this process. One
rough edge is worth naming — a rejected write surfaces as a bare `405 method
not allowed`, which reads as a broken proxy rather than as deliberate policy.
