# The agent CLI is a pluggable Driver; opencode joins claude behind it

> **Amended by [ADR 0064](0064-a-consumer-flake-declares-its-drivers-side-by-side.md):**
> a Consumer flake may declare several Drivers side by side, still one Driver
> per image, each with its own apps and Driver-suffixed image outputs.
>
> **Amended by [ADR 0065](0065-drivers-decode-pass-logs-into-canonical-events.md):**
> on the host side a Driver's parsing is a decoder into canonical events; the
> five Pass-log readers are shared in driverkit.
>
> Note (issue #3490): `claude` and `opencode` are both implemented Drivers
> today — a `lib/drivers/` registry entry plus a matching Go strategy for
> each, and both names in the generated driver-name list (issue #262
> shipped the opencode Driver end-to-end; #263 shipped the GitHub Copilot
> Provider). What remains unshipped is this ADR's *primary* credential
> path: a nix-generated `opencode.json` with `{env:VAR}` placeholders —
> #263 instead landed the `github-copilot` OAuth leg
> (`OPENCODE_AUTH_CONTENT`) and inverted the two legs, per the #260
> amendment below. See CONTEXT.md's Driver/Provider definitions and
> `docs/reference.md`.
>
> The decision text and Consequences below were refreshed in place under the
> same issue, so they read as a record of what shipped; inline markers follow
> the amendment convention in
> [CONTRIBUTING.md](../../CONTRIBUTING.md#decisions--the-public-contract).

Until now the Box ran exactly one agent CLI — `claude -p … --dangerously-skip-permissions` — and Claude-specific assumptions leaked into six places: the invocation, the auth env, the `stream-json` outcome extraction, the `--agents` subagent JSON, skill discovery, and the launcher's Anthropic-specific transient classifier. To add **opencode** (with GitHub Copilot as a model *provider*), the agent CLI becomes a *Driver* seam: a swappable in-box tool selected at build time, exactly analogous to the `runtime` runner seam (ADR 0006). `claude` stays the default; `opencode` is the second Driver. `Driver` started as a provisional name, but it stuck: it now names shipped surface — the `DRIVER` artifact value the launcher reads (`getenvArtifact` — nix-computed plumbing, not an operator knob), the `lib/drivers/` registry, and the `driver` flake option.

The Driver is a **build-time** choice — one Driver per image, picked by a `mkHarness`/flake option beside `runtime`. Switching binaries is a closure change, so it belongs at build time; `MODEL` stays a runtime knob because it is only a string arg to the same binary. Build produces one lean image per Driver — rather than one fat image carrying every tool — with the `spindrift` output name reserved for the host CLI and ADR 0010 authoritative on flake-output naming. Per-image packaging does **not** foreclose a mixed fleet: because the images exist as separate artifacts, per-issue routing (a `driver:opencode` label → the matching image) is a later launcher feature, not a repackaging. Routing granularity is deliberately deferred — this run ships global per-run selection only.

*Amended by issue #3490:* the per-Driver flake output never materialized as an `agent-image-<driver>` family. There is one `agent-image` output, and the Driver rides in the *OCI image name* instead — `spindrift` for claude, `spindrift-<driver>` otherwise. That output is also conditional rather than always present: `lib/mkHarness.nix` gates it on `isLinux && runtime != "bwrap"` and exposes `agent-closure` in its place under bwrap, so only an image-building build has an image output at all.

A Driver is inherently **two coordinated pieces keyed by one name**, because it straddles two hosts that cannot share code. The **in-box** half (invocation argv, agent-config rendering, skill wiring, outcome-line extraction) is baked into the image and is **nix-generated** (ADR 0005), living in a `lib/drivers/` registry that fans out like the env-schema preambles. The **host-side** half (transient classification, heartbeat parsing, usage extraction) lives in the launcher — a single native binary shared across all images, so it is a Go `Driver` strategy interface (the forge/runner idiom) selected at runtime by a `DRIVER` value threaded from the same knob that picks the image. A Go test asserts every nix Driver name has a matching Go strategy so the two registries cannot drift.

Each Driver **normalizes its tool's misbehavior at its own boundary** (on the host side that boundary is the Driver's decoder, [ADR 0065](0065-drivers-decode-pass-logs-into-canonical-events.md)), so the entrypoint tail, launcher, merge gate, and retry logic stay Driver-agnostic. opencode is badly behaved where claude is clean — it exits `0` on error, emits no `result` envelope (its final `step_finish` may not even fire before exit), and its rate-limit markers look nothing like Anthropic's. The opencode adapter therefore surfaces the sentinel as a bare stdout line — both Drivers render the same nix-generated `jq` extraction pipeline (`lib/drivers/outcome-extractor.nix`) and supply only their own `jqSelector`: opencode selects each `text` event's `.part.text` (`lib/drivers/opencode.nix`), claude the `result` event's `.result` (`lib/drivers/claude.nix`) — and **synthesizes** a trustworthy exit code from "valid outcome line present AND no `error` event" because opencode's own code is worthless. The upshot: the **`SPINDRIFT_OUTCOME` line is the true cross-Driver success contract**; the exit code is per-Driver corroboration only (already half-true when this was written, and now wholly so: the missing-line→`TaskFailed` rule is itself per-Driver, in each classifier's own scan — the not-found fallback in the shared `Classify` in `cmd/launcher/internal/driver/driverkit/read_classify.go`, which both Drivers' classifiers run over their own decoder).

*Amended by issue #4431:* outcome-line and result-text extraction is no longer
part of the in-box half. The nix-generated `jq` extractor
(`lib/drivers/outcome-extractor.nix`) and its `jqSelector`s are deleted;
extraction lives only in the launcher's Go `Driver` strategies (`ResultText` in
`cmd/launcher/internal/driver/{claude,opencode}`) and the `outcome` package
(`StripResultText`, `ExtractOutcomeLine`). Each Driver's selection is unchanged:
claude the `result` event's `.result`, opencode each `text` event's `.part.text`.

*Amended by issue #5348:* opencode no longer synthesizes its exit code from
"valid outcome line present AND no `error` event". Its `ResolveExit` keeps a
non-zero process exit as-is and turns a clean exit into `1` only when the
stream carries an unrecovered `error` event. A missing outcome line is left to
the Box's outcome nudge and backstop, the same recovery claude gets.

*Amended by issue #5354:* opencode's `ResultText` now selects only the last
assistant message's text parts (any event with a new `part.messageID`
starts a message, so a textless final step yields no text), matching
claude's final-message `result`, so a mid-pass outcome-looking line no
longer counts. This supersedes the earlier amendment's
"opencode each `text` event's `.part.text`". With exit synthesis gone (#5348),
`ExtractOutcomeLine`'s leading-token rule is the only outcome-line rule over
result text.

*Amended by issue #5391:* an `error` event followed by agent progress
(`driverkit.IsProgress`, the same rule `Classify` applies) was recovered from
and no longer turns a clean exit into `1`, so a run that retried past a 429
still reaches the Box's nudges.

**Provider** is a new axis distinct from Driver: the model backend a Driver talks to (Anthropic, GitHub Copilot, OpenAI). "Add GitHub Copilot" is *not* a new Driver — it is the opencode Driver pointed at the `github-copilot` provider, with `MODEL` provider-namespaced (`github-copilot/…`). Credentials go two ways, mirroring the baked-default/runtime-override pattern already used for prompts: the primary path is a **nix-generated `opencode.json` with `{env:VAR}` placeholders** (documented for apiKey providers; keeps the "secrets are env, never host files" model), with a **materialized/mounted `auth.json`** fallback for Copilot's OAuth device-flow, whose headless credential path is undocumented and must be resolved by an empirical spike before the auth design is finalized.

## Considered Options

- **One fat image carrying all Drivers, selected by a runtime `DRIVER` env** — zero-rebuild A/B across tools, but a heavier image, all providers' auth carried at once, and it fights "the toolchain *is* the image." Rejected as the default; per-issue routing over lean images gives the same tandem capability without the fat image.
- **Single source of truth: nix generates the Go-side transient patterns too** — maximizes pure-nix, but threads generated data into a native binary built independently of any image. Rejected for two small coordinated registries plus a parity test.
- **Demote exit codes pipeline-wide and make the outcome line the universal signal everywhere** — instead of containing opencode's brokenness in its adapter, smear it across the shared launcher and make claude's clean behavior worse to accommodate it. Rejected; misbehavior is absorbed at the Driver boundary.
- **Copilot auth as an opaque `auth.json` blob env var, or as a host-file mount only** — the blob is schema-agnostic but ugly; the mount reintroduces host-filesystem coupling. Both kept as the fallback leg, but the generated-config `{env:}` path is primary because it is documented and in-idiom.

## Consequences

- env-schema knobs become **Driver-conditional**: `claudeOAuthToken`/`ANTHROPIC_API_KEY` are meaningless under `opencode`, and the opencode provider secret is meaningless under `claude`, so required-ness is gated on the selected Driver. That gate shipped as the launcher's `driver-credentials` check (`cmd/launcher/internal/launcherchecks/launcherchecks.go`).
- The hardcoded `scoutModel`/`reviewModel` pair is a special case of a general **N-agent roster** (a list of `{name, model, prompt, mode}` helper agents), which both Drivers render — claude to `--agents` JSON, opencode to `agents/*.md`. That generalization has since shipped in `lib/roster.nix`, configured through the `agents.models.roster`/`agents.models.byName` flake-module options (`lib/flakeModule.nix`; `roster` is a structural option, `byName` is deliberately declared outside that set, but neither has an env var of its own). They supersede the per-agent model knobs — `scoutModel`, `filerModel`, and `workerModel` outright, and `reviewModel` only as an image-time knob, since the code-owned review pass still binds its model from it; that carve-out and its precedence chain are stated in `lib/env-schema.nix`'s `reviewModel` and `reviewEffort` docs, and in `docs/reference.md`.
- A one-time human step is required to mint Copilot credentials (`opencode auth login` on a host), analogous to `claude setup-token`; the device flow cannot run in the Box.
- opencode already reads `.claude/skills/` and falls back to `CLAUDE.md` when no `AGENTS.md` exists, so baked skills and a target repo's `CLAUDE.md` are honored without extra wiring; generating `AGENTS.md` is only needed when we want it to win over an existing `CLAUDE.md`.
- The Driver seam also owns **session pin/resume** (issue #427): a fix box resumes the Driver session the initial run used rather than cold-starting one, via an ephemeral per-issue host directory the launcher mounts writable over the Box's home. The launcher's half is Driver-agnostic — create/mount/evict an opaque directory, keyed strictly per issue — but the pin/resume *verb* is each Driver's own: claude pins a deterministic session id via `--session-id` and resumes it via `--resume`, falling back cleanly to a cold context when no session is found; opencode's half shipped as a deliberate no-op — it wires no resumable session state, so the launcher creates no per-issue cache for it, needing no launcher change, exactly as predicted (`lib/drivers/opencode.nix`). (Amended by issue #5349: opencode now pins and resumes a session; and by issue #5481: pi pins and resumes one with `--session-id`; see the amendments below.)

## Amendment (issue #260): Copilot auth is env-native `OPENCODE_AUTH_CONTENT`, not `{env:}` config

The spike this ADR called for resolved the undocumented unknown, and the
answer inverts the two credential legs. The `github-copilot` provider is
OAuth-only: opencode's bundled Copilot plugin activates its loader and fetch
hook only for a stored `type: "oauth"` credential, and a config-file `apiKey`
never reaches that path — a `{env:VAR}` placeholder in a generated
`opencode.json` registers the provider but routes through the generic
openai-compatible stack without the headers the sanctioned integration uses.
A PAT/`GITHUB_TOKEN` flow was upstream-rejected as not planned. The
generated-config `{env:}` leg therefore remains valid for genuine apiKey
providers (Anthropic, OpenAI) under the opencode Driver, but is a non-path
for Copilot specifically.

The rejected "opaque `auth.json` blob env var" option turns out to be
opencode's own supported mechanism, not a spindrift workaround: when
`OPENCODE_AUTH_CONTENT` is set, opencode parses it as the entire auth store
and never touches `auth.json` — verified empirically on 1.17.15 (provider
registers, OAuth fetch path fires, nothing written to disk). The
materialized/mounted-file fallback is unnecessary; the "secrets are env,
never host files" model holds via a different env var than anticipated.

The credential itself is a single long-lived GitHub OAuth token (`gho_…`),
minted once on a host by the device flow (`opencode auth login -p
github-copilot`, opencode's partnership GitHub App) and stored verbatim in
both `refresh` and `access` with `expires: 0` — no session-token exchange, no
refresh loop, not machine-bound. The auth slice shipped the JSON blob
through verbatim, as the `opencodeAuthContent` knob (`OPENCODE_AUTH_CONTENT`,
`lib/env-schema.nix`) — no bare-token knob, no nix-generated wrapper. The
variable is in the `offArgvKeys` allowlist and gated by the launcher's
`driver-credentials` check. Full findings, sources, and empirical transcript
live on issue #260.

## Amendment (issue #267): Tier 3 cloud-IAM Providers — Bedrock env-native, Vertex needs a file, Azure rides the auth store

Research-only for the three cloud-IAM Providers opencode can point the AI SDK
at — Amazon Bedrock (`amazon-bedrock`), Google Vertex (`google-vertex`),
Azure OpenAI (`azure`) — whose auth is cloud credentials rather than
a plain `{env:VAR}` apiKey. No knobs land here, though the gate they
would hang on has since shipped: the launcher's `driver-credentials` check
walks the selected Driver's `credentials` rules (amended by issue #5505; it
once switched on the Driver), with the `github-copilot/` prefix on any model tier a
prefix-gated rule on `lib/drivers/opencode.nix`, so each leg below is one added
rule there rather than new machinery. What
is still missing is the env-var-content→file primitive one leg needs
(`cmd/launcher/internal/runner/offargv.go`'s `offArgvKeys` only keeps values
off argv, it never writes files). These are findings and a design fork,
to be wired once that primitive exists. The Providers are a **runtime
`MODEL` prefix** on the one opencode Driver (`amazon-bedrock/<model>`,
`google-vertex/<model>`, `azure/<model>`), not new Drivers — so the seam
is the same one-Driver/many-Providers axis this ADR already draws, and each
leg's wiring is **at most** one new **Driver+Provider-conditional** env-schema
entry beside `opencodeAuthContent` (Azure may reuse `opencodeAuthContent`
and add none; Vertex also needs the content→file primitive below), not
a new `lib/drivers/` file.
The three legs fall into three distinct auth shapes:

- **Bedrock — env-native, closest to the existing model, no file.** The AI
  SDK's `@ai-sdk/amazon-bedrock` reads only env *values*: SigV4
  `AWS_ACCESS_KEY_ID`/`AWS_SECRET_ACCESS_KEY`/`AWS_SESSION_TOKEN`, or — the
  single-secret leg to standardize on — the API-key bearer
  `AWS_BEARER_TOKEN_BEDROCK`, which the SDK prefers over SigV4 when set. Same
  shape as `ANTHROPIC_API_KEY`: one `secret = true`, `boxEnv = true` env-var
  knob, joined to `offArgvKeys`. Region is a **non-secret** `AWS_REGION` env
  value (or an `opencode.json` `provider.amazon-bedrock.options.region`, which
  wins over env) and needs no secret handling. Avoid the `AWS_PROFILE` leg: it
  resolves through the AWS credential chain against a mounted
  `~/.aws/credentials`, reintroducing the host-file coupling this model
  rejects. Verdict: the "secrets are env, never host files" model holds
  cleanly — `AWS_BEARER_TOKEN_BEDROCK` is the one-env-var credential.

- **Vertex — the one leg that genuinely needs a file.** opencode's Node CLI
  embeds `@ai-sdk/google-vertex`, whose default (Node) path reads
  `GOOGLE_APPLICATION_CREDENTIALS` as a **filesystem path** to a
  service-account JSON, resolved by `google-auth-library` — the env var holds
  a *path*, not the secret. The env-*value* trio
  `GOOGLE_CLIENT_EMAIL`/`GOOGLE_PRIVATE_KEY`/`GOOGLE_PRIVATE_KEY_ID` the SDK
  documents belongs to its separate `@ai-sdk/google-vertex/edge` entry point,
  not the Node CLI, so it is not a drop-in escape from the file. This is the
  case the issue anticipated: it needs a **materialize-env-content-to-a-file**
  entrypoint primitive that does not exist yet — a generic "write `$VAR`'s
  content to a fixed in-box path, then export `GOOGLE_APPLICATION_CREDENTIALS`
  pointing at it," so the secret still enters the Box as an env value and the
  host filesystem is never mounted. This is distinct from `OPENCODE_AUTH_CONTENT`:
  that var is opencode's *own* auth store (#260), whereas
  `GOOGLE_APPLICATION_CREDENTIALS` is consumed by Google's auth library
  underneath the SDK, so the auth-store path cannot carry it. Project/location
  are **non-secret** env values (opencode names them `GOOGLE_CLOUD_PROJECT`
  and optional `VERTEX_LOCATION`; the raw SDK uses
  `GOOGLE_VERTEX_PROJECT`/`GOOGLE_VERTEX_LOCATION` — opencode wraps and
  renames, worth stating explicitly). Verdict: "secrets are env" holds only
  once the content→file primitive lands; until then this leg is blocked on
  that primitive alone — the Driver-conditional gate it also needs already
  shipped.

- **Azure — rides the auth store, mirroring the Copilot resolution.** The
  correction to the pre-spike guess: `@ai-sdk/azure` *does* read an
  `AZURE_API_KEY` env value (and supports an Entra/`@azure/identity` bearer
  token provider in place of the key), so an env-native leg is not
  structurally impossible. But opencode's *own* documented flow enters the key
  interactively via `/connect` → its auth store (not headless-safe), with only
  the **non-secret** `AZURE_RESOURCE_NAME` left as an env var — structurally
  identical to the Copilot case #260 resolved. The strong first lead is
  therefore the same one that leg landed on: pre-seed the Azure credential
  through `OPENCODE_AUTH_CONTENT` (the whole auth store, env-native, nothing
  written to disk) rather than inventing a new mechanism — to be confirmed by
  the same kind of empirical mini-spike #260 ran (does opencode 1.17.x honor
  an `azure` slice in `OPENCODE_AUTH_CONTENT` the way it honors the
  `github-copilot` slice?). If that holds, Azure needs no new secret env var
  at all — it reuses `opencodeAuthContent` plus a non-secret
  `AZURE_RESOURCE_NAME` env knob; if it does not, the fallback is a plain
  `AZURE_API_KEY` secret knob in the `offArgvKeys`/`{env:}` idiom.

**env-schema / gating shape owed (per leg):** each new knob follows the
`opencodeAuthContent`/`anthropicAPIKey` pattern in `lib/env-schema.nix`
(`env`, `secret`, `doc`, `boxEnv`); the gating itself is a `modelPrefix`
credential rule on `lib/drivers/opencode.nix`, walked by the launcher's
`driver-credentials` check, not in `lib/env-schema.nix`, and keys on
**both** Driver and Provider — required only when `DRIVER=opencode` *and*
any model tier the gate walks (the #5505 amendment above) carries the
matching Provider prefix, ignored otherwise, exactly as the
`modelPrefix = "github-copilot/"` rule scopes `opencodeAuthContent`. Bedrock:
one `awsBedrockBearerToken` (`AWS_BEARER_TOKEN_BEDROCK`) secret + non-secret
`AWS_REGION`. Vertex: a `googleVertexCredentials` **content** secret
(feeding the file primitive above, *not* a raw path knob) + non-secret
`GOOGLE_CLOUD_PROJECT`/`VERTEX_LOCATION`. Azure: reuse `opencodeAuthContent`
(pending the spike) + non-secret `AZURE_RESOURCE_NAME`, or a fallback
`azureAPIKey` secret. Each secret env var named in a rule's `env` gets its
off-argv entry from that rule: `offargv.go`'s `offArgvKeys` takes the
Driver credential half from `credentialenv_gen.go`, rendered from those
rules (regenerate with `nix run .#regen`), so no hand edit to
`offargv.go` is owed.

**Egress / netns:** none of the three needs new runner-netns wiring under the
current coarse network model (`lib/env-schema.nix:268-283`'s `NETWORK_MODE` is a
four-choice knob — `open` (the default), `no-host-loopback`, `none`, `host` —
selecting a whole-namespace stance, with no per-domain egress allowlist). The
regional endpoints each leg reaches —
`bedrock-runtime.<region>.amazonaws.com`,
`<region>-aiplatform.googleapis.com`,
`<resource>.openai.azure.com` — all resolve over the same egress path as
`api.anthropic.com`; region is a config/env choice, not a firewall change.
Worth a one-line note in any future per-domain egress design, not a blocker
here. Full sources (opencode provider docs, the AI SDK provider pages, AWS
Bedrock endpoint reference) and reasoning live on issue #267.

## Amendment (issue #269): Tier 5 local/self-hosted Providers — no auth axis, the crux is the runner netns

Research-only for the three local/self-hosted Providers opencode can point the
AI SDK at — Ollama (`ollama`), LM Studio (`lmstudio`), and a llama.cpp server —
all three OpenAI-compatible endpoints loaded through the same
`@ai-sdk/openai-compatible` npm shim. Unlike Tiers 1–4 (#260, #267), this tier
has **no secret-injection axis at all**: the endpoints need no `apiKey` or
credential, so there is no env-schema knob, no credential rule for the
launcher's `driver-credentials` check, no `offArgvKeys` entry, and nothing for the
content→file primitive #267 called for. What the tier *does* need is the one
thing the cloud tiers get for free — the Box being able to **reach** the model
server — and that reachability is a runner-netns question (ADR 0006), not a
Driver question. No knobs land here; these are findings and a documented
posture, to be wired once `opencode.json` generation exists (see below).

**opencode-side config — `baseURL` + a static `models` map, nothing else.**
Each Provider is registered in the generated `opencode.json` with only a
`baseURL` and an explicit, hand-listed `models` block; none of the three
supports model auto-discovery through the AI SDK, so the model IDs and their
context/output limits must be enumerated in the generated config rather than
probed at runtime. The default local endpoints are:

- Ollama: `http://localhost:11434/v1`
- LM Studio: `http://127.0.0.1:1234/v1`
- llama.cpp server: `http://127.0.0.1:8080/v1`

Since there is no `opencode.json`-generation code yet
(`lib/drivers/opencode.nix` renders agent files but not the provider config
— #262/#263 closed without building the provider-config generation),
this is forward-looking: whatever config-rendering shape eventually lands
should need only a per-Provider `baseURL` + static `models` block here,
with no new env-schema secret knob beside `opencodeAuthContent`.

**Network reachability — the actual crux, per runner.** The two runners differ
sharply in how `NETWORK_MODE` renders, and since #2666 they also differ in what
their own defaults leave reachable:

- **bwrap (`cmd/launcher/internal/runner/bwrap.go`) — host loopback denied by
  default, reachable only under the `NETWORK_MODE=host` opt-out.** Under the
  default `open` the Box sits in its own network namespace behind a hardened
  pasta helper: `isolateNet()` is true for every mode but `host`
  (`bwrap.go:397-399`), `pastaPath()` takes the pasta route for every isolated
  mode but `none` (`bwrap.go:404-406`), and the helper is spawned with
  `pastaHardenedFlags` — `-t none -T none -u none -U none --no-map-gw`
  (`bwrap.go:663`) — so the gateway address that would otherwise splice back to
  the host is never mapped. Egress and DNS are unaffected (`--dns-forward
  169.254.2.2`, `bwrap.go:613`, resolves without reopening that splice);
  what is gone is host loopback. `http://localhost:11434/v1` is therefore
  precisely the case that does **not** work by default, and a regression test
  pins it — `bwrap_integration_test.go:389-436` asserts a guest connection to
  host loopback via pasta's gateway address is blocked under `--no-map-gw`. Nor
  is `--unshare-net` a hard on/off switch keyed to its own knob: it is appended
  only when `isolateNet && !a.pastaPath()` (`bwrap.go:587-589`), i.e. only under
  the fully-offline `NETWORK_MODE=none`. The reachable state is the explicit,
  bwrap-only `NETWORK_MODE=host`, which restores the pre-#2666
  shared-host-netns posture wholesale. What is genuinely unbuilt is the *scoped*
  variant — a targeted host-loopback hole under `open`, which the `--no-map-gw`
  hardening forecloses today — leaving `NETWORK_MODE=host`, wholesale rather
  than scoped, as the only in-repo way in. `no-host-loopback` is not a third
  state here at all: it throws at eval on `runtime=bwrap`
  (`lib/mkHarness.nix:372-373`) precisely because the isolated-by-default `open`
  already renders it.

- **podman/docker (`cmd/launcher/internal/runner/oci.go`) — the compound
  `--network` value already covers it, no code change.** `networkArg()`
  (`oci.go:418-433`) splices a non-empty raw `PODMAN_NETWORK` verbatim as a
  single `--network <value>` arg and lets it win over the mode, so compound
  backend values already flow through the existing knob:
  `PODMAN_NETWORK=pasta:--map-gw` or
  `PODMAN_NETWORK=slirp4netns:allow_host_loopback=true` punch a host-loopback
  hole while keeping the egress-restricting backend. That route is taken with
  `PODMAN_NETWORK` **alone** — `NETWORK_MODE` unset, or (at dispatch only)
  explicitly `open` — never as an override layered on a managed mode: two
  gates refuse that pairing before `networkArg()`'s raw-wins precedence can
  apply. `checkNetworkModeRuntimeGate` (`cmd/launcher/main.go:1111-1131`)
  fails the launch whenever a non-`open` `NETWORK_MODE` is set beside a raw
  knob, and at the Consumer level `networkModeCoherenceOk`
  (`lib/mkHarness.nix:366-371`) is stricter still — `network.mode` set at all
  beside `network.podman` throws at eval, `open` included — so a Consumer
  wanting the compound value sets only `network.podman` and leaves
  `network.mode` unset. Absent that, `no-host-loopback` renders `pasta` on
  podman and `bridge` on docker/nerdctl, `none` renders `none`, and the default
  `NETWORK_MODE=open` renders **no `--network` flag at all**, leaving the
  runtime's own default network in force — correspondingly `DeniesHostLoopback`
  (`runner/config.go`) is true only for `no-host-loopback` and `none`. So the
  **plain default** reachability of host loopback here is
  rootless-podman-version- and `containers.conf`-dependent — a worker wiring
  this must verify it empirically against the podman the flake pins rather than
  assume it "just works."

**Does it weaken the egress-restriction posture?** Under bwrap, yes, directly:
the pre-#2666 answer that local-Provider reachability was **already latent, not
newly opened** is now false there, because today's default already denies host
loopback, so selecting a local Provider *is* itself the callable weakening — it
cannot work without `NETWORK_MODE=host`, which hands back the entire host
namespace rather than one port. Under podman/docker that older answer still
partly holds, since `open` renders no `--network` flag and the runtime's own
default decides; the tension there is confined to an operator who has
*deliberately* opted into `no-host-loopback` and must reopen a host-loopback
path to reach the model server — and that reopen is all-or-nothing, not a mode
plus an override: the operator drops `NETWORK_MODE` entirely and sets
`PODMAN_NETWORK=pasta:--map-gw` (or `slirp4netns:allow_host_loopback=true`)
alone, swapping out of the managed mode rather than amending it. Either way
the widening must be an **explicit, documented opt-in** — never something a
Provider selection silently implies — mirroring how
`NETWORK_MODE`/`PODMAN_NETWORK` are already separate, explicit knobs from
Driver/Provider selection (see `docs/reference.md`'s network-knob rows).

**Shape owed once `opencode.json` generation exists** (#269, no code here):

- `opencode.json` Provider block for each of Ollama/LM Studio/llama.cpp:
  `baseURL` + explicit `models` map, **no credential knob** and no
  `offArgvKeys` entry.
- A doc note (added below in `docs/reference.md`, beside the `NETWORK_MODE` row
  and its `PODMAN_NETWORK`/`BWRAP_UNSHARE_NET` escape hatches) on how to reach
  host loopback per runner: `NETWORK_MODE=host` under bwrap, a compound
  `--network` value under podman.
- An explicit call-out — not silent reachability — when a local Provider is
  selected under a restricted-egress config.
- bwrap: the wholesale `NETWORK_MODE=host` opt-out is the only supported route
  today, per the bwrap runner bullet above.

Full sources (opencode's provider docs at https://opencode.ai/docs/providers/,
the `@ai-sdk/openai-compatible` loader, and this repo's runner code) and the
reasoning live on issue #269.

## Amendment (issue #4945): pi joins as a third Driver

`pi` (nixpkgs `pi-coding-agent`, binary `pi`) becomes a third Driver beside
`claude` and `opencode`. It is selected through ADR 0064's Driver map and
parsed by an ADR 0065 decoder. Its choices, each checked against the locked
package (0.99.2; re-checked against 1.1.0 by issue #5478, see the note after
this list):

- **Invocation.** `--print --mode json --no-approve`, `--model` for the
  model, `--thinking` as the effort flag, positional prompt. Effort values
  pass through: pi's `off`…`max` is a superset of `claude`'s.
- **Roster.** pi has no built-in subagents. Its official subagent extension,
  shipped in the same package, is linked from the store into pi's agent
  directory, not vendored and not loaded with `-e`, so child `pi` processes
  load it too. Roles render as Markdown agent files, like `opencode`'s.
  Per-role effort rides as a `<model>:<level>` suffix, because the extension
  forwards the parent's thinking level only to model-less agents. An
  evaluation assert therefore requires a model on any role that sets an
  effort.
- **Usage by role.** The decoder unwraps each child's messages from the
  subagent tool's result details and tags them with the agent's name as the
  role. Usage comes from those messages, never from the extension's summed
  totals. A captured Pass log from the locked version pins that shape, so a
  nixpkgs bump that reshapes it fails CI.
- **Credentials.** Provider API keys arrive through the environment. pi has
  no env-native auth store (unlike `OPENCODE_AUTH_CONTENT`), so a new secret
  knob `PI_AUTH_CONTENT` is written into pi's `auth.json` inside the Box. It
  uses a new env-value-to-in-Box-file primitive, the one the #267 amendment
  found missing for Vertex. Only non-rotating OAuth entries are accepted,
  which today means GitHub Copilot, whose refresh token is a stable GitHub
  token. pi may refresh inside the Box without invalidating the host's copy.
  Preflight rejects OAuth entries for Providers that rotate refresh tokens;
  host-side refresh for those is a later decision.
- **Hooks.** A spindrift-owned pi extension bridges pi's `tool_call` and
  `tool_result` events to the existing `claude` hook scripts, which it runs
  unchanged. A deny becomes a block, a rewritten input feeds the next script,
  and a replaced output replaces the result. A script failure blocks the
  tool. The script list is generated from the same data as `claude`'s hook
  settings. pi therefore keeps credential deny, the environment credential
  scrub, background-Bash rejection and the Bash output tee; `opencode`'s
  guard plugin (issue #5346) keeps the scrub, background-Bash rejection and
  the Bash output tee (issue #5358), beside its baked credential deny config
  (issue #5345).
- **Project trust.** `--no-approve` ignores the Target repo's `.pi/`
  settings, extensions, MCP servers, skills and prompts, so nothing from the
  repository runs beside the hook bridge. pi still reads `CLAUDE.md` and
  `AGENTS.md`, which need no trust.
- **Session resume.** Both pin and resume render `--session-id <id>`, which
  pi opens or creates. The deterministic id derivation moves into driverkit,
  shared with `claude`.
- **Subagent capability.** The `opencode`-only branch that disables the Filer
  and worker becomes a Driver capability, "hosts subagents". `pi` declares
  it; `opencode` does not. (Amended by issue #5330: that branch now
  disables only the worker. Filer presence follows the roster under every
  Driver, since `opencode` renders the Filer as an on-disk agent file, so
  the capability gates the worker alone.)

*Amended by issue #5478:* the `piseam` seam tests re-checked these choices
against the locked 1.1.0 package, running it against a stub Provider. The
invocation, `--session-id` open-or-create, the `CLAUDE.md`/`AGENTS.md` reads
and the `read{path}` and `bash{command}` tool names all held. `--no-approve`
trust held too: a project `.pi/settings.json` effort, `.pi/APPEND_SYSTEM.md`,
`.pi/skills/` and a `.pi/extensions/` file that writes a marker on load are
all ignored, and all take effect under `--approve`. Agent-directory skills
load without trust. pi reads `auth.json` from the agent directory, and a
set `PI_CODING_AGENT_DIR` wins over `~/.pi/agent`. A custom Provider with no
`apiKey` takes its key from that file, so `PI_AUTH_CONTENT` can be written
there; pi does not check the file's mode. Sessions persist under the same
directory, as `sessions/<one dir per working directory>/<id>.jsonl`, so a
Driver's session cache is the agent directory's `sessions/`. Three facts
changed or were imprecise, and the last bullet records one the amendment
did not cover.

- The subagent extension is not a package feature in 1.1.0 but a bundled
  example, `examples/extensions/subagent` under the package's
  `lib/node_modules/pi-monorepo`. It loads from
  `<agent-dir>/extensions/subagent`. The mechanism is otherwise as described:
  children inherit the agent directory through the environment and are spawned
  through `process.execPath`.
- A `:<level>` suffix on the model does set the effort, but an unknown level is
  not rejected. pi warns and sends `<model>:<bogus>` as the model id, so the
  evaluation should check that the level is valid, not only that a model is
  present. `--thinking` beats the suffix on the same invocation; the
  extension's source passes `--thinking` only to agents without a model, so
  the two never meet (the seam runs only a child whose agent file names one).
- Usage by role reads `details.results[].agent` and
  `details.results[].messages[]`, whose assistant messages carry `usage`.
  `results[].model` keeps the suffix verbatim, and partial
  `tool_execution_update` events carry the same details, the child's messages
  included, before the tool's `tool_execution_end`.
- A failed turn exits 0 with `stopReason: "error"` in the stream, and the
  stream always ends with `agent_settled`. Only invocation errors, such as an
  unknown provider or a bad session id, exit nonzero with a stderr message and
  empty stdout. That is the input to the Driver's `ResolveExit`.

Full findings and the testing plan live on issue #4945.

## Amendment (issue #5479): the first pi slice

The first `pi` slice registers the Driver, decodes its Pass logs and scrubs
its corpus, and nothing more. It ships no roster (no scout, worker or Filer
is provisioned; only the reviewer's model and effort survive, for the review
pass), no session resume, no hooks and no credential knob; Provider API keys
come from the environment. Each of those four shipped later, with the
differences its amendment below records: the roster under issue #5483,
session resume under issue #5481, the hook bridge under issue #5482 and
`PI_AUTH_CONTENT` under issue #5484. Two facts changed:

- pi's skills directory is its agent directory, `~/.pi/agent/skills`, not
  claude's. "Every Driver reads the first Driver's skills directory" was only
  ever opencode's fact, so that check moved into opencode's own check file
  (`drivers-opencode-skills-dir-matches-claude`) and `pi` has a check of its
  own.
- Heartbeat lines and Dispatch Records report pi's observed model as
  `<provider>/<model>:<thinkingLevel>`, pi's own model-suffix notation, which
  is how the effort in force surfaces next to the model. (Amended by issue
  #5592: Dispatch Records and usage now carry the bare id; see the amendment
  below.)

## Amendment (issue #5483): pi ships the roster

The Roster and Usage-by-role choices above now ship. Three details differ from
the plan. The effort rule runs after the `model = ""` opt-out drop, so an
opted-out role is removed with its effort. A role with an effort and no model
takes the coordinator's default model instead of failing, and evaluation fails
only when that is empty too. (Amended by issue #5626: neither branch was
reachable, since `normalizeRoster` rejects an entry without a string `model`
and the opt-out drop runs first, so both are gone.) The tool mapping sends
`WebFetch`, `WebSearch`, `TodoWrite` and `Skill` to nothing; an unknown tool
name, or a `tools` list that maps to nothing, fails evaluation, since an empty
`tools` line would grant every tool. Effort levels are checked at evaluation,
as a bad level would otherwise be read as part of the model id.

## Amendment (issue #5482): pi gets the hook bridge

Issue #5482 supersedes "no hooks" from the first pi slice:
`agent/pi-hook-bridge.js`, baked into the agent directory, with its hook table
generated from the `pi` column of `lib/bash-hooks.nix`, carries claude's five
tool hooks
(fail closed, except the post-tool summary). pi runs with `--no-approve`, so
the Target repo's `.pi/` is ignored while `AGENTS.md` and `CLAUDE.md` are
still read.

## Amendment (issue #5481): pi resumes its session

Issue #5481 supersedes "no session resume" from the first pi slice, shipping
the #4945 amendment's "Session resume" bullet as planned.
`cmd/launcher/internal/driver/pi/session.go` renders `--session-id <id>` for
the initial pass and for every resume (the outcome nudge, the PR-intent
nudge, the fix box and the issue pass re-dispatched after a hold).
A resume does no store lookup, unlike claude's `--resume` or opencode's title
lookup: pi opens the session when it exists and creates it otherwise, so a
missing store starts the pass fresh rather than failing.

`sessionCacheDirRelative = ".pi/agent/sessions"` in `lib/drivers/pi.nix` makes
the launcher mount the per-issue cache over pi's `sessions/` directory, the
layout the #5478 note above records, not over the whole `.pi/agent`, which
would shadow `auth.json` and `skills/` beside it. Beyond the shared id
derivation, the launcher's half is unchanged.

## Amendment (issue #5349): opencode corrective resumes continue the pinned session

opencode's session half is no longer a no-op. It cannot pin a session id at
creation: `opencode run --session <id>` only continues an existing
opencode-generated `ses_…` id, and an unknown one exits 1 ("Session not
found"). So the initial pass pins a deterministic **title** instead, `--title
spindrift-<32 hex of sha256("spindrift-session:" + REPO_SLUG + ":" +
ISSUE_NUMBER)>`, recomputable from the same two env vars the claude id uses.

opencode's store is SQLite. `envCommon.OPENCODE_DB` relocates it to
`/home/agent/.local/share/opencode/spindrift-session/opencode.db`, and
`sessionCacheDirRelative = ".local/share/opencode/spindrift-session"` in
`lib/drivers/opencode.nix` makes the launcher mount a per-issue cache there,
as it does for claude's `.claude/projects`. The subdirectory is dedicated so
the mount never shadows `~/.local/share/opencode/auth.json`.

On a resume (the outcome nudge, the PR-intent nudge, and the fix box)
`cmd/launcher/internal/driver/opencode/session.go` reads that store for the
newest root, non-archived session carrying the pinned title and renders
`--session <id>`. A missing store, no match, or any read error renders nothing
and the pass runs cold, the same fallback as claude. `--continue` is never
used: it picks the last session, not this issue's. The launcher's half is
unchanged, as the original design predicted.

Issue #5378 later moved the lookup off a hand-built path: `session.go` reads
the store path from `OPENCODE_DB` itself rather than rebuilding it from
`$HOME`, so the Go and Nix halves cannot drift. An unset or empty
`OPENCODE_DB` renders nothing.

## Amendment (ADR 0067): the closed registry has named triggers

The Driver registry stays closed on both halves, as decided here. ADR 0067
records what would open it: a Driver spindrift will not bundle, or `pi`'s
dogfood (issue #5487) showing a third bundled Driver's cost still lands on
every contributor, either of which builds a process seam for out-of-tree
Drivers; or a Consumer variant that overlays and the per-Driver options (ADR
0064) cannot express, which opens the Nix half alone. ADR 0066 makes the
host-side strategy a declarative literal, so the per-Driver code this ADR
anticipated is smaller than it described.

## Amendment (issue #5484): pi's Copilot credential ships

`PI_AUTH_CONTENT` ships as planned in the #4945 amendment. The
env-value-to-in-Box-file primitive is a Driver entry's `envFiles`, a list of
`{ env; pathRelative; }` the Box writes under `$HOME` with mode `0600` before
the first pass. It is Driver-neutral, so the Vertex leg the #267 amendment
found missing can declare its credential file the same way. The OAuth
allow-list is a separate row datum, `authContent = { env; oauthProviders;
apiKeyProviders?; }` (`apiKeyProviders` maps an OAuth id to the Provider
holding its API key), rendered into the Driver Descriptor so preflight reads
it from Nix rather than a Go literal. pi lists `github-copilot` alone;
preflight refuses any other OAuth entry and names that Provider's API-key
variable where spindrift has one, else says the Provider is unsupported.
`api_key` entries in the auth store are not restricted.

## Amendment (issue #5592): pi's observed model is the bare id, effort is its own field

Issue #5592 amends the observed-model bullet in the #5479 amendment. Dispatch
Records and usage report pi's observed model as the bare `<provider>/<model>`,
and the observed thinking level rides separately on the event and lands in
each Pass's `efforts`. An id that changes with the thinking
level fragmented per-model accounting (`SummedByModel`, `stats by model`, the
tuning `ModelKey`): a mid-Pass level change, or a same-model subagent at
another effort, produced two ids, and neither matched the configured
`<provider>/<model>`. Heartbeat role headers keep the `:<thinkingLevel>`
suffix, so the effort in force still shows next to the model; the live status
and report model carries the bare id only. Pass logs ingested before this
change are re-read once (store migration 14) to pick up bare ids and efforts;
a Record whose log was already pruned keeps its suffixed ids, so `stats by
model` can still mix suffixed and bare ids for that history.

## Amendment (issue #6048): pi's Tier 5 Providers come from `PI_MODELS_CONTENT`

The #269 amendment found that a local/self-hosted Provider needs no
secret-injection axis, only a way to declare the Provider to the Driver CLI.
For opencode that was a generated `opencode.json`. pi reads its custom
Providers from `models.json` in its agent directory, so `PI_MODELS_CONTENT`
carries the contents of that file and the pi row's `envFiles` writes it to
`.pi/agent/models.json` before the first pass, the same primitive as
`PI_AUTH_CONTENT` (#5484). Unset, nothing is written.

The tier is still not a credential. There is no credential rule for the knob,
so preflight requires no key for a Provider whose name no credential rule
matches, and a `$VAR` `apiKey` in it resolves only against variables the Box
already has. What changed is
that the file may still hold a literal `apiKey`, so the knob is treated as
credential-bearing: it is a secret knob (`_CMD` and `_FILE` sources, never
argv, never baked) and it is on the Bash scrub list. To get there without a
credential rule, the scrub list and the runner's off-argv set now derive from
every `envFiles` env as well as every credential-rule env (ADR 0066, item 7).
Every `envFiles` env that existed was already a credential env, so
`PI_MODELS_CONTENT` is the only name this adds. Reachability of the endpoint
is still the runner-netns question the #269 amendment named.

## Amendment (issue #6052): pi's `settings.json` comes from `PI_SETTINGS_CONTENT`

A self-hosted Provider streams nothing before its first token, and pi aborts a
request that sees no bytes for `httpIdleTimeoutMs` (default five minutes), so a
long cold prefill is dropped as a network transient. pi reads that setting from
`settings.json` in its agent directory. `PI_SETTINGS_CONTENT` carries the
contents of that file and the pi row's `envFiles` writes it to
`.pi/agent/settings.json` before the first pass, the same primitive as
`PI_AUTH_CONTENT` and `PI_MODELS_CONTENT`. Unset, nothing is written. It has no
credential rule and no preflight check. Like its siblings it is a secret knob
(`_CMD` and `_FILE` sources, never argv, never baked) on the Bash scrub list,
but unlike theirs its written file is deliberately left off the
credential-deny list (`lib/credential-paths.nix`): pi's `settings.json`
carries no credential, so the agent's own `read` and `bash` calls may open it.
