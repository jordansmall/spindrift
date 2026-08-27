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

**v1 ships `cargo` alone.** Other ecosystems are additive table entries, filed
as their own tickets, added when something needs them.

## Consequences

- **The binding mechanism is a swappable last mile, not an architecture.**
  Ingress, unset-at-startup, socket, forwarder, and proxy policy are identical
  whether the client is bound by config or by TLS interception. If the
  per-ecosystem table becomes painful — the JVM is the likeliest trigger, since
  Gradle and Maven ignore `HTTPS_PROXY` and want `-Dhttps.proxyHost` — MITM can
  be added later as an additional binding mode, on evidence, without disturbing
  anything above.

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
