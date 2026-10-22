# Registry host profiles are advisory data, read only by discovery

Status: proposed

## Context

The Registry proxy is generic about the upstream host by design (ADRs
0044, 0045, 0047). In practice, JFrog Artifactory is the only host that
has shaped it:

- The `header:<Name>` auth scheme exists for `X-JFrog-Art-Api`.
- The proxy keeps a scheme already inside the token, so Artifactory's
  bearer-in-token cargo credential doesn't become `Bearer Bearer`
  (issue #3124).
- `allow` exists for Artifactory's sibling download endpoint, as does
  cargo's same-host `dl` learning (ADR 0047).

None of this is written down as host knowledge. Each one is an inline
rule that applies to every host.

The tests follow the same pattern. Corpora use fake hosts that copy
Artifactory's paths (`acme.jfrog.test/artifactory/api/...`) or the
public crates.io and npmjs registries. Other hosts barely appear:

- Nexus appears in one rewrite fixture.
- GitHub Packages and GitLab appear only in ADR 0048's reasoning.
- CodeArtifact, Google Artifact Registry and Azure Artifacts appear
  nowhere.

An operator on any of those hosts gets no help from `spindrift registry
discover`:

- The probe reads the leading `WWW-Authenticate` token, returns `bearer`
  when it can't tell, and can never propose `header:<Name>`.
- The username convention a host expects under `basic` (`aws:` for
  CodeArtifact, a GitHub user for GitHub Packages) isn't known anywhere.
- An operator only finds a host's sibling paths after a 403.

Credentials resolve once per dispatch: every daemon child runs
`bootstrap`, which runs `buildRegistryProxyRoutes`. An `exec` helper's
output is stored as a static string. A token that expires before the
run ends, such as a one-hour Google Artifact Registry or Entra token,
starts failing partway through. Nothing refreshes it, unlike `GH_TOKEN`
(`tokenrefresh`, issue #1027).

NuGet support, the first ecosystem added under ADR 0048's 2026-10-22
amendment, targets Artifactory.

## Decision

1. **A host profile table holds what is known about upstream host
   products.** It lives in a new package, `internal/hostprofile`. Each
   row is a value with no functions, the same row-value shape as an
   Ecosystem Strategy:
   - a name;
   - matchers;
   - a default auth scheme;
   - a username convention for `basic`;
   - per-ecosystem sibling path patterns, relative to the registry path
     the Target repo declares;
   - a credential-helper hint;
   - a short note for the operator.

   A row matches on any of three signals:
   - a host pattern, for SaaS hosts (`*.jfrog.io`, `pkgs.dev.azure.com`);
   - a path prefix in the registry URL the repo declares
     (`/artifactory/api/`), for self-hosted installs that a hostname
     cannot identify;
   - a response header the discovery probe already receives
     (`X-Artifactory-Id`).

   The first matching row in table order wins.
2. **Profiles are advisory. Only discovery and doctor read them.**
   `registry discover` writes a matched profile's advice into the routes
   file it proposes:
   - the `auth-scheme`;
   - the `allow` entries;
   - the username convention and helper hint, as comments.

   The operator reviews the file and commits it. A challenge the probe
   actually received wins over the profile's scheme. The profile only
   fills in what the probe could not tell. `doctor` warns when a route
   on a profiled host departs from its profile, and never fails on it.
   The proxy, route resolution and the launch gate never read a profile.
   A guard test pins that `registryproxy`, `registryroutes` and the
   launcher's route resolution do not import `hostprofile`. The routes
   file stays the only declaration surface (ADR 0045), and `allow` stays
   operator-owned (ADR 0047). A spindrift release that edits a profile
   changes no Consumer's enforced path-set.
3. **A profile is only as good as its corpus.** Host cases are
   `ecosystemtest` cases in each ecosystem's corpus. They use the host's
   real URL shapes. The probe is stubbed with the host's real challenge
   and headers. The discovery-stage golden then pins the profile's
   advice. A profile row with no corpus case fails a test.
4. **The first cut is Artifactory plus the public defaults.**
   - Artifactory gets a case in every ecosystem's corpus, including
     NuGet's `/api/nuget/v3/<repo>` once that package lands.
   - crates.io, registry.npmjs.org, proxy.golang.org and nuget.org get
     rows that say "known host, unauthenticated, nothing to configure".
   - Nexus, GitHub Packages, Azure Artifacts, CodeArtifact and GitLab
     each come later as their own ticket, verified against a real
     instance where possible, because a profile written from vendor
     docs alone is confident advice that may be wrong.
5. **Credential lifetime is out of scope.** *Trigger:* the first Consumer
   whose route credential lives less than one dispatch. The fix would
   be an optional per-route re-run of the `exec` source inside the
   launcher, modelled on `tokenrefresh`. That is a `credresolver`
   feature, and neither the profile table nor any ecosystem would
   change.

## Considered Options

- **A host kit: one package per host product with a Strategy and a
  conformance kit, mirroring ecosystems.** Rejected for now. A kit pays
  off when units carry behaviour. Every host quirk found so far is a
  value: a scheme, a username, a path or a header name. Behaviour stays
  on the ecosystem side; cargo still repoints `dl` even when the host is
  Artifactory. *Promotion trigger:* the first host that needs code
  rather than values, such as a token-exchange handshake inside the
  proxy or a response rewrite keyed by host. Keeping rows in the
  row-value shape keeps that promotion mechanical. It is the expected
  end state once hosts with real behaviour arrive.
- **Profiles as runtime defaults.** In this option, route resolution
  would fill in an unset `auth-scheme` and merge a profile's `allow`
  paths at launch. Rejected:
  - it makes a second, unreviewed policy surface;
  - a profile edit in a spindrift release would widen what every
    matching Consumer's proxy forwards with credentials attached;
  - that is the confused-deputy widening ADR 0047 closed.
- **Corpora and refresh only, with no profile table.** Rejected.
  Discovery would keep proposing `bearer` to hosts that want `basic`,
  and operators would keep finding sibling paths through 403s. That gap
  is the point of this ADR.

## Consequences

- The probe has to report "no usable challenge" separately from
  `bearer`, so a profile can fill in a scheme the probe couldn't
  determine without overriding one it did determine. `normalizeAuthScheme`'s
  fallback to `bearer` moves after the profile is consulted.
- The routes file discovery writes gains comments naming the matched
  profile. Operators who already have a routes file see no change until
  they rerun discovery. `doctor`'s drift warning applies to them
  immediately.
- A new host is a profile row plus corpus cases in the ecosystems it
  serves. A new ecosystem's corpus includes an Artifactory case if the
  Artifactory profile claims it.
