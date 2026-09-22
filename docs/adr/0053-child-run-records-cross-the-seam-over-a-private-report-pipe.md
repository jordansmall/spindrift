# Child run records cross the seam over a private report pipe

The daemon needs two facts about a running child: which issue it claimed,
and what became of that issue once the child settled it. It got the first
by regex-scanning the child's own stdout for the human announce line
(`    -> #123 (fix-pass-1): title`) and never got the second at all. Scraping
a prose line as a machine channel coupled every wording tweak in that line
to the daemon's parser, and needed a raised scanner cap plus a drain
fallback just to keep a wide or wedged line from stalling the scan. It also
had no way to carry a terminal outcome, since the line is printed once, at
Box start, before anything is decided. With the daemon's own terminal gone
(the seam this replaces predates it), "what happened to #123" survived
nowhere on disk once a run finished — the gap the issue's own follow-up
requirement named directly.

**Decision: the daemon opens a private pipe per child before starting it,
hands the write end down as the child's descriptor 3, and names that
descriptor in the child's environment as `SPINDRIFT_REPORT_FD`. The child
writes one JSON object per line to it: a `box` record (issue, phase) each
time it starts working an issue, and a `settled` record (issue, state,
note) once, when that issue reaches a terminal state.** The child validates
by `fstat` that the descriptor really does name a pipe and refuses — one
line to stderr, no crash — if it names anything else, and marks it
close-on-exec before it spawns anything, so no Box, runtime, or subprocess
it starts can ever inherit or hold the write end open. An unset variable
means no reporter and nothing written, so a launcher run by hand or driven
by `dogfood.sh` is unchanged.

## What crosses the pipe, and what doesn't

The daemon treats what arrives on the pipe as data, not as trusted control
input from a privileged peer: an unrecognised event is ignored, a
malformed line is reported once and skipped, and an issue number is
validated before anything acts on it. `MaxLine` is declared once, in the
child's own reporting package, and shared by both sides — the writer clips
a long note to stay under it, the reader sizes its buffer to it — so the
two bounds can never drift into a note the writer thinks fits and the
reader silently discards. The event and phase vocabulary the wire carries
(`report.EventBox`, `report.EventSettled`, the phase constants) is Go
constants imported by both the writer and the daemon's own event stream,
not a string literal duplicated on each side, so a typo in the wire value
is a compile error rather than a silent parse miss.

`SPINDRIFT_REPORT_FD` is daemon-to-child plumbing, not an operator knob: no
operator sets it, and it does not appear in `harness.env.example`. The
child's own stdout is unaffected by any of this — it is relayed to the
daemon's stderr byte-for-byte, with no userspace parsing standing between
the two.

## Consequences

The announce line goes back to being pure prose: the daemon no longer
reads it, so the regex, the raised scanner cap, and the drain fallback that
existed only to keep that scan from wedging the daemon are gone, along with
the parity test that pinned the line's format against the regex. The
contract between the two sides changes atomically rather than needing a
compatibility window, because the daemon and the child it spawns are built
from the same source tree — a child is never newer than the daemon that
started it. A `settled` record now gives an issue's terminal state and
settling note a place to live past the run that produced them, closing the
gap that motivated this change in the first place.
