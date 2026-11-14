# Changelog

## [0.2.0](https://github.com/jordansmall/spindrift/compare/github-v0.1.0...github-v0.2.0) (2026-11-14)


### ⚠ BREAKING CHANGES

* **connectorhost:** core refuses a Connector declaring protocol 0.1.x.
* **connectorhost:** core refuses a Connector declaring protocol 0.0.x.

### Features

* **connectorhost:** speak Connector protocol 0.2 ([ee97dce](https://github.com/jordansmall/spindrift/commit/ee97dcec1993bebf375af531921eac2d44a10766)), closes [#6853](https://github.com/jordansmall/spindrift/issues/6853)
* **connectorhost:** speak Connector protocol 0.3 ([28493d2](https://github.com/jordansmall/spindrift/commit/28493d2ed2f0515019fcedf779a722c5cbae526c)), closes [#6862](https://github.com/jordansmall/spindrift/issues/6862)
* **connectors/github:** declare the tracker write Routes ([85f2f5f](https://github.com/jordansmall/spindrift/commit/85f2f5f7ee34f7e5b8beb4ceea0f9aa145cfef15)), closes [#6881](https://github.com/jordansmall/spindrift/issues/6881)
* **connectors/github:** declare the write-half Route table ([b9d68f0](https://github.com/jordansmall/spindrift/commit/b9d68f0db899ea66dffdbaf23da96e29f7894ff4)), closes [#6882](https://github.com/jordansmall/spindrift/issues/6882)
* **connectors/github:** serve the forge branch primitives ([ae1ed08](https://github.com/jordansmall/spindrift/commit/ae1ed088052d95f99edbc5a1cfa2e32e8cc7914c)), closes [#6882](https://github.com/jordansmall/spindrift/issues/6882)
* **connectors/github:** serve the forge PR primitives ([3fc2613](https://github.com/jordansmall/spindrift/commit/3fc261376a0f43b8332df3125204df19e05a4d16)), closes [#6882](https://github.com/jordansmall/spindrift/issues/6882)
* **connectors/github:** serve the removed labels from the timeline ([f15f4c7](https://github.com/jordansmall/spindrift/commit/f15f4c7fc447c945940fa5fd29f4c6a6a7889162)), closes [#6884](https://github.com/jordansmall/spindrift/issues/6884)
* **connectors/github:** serve the token grant ([21de8d4](https://github.com/jordansmall/spindrift/commit/21de8d437cbdef095437668000b90aa0a28d3711)), closes [#6884](https://github.com/jordansmall/spindrift/issues/6884)
* **connectors/github:** serve the tracker probe and demand count ([383b6de](https://github.com/jordansmall/spindrift/commit/383b6de470603110f8ef034971b1f001b0ac1472)), closes [#6884](https://github.com/jordansmall/spindrift/issues/6884)
* **connectors/github:** serve the tracker write primitives ([50c34ff](https://github.com/jordansmall/spindrift/commit/50c34ff48c639f15f1a2ee51de084ebcf89766ec)), closes [#6881](https://github.com/jordansmall/spindrift/issues/6881)
* **connectors:** add the github Connector's forge read path ([f7ee55d](https://github.com/jordansmall/spindrift/commit/f7ee55d68fc4af5bb7d583eb5a4a2b2b376a4c3f)), closes [#6808](https://github.com/jordansmall/spindrift/issues/6808)
* **connectors:** add the github Connector's tracker read path ([5e49f6d](https://github.com/jordansmall/spindrift/commit/5e49f6d152d482e2bff9d16077f163ffdb692c65)), closes [#6807](https://github.com/jordansmall/spindrift/issues/6807)
* **connectors:** run the conformance suite from each subflake ([7de4ddc](https://github.com/jordansmall/spindrift/commit/7de4ddc2170f9e9d3129d2580fdf917fa1196052)), closes [#7009](https://github.com/jordansmall/spindrift/issues/7009)
* **forge:** read failing job logs through the github Connector ([b080803](https://github.com/jordansmall/spindrift/commit/b08080313d139f2d043abfc9eb3c3a6c104a0fc0)), closes [#6893](https://github.com/jordansmall/spindrift/issues/6893)
* **forge:** report a token's OAuth scopes with its grant ([76c7dfc](https://github.com/jordansmall/spindrift/commit/76c7dfc20ee28a80fe571cec3382688b7dd242ec)), closes [#6884](https://github.com/jordansmall/spindrift/issues/6884)
* **nix:** turn on the github read shadow for dogfood ([1821c55](https://github.com/jordansmall/spindrift/commit/1821c55bed2fee64b9082823b8b99f096969957c)), closes [#6810](https://github.com/jordansmall/spindrift/issues/6810)
* **tracker:** list by Dispatch state and read what an issue blocks ([9865816](https://github.com/jordansmall/spindrift/commit/9865816a4ae9197b6a2602590433608a46105c2a)), closes [#6853](https://github.com/jordansmall/spindrift/issues/6853)


### Documentation

* say each Connector subflake exposes its conformance checks ([1d643c8](https://github.com/jordansmall/spindrift/commit/1d643c809c6be4307f1d61473fca3565dcc21273)), closes [#7009](https://github.com/jordansmall/spindrift/issues/7009)
