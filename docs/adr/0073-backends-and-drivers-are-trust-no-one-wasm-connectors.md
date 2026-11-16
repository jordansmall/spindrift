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
   budget (its own work; see the #7097 amendment) or exceeds its memory fails
   closed with a named error.
5. **The contract is translation primitives; core owns policy.** A method
   enters the Connector protocol only if answering it needs the backend's API.
   Moving into core:
   - branch naming (`AgentBranch`; done, #6859);
   - dependency and touched-file parsing (`DepsOf`, `TouchesOf`; done, #6802);
   - record and verdict rendering;
   - the github-tracker marker (done, #6880);
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
    starts Boxes, with no mounts but the host binary, no network and no
    credentials. On macOS that means a container in the runner's VM, not a weaker
    process sandbox. `http_request` crosses back to the Launcher over the
    sandbox's attached stdio (see the #7013 amendment), and the Launcher still
    checks routes, attaches credentials and audits. The protocol does not
    change, so Connectors do not notice.

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

## Amendment (issue #6850): Pass-log lines decode in batches

The Driver gains `driver.decodeBatch`, taking `{decoder, lines}` and returning
one event array per line, decoded in order on one stateful decoder. It is
additive within 0.3 and gated by a capability of the same name: a Connector
without it is decoded one line per call, as before, and `driverkit`'s
`Strategy.BatchDecoder()` hides the difference. A `spindrift_op` line in a batch
decodes as core's host op, as in a single call. A request packs lines under the
`maxRequest` cap; a request that fails, or whose answer is not one element per
line, makes each of its lines one `unparsed` event, as a failed per-line call
does, and a rejected element makes only its own line one. The request limits
(time, memory, output) scale with request bytes, so a batch gets one fixed base
rather than one per line, and never more than its bytes allow any request.
Batch and per-line decoding agree for a well-behaved guest: a guest JSON-RPC
error fails only that line on `driver.decode` but every line of the request on
`driver.decodeBatch`. SDK guests error only on an unknown handle, which fails
every line in both modes.

The shadow replay decodes its Connector side in batches of about 1 MiB of
lines and still compares line by line, so a divergence reports the same line
number. The conformance kit checks that a Connector's batch answers equal its
per-line answers.

`BenchmarkConnectorDecodeBatch` runs the corpora of `BenchmarkConnectorDecode`
through a batch decoder in 1 MiB batches, the shadow's bound. Medians of 3,
`-benchtime=1x`, one run on a shared host that was slower than #6849's (the
per-call dogfood interpreter row is 104 ms there against 80.3 ms above), so
compare the rows within this table, not with the ones above. Native has no
batch decoder, so its batched row is the per-line fallback and shows run noise:

| per line | native | interpreter | compiler |
|---|---|---|---|
| Decode, dogfood corpus, per call | 170 µs | 104 ms | 14.5 ms |
| Decode, dogfood corpus, batched | 183 µs | 96.6 ms | 13.7 ms |
| Decode, 769-line corpus, per call | 32.5 µs | 19.4 ms | 2.65 ms |
| Decode, 769-line corpus, batched | 41.2 µs | 20.9 ms | 2.62 ms |

On the dogfood corpus batching saves about 7 percent on the interpreter and 5
percent on the compiler, which is the per-Call base the #6849 fit puts at about
5 ms of 80. The cost is per byte, and batching leaves the bytes alone. On the
small-line corpus there is no measurable saving. Batching therefore removes the
per-line base but does not change the whole-log picture above.

`BenchmarkConnectorShadowReplay` now batches through `shadow.Run`. Three runs
on `large-pass` took 55.4 s, 49.2 s and 43.6 s, with no divergences: median
0.41 of the 2-minute `shadowBudget`, against 53.2 s and 0.44 under #6849. The
spread is wider than the difference, so the headroom for the largest Pass is
unchanged in practice, and a log above roughly 5 MB still exhausts the budget.

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

## Amendment (issue #6857): a Connector's fragment folder reaches the Box

`spindrift.connectors.<name>.fragments` carries `{ name; dir; shipped; }`: a
prompt-fragment folder the Connector contributes, with `shipped` listing its
files. Evaluation validates the declaration against the fragment registry,
naming the Connector and path on a file for a folder not of its row's axis or
on a missing, stray or duplicate file, and `mkHarness` overlays `dir` onto the
Box's `prompts/fragments`. The jira Connector ships jira's tracker-axis folder
this way, now that jira has its own tracker read signal instead of riding
github's.

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

## Amendment (issue #6893): a Route may follow one redirect

The host never follows a redirect, and the guest's Response carries only a
status and a body, so a Connector cannot read an answer that lives behind a 302.
GitHub answers a job-log read with a 302 to a pre-signed blob URL on a host that
varies. A Route may therefore set `followRedirect`: on a GET route only, the
host follows exactly one hop, to an `https` URL without userinfo, sending it
afresh with neither the credential nor the guest's headers (the pre-signed URL
is its own authority), under the same body cap, except that a body over the cap
keeps its tail rather than failing the call (an oversized body is fatal to the
Connector, and the tail of a log is the useful part). The audit line records the
target as `redirect_host`. The hop asks for the tail with a `Range` suffix and is
bounded to half of what remains of the request's network timeout, failing
non-fatally (not killing the Connector) past it. `Route.String` shows the
opt-in, so the Route-table diff `spindrift doctor` prints, and a baseline that
lacks it, count it as a widening.

## Amendment (issue #6863): a composed PR forge lets the Connector write PRs

`connectorforge.New`'s read-only wrap still keeps every write native. A
composed PR forge (`connectorforge.Compose` over a Connector declaring
`forge.prs`) instead has the Connector perform the PR writes: open, merge, draft
flips, comments and auto-merge. Git push and rebase stay in core with core's
credential, and every landing and merge-gate decision stays in core too: merge
failure classification, PR URL pinning and the cap on failure detail.

## Amendment (issue #6864): the forge axis is final; draft PRs are optional

The #6863 amendment put PR open in the always-on PR surface. It is instead the
optional `forge.draftPRs` capability (`forge.openPR`): core opens a PR only to
host-mediate, and a forge without it still lands and merges a PR the Box opened
itself. Without it the forge lacks draft creation exactly as a native forge
without the API does.

The protocol stays 0.3.0, with no bump. Moving `forge.openPR` behind
`forge.draftPRs` breaks a Connector that relied on the #6863 surface, but
protocol 0.3 (commit 28493d2ed) has not shipped in any spindrift release, so no
released Connector declares the old shape.

The forge axis is now final: `forge` (reads), `forge.branches` (`remote`,
`branchExists`, `deleteBranch`), `forge.pushOnly` or `forge.prs` (`behind`,
`prFiles`, `setDraft`, `comment`, `merge`), and the optional `forge.autoMerge`,
`forge.ciRun`, `forge.branchProtection`, `forge.probe` and `forge.draftPRs`. A
later forge surface is a new optional capability, an additive protocol bump,
not a change to these.

A read-only Box is host-mediated on a Connector forge declaring
`forge.draftPRs`, and only there. Core's git pushes a branch it built,
relays the Box's bundle and reads its commit subjects, in its own clone of the
validated `forge.remote` URL with its own credential, so the Connector never
sees the bundle, git or the credential (decision 6 holds). Only the draft-PR
create crosses the Connector. The Connector has no duplicate class, so a failed
create adopts the branch's open PR as not created, which is what a retried
hand-off needs. Without the capability, Settle blocks a read-only hand-off as it
does for a native PR forge without bundle relay.

Error classes stay the five of the Connector protocol and core decides on each:
`transient` retries, except for the non-idempotent `forge.merge`, `forge.openPR`
and `forge.comment`, which run once (a merge's `transient` becomes
`ErrMergeTransient`), `rate-limited` feeds core's rate-limit hold, `auth` is an auth failure,
`not-found` on a retried delete is success, and `permanent` returns as it came.

Interfaces that exist only for the local forge, the integration tip
(`LandingRef`), landing containment (`LandingContainmentQuery`), `LandingRepair`
and branch protection on the Accumulation repo, never enter the protocol: they
need the Accumulation repo, which only a Built-in backend owns, so the `local`
forge stays native (decisions 5 and 7).

## Amendment (issue #6897): the read-only mirror is on github and forgejo

The consequence that the Box's mounts gain a read-only mirror on every forge
has happened for `github` and `forgejo` under
`BOX_FORGE_AND_ISSUE_ACCESS=read-only`. The Launcher fetches the Target repo
host-side before each Pass, mounts it at `/repo`, and forwards
`BOX_HOST_SEEDED_MIRROR=1`; the Box clones it without a forge credential,
keeping a credential-free `forge` remote so `gh` and `fj` resolve the repo; it
is also the push default, so `gh` finds the current branch's PR. ADR 0033's
amendment for this issue records the widened exception. Two parts of the
code plane are still to come: `read-write` keeps its direct in-box clone until
it is removed, and the read-only Box still holds `GH_TOKEN` for its in-box
reads, so removing the Box token is separate work (spec #6757).

## Amendment (issue #6854): the tracker Connector performs lifecycle writes

A Connector declaring the tracker write primitives (`tracker.transition`,
`tracker.editLabels`, and `tracker.labels` for the label list and create)
performs claims, transitions and verdict label edits through `http_request`.
Jira's Route table widens by PUT and POST `/rest/api/2/issue` and GET
`/rest/api/2/label` (`connectors/jira/routes.nix`). Every lifecycle decision
stays in core, in `connectortracker`'s transition layer: the hybrid fallback to
a label swap, the status-only refusal, the settled-label strip before a native
claim, the already-claimed check, verdict completion, and research staying
label-only. The Connector answers only whether a move happened, was unavailable
or was unmapped. The production wiring waits for the #6753 cut-over: the native
adapter stays authoritative until then (amended by issue #7127: a jira row with
`primary` builds its tracker from the Connector, status lifecycle included),
and `readshadow.go` still builds the
Connector tracker with dispatch labels alone, no status lifecycle.

## Amendment (issue #6855): host-posted comments and findings go through the Connector

A tracker Connector may also declare `tracker.comment` and `tracker.fileIssue`,
and core then resolves its host-posted commenter and issue filer from the
handshake, so host mediation (ADR 0072) keeps its shape. Core sends each in
canonical form: the first research marker line in a comment's body becomes a
`research` flag, and a finding's dedup marker is split off the body and sent
beside it, with the issue type core files as. The jira Connector converts the
Markdown to wiki markup with a copy of the native converter, stores the
research flag as the `spindrift-research` comment property and the dedup
marker as the `spindrift.dedup` issue property, and returns the filed issue's
browse URL. Both writes go through POST `/rest/api/2/issue`, already in its
Route table.

## Amendment (issue #6884): a response-header read beside `http_request`

A network Connector may also import `spindrift.http_response_header`, which
returns one named header of the last `http_request`'s response. A guest sees
only status and body otherwise, so it cannot read a header such as
`X-OAuth-Scopes`. The call is additive: a Connector that never imports it, and
protocol 0.3, are unchanged. It exposes only response headers (`Set-Cookie`
excluded), nothing of the request or the credential. The reference's
Network Connectors entry has the ABI.

## Amendment (issue #7019): first-party marks gate only the Box

A Driver Connector the batteries flake builds carries a first-party mark on its
wire row. `mkHarness` derives the mark and no row can set it: it renders the
mark only when the row's `module` evaluates to the same wasm store path the
batteries flake itself builds for a Connector of that name, and a row that sets
`firstParty` at all fails evaluation. Overriding `module` on the public `claude`
row, or renaming it, therefore yields no mark. The residual is that trust rests
on the store path, so on the Nix store and its substituters.

The mark has a single purpose, the Box-side gate. A Driver Connector decodes
in-process inside the Box, and the Box is not an empty room: it holds the
agent's model credential and the repo, plus a forge token until #6757. The gate
sits in `driverrows.UseFile`, the entry driver-exec, the orchestrator and the
box share. An unmarked module row still registers; loading that Driver fails
with a named error (`driverrows.ErrNotFirstParty`) and no in-process fallback,
while the Box's other Drivers run, and an unmarked shadow row is skipped with
that reason. Third-party Driver Connectors wait for a later design that decodes
somewhere empty.

On the Launcher there is no first-party exception, and host-side `Use` is
ungated. The mandatory Connector sandbox (decision 14, spec #6763) has not
landed; once it does, every Launcher-hosted Connector moves into it. Only
Driver Connectors carry the mark: Backend Connectors such as `github` and
`jira` load in the Launcher and need none. This is the most-secure reading of
#6763's "in-process only for first-party" (decided 2026-11-13).

Decision 1 stands: trust no one, first-party included. The mark is a placement
rule, not trust.

## Amendment (issue #7013): the sandbox transport is stdio, and modules arrive over the pipe

Decision 14 named the Launcher socket (ADR 0052) as the way `http_request`
crosses back out of the Connector sandbox. That is replaced by the sandbox's
attached stdio. A socket needs a bind mount, and its TCP fallback (macOS
Docker) needs a container network, so either would break the sandbox's "no
mounts, no network" promise. Stdio rides the container runtime's own client
connection, so it works unchanged on podman, docker, nerdctl, bwrap and inside
a macOS runner VM. Checked on podman and bwrap: 5 MB round-tripped
byte-identical with `--network=none` and `--unshare-all`.

The host is the `connector-host` binary, a static cgo-off build. The Launcher
starts it and speaks length-prefixed frames on its stdin and stdout; stderr
carries logs only. The Launcher sends the module bytes over that pipe, and the
host never reads a module path, so the sandbox holds only the read-only host
binary and needs no module mounts. Every `http_request` goes back over the same
pipe to the Launcher's Host-call handler, so Routes, credentials, audit and
output validation stay in the Launcher and the host never sees a credential.
Named errors survive the wire. The host exits on stdin EOF. The container
itself comes in a later ticket of spec #6763; until then the host runs as a
plain child process of the Launcher.

## Amendment (issue #7018): what the Connector sandbox costs

Measured with `BenchmarkConnectorSandboxDecode`, `BenchmarkConnectorSandboxLoad`,
`BenchmarkConnectorSandboxStart` and `BenchmarkConnectorSandboxHostCall` in
`cmd/launcher/internal/seamtest` (build tag `integration`; medians of 5; Linux
6.18 amd64, Ryzen 9 5950X, 32 threads, go1.26.8, wazero v1.11.0; rootless
podman 5.8.9 on crun and cgroup v2; bubblewrap 0.12.0 with the
`lib/seccomp.nix` filter; the claude Connector over the 769-line corpus; for
the Host call, the `network.wasm` fixture against a local TLS backend, because
the claude Connector makes no `http_request` Host call and that row needs a
Connector that does). They sit with the seam tests rather than beside
`BenchmarkConnectorDecode` because only the runner families and seam tests may
import a family (ADR 0069). All four columns come from one run on a
workstation, not a Box, so compare ratios within this table, not across runs.
The connector host runs its guests on wazero's interpreter, so the in-process
column is the interpreter and the columns compare like with like.

| bench | in-process (interpreter) | process sandbox | podman | bwrap |
|---|---|---|---|---|
| Decode, per line | 16.62 ms | 16.03 ms | 20.39 ms | 19.67 ms |
| Load (wasm bytes to ready Strategy) | 187.6 ms | 192.7 ms | 190.6 ms | 181.5 ms |
| Sandbox start (runner start to the host's hello) | n/a | 2.37 ms | 206.4 ms | 20.73 ms |
| One call making one `http_request` | 1.82 ms | 1.96 ms | 2.34 ms | 2.22 ms |

The process row is the pipe hop with no isolation, and it sits within noise of
in-process: its Decode line is faster than in-process, inside the in-process
row's own spread (15.2 to 17.9 ms across the five runs), and it adds 0.15 ms
(8%) to the call that makes one Host call. Load does not differ across the
columns beyond its run-to-run spread (151 to 204 ms).

The isolating runtimes cost more than the pipe. Against the process row, podman
adds 4.35 ms (27%) to a Decode line and 0.38 ms (19%) to the Host-call call;
bwrap adds 3.64 ms (23%) and 0.26 ms (13%). The Host-call call makes two round
trips over the same stdio yet gains well under half a millisecond, so most of a
Decode line's added time is the guest running slower inside the sandbox, not
the transport. That holds for bwrap too, whose stdio passes straight through
like the process row's. The cause is not established here; the host's
speculative-store-bypass mitigation is prctl-only, so seccomp does not switch
it on. The sandbox's own clock around each exchange (`hop-ns/call`: 15.95,
20.26 and 19.56 ms for a Decode line in the process, podman and bwrap rows;
1.83, 2.18 and 2.07 ms for the Host-call call) includes the guest's run, so it
is not the pipe's cost alone; the difference between the columns is.

Start is paid once per run. The process host says hello in 2.37 ms, bwrap
(namespaces and the seccomp filter) in 20.73 ms, and podman in 206.4 ms; a full
podman start-and-stop cycle, the benchmark's ns/op, takes 584 ms. bwrap could
not write its cgroup `pids.max` and `memory.max` on this host (the user's
systemd scope does not delegate them), so its row ran with cgroup tracking but
without those two limits.

The macOS remote-attach hop (podman machine or Docker Desktop, where stdio
crosses into the runner VM) is unmeasured until the human macOS check (#7021).

A real dogfood run reports the same costs (`sandbox_start_ns`,
`sandbox_calls`, `sandbox_host_calls`, `sandbox_hop_ns`, and
`in_process_loads`) in the totals line that ends its primary record's audit
file (#7116). Since #6998 made github primary, dogfood writes no read shadow
`shadow_summary`, which carries the same keys for a shadow row. See [the
reference](../reference.md#backend-primary-mode).

## Amendment (issue #7074): the sandboxes do not slow Decode

The #7018 amendment's Decode gap (podman +27%, bwrap +23% over the process
row) does not reproduce. It came from the run, not the sandbox. `go test
-count=5` times each row as one block, in the fixed order in-process, process,
podman, bwrap, so a load change on the shared workstation lands on the later
rows. Re-measured on the same host with one row per `go test` invocation,
rows shuffled within each round, every row moves with the round: in one
pinned run (`taskset -c 10-13`), rounds 1–3 read 15.9 to 18.4 ms per line and
rounds 4–5 read 19.0 to 20.9 ms per line on every row, the process row
included. That 20% swing is the size of the gap #7018 reported.

Read against the process row from the same round, nothing in the sandbox
costs Decode anything outside that noise:

| row (pinned, 6 rounds) | Decode, median per line | vs process, median of per-round ratios |
|---|---|---|
| process sandbox | 17.58 ms | — |
| bwrap | 17.86 ms | −0.7% |
| bwrap without the seccomp filter | 16.23 ms | −1.3% |
| bwrap without namespaces (no `--unshare-*`) | 17.20 ms | +1.2% |
| podman | 17.33 ms | +5.5% |

The per-round ratios spread about ±10%. Podman's one positive median came from
a run pinned with `taskset`, which crun may not carry into the container, so
podman was re-run unpinned against the process row alone over 8 rounds:
16.66 ms against 16.39 ms per line (process spread 16.15 to 17.61 ms), a
per-round median of +1.3% (−4.6% to +7.2%). Both medians sit inside the process
row's spread, so neither the seccomp filter, the namespaces, the mount and
`/proc` view, nor the bwrap row's own cgroup costs Decode anything measurable.
Nothing is removed and nothing is kept for a cost. This supersedes the #7018
amendment's Decode row and its claim that the guest runs slower inside the
sandbox. Its Load, Start and Host-call rows were timed in the same blocks and
are not re-checked here. The benchmark header now gives the interleaved
recipe.

## Amendment (issue #6998): primary mode sits between shadow and cut-over

Decision 13's "each shadow cuts over after 20 consecutive clean dogfood runs"
becomes shadow, then primary, then cut-over. A shadow compares a sample and
leaves the native result authoritative, so it never shows what the Connector
does once the host depends on it: the reads a shadow skipped, the call volume of
a daemon that relies on the answers, a load failure that the native path
absorbs. Deleting the native path on shadow evidence alone would be the first
time anything depended on the Connector.

Primary mode is the row option `spindrift.connectors.<name>.primary`, off by
default and exclusive with `shadow`. The Connector answers every read the native
backend would, so cut-over (#6811) can delete the native read path; writes stay
native (amended by issue #7127: only on github, see the amendment below). There is no fallback to the native read. A failure that fell back would be invisible, so
a Connector that fails to load, or a request its Route table refuses, fails the
read with its own error and is recorded: a per-process
`primary-<backend>-<stamp>-<pid>.audit.jsonl` of Connector audit lines (plus a
sandbox totals line with no decision, #7116) and a `.log` of failed reads
(amended by issue #7127: jira's `.log` records every failed operation). The
stage is reversible by dropping `primary` from the row, with no code revert,
and dogfood runs github there first.

The bars become: a shadow moves to primary after 20 consecutive clean shadow
runs, and primary cuts over (#6811, which deletes the native github read path)
after 20 consecutive dogfood runs with zero Connector read errors and zero audit
refusals. The reference's Backend primary mode section has the recipe. A `driver`
row gets the same stage with #6999; until then eval refuses `primary` on one
(amended by issue #6999: it is accepted, see the amendment below).

## Amendment (issue #7097): a backend call's budget covers its own work, not GitHub's latency

The #7091 Filer dedup scan reads 500 closed issues with their bodies, about
3 MB over 5 pages. Decoding that alone needs more than the 5 s call budget
under the interpreter (4 to 11 s per MiB), so a correct Connector failed
closed on a correctly answered read. The budget had been charging the guest
for time it spent waiting on GitHub and had no allowance for the bytes the
host handed back.

The bases are unchanged (30 s `_initialize`, 5 s handshake, 5 s plus 60 s per
MiB of request for a method call), but the clock now measures guest work:

- It stops while the guest is blocked in a host `http_request`, in-process and
  under the Connector sandbox alike (the Launcher and the sandbox's own engine
  each pause their budget, so the two paths agree).
- Each `http_request`'s response bytes credit the budget at the same 60 s per
  MiB, up to 32 MiB of responses per call (`maxResponseCredit`, at most 32
  minutes).
- Each `http_request` has a 60 s network timeout (`httpRequestTimeout`)
  covering the send, a followed redirect hop and the body read. A timeout is a
  plain send failure: the guest gets the status-0 send-failure reply, the
  Connector stays usable, and the audit entry's reason names the timeout. A
  redirect hop gets half of what remains of that timeout, or of the caller's
  deadline if sooner.
- One call may make at most 128 `http_request`s (`maxHTTPRequests`); the 129th
  is refused and fails the call closed with `ErrHTTPRequestLimit`.

Each bound exists because of another. wazero has no instruction budget, so
wall-clock on guest time stays the only CPU bound, which is why the clock is
paused for the wait rather than the budget being raised. A paused clock alone
would let a guest buy unbounded wall-clock by issuing requests, hence the
request cap; the credit cap keeps the guest-time bound finite however much the
host delivers. A looping guest is still bounded: CPU time by the base, the
request allowance and at most 32 minutes of response credit, network wait by
128 times 60 s. The worst-case wall clock of one call is therefore 5 s plus 32
minutes of credit plus 128 minutes of network wait, about 2 h 40 min (8 minutes
more for a request at the 8 MiB cap). With this, the dedup scan completes in
about 7 s under the interpreter
(`TestConnector_DedupScanOf500IssuesFitsUnderTheInterpreter`).

## Amendment (issue #7108): Pass-log lines reach a Driver Connector as raw bytes

A native profile of the claude Connector (1 MB of real lines from issue-6998's
Pass log) put about 30% of Decode in the JSON envelope: `driver.decodeBatch`
carried each line as a JSON-escaped string, so the host escaped and the guest
unescaped every byte of the log before the decoder saw it. The decoder itself
is the other ~65%.

A Driver gains the optional capability `driver.decodeRaw`. Its request is not
JSON but the bytes `"driver.decodeRaw\n<decoder>\n"` followed by each line and
a terminating `\n`; the lines are never escaped. The answer is
`driver.decodeBatch`'s, one event list per line. The host prefers it for both
batch and per-line decoding when the Connector advertises it and falls back to
`driver.decodeBatch` / `driver.decode` otherwise, so an older Connector still
loads. A line that itself contains a newline is refused, since the framing could
not carry it. Every SDK Driver advertises it, and the conformance suite checks
each raw answer against the JSON fallback's.

Measured under the interpreter, one session on one host (AMD Ryzen 9 5950X, 32
threads, Go 1.26.8, linux/amd64), base f21d42bcb against this change, claude
wasm built from each tree. The two sides ran alternately, four rounds each, one
iteration per round (an interpreter iteration is 9 to 50 s); medians:

```sh
SPINDRIFT_DRIVER_CONNECTOR_WASM=/tmp/claude-<before|after>.wasm \
  go test -run '^$' -bench 'Connector(DecodeBatch|PostPass)/.*/interpreter' \
  -benchmem -benchtime 1x ./internal/driver/claude/
```

| benchmark | time before | time after | B/op before → after | allocs/op before → after |
|---|---|---|---|---|
| DecodeBatch, corpus 6798 | 12.01 s (15.6 ms/line) | 9.26 s (12.0 ms/line), −22.9% | 281 MB → 225 MB, −19.9% | 9.60 M → 7.25 M, −24.5% |
| DecodeBatch, corpus dogfood | 48.7 s (76.5 ms/line) | 39.2 s (61.6 ms/line), −19.5% | 1045 MB → 830 MB, −20.5% | 39.9 M → 31.4 M, −21.2% |
| PostPass, Decode | 46.3 s | 32.7 s, −29.4% | 960 MB → 695 MB, −27.6% | 36.6 M → 25.8 M, −29.4% |
| PostPass, DecodeAndReads | 43.6 s | 33.0 s, −24.4% | 934 MB → 681 MB, −27.1% | 36.7 M → 26.5 M, −27.9% |

The PostPass rows measure one raw request per line, not batches:
`driverkit.DecodeLog`, which the post-Pass readers use, decodes through the
Strategy's per-line decoder, so each line is its own `driver.decodeRaw` call.
They still gain what the DecodeBatch rows do, because the saving is the
escaping, not the batching.

The envelope's share is gone from the allocations (−21% to −29%, stable to
under 0.01% across rounds) and from the wall clock (−20% to −29%), close to the
~30% the profile attributed to it. The wall clock is noisy on this
host (rounds spread about ±10%, and the 6798 corpus's before and after
ranges touch: 11.3 to 12.4 s against 9.1 to 11.6 s), so the allocation counts
carry the claim and the timing agrees with them. What remains is the decoder,
which the single-pass scanner work targets. MB/s is not tabulated: the
benchmark prints it to two places, which reads 0.05 to 0.08 on every row.

## Amendment (issue #7107): Driver Connectors decode each line in one pass

The claude Connector's decoder ran `encoding/json` over each line about eight
to ten times (the top-level probe, the host-line check, turn classification,
the result totals, the content envelope and array, each block), and every
`Unmarshal` ran its own validation scan first. `sdk/driver`'s `Scan` now walks
a line once, validating as it goes, into a flat tape of values. It unescapes
only the strings a caller reads, and its `Decode*` helpers and `KeyMatches`
keep `encoding/json`'s store and field-matching rules, so every probe still
fails or succeeds exactly as its own `Unmarshal` did. Fuzz tests pin the
scanner against `encoding/json` and the Connector against the native Strategy
(`FuzzConnectorDecodeMatchesNative`). The interpreter stays (decision 14): this
cuts the work, not the engine's safety.

Before and after, both on one host in one session, one `go test -bench
'ConnectorDecodeSize|ConnectorPostPass'` run each against a Connector built
from `main` and from this change:

| line | interpreter before | interpreter after | compiler before | compiler after |
|---|---|---|---|---|
| 245 B | 7.94 ms | 4.29 ms | 1.15 ms | 0.59 ms |
| 1,005 B | 27.0 ms | 8.22 ms | 3.78 ms | 1.50 ms |
| 8,153 B | 146 ms | 67.1 ms | 22.4 ms | 9.50 ms |
| 65,297 B | 1.05 s | 475 ms | 179 ms | 79.6 ms |
| 261,040 B | 5.34 s | 1.83 s | 791 ms | 283 ms |
| 866,965 B | 16.8 s | 6.85 s | 2.51 s | 908 ms |

A linear fit over the six sizes puts the interpreter at 19.4 µs per byte
before and 7.9 µs after, and the compiler at 2.9 µs before and 1.0 µs after.
The baseline reads below the #6849 fit's 23.3 µs because the host differs; the
ratio within this run is what counts. `BenchmarkConnectorPostPass`'s `Decode`
row, the whole dogfood corpus through one decoder, goes from 43.2 s to 19.4 s
on the interpreter and from 5.85 s to 2.79 s on the compiler (native 81.5 ms
and 67.0 ms, unchanged code, so that spread is the run's noise).

The remaining per-byte cost in these runs was mostly outside the decoder: the
guest still decoded `driver.decode`'s params, the line as one JSON string, with
`encoding/json`, and encoded the events back. `driver.decodeRaw` (issue #7108,
above) has since removed the line's escaping; the events are still encoded.

A decoder panic no longer traps the guest. The SDK recovers it into a rejected
`driver.decode`, which the host already turns into the line's one unparsed
event; in `driver.decodeBatch` and `driver.decodeRaw` only that line's element
is rejected.

A line the scanner rejects is not a panic: it becomes a `KindError` event, as
the native Strategy and the goldens require. Only a decoder panic becomes the
line's one unparsed event (a rejected element in the batch answer).

## Amendment (issue #6999): the claude Driver Connector is primary on dogfood

A `driver` row with `primary` registers its Connector as the Driver ahead of
the native Strategy of the same name. The row reaches both sides: the launcher
input document's `drivers` (`driverrows.Use`) and the Box's rows file
(`driverrows.UseFile`), whichever Driver the Harness selects. It needs a
`module` and a core Driver of its name, and excludes `shadow`. The native
Strategy stays linked in and is still the default for every Consumer that sets
no `primary`. The Box still refuses a Connector that is not first-party.

As for backends, there is no fallback: a Connector that fails to load fails the
run with an error naming the Driver (`DRIVER "claude": ...`). The stage is
reversible by dropping `claude` from `nix/dogfood-primary.nix`, with no code
revert. A primary Driver emits no `shadow_summary`, so the shadow streak recipe
applies only while a Driver is shadowed.

Evidence for the cut-over (2026-11-16): 101 consecutive clean dogfood shadow
runs against a bar of 20, 211 Passes with a `shadow_summary`, every one loaded
with no error, 39,411 lines compared, 0 divergences. Those runs predate the
decode-cost fixes of #7107 and #7108; the Connector then cost about 48 ms per
line against about 0.09 ms for the native Strategy, so primary on dogfood is
also where the post-fix cost is first measured in production.

## Amendment (issue #7127): jira's whole tracker goes through its Connector under primary

What `primary` routes is per backend. A github row answers reads only, with
writes native until its write switch (#6756). A jira row builds its whole
tracker from the Connector in every launcher process that builds one (Pass,
recover, doctor, reconcile, the daemon's demand counting, quickstart): reads,
the claim, transitions under both lifecycles, labels, comments, host-posted
comments, filed findings and the labeled dedup scan. Lifecycle decisions stay in
core (`connectortracker`), as #6854 decided.

There is no fallback, as for github: a Connector that fails to load, lacks a
required capability, or has a route refused fails the operation with that
error. A dead Connector is reloaded by the next call, with github's backoff. The
records of #6998 hold for jira, in the shared `primaryrecord` package, except
that the `.log` also records failed writes, names each `op` by the guest RPC
method rather than the read's verb, and has no `reload the Connector` line (a
failed reload is recorded under the method that needed it); the sandbox totals
line stays github's. The native jira adapter is kept and still built and validated, so
dropping `primary` reverts with no code change; its deletion is #6858.

## Amendment (issue #6883): primary github carries the first write switch

The #6998 and #7127 amendments' "writes stay native" no longer holds for every
github write.
Under `primary`, github's claim, un-claim and reclaim, settle transitions,
research verdicts, butler label adds, label creation and host-posted issue
comments go through the Connector, still with no fallback: a load failure,
missing capability or refused route fails the write with its named error and is
recorded in the primary log, so the clean-run recipe covers writes too. A lost
claim race is an expected outcome and is not recorded. Issue filing, PRs, merges
and `CloseMergedIssue` stay native until their switches (#6885, #6886, #6887).
