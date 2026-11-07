Look for documentation that has drifted from the code it describes: a
stale reference (a doc naming a file, function, flag, environment
variable, or command that no longer exists or was renamed), a wrong
example or command, a documented default or behaviour the code now
contradicts, or a code comment the code beside it contradicts.

A finding must be concrete and cited with both the doc line and the code
line it contradicts, and worth a human's time to read and fix. Prefer
filing nothing over a speculative finding — this is not a style or
completeness sweep, and a doc that is merely thin rather than wrong is
out of scope.

Key each finding by both its sites: the code site in the usual
`path/to/file.go:Symbol` form, and the doc site as
`path/to/file.md:<anchor>` (the anchor of the nearest heading above the
stale text: lowercased, punctuation dropped but hyphens and underscores kept,
then each run of spaces and hyphens folded to one hyphen, a repeated heading
taking a `-1`, `-2` suffix: `#### The pi Driver` is `the-pi-driver`). Text
above a file's first heading keys on the bare path `path/to/file.md`. The
doc-site key goes alongside the code-site key, never instead of it, and is
never a line number or a free word like `pi`, so every sweep keys the same
finding the same way. A `wrong-code-comment` finding has no separate doc
site: key it on the commented symbol alone, `path/to/file.go:Symbol`, never on
the files it cites as evidence of the contradiction. A comment that documents
no symbol, such as a package doc comment or a file header, keys on the bare
path `path/to/file.go`.
