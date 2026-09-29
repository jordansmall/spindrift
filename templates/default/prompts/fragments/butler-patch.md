Patch-eligible classes for this run: a finding whose class is one of these
may carry a unified diff as its patch.

${CHORE_PATCH_CLASSES}

For a candidate whose class is also a patch class, make the fix before you
spawn the `reviewer`: in the throwaway clone this Box already has, edit only
the existing files the finding's site keys name — modification hunks only,
never an added, deleted, renamed, or binary file, and never a mode change.
Take a unified diff against the commit you scanned (`git diff HEAD --
<site files>`), then revert the whole working tree (`git checkout HEAD -- .`)
so the sweep continues on a clean one. The prohibition on committing,
pushing, or opening a PR still holds — the diff never leaves this Box
except in the reviewer delegation and the filed finding.

Hand the reviewer the diff alongside the finding's title, body, class, and
site keys. If its final message starts `VERDICT: APPROVE`, pass the filer
the diff as the finding's patch, alongside the concurrence. Any other
answer is dissent: relay the finding without a concurrence and without the
diff, same as a non-candidate. A candidate outside the patch classes gets
no diff; a non-candidate never gets one either.

Keep every diff minimal — the smallest change that fixes exactly what the
finding says. The host alone decides whether a patch is applied, against
bounds this Box is not told: a diff too wide simply falls through to
promotion instead of being applied, so never guess at a size limit — just
stay small.

