# Changelog

## [0.3.1](https://github.com/jordansmall/spindrift/compare/claude-v0.3.0...claude-v0.3.1) (2026-11-23)


### Documentation

* **go:** trim and correct comments in Go sources ([b5ea3c7](https://github.com/jordansmall/spindrift/commit/b5ea3c7b4ded1ce6d19cd14b0d02c1749b1530ab))

## [0.3.0](https://github.com/jordansmall/spindrift/compare/claude-v0.2.0...claude-v0.3.0) (2026-11-21)


### ⚠ BREAKING CHANGES

* **nix:** spindrift.connectors.claude.primary and .shadow are refused; the claude Connector is always the claude Driver.
* **driver:** claude can no longer be shadowed or set primary, and a claude Connector that fails to load fails the run.

### Features

* **driver:** delete the native claude Strategy ([db8a3e7](https://github.com/jordansmall/spindrift/commit/db8a3e7d09f0830cd559332a90b75360843cd53d)), closes [#6799](https://github.com/jordansmall/spindrift/issues/6799)
* **nix:** render claude's Connector row for every Consumer ([fd73c22](https://github.com/jordansmall/spindrift/commit/fd73c227ed93d80b1c279e65d0178995fb343b08)), closes [#6799](https://github.com/jordansmall/spindrift/issues/6799)

## [0.2.0](https://github.com/jordansmall/spindrift/compare/claude-v0.1.0...claude-v0.2.0) (2026-11-18)


### ⚠ BREAKING CHANGES

* **sdk:** Strategy.NewDecoder in spindrift.dev/sdk/driver is now func(now time.Time) Decoder instead of func() Decoder. A Connector built on the SDK takes the extra argument (or ignores it). The wire protocol stays at 0.3.0, which has not shipped in a release.

### Features

* **connectors:** carry claude.wasm on the claude Connector's row ([67186ef](https://github.com/jordansmall/spindrift/commit/67186efbe4dd7f8543bc1b90e2f2a56dedf78f04)), closes [#6797](https://github.com/jordansmall/spindrift/issues/6797)
* **connectors:** contribute claude's Driver row from its Connector ([48d67b0](https://github.com/jordansmall/spindrift/commit/48d67b02ed807cde98a1da71580e7bdebf19e090)), closes [#6796](https://github.com/jordansmall/spindrift/issues/6796)
* **connectors:** make claude a Connector subflake ([e670ee7](https://github.com/jordansmall/spindrift/commit/e670ee7344d5afc10fa555f4c13dff36f68b74d0)), closes [#6795](https://github.com/jordansmall/spindrift/issues/6795)
* **connectors:** port the claude Driver's host half to the SDK ([1b33e06](https://github.com/jordansmall/spindrift/commit/1b33e063871517c20a62c15bedb16495b31ee4ce)), closes [#6795](https://github.com/jordansmall/spindrift/issues/6795)
* **connectors:** run the conformance suite from each subflake ([7de4ddc](https://github.com/jordansmall/spindrift/commit/7de4ddc2170f9e9d3129d2580fdf917fa1196052)), closes [#7009](https://github.com/jordansmall/spindrift/issues/7009)


### Bug Fixes

* **sdk:** hand a decoder the host's wall clock ([cc61ffd](https://github.com/jordansmall/spindrift/commit/cc61ffd59a680943c608bd267f686fb7be090a29)), closes [#6825](https://github.com/jordansmall/spindrift/issues/6825)


### Performance Improvements

* **connector/claude:** decode each Pass-log line in one scan ([0122a30](https://github.com/jordansmall/spindrift/commit/0122a304b8974ec2bf1bfd054853a895b5d73cec)), closes [#7107](https://github.com/jordansmall/spindrift/issues/7107)


### Documentation

* describe Driver primary mode and dogfood's claude cut-over ([9812e1f](https://github.com/jordansmall/spindrift/commit/9812e1fa7506911e22912e1ac542c88a29d4c01e)), closes [#6999](https://github.com/jordansmall/spindrift/issues/6999)


### Tests

* **connector/claude:** pin the decoder against a frozen reference ([ad0cf57](https://github.com/jordansmall/spindrift/commit/ad0cf57bd3e422b529e9b06fac58927fba4c0bb6)), closes [#7107](https://github.com/jordansmall/spindrift/issues/7107)
* **connectors:** require a Connector's component to be its name ([a86891b](https://github.com/jordansmall/spindrift/commit/a86891b5c31e4b7934fe33dec0c24a14bf4072b2)), closes [#6795](https://github.com/jordansmall/spindrift/issues/6795)
