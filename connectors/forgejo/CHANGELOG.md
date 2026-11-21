# Changelog

## [0.2.0](https://github.com/jordansmall/spindrift/compare/forgejo-v0.1.1...forgejo-v0.2.0) (2026-11-21)


### ⚠ BREAKING CHANGES

* **forgejo:** primary on the forgejo backend row is refused at evaluation; drop it. A shadow-only forgejo row no longer builds a tracker.
* **nix:** a run whose CODE_FORGE is forgejo needs SPINDRIFT_FORGEJO_CONNECTOR_TOKEN beside FORGEJO_TOKEN, and MERGE_METHOD must be rebase or unset.
* **connector/forgejo:** a forgejo/<name>.md in a custom fragments directory (SPINDRIFT_PROMPT_DIR) no longer overrides the shipped step, because a Connector's declared file wins over a custom fragments directory.

### Features

* **connector/forgejo:** perform the tracker writes ([f380ada](https://github.com/jordansmall/spindrift/commit/f380ada85cb39ca7cc8d2e127925ff689deb0b77)), closes [#6865](https://github.com/jordansmall/spindrift/issues/6865)
* **connector/forgejo:** serve probe, count, labeled and search reads ([b62b075](https://github.com/jordansmall/spindrift/commit/b62b075bb3b9e7b12685601d7bfea7db7e926dbb)), closes [#7129](https://github.com/jordansmall/spindrift/issues/7129)
* **connector/forgejo:** serve the tracker reads ([319756f](https://github.com/jordansmall/spindrift/commit/319756f295f5a701b6ea5e109f5f203967c0b842)), closes [#6865](https://github.com/jordansmall/spindrift/issues/6865)
* **connector/forgejo:** ship forgejo's fragments from its Connector ([81bde31](https://github.com/jordansmall/spindrift/commit/81bde3135b51ee2f9a9fb428c07fce01350052c9)), closes [#6867](https://github.com/jordansmall/spindrift/issues/6867)
* **forgejo:** build the tracker only from the Connector row ([013c878](https://github.com/jordansmall/spindrift/commit/013c878f17f61733d9bfd20a3b98272a3ff16662)), closes [#7122](https://github.com/jordansmall/spindrift/issues/7122)
* **nix:** give every Consumer the forgejo Connector row ([3aa5e7d](https://github.com/jordansmall/spindrift/commit/3aa5e7d1342085be5b1642d2baf78ea3d24a2845)), closes [#6868](https://github.com/jordansmall/spindrift/issues/6868)
* **prompt:** drop the read-write forge fragments ([fd20baa](https://github.com/jordansmall/spindrift/commit/fd20baae6da4320a4d87071ae3b19a11c9722242)), closes [#6901](https://github.com/jordansmall/spindrift/issues/6901)
* **prompt:** drop the read-write tracker fragments ([255ab09](https://github.com/jordansmall/spindrift/commit/255ab09f6cf92d4192989e5bcddc481f1202c4e4)), closes [#6901](https://github.com/jordansmall/spindrift/issues/6901)


### Bug Fixes

* **forgejo:** read a failing job's log in the Connector ([cc82dfe](https://github.com/jordansmall/spindrift/commit/cc82dfe62e5108807ff4f836b3b32df494c9ed77)), closes [#6937](https://github.com/jordansmall/spindrift/issues/6937)
* **forgejo:** report a 405 or 409 merge refusal as not-mergeable ([db337d5](https://github.com/jordansmall/spindrift/commit/db337d5ea21e147df7378528e4c813563e1163c7)), closes [#6960](https://github.com/jordansmall/spindrift/issues/6960)


### Documentation

* describe forgejo's primary as covering its tracker ([b42b89f](https://github.com/jordansmall/spindrift/commit/b42b89fa40792fc165ef5c39bdaedf18e8f21609)), closes [#7129](https://github.com/jordansmall/spindrift/issues/7129)
* describe forgejo's tracker as Connector-only ([664f823](https://github.com/jordansmall/spindrift/commit/664f823411233ad91ebc41a7429b227abf4fe059)), closes [#7122](https://github.com/jordansmall/spindrift/issues/7122)

## [0.1.1](https://github.com/jordansmall/spindrift/compare/forgejo-v0.1.0...forgejo-v0.1.1) (2026-11-18)


### Features

* **connector/forgejo:** add the forgejo Connector's forge reads ([a6a1f9d](https://github.com/jordansmall/spindrift/commit/a6a1f9d42bfe9ddb3430c969a55ca0db168354b9)), closes [#6866](https://github.com/jordansmall/spindrift/issues/6866)
* **connector/forgejo:** add the forgejo Connector's PR primitives ([3012d1d](https://github.com/jordansmall/spindrift/commit/3012d1dd08b0a9169ea207fe271176b50823c4cf)), closes [#6866](https://github.com/jordansmall/spindrift/issues/6866)


### Documentation

* describe switching forgejo's Code Forge with primary ([eff3669](https://github.com/jordansmall/spindrift/commit/eff36692b7d3fe99588685c1039280b082a58cf1)), closes [#7128](https://github.com/jordansmall/spindrift/issues/7128)
* **reference:** describe the forgejo Connector's forge half ([583580e](https://github.com/jordansmall/spindrift/commit/583580e27a1205392502322028142fec78f0211f)), closes [#6866](https://github.com/jordansmall/spindrift/issues/6866)
