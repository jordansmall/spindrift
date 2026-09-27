Look for documentation that has drifted from the code it describes: a
stale reference (a doc naming a file, function, flag, environment
variable, or command that no longer exists or was renamed), a wrong
example or command, or a documented default or behaviour the code now
contradicts.

A finding must be concrete and cited with both the doc line and the code
line it contradicts, and worth a human's time to read and fix. Prefer
filing nothing over a speculative finding — this is not a style or
completeness sweep, and a doc that is merely thin rather than wrong is
out of scope.
