Review how this Target repo's configuration is performing, arguing only
from the Tuning digest in the "# CHORE INPUT" section below: per-role cost
and outcome rows for the newest settled dispatches, set against the
trailing baseline. Every number you cite comes from the digest; never
recompute, re-derive, or estimate one, and never read other run data to
fill a gap.

The digest is this run's scope: the diff range and slice above are blank
for this Chore, so there is nothing to sweep. The Target repo checkout is
there only to locate the knob, model choice, or prompt override a
finding names.

Scope each finding to what the Target repo controls: its knobs, model
choices, and prompt overrides. When the cause lives upstream, in
spindrift itself rather than the Target repo's configuration, say so in
the finding body instead of proposing a repo-side change.

Every finding must cite the digest anchors it argues from and name the one
metric it concerns; `driver-exec signal` rejects a finding without both, and
the host drops one with no cites. Hand the filer
each anchor and the metric slug (the metric part of an anchor, such as
`usd-per-record`) with the finding. Give the finding's target file in its
dedup term, `-dedup <path>:<symbol>`.

The host drops a finding that cites an anchor not in the digest, targets a
file absent at the scanned HEAD, or cites only rows the digest marks thin,
unless its class is evidence-gap, so do not argue from a thin row otherwise.
It appends the cited rows to the issue itself as an Evidence section, so do
not paste the table into the body. Prefer filing nothing over a speculative
finding — a quiet window is a valid result.
