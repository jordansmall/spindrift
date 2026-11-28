# Changelog

## [0.4.0](https://github.com/jordansmall/spindrift/compare/forgejo-v0.3.0...forgejo-v0.4.0) (2026-11-28)


### ⚠ BREAKING CHANGES

* **connectors:** Connector flakes no longer export flakeModules.default and core.lib.connectorModule is gone; register a Connector by adding its connectorData output to spindrift.connectorData.
* **connector/forgejo:** a forgejo listing of more than 100 pages now fails with a permanent error naming the bound, instead of paging on until the host closes the Connector.
* **protocol:** core refuses a forge or Driver Connector built against protocol 0.3; rebuild it against the 0.4 SDK.
* **forge:** sdk/forge's Probe moved from PRs to Branches and is required there. A forge Connector without forge.probe no longer composes.
* **protocol:** forge.remote is no longer part of the protocol and sdk/forge's Branches has no Remote field. An out-of-tree forge Connector must drop it.
* **forgejo:** SPINDRIFT_FORGEJO_CONNECTOR_TOKEN and SPINDRIFT_FORGEJO_CONNECTOR_TOKEN_CMD are removed. Set FORGEJO_TOKEN (or FORGEJO_TOKEN_CMD), which now authorizes forgejo Connector requests and git.

### Features

* **connectors:** declare the headers github and forgejo send and read ([d1ef6e6](https://github.com/jordansmall/spindrift/commit/d1ef6e637e7f2c3109a2f1c8c9a23d75006ca3b7)), closes [#7565](https://github.com/jordansmall/spindrift/issues/7565)
* **connectors:** register the forgejo Connector as data ([1ecdee7](https://github.com/jordansmall/spindrift/commit/1ecdee7a0575f1db095bf62ac13781b1f77326cb)), closes [#7558](https://github.com/jordansmall/spindrift/issues/7558)
* **forgejo:** authorize the forgejo Connector with FORGEJO_TOKEN ([a47ee04](https://github.com/jordansmall/spindrift/commit/a47ee04d344317efc4e8d536acdf9ad3b3aa2e47)), closes [#7500](https://github.com/jordansmall/spindrift/issues/7500)
* **forge:** probe a forge's liveness through forge.probe ([1c588aa](https://github.com/jordansmall/spindrift/commit/1c588aa53cbed60d6a2cb3e9d371c95bdda115ce)), closes [#7510](https://github.com/jordansmall/spindrift/issues/7510)
* **nix:** declare each forge Connector's Git remote on its row ([1ac9d03](https://github.com/jordansmall/spindrift/commit/1ac9d03953f885f10f497c545a9ebe9f11ae52b8)), closes [#7508](https://github.com/jordansmall/spindrift/issues/7508)
* **protocol:** remove forge.remote from the Connector protocol ([5f48d7a](https://github.com/jordansmall/spindrift/commit/5f48d7a9d72f646410fde99d56fc343c97868bb0)), closes [#7510](https://github.com/jordansmall/spindrift/issues/7510)
* **protocol:** speak Connector protocol 0.4.0 ([217fa22](https://github.com/jordansmall/spindrift/commit/217fa22ba34d238b87a1a79e491f42960c0478d7)), closes [#7510](https://github.com/jordansmall/spindrift/issues/7510)


### Bug Fixes

* **connector/forgejo:** class a failed send transient, via SDK helpers ([3df3b72](https://github.com/jordansmall/spindrift/commit/3df3b722907f03ce500067cb590787e9de052c8a)), closes [#7570](https://github.com/jordansmall/spindrift/issues/7570)
* **connector/forgejo:** fail a listing past 100 pages, not page on ([30065aa](https://github.com/jordansmall/spindrift/commit/30065aad062dc9b22f1e254b98dec81d16aaa206)), closes [#7570](https://github.com/jordansmall/spindrift/issues/7570)
* **connector/forgejo:** re-check every issue before trusting the count ([5d802c3](https://github.com/jordansmall/spindrift/commit/5d802c3ae51ef1bec48014d0ec6f684f83c57976)), closes [#7409](https://github.com/jordansmall/spindrift/issues/7409) [#7570](https://github.com/jordansmall/spindrift/issues/7570)
* **connectors/forgejo:** refuse a pull request in tracker issue() ([e964a9f](https://github.com/jordansmall/spindrift/commit/e964a9fad9ac17ef39d14a43fe933a3e80e2c718)), closes [#7358](https://github.com/jordansmall/spindrift/issues/7358)


### Security

* **connectors:** narrow the job-log Routes to a job's log ([62821a4](https://github.com/jordansmall/spindrift/commit/62821a458aa9918493da52eaf641312a805f7995)), closes [#6926](https://github.com/jordansmall/spindrift/issues/6926)


### Code Refactoring

* **connector/forgejo:** drop the send seam, test on the fake host ([50bfe67](https://github.com/jordansmall/spindrift/commit/50bfe674ee9658f64c1a3c44c205ffce03cbac24)), closes [#7570](https://github.com/jordansmall/spindrift/issues/7570)
* **connectorforge:** probe liveness without forge.remote ([84db84d](https://github.com/jordansmall/spindrift/commit/84db84ddbff486dc1cec63d691eb95ee41f3915b)), closes [#7509](https://github.com/jordansmall/spindrift/issues/7509)
* **connectors:** drop the flake-module registration path ([cc88d2e](https://github.com/jordansmall/spindrift/commit/cc88d2e506394742c94394806726e4ea351843ff)), closes [#7562](https://github.com/jordansmall/spindrift/issues/7562)


### Tests

* **forgejo:** cover a self-hosted instance under a subpath ([134dfd9](https://github.com/jordansmall/spindrift/commit/134dfd9a8ae5d4faf624e627be143f1844c8969f)), closes [#7509](https://github.com/jordansmall/spindrift/issues/7509)
* **forgejo:** run the hostile corpus and the capability check ([bfcf036](https://github.com/jordansmall/spindrift/commit/bfcf0363f8dc56a15e6e50b38ea6100d73d6edfa)), closes [#7574](https://github.com/jordansmall/spindrift/issues/7574)

## [0.3.0](https://github.com/jordansmall/spindrift/compare/forgejo-v0.2.0...forgejo-v0.3.0) (2026-11-23)


### ⚠ BREAKING CHANGES

* **sdk:** Reader.LabeledPage takes a route []Selector argument after label.
* **forgejo:** primary on the forgejo backend row is refused at evaluation; drop it. A shadow-only forgejo row no longer builds a tracker.
* **nix:** a run whose CODE_FORGE is forgejo needs SPINDRIFT_FORGEJO_CONNECTOR_TOKEN beside FORGEJO_TOKEN, and MERGE_METHOD must be rebase or unset.
* **connector/forgejo:** a forgejo/<name>.md in a custom fragments directory (SPINDRIFT_PROMPT_DIR) no longer overrides the shipped step, because a Connector's declared file wins over a custom fragments directory.

### Features

* **connector/forgejo:** add the forgejo Connector's forge reads ([a6a1f9d](https://github.com/jordansmall/spindrift/commit/a6a1f9d42bfe9ddb3430c969a55ca0db168354b9)), closes [#6866](https://github.com/jordansmall/spindrift/issues/6866)
* **connector/forgejo:** add the forgejo Connector's PR primitives ([3012d1d](https://github.com/jordansmall/spindrift/commit/3012d1dd08b0a9169ea207fe271176b50823c4cf)), closes [#6866](https://github.com/jordansmall/spindrift/issues/6866)
* **connector/forgejo:** perform the tracker writes ([f380ada](https://github.com/jordansmall/spindrift/commit/f380ada85cb39ca7cc8d2e127925ff689deb0b77)), closes [#6865](https://github.com/jordansmall/spindrift/issues/6865)
* **connector/forgejo:** serve probe, count, labeled and search reads ([b62b075](https://github.com/jordansmall/spindrift/commit/b62b075bb3b9e7b12685601d7bfea7db7e926dbb)), closes [#7129](https://github.com/jordansmall/spindrift/issues/7129)
* **connector/forgejo:** serve the tracker reads ([319756f](https://github.com/jordansmall/spindrift/commit/319756f295f5a701b6ea5e109f5f203967c0b842)), closes [#6865](https://github.com/jordansmall/spindrift/issues/6865)
* **connector/forgejo:** ship forgejo's fragments from its Connector ([81bde31](https://github.com/jordansmall/spindrift/commit/81bde3135b51ee2f9a9fb428c07fce01350052c9)), closes [#6867](https://github.com/jordansmall/spindrift/issues/6867)
* **forgejo:** build the tracker only from the Connector row ([013c878](https://github.com/jordansmall/spindrift/commit/013c878f17f61733d9bfd20a3b98272a3ff16662)), closes [#7122](https://github.com/jordansmall/spindrift/issues/7122)
* **forgejo:** join the Issue route to the native labels filter ([2c0f63c](https://github.com/jordansmall/spindrift/commit/2c0f63ce4fa4d615d7f4615bcdb896723f05fa9d)), closes [#7334](https://github.com/jordansmall/spindrift/issues/7334)
* **nix:** give every Consumer the forgejo Connector row ([3aa5e7d](https://github.com/jordansmall/spindrift/commit/3aa5e7d1342085be5b1642d2baf78ea3d24a2845)), closes [#6868](https://github.com/jordansmall/spindrift/issues/6868)
* **prompt:** drop the read-write forge fragments ([fd20baa](https://github.com/jordansmall/spindrift/commit/fd20baae6da4320a4d87071ae3b19a11c9722242)), closes [#6901](https://github.com/jordansmall/spindrift/issues/6901)
* **prompt:** drop the read-write tracker fragments ([255ab09](https://github.com/jordansmall/spindrift/commit/255ab09f6cf92d4192989e5bcddc481f1202c4e4)), closes [#6901](https://github.com/jordansmall/spindrift/issues/6901)
* **sdk:** add the tracker.matchesRoute per-issue route read ([cc44f85](https://github.com/jordansmall/spindrift/commit/cc44f858138cf314a9358a3884328f8fb81b42cf)), closes [#7335](https://github.com/jordansmall/spindrift/issues/7335)
* **sdk:** carry the Issue route on filing and the labeled read ([68d146d](https://github.com/jordansmall/spindrift/commit/68d146d56d8b4610f489376f7d43e0ec7f366520)), closes [#7333](https://github.com/jordansmall/spindrift/issues/7333)


### Bug Fixes

* **forgejo:** read a failing job's log in the Connector ([cc82dfe](https://github.com/jordansmall/spindrift/commit/cc82dfe62e5108807ff4f836b3b32df494c9ed77)), closes [#6937](https://github.com/jordansmall/spindrift/issues/6937)
* **forgejo:** report a 405 or 409 merge refusal as not-mergeable ([db337d5](https://github.com/jordansmall/spindrift/commit/db337d5ea21e147df7378528e4c813563e1163c7)), closes [#6960](https://github.com/jordansmall/spindrift/issues/6960)


### Documentation

* describe forgejo's primary as covering its tracker ([b42b89f](https://github.com/jordansmall/spindrift/commit/b42b89fa40792fc165ef5c39bdaedf18e8f21609)), closes [#7129](https://github.com/jordansmall/spindrift/issues/7129)
* describe forgejo's tracker as Connector-only ([664f823](https://github.com/jordansmall/spindrift/commit/664f823411233ad91ebc41a7429b227abf4fe059)), closes [#7122](https://github.com/jordansmall/spindrift/issues/7122)
* describe switching forgejo's Code Forge with primary ([eff3669](https://github.com/jordansmall/spindrift/commit/eff36692b7d3fe99588685c1039280b082a58cf1)), closes [#7128](https://github.com/jordansmall/spindrift/issues/7128)
* **go:** trim and correct comments in Go sources ([b5ea3c7](https://github.com/jordansmall/spindrift/commit/b5ea3c7b4ded1ce6d19cd14b0d02c1749b1530ab))
* **nix:** trim and correct comments in Nix sources ([862042b](https://github.com/jordansmall/spindrift/commit/862042b35483dfacd356051cdcf0caacba385a9c))


### Tests

* **go:** trim and correct comments in Go tests ([61fbf0f](https://github.com/jordansmall/spindrift/commit/61fbf0fd01dc860ac09ca9b5696b3e9fb6c22a51))


### Miscellaneous Chores

* release main ([34a1ece](https://github.com/jordansmall/spindrift/commit/34a1ece4c831acfa8484b736c60306fa6720bfe6))
* release main ([5472cb0](https://github.com/jordansmall/spindrift/commit/5472cb0ef4edc50f8b867793d1854b87bd8aa6d7))

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
