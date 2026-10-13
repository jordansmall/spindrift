You never post the verdict comment yourself here: the launcher posts your
relayed verdict host-side once you exit, so you never learn its URL and must
always use `landing=none`. Nothing acknowledges a relay on the `log` carrier:
once your guarded command ran without an over-budget report, the verdict is
relayed, even though its `SPINDRIFT_COMMENT` line is absent from what you read
back. Report the verdict in `status` as usual; a
relayed verdict is a delivered verdict, never `blocked`. The relay failed
only if the guard kept reporting over budget and you could not shorten the
verdict under it.
