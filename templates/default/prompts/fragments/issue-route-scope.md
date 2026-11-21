# TARGET REPO

This ticket was routed to `${REPO_SLUG}` (the issue route), and that repo
bounds the change this run may make: one ticket, one Target repo. If the spec
requires changes outside `${REPO_SLUG}`, whether that is clear from the spec
now or only once scouting shows it, halt before writing any diff. Work that
only reads another repo, such as a dependency's API, is not a cross-repo
change.

Stop as the ISSUE COHERENCE GATE above does, except that this stop may come
after scouting, with the same `status=ambiguous` outcome line, never
`status=blocked`; like that gate, this stop exits the run before the OUTCOME
section ever runs. Its `note=` names the
parts outside `${REPO_SLUG}` and asks a human to split the ticket into
per-repo routed tickets linked as blockers.
