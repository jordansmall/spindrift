# Console styling adopts the terminal palette, with an optional base16 override

The section-switched reskin (ADR 0030) needs color: state-tagged rows, section
tabs, borders, and an accent. Hardcoding a palette would fight every operator's
terminal theme and force a config surface to make it adjustable.

**Decision: the Console styles by semantic role against the 16 ANSI palette
slots, so it adopts whatever theme the terminal is set to; an optional base16
override can bake an exact palette for operators who want precise control.**
Colors are assigned to roles — running, held, settled, failed, accent,
dim/borders — each mapped to an ANSI slot rather than a hex value, so the
terminal owns the palette. Because Stylix themes the terminal's ANSI colors from
its base16 scheme, the Console inherits Stylix transitively with no
spindrift-side code. A single palette-resolver seam — "use ANSI slot N" by
default, "use base16 hex N" when an override is present — leaves room for an
explicit base16 module later without touching call sites. Glyphs are plain
Unicode, not nerd-fonts; lipgloss/termenv auto-degrades to 256/16/no-color and
honors `NO_COLOR`.

## Considered Options

- **A single hardcoded hex palette.** Rejected: it overrides the operator's
  terminal theme and needs a config surface to become adjustable.
- **A full theme-config surface now** (named, operator-selectable palettes).
  Rejected for the prototype: a schema, loader, validation, and settings-model
  coupling (ADR 0015, ADR 0020) for value the terminal palette already provides
  for free.
- **Nerd-font glyphs.** Rejected: a dependency on the operator's installed font
  — the same reason the banner is hardcoded rather than figlet-rendered.

## Consequences

- Non-stylix users get a themed Console for free from their terminal; stylix
  users get it transitively, and can opt into the base16 module for exact
  control.
- The base16 override module (its nix surface and settings wiring) is deferred;
  only the resolver seam ships now.
- No hardcoded colors live in `internal/console` — role-to-slot is the only
  color vocabulary the layout speaks.
