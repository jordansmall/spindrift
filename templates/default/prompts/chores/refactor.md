Look for cheap, behaviour-preserving cleanups in the scoped code: dead code
(unused functions, unreachable branches, stale flags), duplication worth
consolidating, needless indirection or pass-through layers that add a hop
without adding meaning, and misleading names that no longer match what the
code does.

A finding must be concrete and cited with file:line, small enough to land
without changing behaviour, and worth a human's time to read and fix. This
is not a style or formatting sweep, and not a redesign — prefer filing
nothing over a finding that only reshuffles code without removing real
waste (dead code) or real duplication.
