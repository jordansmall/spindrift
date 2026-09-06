# Box-artifact freshness hot-swaps; only the launcher needs a restart

[ADR 0019](0019-dispatch-exits-at-the-wave-boundary.md) made the launcher
invocation the image-freshness boundary: a continuous run that finds itself
stale stops refilling, lets in-flight Boxes drain, and exits so the driving
loop can pull, rebuild, and re-invoke. That shape is right while the
artifact is an OCI image, because the rebuild is the expensive half — bake a
~1.1 GB layered image, load it, register a tag — and a drain that overlaps
nothing costs little next to it.

[ADR 0042](0042-the-bwrap-box-gets-in-box-nix-from-an-ephemeral-overlay-store.md)
changes those economics. A bwrap Box has no image: `build` realizes an agent
closure and takes a store-DB snapshot, and the launcher already does exactly
that with `nix build <agentFilesDrv>^* --no-link`. The rebuild half collapses
to seconds of mostly-cached realization. The drain half does not move at all —
it is still up to every slot but one sitting idle for a full agent run. So
under bwrap the drain stops being the cheap half and becomes the entire stall.

The drain was never about building anything. It is a process-restart boundary:
the launcher supervises live Boxes and holds its artifact references
(`AGENT_FILES`, `AGENT_FILES_DRV`) baked at wrap time. This ADR splits that
boundary, because the two things it currently conflates need different
answers.

The split is available because the stale signal is already Box-only by
construction. `driverExecBin`'s fileset deliberately excludes the dispatch and
refill orchestration, so a commit to the wave loop moves `launcherBin` but
leaves the probed artifact untouched — the probe does not see it at all
(issue #1364). The launcher refresh that a drain-exit delivers today is
incidental, a side effect of the driving loop re-running `nix run .#`, not
something the verdict detected.

The decision set:

- **The verdict names which half moved, and each half gets the boundary it
  actually needs.** A Box-artifact-only change hot-swaps in place: realize the
  tip closure, bind it for subsequent launches, keep refilling, never exit.
  A launcher change drains and exits as today, because a process cannot swap
  itself. When both moved, the launcher wins — a restart refreshes the Box
  artifact too, so there is no case where a swap is preferable to a restart
  that is already happening.

- **The Box artifact is the `agent-closure` linkFarm, and every knob that
  changes Box behavior at runtime has to live inside it.** `files`, `env`,
  and `nix-config` already bake the sandbox's filesystem, environment, and
  in-box nix config; `prefetch` is a fourth child alongside them, because it
  reaches the Box as `--setenv PREFETCH` exactly as an OCI image bakes the
  same string into its `Env`. A merge that only touches `prefetch` therefore
  moves the closure's own output path, so the bwrap probe's byte compare
  reports Box-artifact-stale rather than fresh, and the swap that follows
  rebinds `PREFETCH` from the swapped closure's own `prefetch` child — a Box
  launched after the swap never keeps running the snippet baked at launcher
  startup (issue #2954).

- **Hot-swap is bwrap-only; the OCI path keeps the drain-exit unchanged.**
  Swapping under an OCI runtime means baking and loading an image mid-wave,
  which is the mid-run refresh ADR 0019 rejected and still rejects. Under
  bwrap the swapped artifact is one bound store path
  (`--ro-bind $agentFiles/agent /agent`) with no load, no tag registration,
  and no image GC, and the launcher already both evaluates (the probe's
  hermetic `nix eval` at the fetched tip) and realizes. The swap composes two
  capabilities the launcher has rather than introducing a third, which is why
  ADR 0005/0007's "realize pre-evaluated derivations, do not evaluate flakes"
  line is not crossed here — continuous dispatch crossed it for evaluation
  when the probe landed.

- **The exit-code contract does not grow a runtime branch.** A swap is not a
  terminal condition and reports nothing; a launcher-stale verdict is still
  exit 4 under both runtimes. The driving loop is unchanged, which is the same
  property the bwrap freshness probe was specified to preserve.

- **Launcher currency is a revision-independent derivation, not
  `launcherBin`.** `launcherBin` bakes `-X main.revision=${revision}` from
  `self.shortRev`, so its output path moves on every commit including
  docs-only ones; comparing it is comparing the raw commit rev, and a
  continuous run would restart perpetually. The currency is a sibling
  derivation over the same source with the revision normalized. The real
  revision still reaches `--version`; it simply stops being a freshness input.

- **Store-DB snapshots are generation-scoped and outlive the swap that
  replaced them.** ADR 0042's snapshot is taken once by `build`, after the
  agent closure is realized, and each Box mounts it as an overlay lower for
  the whole life of the sandbox. A swap therefore adds a generation named for
  the closure it was taken against; it never replaces a file a running Box is
  reading. Generations are reclaimed once no live Box references them —
  see the Consequences section below for the divergence the shipped
  implementation accepted instead.

- **Non-convergence halts a swap loop exactly as it halts a rebuild loop.**
  The host-taint guard keys on the fetched rev and the tip tag, and a swap
  that never converges is the same pathology as a rebuild that never
  converges — a host-system derivation reaching the artifact graph. It gets
  the same halt, not a second mechanism.

- **The git pull stays in the driving loop.** A hot-swap does not pull and
  does not touch the working copy: the probe already evaluates hermetically at
  the fetched tip, never from `$PWD`. The checkout advances only on the loop's
  own iteration, exactly as before.

## Considered Options

- **Keep refilling from the stale artifact until the drain completes.** This
  is the only option that removes the idle slots without any new mechanism,
  and it is issue #477 verbatim: a Box launched after the boundary runs code
  its blocker's merge already changed. Wave boundaries exist to prevent
  precisely this, so the utilization is not available at that price.

- **Exit immediately and adopt the in-flight Boxes in the next invocation.**
  Foreclosed by ADR 0042's own decisions rather than by this one: a bwrap Box
  runs with `--die-with-parent` and is strictly ephemeral, so exiting while
  Boxes are live destroys them. Under an OCI runtime containers do survive a
  launcher exit, but settle runs in-process — adoption would mean re-attaching
  to a live Box's log stream and completing a host-decided outcome from a
  different process than the one that launched it. Ironically the runtime this
  ADR is about is the one where adoption is least possible.

- **Amend ADR 0019 in place.** Rejected. The boundary it establishes remains
  correct for the runtime it was written about, and remains correct for the
  launcher under every runtime; nothing in it becomes false. What changed is
  that a second artifact now exists whose refresh does not require a process
  restart. That is a new decision on top, not an erosion of the old one, and
  recording it as one keeps 0019 readable as what it was.

- **Overlap the rebuild with the drain instead.** Realizing the tip artifact
  while the drain runs shortens the restart, and is worth doing on its own —
  but it shortens the half that bwrap already made cheap and leaves the half
  that dominates. It is independent of this decision, not an alternative to
  it.

## Consequences

Two freshness mechanisms now exist where ADR 0019 deliberately kept one, which
is the cost it named. It is accepted on the bwrap path only, and only against
a measured drain cost from a real dogfood queue rather than an assumed one —
if the idle slot-time turns out to be small, the swap is not worth its
complexity and the drain-exit is the better resting point for both runtimes.

A hot-swapping launcher can run for a very long time without restarting, so
launcher-half staleness stops being a latency problem and becomes a
correctness one: under-detecting it now means a long-lived process orchestrating
fresh Boxes with stale code, with no incidental restart to rescue it. The
launcher dimension of the probe is a prerequisite for the swap, not a
companion improvement to it.

A bwrap Box's snapshot generation pins the store paths it was taken against,
so several generations can be live at once and the host holds their disk
until something reclaims it. In principle that bound is the number of
concurrent Boxes referencing a generation; in the implementation issue #2682
shipped, it isn't, because reclaim only ever ran at `launcher build` time
(ADR 0042) and no mechanism tracks which generations a live-but-idle
`Dispatch` still references across a `Run`-then-`Fix` gap (a Box can finish
and sit idle for minutes awaiting CI before launching another Box against
the same generation). Reclaiming on the swap itself would risk deleting a
generation still needed there, so `SnapshotGeneration` never calls
`reclaimStaleSnapshots` at all: a hot-swapping launcher simply accumulates
one generation per swap, unreclaimed for the rest of the process's life, and
the actual bound is swap count over process lifetime, not concurrent Box
count. `launcher build` remains the only reclaim point, so a launcher that
hot-swaps for hours without restarting holds every generation it ever swapped
to. This is accepted for the same reason the two-mechanism cost above is
accepted — pending a real reference-counted reclaim design, which is out of
scope for #2682 — and it interacts with ADR 0042's accepted
garbage-collection race rather than worsening it: a generation, live-Box or
not, is exactly the thing a host `nix-store --gc` could already invalidate
underneath.

Reproducibility is unchanged. Each Box still runs from one realized closure
decided before it launched; the swap changes which closure the *next* Box
gets, never one a running Box is already using.
