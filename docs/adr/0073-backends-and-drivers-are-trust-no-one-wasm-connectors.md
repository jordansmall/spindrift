# Backends and Drivers are trust-no-one WASM Connectors

Status: proposed

## Context

spindrift began as one opinionated workflow and is now a platform: five
backends, three Drivers, and contributors about to add support functions of
their own. Every backend and Driver lives in core. ADR 0068 and ADR 0066 made
each one a Strategy literal behind a Bundle, and core's Go tests already run on
the forge fake, so adding one in-tree is a row, a package and a fragment
folder. What remains in-tree is the cost a contributor cannot avoid: editing
core, running core's pipeline, sharing core's release cadence.

ADR 0067 decided the shape of an out-of-tree Driver, a process seam, and
deferred building it until a second party needed it: "a versioned contract with
no second party is a cost with no reader". This ADR builds the seam anyway, for
backends and Drivers together, as a deliberate platform bet. Pipeline speed is
not the reason. The 2026-10-20 profiling put each per-adapter check at 4–8s,
with evaluation and cache staleness as the real cost, so that is a separate
campaign.

A backend adapter runs in the Launcher's trusted half and holds the real forge
token. The github adapter shells out to `gh`, which holds it too. A Connector
from outside core, or a first-party one compromised through a flake update,
would sit next to the most powerful credential in the system. Security is the
constraint this design is ranked by.

The question was grilled on 2026-11-09. Specs #6750–#6759 carry the work.

## Decision

1. **Trust no one.** Every Connector is treated as hostile, first-party
   included. A Connector is a WebAssembly module, run in-process by an embedded
   runtime (wazero: pure Go, no cgo, the same on macOS and Linux). A module has
   no filesystem, environment, sockets, subprocesses or clock except what the
   Connector host grants. Isolation comes from the format, not the operating
   system, which a process seam cannot offer on macOS.
2. **One Host call for network Connectors.** A backend Connector is granted
   `http_request` (and logging). The Connector host:
   - checks each request against the row's **Route table** (host, method and
     path pattern, validated like registry routes, ADR 0045/0047);
   - attaches the credential itself;
   - sends the request;
   - records an audit entry;
   - refuses anything else unsent.

   A Driver Connector is granted no Host calls: it only transforms the
   Pass-log lines it is given. A Connector never holds a credential.
3. **The Route table is a permissions manifest.** Doctor lists it, and a
   Connector update that widens it surfaces as a diff. Nix's lockfile already
   pins a Connector's content.
4. **Outputs are untrusted.** Every result is schema-checked and size-capped,
   and text is handled like issue comments. A Connector that traps, exhausts its
   budget or exceeds its memory fails closed with a named error.
5. **The contract is translation primitives; core owns policy.** A method
   enters the Connector protocol only if answering it needs the backend's API.
   Moving into core:
   - branch naming (`AgentBranch`);
   - dependency and touched-file parsing (`DepsOf`, `TouchesOf`; done, #6802);
   - record and verdict rendering;
   - the github-tracker marker;
   - transient-versus-permanent classification.

   Local-only interfaces never enter the protocol. Optional interfaces become
   handshake capabilities. The tracker axis is designed from jira and the forge
   axis from forgejo, beneath the Settle Lander (#6568).
6. **Git stays in core.** Clone, push, rebase and bundle relay run in core's
   trusted git plumbing, which attaches its own credentials. A Connector
   computes URLs and refs, never with a token embedded.
7. **Built-in backends.** `local` and `git` stay native. The line is principled:
   built-ins are exactly the backends needing capabilities Connectors are
   denied (filesystem, git). Core's shared test Fake and test fake Driver are
   Connectors, so every core test crosses the real seam (ADR 0067's all or
   none). Native network adapters and native Driver Strategies may coexist with
   Connectors only during a migration window, which closes when github's merge
   crosses.
8. **Drivers cross in the same campaign. Runners never do**, because they are
   the sandbox.
9. **The Box ends with no forge credential.**
   - The `BOX_FORGE_AND_ISSUE_ACCESS` default flips to `read-only` after a
     deprecation window.
   - Every in-box forge read an agent needs becomes a Signal-socket verb,
     answered by core through the backend.
   - The code plane becomes host-seeded on every forge, generalising ADR 0033:
     the Launcher fetches host-side with its own credentials and gives the Box
     a read-only mirror to clone from, and the Box hands back a bundle. Each
     fix pass gets a fresh seed. Today the Box clones github and forgejo
     directly with the Box token, so this has to land before the token goes.
   - The Box token, in-box CLIs, in-box CLI setup and the credential helper are
     then removed, but only after the github read path has crossed.

   A Connector's in-box half is prompt fragments only; today's in-box path is
   core-owned legacy.
10. **Connector protocol versioning.** Semver `0.y.z`, independent of
    spindrift's release version. A `0.y` bump is breaking and `0.y.z` is
    additive. Core supports the current and previous `0.y`. The handshake
    returns `{protocolVersion, capabilities}` and refuses an out-of-range
    Connector with an error naming both versions. A Go SDK hides the encoding;
    the protocol, not the SDK, is the contract.
11. **One repository, three flake roles.**
    - The root flake is the batteries flake and keeps the `spindrift` name and
      URL, with github still the default.
    - Core is a subflake, imported alone as
      `github:jordansmall/spindrift?dir=core`, as a thin wrapper reading the
      source tree in place.
    - Each Connector is a subflake under the Connector directory.

    The root plain-imports core and the Connectors, with no flake inputs
    between in-repo pieces. A Connector exports `lib.connector { core }`, and
    the batteries flake always passes its own core. The registry is the module
    system: each Connector ships a flake-parts module contributing to
    `spindrift.connectors.<name>`, so duplicate names and duplicate knobs fail
    at evaluation. Each Connector carries its own release-please package and
    CODEOWNERS entry.

    A guard forbids Connector Nix from reaching outside its own directory. A CI
    tripwire evaluates the layout under Lix and CppNix. A physical split later
    is a mechanical history filter, and never needed.
12. **CI gating.** A PR touching the contract (SDK, canonical types, Host calls,
    protocol schema) requires every first-party Connector's conformance suite.
    Other PRs run none.
13. **Proof order.**
    1. `claude` in shadow mode: the plumbing proof.
    2. github reads in sampled shadow mode: the TNO proof.
    3. jira, full cut-over: the first writes.
    4. forgejo: the forge axis.
    5. `opencode` and `pi`.
    6. github writes, merge last, which closes the window.

    In shadow mode the native path stays authoritative and divergences are
    recorded. Each shadow cuts over after 20 consecutive clean dogfood runs.
14. **Two layers before anyone else's code runs.** The Launcher runs on the
    host, not in a container: as the operator's user locally, and on the CI
    runner VM in CI. A wazero escape inside it would be arbitrary code with:
    - the operator's home directory, including SSH keys and the vault or
      password-manager CLIs the `_CMD` knobs call;
    - the container runtime's control socket;
    - every Launcher credential.

    WebAssembly isolation is the only layer while every Connector is
    first-party and reviewed, hardened from the first Connector:
    - pin wazero and track its security releases;
    - fuzz the Host-call boundary;
    - prefer wazero's interpreter to its compiler, unless the per-line cost
      measured in the claude proof rules it out.

    Before any third-party Connector can run, the Connector host moves into a
    **Connector sandbox**: an empty container started by the same runner that
    starts Boxes, with no mounts, no network and no credentials. On macOS that
    means a container in the runner's VM, not a weaker process sandbox.
    `http_request` crosses back to the Launcher over the Launcher socket
    transport (ADR 0052), and the Launcher still checks routes, attaches
    credentials and audits. The protocol does not change, so Connectors do not
    notice.

### Layout evidence

A spike on 2026-11-09 tested each layout in a throwaway repository under Lix
2.95.4 and CppNix 2.35.3:

| layout | Lix | CppNix |
|---|---|---|
| `path:` inputs between in-repo subflakes | fails: copies only the subdir, so `../` escapes to `/nix/store` | works |
| relative `git+file:.?dir=…` inputs | stale: the lock pins a past commit of the same repo | stale |
| plain imports at the root, thin subflake wrappers | works, edits seen immediately | works |
| `?dir=core` standalone, reading `../` | works | works |
| Connector subflake with core overridden to the local tree, no lock write | works | works |

## Considered Options

- **A process seam (ADR 0067), with JSON-RPC over stdio and credential
  brokering.** Keeps the token out of the process, but a process keeps every
  ambient power the operating system grants it, and macOS has no bwrap to take
  them away. Rejected: TNO holds on both platforms only with WASM.
- **Go composition per Consumer** (the xcaddy model). Rejected:
  - every composition is a distinct binary, so no Consumer gets cached
    binaries;
  - Go's vendor hash is fixed per composition;
  - core's internal interfaces would become public API;
  - Connectors would be Go-only.
- **First-party Connectors compiled in, third-party ones over the seam.**
  Rejected: an in-tree path would be a privileged second contract that never
  tests the real one.
- **Strict all-or-none, with `local` and `git` as Connectors.** Rejected: it
  would grant filesystem and git Host calls for two backends, against TNO.
- **Mirroring today's 57 seam methods, or mirroring now and redesigning
  later.** Rejected: it freezes accidents into a versioned API, and it leaves
  policy inside untrusted code.
- **Separate repositories immediately.** Rejected while the protocol churns at
  `0.y`: every contract change would become a multi-repository update. The
  subflake layout delivers the ownership, versioning and CI separation now.
- **A root core flake with batteries in a subdirectory.** Rejected: it breaks
  every Consumer URL.
- **Lockstep or per-capability versioning.** Rejected: lockstep breaks every
  Connector on every core major, and per-capability negotiation solves a
  problem there is not yet.
- **Keeping a Box-side forge path.** Rejected: it doubles every Connector's
  surface and leaves a credential beside the agent.
- **WebAssembly isolation as the only layer, permanently.** Rejected: a
  runtime escape would land with the operator's full user privileges. It is
  accepted only while every Connector is first-party.
- **A bwrap helper process for the Connector host.** Rejected: there is no
  bwrap on macOS, and `sandbox-exec` is deprecated. The runner already
  isolates on both platforms.
- **The Connector sandbox from the first proof.** Close. It was deferred
  because a runtime escape during the proofs would need both a malicious
  first-party Connector and a wazero bug, and the sandbox adds a container
  start per run.

## Consequences

- ADR 0067 is superseded on the seam's shape and its triggers. ADR 0009's
  closed registry opens through the module system. ADR 0068's network Backend
  Strategies move into Connectors, and the built-ins keep theirs. ADR 0013's
  contract becomes primitives with handshake capabilities. ADR 0034 gains the
  credential-free Box as its end state.
- The github Connector runs on REST, so the host no longer needs `gh`.
- A Connector can still misreport what its backend said, and can still do
  anything its Route table allows. TNO bounds that and makes it visible; it
  does not prevent it. Core, the batteries flake and the Consumer's selection
  of Connectors remain the root of trust.
- `?dir=core` fetches the whole repository, so separation holds at evaluation,
  not download.
- Reading above the subflake's own directory depends on how both evaluators
  handle sources today. The tripwire turns an evaluator change into a CI
  failure. Moving core sources under the core directory is the fallback.
- Flipping the Box default, removing `read-write` and removing the Box-token
  knobs are versioned breaks with migration notes.
- GitHub App token refresh is honoured by the Connector host. Dogfood does not
  use the App yet; its setup is separate work.
- The Connector sandbox is a hard gate on the protocol's first outside
  release (spec #6763). Once it lands, each run pays a container
  start and each Host call pays a local socket hop.
- With the code plane host-seeded, the Box's mounts include a read-only mirror
  on every forge, not only `local`. That widens ADR 0033's documented
  exception to sharing no host filesystem from one forge to all of them.

## Amendment (issue #6798): the launcher keeps the interpreter

The per-line cost measured in the claude proof does not rule the interpreter
out, so the launcher's `connectorhost.Load` stays on wazero's interpreter and
decision 14's interpreter rule stands as written. Test binaries keep
`LoadCompiled`.

Measured with `BenchmarkConnectorDecode`, `BenchmarkConnectorHeartbeat` and
`BenchmarkConnectorLoad` (medians of 5; Linux amd64, Ryzen 9 5950X, go1.26.8,
wazero v1.11.0; the 4.38 MB claude Connector, stock Go `GOOS=wasip1`; a
corpus of 769 lines, 560,290 bytes, from 11 `pass.jsonl`):

| bench | native | interpreter | compiler |
|---|---|---|---|
| Decode, per line | 24.2 µs | 15.3 ms | 2.0 ms |
| Heartbeat (97-byte chunks through `NewHeartbeatWriter`), per line | 27.7 µs | 16.1 ms | 2.2 ms |
| Load (wasm bytes to ready Strategy) | n/a | 175 ms | 1.14 s |

The interpreter is about 630x native per line and the compiler about 83x, so
the compiler is about 7.6x faster per line but costs about 6.5x more to Load
without a cache. Heartbeat framing adds 5 to 15 percent over bare decode,
about 5 percent under the interpreter. The numbers are host-relative; the ratios are what matter.

What the live heartbeat needs is far below the interpreter's rate. The
densest real pass in the corpus emits about 2.8 lines/s (61 lines in 21.9 s,
`real-conflict-resolve-error`); the interpreter sustains about 62 lines/s
through the heartbeat, roughly 20x headroom, and about 5 percent of one core
at the real rate. (Not on dogfood-sized lines; see the #6849 re-measure
below.)

The compiler would add surface for a gain the heartbeat does not need. It
generates native code in the launcher's process from an untrusted module, so
a compiler bug becomes a host memory-safety escape, beyond the interpreter's
"wazero escape" surface. Its 1.1 s cold Load is worth paying only with an
on-disk compilation cache, and that cache holds native code, so it must be
per-user (`seamtest/strategy.go`) or another user could plant code in it.

Where the interpreter is weaker: re-decoding a whole log, such as reading
back a 332-line pass, takes seconds, not milliseconds. Revisit if a Connector
decodes at a much higher line rate, for example partial-message streaming, or
a whole-log path becomes interactive. The #6849 re-measure below puts numbers
on this: about 50 s a read. One path is already interactive: the console's
`RenderTranscript` read (`internal/console/transcript.go`) would take about
50 s on the largest Pass after a cut-over to the interpreter.

To re-run, use the command in the header of
`cmd/launcher/internal/driver/claude/connector_bench_test.go` with `-count 5`,
the wasm built as `nix/checks/go.nix`'s `setupFor` builds it.

### Re-measured on dogfood-sized Pass logs (issue #6849)

The 769-line corpus above averages 0.7 KB a line. Dogfood Pass logs average
about 5 KB a line, with tool results up to 850 KB. The new corpus,
`cmd/launcher/internal/driver/claude/testdata/dogfood/` (`NOTES.md`), is two
synthetic Passes: 636 lines, 2.96 MB; the largest (`large-pass`) is 511 lines,
2.57 MB. Medians of 3, same machine and toolchain, run inside a dogfood Box on
a shared host, so the old corpus re-measured at 19.0 ms/line on the
interpreter against 15.3 ms above. Compare ratios within one run, not across
runs. To re-run, use the same header command with `-count 3`.

| bench, per line | native | interpreter | compiler |
|---|---|---|---|
| Decode, dogfood corpus | 140 µs | 80.3 ms | 11.1 ms |
| Heartbeat (97-byte chunks), dogfood corpus | 258 µs | 87.5 ms | 13.4 ms |
| Decode, 769-line corpus (same run) | 29.1 µs | 19.0 ms | 2.59 ms |

The cost is per byte, not per call. `BenchmarkConnectorDecodeSize` decodes one
`tool_result` line per Call:

| line | native | interpreter | compiler |
|---|---|---|---|
| 245 B | 14.9 µs | 10.8 ms | 1.35 ms |
| 8,153 B | 390 µs | 189 ms | 28.9 ms |
| 261,040 B | 13.4 ms | 6.42 s | 930 ms |
| 866,965 B | 47.2 ms | 19.9 s | 2.50 s |

A linear fit over six sizes gives the interpreter about 4.9 ms per Call plus
23.3 µs per byte (every point within 5 percent), the compiler about 0.58 ms
plus 3.3 µs per byte, native about 2.4 µs plus 49 ns per byte. Above about 200
bytes a line, cost is per byte; the interpreter's is about 475x native's. The
fit is of `sizedLine`, which carries each result once; the corpus repeats it in
`content` and `tool_use_result`, so on the corpus itself the interpreter costs
about 17-18 µs per byte (80.3 ms a line at a 4.65 KB mean; `ResultText` 47 s
over 2.57 MB) and the fit over-predicts it by about 30 percent. On that
synthetic shape, #6849's two-point estimate from shadow summaries (19 ms fixed,
about 11 µs per byte) is off: the fixed part is about a quarter of that, the
per-byte part about twice. Size-free Calls cost: `newDecoder` 0.79 ms
interpreter, 0.23 ms compiler; on the interpreter `sessionFlags` 2.25 ms and
`resultEvent` 1.53 ms. Load is 205 ms interpreter, 1.58 s compiler.

After a Pass, core makes whole-log reads, each opening its own decoder and
decoding the log (`BenchmarkConnectorWholeLog`, on `large-pass`):

| read | native | interpreter | compiler |
|---|---|---|---|
| `ResultText` | 103 ms | 47.0 s | 6.94 s |
| `RenderTranscript` | 96.6 ms | 48.6 s | 6.60 s |
| `ExtractUsageAfter` | 323 ms | 184 s | 25.6 s |
| `ClassifyTransient` | 82.7 ms | 54.0 s | 7.03 s |
| `ReadPasses` | 87.7 ms | 49.1 s | 8.20 s |
| `SessionBaselinesAfter` | 104 ms | 55.2 s | 6.43 s |

`ResolveExit` makes no Connector call for claude. `ExtractUsageAfter` scans
four times, so it costs about 3.5x a single-scan read on every engine. The
first five rows sum to about 0.69 s native, 383 s (6.4 minutes) on the
interpreter, 54 s on the compiler, for one Pass, before
`SessionBaselinesAfter` on prior logs.

Shadow replay (`BenchmarkConnectorShadowReplay`, `shadow.Run` on `large-pass`,
interpreter) took 53.2 s with no divergences: 0.44 of the 2-minute
`shadowBudget`. It decodes the log once, at about 20.7 µs per byte, so a Pass
log above roughly 5 MB would exhaust the budget and record a summary error.

Do the #6798 conclusions still hold?

- **Heartbeat headroom: no, not as stated.** The interpreter sustains about
  11 lines/s through the heartbeat on the dogfood corpus (87.5 ms/line)
  against the densest real pass's 2.8 lines/s: about 4x headroom, not 20x, and
  about a quarter of one core at that rate. Worse, the cost is per byte, and
  the heartbeat writer decodes inside `Write`, so one 850 KB tool result takes
  about 20 s and stalls the stream behind it. The heartbeat keeps up on
  average, not per line.
- **Interpreter choice: holds for shadow mode, not for cut-over.** The
  largest Pass fits the shadow budget at 0.44 and the live heartbeat keeps up
  on average. For cut-over (#6799), each whole-log read becomes about 50 s and
  the after-Pass set about 6.4 minutes. The compiler alone does not fix that:
  about 7x faster is still about 54 s for the set, plus a 1.6 s Load and the
  surface rejected above. The engine stays the interpreter, and #6799 should
  not cut over until the whole-log path is resolved.

Open for #6799, not decided: decode a Pass log once and share its canonical
events across the readers (about nine full scans after a Pass today); cut the
Connector's per-byte cost (the bulk is large `tool_result` strings the readers
mostly do not need); or the compiler with a per-user cache.

## Amendment (issue #6851): the post-Pass reads share one decode

`driverkit.Driver.Decode` decodes a Pass log once into a `PassLog`, and its
readers (`ResultText`, `ExtractUsageAfter`, `RenderTranscript`, `ReadPasses`,
`SessionBaselinesAfter`, `ClassifyTransient`) answer from that decode. The
orchestrator decodes each Pass log once and runs its post-Pass reads on it. The
path-based `Driver` methods are unchanged in signature, and each now decodes
once; `ExtractUsageAfter` used to scan four times. This is the "decode once and
share" option the #6849 paragraph left open.

`BenchmarkConnectorPostPass` measures it on `large-pass`: `Decode` alone, then
`DecodeAndReads`, one decode followed by every reader above. Before the clock
starts, each reader's output is checked against the native path-based reader's.
Single runs (`-benchtime=1x`) for the Connector, a 3 s run for native, whose
1x figure swung between 60 and 110 ms:

| | native | interpreter | compiler |
|---|---|---|---|
| `Decode` alone | 87.9 ms | 43.4 s | 6.39 s |
| `DecodeAndReads` | 79.5 ms | 43.7 s | 6.12 s |
| the same six reads, each decoding its own copy | 650 ms | 299 s | 42.9 s |

The readers add nothing measurable to the decode: they walk events already in
memory and make no Connector call. The last row sums the path-based rows of
`BenchmarkConnectorWholeLog` from this run, so it is the cost of the same set
before this change, about 7x the shared decode on both the interpreter and
the compiler. Those rows, with `ExtractUsageAfter` now one decode:

| read | native | interpreter | compiler |
|---|---|---|---|
| `ResultText` | 109 ms | 44.5 s | 6.21 s |
| `RenderTranscript` | 118 ms | 48.0 s | 7.46 s |
| `ExtractUsageAfter` | 102 ms | 52.5 s | 7.60 s |
| `ClassifyTransient` | 109 ms | 50.4 s | 7.44 s |
| `ReadPasses` | 112 ms | 46.1 s | 6.79 s |
| `SessionBaselinesAfter` | 102 ms | 57.1 s | 7.39 s |

`ExtractUsageAfter` fell from 184 s to 52.5 s on the interpreter and from
25.6 s to 7.60 s on the compiler, from about 3.5x a single-scan read to about
1.1x. Runs differ by a few seconds on the same engine (`ResultText` was 47.0 s
under #6849 and 44.5 s here), so read the rows as one-decode cost, not as a
ranking.

The post-Pass reads run in three processes, and each shares its decode:

- The in-Box orchestrator decodes each Pass log once for its verdict, outcome
  and usage reads. It writes the final Pass's result text to
  `<stream-log>.result` (a path convention, since the Box-to-orchestrator argv
  is pinned by the driver-invocation goldens), so the Box reads that instead
  of decoding the log again. The file is removed before each Pass truncates
  the log, and after a delta-review BLOCK appends to it, so a Box that finds
  no file decodes the log itself.
- The host Dispatch memoizes its decode of each attempt log, so classify and
  every later usage read over the attempt logs share it. The memo keys on file
  identity rather than path, because a retry rotates log X to X.1 and writes a
  new X.
- driver-exec's `ResolveExit` makes no Connector call for claude.

The host's attempt log and the Box's stream log are different files on
different machines, so each is decoded once. The post-Pass cost through the
interpreter is therefore about 44 s per log on the largest Pass, not 44 s per
reader. One whole-log read stays outside this: `dispatchrecord.ParseLog`
decodes the primary log when a fix or conflict-resolve Dispatch starts, to
continue its Record ID. The per-byte cost of a decode is unchanged, so #6799
still has the other two options above open: cut that per-byte cost, or the
compiler with a per-user cache.

## Amendment (issue #6796): a Connector's Driver row reaches the binaries

`spindrift.connectors.<name>.driver` carries a Driver row, and the flake module
passes it to `mkHarness` as `driverRows`. A row outside the core registry also
carries the Driver Connector's wasm `module`; its Descriptor facts and module
path travel in the launcher input document's `drivers` section and in a rows
file the Box hands to the orchestrator and driver-exec. A native Strategy
still wins over a row of the same name, and with no outside rows the binaries
use the generated Driver Descriptor. `mkHarness` refuses a row without a
module, a core-named row whose facts differ from core's, and a credential env
outside the set generated from the core rows. The hooks column stays core-only,
so an outside Driver has no per-Driver guard hook rows yet.

## Amendment (issue #6797): shadow mode for the claude Connector

The shadow switch is the row option `spindrift.connectors.<name>.shadow`, not an
env knob, since a Connector row's knobs reach no flake option or Box env yet.
The comparator runs after each Pass in `driver-exec`, against the native
Strategy that stays authoritative, and its records are `shadow_divergence` and
`shadow_summary` host ops in the Pass log, which CI already uploads. The
cut-over count and its recipe are in the reference's Shadow mode section.

## Amendment (issue #6805): a Connector's backend row reaches the launcher

`spindrift.connectors.<name>.backend` carries `{ module; routes; credential; }`,
and the flake module passes the rows to `mkHarness` as `backendRows`, with
`credential` resolved to its env var; it must name a secret, host-only
(`boxEnv = false`) knob the Connector itself owns, so a Connector never gets a
core credential such as the forge token. Evaluation checks the Route table with
the registry-route rules the Connector host applies at load, naming the
Connector and the route, and refuses a row that would replace the built-in `git`
or `local` backend. The rows travel in the launcher input
document's `backends` section; the launcher and daemon validate and store the
rows. Binding a module to its Route table and attaching the credential
host-side happen once a Connector backend is selected as the tracker or forge,
which is later work (issues #6807 and #6810). A backend without a row is still
built from the generated registry.

## Amendment (issue #6806): the diff is against a baseline in the Consumer flake

Decision 3's diff is taken against `spindrift-connector-routes.json` at the
Consumer flake's root, a Connector-name-keyed list of accepted routes. The file
is read from the flake's own source, not an option, so a Connector's module
cannot accept its own routes. A missing file or key accepts nothing, so a
Connector arriving through a flake-lock bump shows every route as added. A live
route is covered when an accepted route has the same method and host and its
path equals the accepted path or lies beneath it; a narrowing is not flagged.
`spindrift doctor` prints the table and the added routes as Advisory rows, a
report that never fails configuration validation; accepting a widening is a
reviewed commit to the file. The reference's Reviewing a widened Route table
has the procedure.

## Amendment (issue #6810): github reads in sampled shadow mode

Proof-order step 2 is in place on dogfood: the launcher wraps the native github
tracker and forge, and the Connector answers a sample of the same reads beside
them. Native stays authoritative and every write stays native. Sampling is per
operation at a row-owned rate (`spindrift.connectors.<name>.shadowEvery`,
default 10, first call always sampled), so a rare read such as `FailureDetail`
is still compared. A rate limit is classified host-side, in the Connector
host's audit entry, because the guest sees only status and body; the shadow
then suspends itself for the rest of the run so it adds no load once GitHub
pushes back, and the summary says so, which makes the run not clean. The
credential is a static token, `SPINDRIFT_GITHUB_CONNECTOR_TOKEN`, for now: the
backend row's `Authorize` reads the environment at each request, and nothing
yet wires a `GH_TOKEN_REFRESH_FILE` follower into `backendrows.Load` (the
network host can follow one, and `network_rotate_test.go` covers it). The
cut-over bar is the same 20 consecutive clean runs, counted over the
`shadow-github-*.log` records; the reference's Shadow mode section has the
recipe and what counts as clean.
