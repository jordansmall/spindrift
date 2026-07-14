# Per-package Config subsets stay; no shared settings module

The 2026-07-13 architecture review flagged the launcher's config plumbing as
a deepening candidate: `main`'s config struct fans out through five pure
field-for-field mappers (`runnerConfig`, `dispatchConfig`, `settleConfig`,
`wavesConfig`, `selectiveWavesConfig`) into per-package `Config` subset
structs, so adding a knob is a multi-site edit through mappers that hold no
behaviour. The proposed fix was a shared settings module every leaf package
reads directly.

Rejected. The subset structs are each package's *interface*, not plumbing:
`settle.Config` is the complete, compiler-checked list of knobs settle reads,
visible at a glance and constructed exactly by its tests. A shared settings
module would widen every leaf package's interface to the whole knob surface —
which fields a package actually consumes would become unknowable without
reading its code, and every test would construct (or fake) the full surface.
The mappers are the visible price of that explicitness, and they are cheap:
pure copies with no failure modes. The launcher-input work (ADR 0020)
independently removes the worst of the per-knob tax at the `loadConfig` end
(generated defaults, generated document struct), leaving the mapper hop as
the only remaining site — a two-line, compile-checked edit.

A leaf-owned constructor variant (`settle.ConfigFrom(doc)`) was also
considered: it moves the mappers rather than deleting them, and reverses the
dependency arrow so every leaf imports the generated document type. Rejected
for the same reason — the subset's independence *is* the value.

Re-open trigger: a mapper that accrues logic (validation, derivation,
conditionals — no longer a pure copy), or a knob consumed by four or more
packages so one addition repeatedly fans out across every mapper. Until
then, architecture reviews should not re-suggest a shared settings module.
