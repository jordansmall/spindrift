# Dogfood-sized corpus

Synthetic Pass logs sized to dogfood shadow summaries (issue #6849): a 2-8 KB
mean line, with a tail of single `tool_result` lines from ~100 KB to ~800 KB.
They exist for the Connector cost benchmarks, which a ~730 B/line corpus
understates. The event shapes follow the `real-*` cases; the bulky fields are
padded with source-like filler. Nothing here is real log content.

The cases sit outside `testdata/corpus` on purpose: `TestCorpus` and the
benchmarks sweep that directory, and these logs carry no goldens.

Regenerate (the generator is `dogfood_corpus_gen_test.go`; output is
byte-stable, so `TestDogfoodCorpusRegenerates` fails on drift):

```sh
nix run .#regen-goldens
```

`TestDogfoodCorpusShape` pins the size criterion and `TestDogfoodCorpusDecodes`
that every line stays valid claude stream-json.
