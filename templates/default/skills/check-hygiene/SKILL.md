---
name: check-hygiene
description: Run check and build gates so their result actually reaches the run — bounded log reads, foreground waits, and killed-build handling.
---
Every Bash command's output is already teed to a file and returned to you as a
bounded tail, so no manual redirect is needed — but never `cat` a whole
build/test log into context; grep or tail the log file on disk for anything the
bounded tail didn't cover.

Run every check or build gate in the foreground and block on it yourself —
never background it (`&`, detached job, background task) and end your turn
while it is still pending. Backgrounding a gate means your turn ends before the
gate finishes, no `SPINDRIFT_OUTCOME` line is ever printed, and the run is lost
even when the underlying work was green. Wait for the gate to finish before
moving on, and do not stop this run until a terminal `SPINDRIFT_OUTCOME` line
(`status=ready` or `status=blocked`) has been printed.

If you ever fall back to a background-and-poll pattern for a gate anyway, treat
a vanished process as a failure, not as still-pending: a build that is killed
outright (OOM, SIGKILL) never writes the exit marker you are polling for, so an
unbounded wait for it hangs forever. Bound the wait, and the moment the marker
fails to show up, emit a `status=blocked` `SPINDRIFT_OUTCOME` instead of
looping.
