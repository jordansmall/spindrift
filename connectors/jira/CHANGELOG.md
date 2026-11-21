# Changelog

## [0.2.0](https://github.com/jordansmall/spindrift/compare/jira-v0.1.1...jira-v0.2.0) (2026-11-21)


### ⚠ BREAKING CHANGES

* **nix:** spindrift.connectors.jira.primary = true now fails evaluation; drop the option, since the Connector is jira's only backend.

### Features

* **connector/jira:** page tracker.labeled by Jira's own cursor ([e30abf7](https://github.com/jordansmall/spindrift/commit/e30abf7522c4c206a9835640ae0c960d596e4e80)), closes [#7137](https://github.com/jordansmall/spindrift/issues/7137)
* **nix:** refuse `primary` on the jira Connector row ([180f6b7](https://github.com/jordansmall/spindrift/commit/180f6b735a90243568897d563ff69099cb0024a3)), closes [#6858](https://github.com/jordansmall/spindrift/issues/6858)
* **sdk:** carry limit and fresh on tracker.count ([994cebe](https://github.com/jordansmall/spindrift/commit/994cebe1776683a956e24dd8cff2399ba8a45969)), closes [#6982](https://github.com/jordansmall/spindrift/issues/6982)


### Documentation

* describe jira as running through its Connector only ([04cc8ce](https://github.com/jordansmall/spindrift/commit/04cc8cefa81c65a2cac38afd5983cbc12fcd8a7e)), closes [#6858](https://github.com/jordansmall/spindrift/issues/6858)


### Code Refactoring

* **jira:** delete the native jira adapter ([fadfbba](https://github.com/jordansmall/spindrift/commit/fadfbba6308f86cc8f10ccf51021d93839c80e92)), closes [#6858](https://github.com/jordansmall/spindrift/issues/6858)

## [0.1.1](https://github.com/jordansmall/spindrift/compare/jira-v0.1.0...jira-v0.1.1) (2026-11-18)


### Features

* **connectors:** add the jira Connector subflake ([8b502a9](https://github.com/jordansmall/spindrift/commit/8b502a91e7d698e0a0b9d11064eefa6b630fbb67)), closes [#6853](https://github.com/jordansmall/spindrift/issues/6853)
* **connectors:** run the conformance suite from each subflake ([7de4ddc](https://github.com/jordansmall/spindrift/commit/7de4ddc2170f9e9d3129d2580fdf917fa1196052)), closes [#7009](https://github.com/jordansmall/spindrift/issues/7009)
* **jira-connector:** serve the doctor and demand-count reads ([8e9e97c](https://github.com/jordansmall/spindrift/commit/8e9e97c91fef349fe0dbc7e5730e5743bc31e12c)), closes [#6856](https://github.com/jordansmall/spindrift/issues/6856)
* **jira:** perform transitions and label edits in the jira Connector ([07bd9d1](https://github.com/jordansmall/spindrift/commit/07bd9d176bbc0bceb4eb491e0b61434da16f835d)), closes [#6854](https://github.com/jordansmall/spindrift/issues/6854)
* **jira:** post comments and file issues from the Connector ([6e7d6c7](https://github.com/jordansmall/spindrift/commit/6e7d6c79ba616ee33f50dfcb5cc96f8863980de3)), closes [#6855](https://github.com/jordansmall/spindrift/issues/6855)
* **jira:** serve the labeled dedup scan from the Connector ([3c47416](https://github.com/jordansmall/spindrift/commit/3c4741685dfc38e8dc8b307073e8ebc1e97181fe)), closes [#7127](https://github.com/jordansmall/spindrift/issues/7127)
* **jira:** ship jira's tracker fragments from its Connector ([0145369](https://github.com/jordansmall/spindrift/commit/01453699aaf65a047253d8adb5554ecdeb6e50ce)), closes [#6857](https://github.com/jordansmall/spindrift/issues/6857)


### Documentation

* describe primary per backend and how to switch jira ([4960b4c](https://github.com/jordansmall/spindrift/commit/4960b4c21d6bfcdc351a9691b253e3c08883ae93)), closes [#7127](https://github.com/jordansmall/spindrift/issues/7127)
