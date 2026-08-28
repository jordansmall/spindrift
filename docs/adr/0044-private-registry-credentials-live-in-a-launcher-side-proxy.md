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
  dependency is added here, so the entrypoint's bats suite covers the
  generated init script's shape and the entrypoint's control flow, not an
  actual Gradle invocation. `-Dhttps.proxyHost`/`JAVA_TOOL_OPTIONS` were
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
