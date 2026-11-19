# Dogfood-sized corpus

Synthetic Pass logs sized to dogfood shadow summaries (issue #6849): a 2-8 KB
mean line, with a tail of single `tool_result` lines from ~100 KB to ~800 KB.
They exist because a ~730 B/line corpus understates Connector read cost; only
`TestDogfoodCorpusShape` reads them now. The event shapes follow the `real-*`
cases; the bulky fields are padded with source-like filler. Nothing here is
real log content.

The cases sit outside `testdata/corpus` on purpose:
`TestDecodeMatchesReference` sweeps that directory, and these logs carry no
goldens.

The generator that wrote them went with the native Strategy (issue #6799); the
files are now plain fixtures, edited by hand if a different size is needed.

`TestDogfoodCorpusShape` pins the size criterion.
