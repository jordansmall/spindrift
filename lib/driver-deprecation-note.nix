# The one phrasing every generated doc uses for a deprecated single-Driver
# option (ADR 0064). Split from lib/driver-map.nix, which re-exports it,
# because lib/structural-options-doc.nix needs it and driver-map.nix imports
# that file. `movedTo` is the path the setting now lives at.
movedTo:
"**Deprecated** (ADR 0064): set `${movedTo}` instead; the top-level form still works and warns."
