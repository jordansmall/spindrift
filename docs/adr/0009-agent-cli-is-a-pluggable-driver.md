# The agent CLI is a pluggable Driver; opencode joins claude behind it

> Note (issue #609): the decision below is the seam's design target, not
> shipped behavior. `opencode` has not landed — `claude` remains the only
> implemented Driver (single entry in the `lib/drivers/` registry and in the
> generated Go driver-name list). See CONTEXT.md's Driver/Provider
> definitions and `docs/reference.md`.

Until now the Box ran exactly one agent CLI — `claude -p … --dangerously-skip-permissions` — and Claude-specific assumptions leaked into six places: the invocation, the auth env, the `stream-json` outcome extraction, the `--agents` subagent JSON, skill discovery, and the launcher's Anthropic-specific transient classifier. To add **opencode** (with GitHub Copilot as a model *provider*), the agent CLI becomes a *Driver* seam: a swappable in-box tool selected at build time, exactly analogous to the `runtime` runner seam (ADR 0006). `claude` stays the default; `opencode` is the second Driver. `Driver` is a provisional name.

The Driver is a **build-time** choice — one Driver per image, picked by a `mkHarness`/flake option beside `runtime`. Switching binaries is a closure change, so it belongs at build time; `MODEL` stays a runtime knob because it is only a string arg to the same binary. Build produces one lean image per Driver — named in the `agent-image-<driver>` family (`agent-image-claude`, `agent-image-opencode`, …), since the `spindrift` output name is reserved for the host CLI and ADR 0010 is authoritative on flake-output naming — rather than one fat image carrying every tool. Per-image packaging does **not** foreclose a mixed fleet: because the images exist as separate artifacts, per-issue routing (a `driver:opencode` label → the matching image) is a later launcher feature, not a repackaging. Routing granularity is deliberately deferred — this run ships global per-run selection only.

A Driver is inherently **two coordinated pieces keyed by one name**, because it straddles two hosts that cannot share code. The **in-box** half (invocation argv, agent-config rendering, skill wiring, outcome-line extraction) is baked into the image and is **nix-generated** (ADR 0005), living in a `lib/drivers/` registry that fans out like the env-schema preambles. The **host-side** half (transient classification, heartbeat parsing, usage extraction) lives in the launcher — a single native binary shared across all images, so it is a Go `Driver` strategy interface (the forge/runner idiom) selected at runtime by a `DRIVER` value threaded from the same knob that picks the image. A Go test asserts every nix Driver name has a matching Go strategy so the two registries cannot drift.

Each Driver **normalizes its tool's misbehavior at its own boundary**, so the entrypoint tail, launcher, merge gate, and retry logic stay Driver-agnostic. opencode is badly behaved where claude is clean — it exits `0` on error, emits no `result` envelope (its final `step_finish` may not even fire before exit), and its rate-limit markers look nothing like Anthropic's. The opencode adapter therefore scans every `text` event's `part.text` for the sentinel and surfaces it as a bare stdout line (claude uses `jq` on the result event), and **synthesizes** a trustworthy exit code from "valid outcome line present AND no `error` event" because opencode's own code is worthless. The upshot: the **`SPINDRIFT_OUTCOME` line is the true cross-Driver success contract**; the exit code is per-Driver corroboration only (already half-true — `outcome.LastInLog` treats a missing line as `taskFailed` regardless of exit).

**Provider** is a new axis distinct from Driver: the model backend a Driver talks to (Anthropic, GitHub Copilot, OpenAI). "Add GitHub Copilot" is *not* a new Driver — it is the opencode Driver pointed at the `github-copilot` provider, with `MODEL` provider-namespaced (`github-copilot/…`). Credentials go two ways, mirroring the baked-default/runtime-override pattern already used for prompts: the primary path is a **nix-generated `opencode.json` with `{env:VAR}` placeholders** (documented for apiKey providers; keeps the "secrets are env, never host files" model), with a **materialized/mounted `auth.json`** fallback for Copilot's OAuth device-flow, whose headless credential path is undocumented and must be resolved by an empirical spike before the auth design is finalized.

## Considered Options

- **One fat image carrying all Drivers, selected by a runtime `DRIVER` env** — zero-rebuild A/B across tools, but a heavier image, all providers' auth carried at once, and it fights "the toolchain *is* the image." Rejected as the default; per-issue routing over lean images gives the same tandem capability without the fat image.
- **Single source of truth: nix generates the Go-side transient patterns too** — maximizes pure-nix, but threads generated data into a native binary built independently of any image. Rejected for two small coordinated registries plus a parity test.
- **Demote exit codes pipeline-wide and make the outcome line the universal signal everywhere** — instead of containing opencode's brokenness in its adapter, smear it across the shared launcher and make claude's clean behavior worse to accommodate it. Rejected; misbehavior is absorbed at the Driver boundary.
- **Copilot auth as an opaque `auth.json` blob env var, or as a host-file mount only** — the blob is schema-agnostic but ugly; the mount reintroduces host-filesystem coupling. Both kept as the fallback leg, but the generated-config `{env:}` path is primary because it is documented and in-idiom.

## Consequences

- env-schema knobs become **Driver-conditional**: `claudeOAuthToken`/`ANTHROPIC_API_KEY` are meaningless under `opencode`, and the opencode provider secret is meaningless under `claude`, so `validate()` must gate required-ness on the selected Driver.
- The hardcoded `scoutModel`/`reviewModel` pair is a special case of a general **N-agent roster** (a list of `{name, model, prompt, mode}` helper agents), which both Drivers render — claude to `--agents` JSON, opencode to `agents/*.md`. That generalization is sequenced *after* the opencode Driver lands (opencode first renders today's fixed pair), as an independent vertical slice.
- A one-time human step is required to mint Copilot credentials (`opencode auth login` on a host), analogous to `claude setup-token`; the device flow cannot run in the Box.
- opencode already reads `.claude/skills/` and falls back to `CLAUDE.md` when no `AGENTS.md` exists, so baked skills and a target repo's `CLAUDE.md` are honored without extra wiring; generating `AGENTS.md` is only needed when we want it to win over an existing `CLAUDE.md`.
- The Driver seam also owns **session pin/resume** (issue #427): a fix box resumes the Driver session the initial run used rather than cold-starting one, via an ephemeral per-issue host directory the launcher mounts writable over the Box's home. The launcher's half is Driver-agnostic — create/mount/evict an opaque directory, keyed strictly per issue — but the pin/resume *verb* is each Driver's own: claude pins a deterministic session id via `--session-id` and resumes it via `--resume`, falling back cleanly to a cold context when no session is found; opencode would implement its own equivalent without any launcher change.

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
refresh loop, not machine-bound. The auth slice's remaining design choice is
knob shape: pass the JSON blob through verbatim, or (more in-idiom with
`claudeOAuthToken`) a bare-token knob from which the nix-generated in-box
half synthesizes the static `OPENCODE_AUTH_CONTENT` wrapper. Either way the
variable joins the `bwrapSecrets` allowlist and the Driver-conditional
`validate()` gate. Full findings, sources, and empirical transcript live on
issue #260.

## Amendment (issue #267): Tier 3 cloud-IAM Providers — Bedrock env-native, Vertex needs a file, Azure rides the auth store

Research-only for the three cloud-IAM Providers opencode can point the AI SDK
at — Amazon Bedrock (`amazon-bedrock`), Google Vertex (`google-vertex`),
Azure OpenAI (`azure`) — whose auth is cloud credentials rather than a plain
`{env:VAR}` apiKey. No knobs land here: the Driver-conditional `validate()`
gate this section owes is itself owed by #262/#263, and one leg needs an
env-var-content→file primitive that does not exist yet
(`cmd/launcher/internal/runner/bwrap.go`'s `bwrapSecrets` only keeps values
off argv, it never writes files). These are findings and a design fork, to be
wired once the base opencode Driver and the `validate()` machinery exist. The
Providers are a **runtime `MODEL` prefix** on the one opencode Driver
(`amazon-bedrock/<model>`, `google-vertex/<model>`, `azure/<model>`), not new
Drivers — so the seam is the same one-Driver/many-Providers axis this ADR
already draws, and each leg's wiring is **at most** one new
**Driver+Provider-conditional** env-schema entry beside `opencodeAuthContent`
(Azure may reuse `opencodeAuthContent` and add none; Vertex also needs the
content→file primitive below), not a new `lib/drivers/` file.
The three legs fall into three distinct auth shapes:

- **Bedrock — env-native, closest to the existing model, no file.** The AI
  SDK's `@ai-sdk/amazon-bedrock` reads only env *values*: SigV4
  `AWS_ACCESS_KEY_ID`/`AWS_SECRET_ACCESS_KEY`/`AWS_SESSION_TOKEN`, or — the
  single-secret leg to standardize on — the API-key bearer
  `AWS_BEARER_TOKEN_BEDROCK`, which the SDK prefers over SigV4 when set. Same
  shape as `ANTHROPIC_API_KEY`: one `secret = true`, `boxEnv = true` env-var
  knob, joined to `bwrapSecrets`. Region is a **non-secret** `AWS_REGION` env
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
  that primitive, not just on #262/#263.

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
  `AZURE_API_KEY` secret knob in the `bwrapSecrets`/`{env:}` idiom.

**env-schema / `validate()` shape owed (per leg, once #262/#263 land):** each
new knob follows the `opencodeAuthContent`/`anthropicAPIKey` pattern in
`lib/env-schema.nix` (`env`, `secret`, `doc`, `boxEnv`) and gates on **both**
Driver and Provider — required only when `DRIVER=opencode` *and* `MODEL`
carries the matching Provider prefix, ignored otherwise, exactly as
`opencodeAuthContent` is scoped to `MODEL=github-copilot/<model>`. Bedrock:
one `awsBedrockBearerToken` (`AWS_BEARER_TOKEN_BEDROCK`) secret + non-secret
`AWS_REGION`. Vertex: a `googleVertexCredentials` **content** secret (feeding
the file primitive above, *not* a raw path knob) + non-secret
`GOOGLE_CLOUD_PROJECT`/`VERTEX_LOCATION`. Azure: reuse `opencodeAuthContent`
(pending the spike) + non-secret `AZURE_RESOURCE_NAME`, or a fallback
`azureAPIKey` secret. Each secret env var joins the `bwrapSecrets` allowlist
in `bwrap.go` when its knob lands.

**Egress / netns:** none of the three needs new runner-netns wiring under the
current coarse network model (`lib/env-schema.nix`'s network mode is
unset/NAT vs `pasta`, with no per-domain egress allowlist). The regional
endpoints each leg reaches —
`bedrock-runtime.<region>.amazonaws.com`,
`<region>-aiplatform.googleapis.com`,
`<resource>.openai.azure.com` — all resolve over the same egress path as
`api.anthropic.com`; region is a config/env choice, not a firewall change.
Worth a one-line note in any future per-domain egress design, not a blocker
here. Full sources (opencode provider docs, the AI SDK provider pages, AWS
Bedrock endpoint reference) and reasoning live on issue #267.
