# Runners are family rows with a Strategy and a contract kit

Status: proposed

## Context

The runner seam has two real adapter families, OCI (podman, docker and
rancher) and bwrap, plus a build-time bwrap variant and a `Fake`. It has
none of the kit ADR 0066 gave the Drivers and ADR 0068 gives the backends.
Each family's facts are decided by its name outside the runner package:
about 34 `runnerKind == bwrap` checks across seven files, and 58 runtime-name
literals in Nix. Those checks fall into five groups:

- picking the constructor;
- which network modes a family can render, a matrix stated twice: a Nix
  assertion and a Go launch gate;
- host prerequisites: pasta, the overlay mount, and the podman machine;
- what a Box boots from, which freshness, host-taint diagnosis and hot-swap
  all branch on;
- the run-artifact document keys in `preambles.nix`.

A third family gets OCI semantics wherever it is not named, and nothing
fails to compile.

There is no shared test. `oci_test.go` (115 tests) and the bwrap suites (169
tests) cover different behaviour: `RegistryProxyTransport` has 27 tests on
one side and 1 on the other, and `ListRunning` has 0 and 5. Since August, 15
commits have implemented one Box feature by hand in both adapters, among
them network-mode rendering, the kill-signal exit code, add-host probing and
the skills override.

`RUNTIME` is the operator's choice. `RUNNER_KIND` is derived from it at eval
time and fixes what the build produces.

## Decision

Runners get the backend kit of ADR 0068, keyed by family.

1. **One row per family.** `lib/runners/oci.nix` and `lib/runners/bwrap.nix`
   each list the `runtimes` they serve, replacing `runtime-values.nix` and
   `mkHarness`'s `runnerKind` collapse. Docker, podman and rancher
   differences stay inside the OCI adapter as its own dialect table. Core
   never compares a runtime value.
2. **A Runner Strategy per family package.** `internal/runner` splits into
   `runner/oci` and `runner/bwrap`, each exporting its Strategy to the Bundle.
   This split lands first. `internal/runner` keeps the `Runner` interface,
   `Box`, the shared plan, the `Fake` and the kit. A Strategy may carry an
   optional hot-swap preparation hook: before a hot-swap binds a realized
   closure for a nixInBox Consumer, core calls the active family's hook
   (bwrap writes its store-DB snapshot generation). A nil hook means the
   family needs no preparation.
3. **The Box artifact is its own seam.** It has two adapters, `oci-image` and
   `store-closure`, owned by freshness. Each supplies how a tip build is
   identified and compared, the wording, the host-taint diagnostic, and
   whether a box-only rebuild can be swapped in. A runner row names the
   artifact it boots. Freshness and preview never read the runner family;
   hot-swap reads it only through the Strategy's preparation hook
   (decision 2).
4. **Runner prerequisites on the Strategy.** Each family declares named
   checks, marked as launch gates (which doctor reports too) or doctor-only.
   The launch gates and doctor walk the active family's list. Network-mode
   support is a list on the row, read by both the Nix assertion and the Go
   gate. `PODMAN_NETWORK` and `BWRAP_UNSHARE_NET` become row-owned knobs
   (ADR 0068).
5. **A contract kit, `runnertest`.**
   - A lifecycle contract that oci, bwrap and `Fake` all pass. It covers
     `Run`, `Kill`, `IsRunning`, `ListRunning`, `ErrAlreadyRunning`, the
     kill-signal exit code and `RemoveHostTree`.
   - A rendering contract. A table of `Box` specs runs through each adapter
     against a recording fake CLI, and a test-only decoder per adapter
     parses the argv back. A `Box` field one adapter does not render fails
     that adapter.
   - Real-runtime integration tests stay per adapter and opt in.
6. **Decide once, render twice.** The Box decisions both adapters now make
   for themselves move into the shared plan beside `buildMountSpecs`: the
   network mode, the add-host list and the skills override. Adapters only
   render the plan.
7. **The Nix half reads the rows.** Each row carries a `runArtifacts`
   function returning its run-document keys. The Nix checks map over
   `lib/runners/*` instead of `[ "bwrap" "oci" ]`, and `adapterGoTests`
   (ADR 0068) yields one Go test derivation per family.

## Considered Options

- **One row per runtime value.** Rejected. It repeats the family's facts
  three times and invites core to compare `runtime == "docker"`.
- **Artifact facts as runner Descriptor booleans** (`hotSwap`,
  `artifactNoun`, and so on). Rejected. They scatter one concept across
  flags, and the comparison still has to branch somewhere.
- **A lifecycle contract only.** Rejected. The drift that actually happened
  was in rendering.

## Consequences

- A new family is a row, a package with its Strategy, its decoder for the
  rendering contract, its prerequisites, and, if a hot-swapped closure needs
  preparing, the Strategy's preparation hook. A remote runner that boots OCI
  images reuses the `oci-image` artifact unchanged.
- Adding a field to `Box` fails every adapter that does not yet render it,
  which turns a feature built twice by hand into a compile-and-test
  checklist.
- Decision 1's "core never compares a runtime value" holds for core non-test
  code, and a guard enforces it (issue #5803): `TestRunnerFamilyGuard`
  (`cmd/launcher/runner_family_guard_test.go`) fails on a runtime or family
  name compared, a family constant referenced, or a family package imported.
  It exempts the family packages (they own the runtime spellings), the Bundle
  (it links every family in by name), generated code (`families_gen.go`
  renders the rows; the guard walks it but `ast.IsGenerated` quiets it by its
  header), `internal/runner/runnertest/**` (the contract kit scopes a row to
  a family on purpose), and `_test.go` files (a test asserts
  per-family outcomes and sets fixtures rather than branching production
  behavior, as the backend, kind-axes and tuning guards also skip them).
- Like ADR 0068, this ships incrementally, and each change leaves `main`
  releasable.
