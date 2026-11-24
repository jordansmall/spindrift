# Changelog

## [0.4.0](https://github.com/jordansmall/spindrift/compare/jira-v0.3.0...jira-v0.4.0) (2026-11-24)


### ⚠ BREAKING CHANGES

* **launcher:** a run-input document that still carries a backend row's shadow, shadowEvery or primary field fails to load; regenerate it.

### Features

* **connectors/jira:** report error classes on non-2xx and transport ([5bc8a72](https://github.com/jordansmall/spindrift/commit/5bc8a72cc2f3f8630131696e6fc486bcce9b59cf)), closes [#7459](https://github.com/jordansmall/spindrift/issues/7459) [#7367](https://github.com/jordansmall/spindrift/issues/7367)
* **launcher:** delete the backend read shadow ([c04637b](https://github.com/jordansmall/spindrift/commit/c04637b422e5d9e7cca4e55ca75dbbf516e930c0)), closes [#7456](https://github.com/jordansmall/spindrift/issues/7456) [#7359](https://github.com/jordansmall/spindrift/issues/7359) [#7298](https://github.com/jordansmall/spindrift/issues/7298)


### Bug Fixes

* **jira:** report an anonymous comment author as Anonymous ([2ea5aca](https://github.com/jordansmall/spindrift/commit/2ea5aca7fe5ec5daa51bd2274ce9828252390a2d)), closes [#7464](https://github.com/jordansmall/spindrift/issues/7464)

## [0.3.0](https://github.com/jordansmall/spindrift/compare/jira-v0.2.0...jira-v0.3.0) (2026-11-23)


### ⚠ BREAKING CHANGES

* **sdk:** Reader.LabeledPage takes a route []Selector argument after label.

### Features

* **connectors/jira:** report each status's category ([40655ef](https://github.com/jordansmall/spindrift/commit/40655efe08c04a0ad35c17d1ef0171e7ae786bf9)), closes [#7337](https://github.com/jordansmall/spindrift/issues/7337)
* **connectors/jira:** route queries and filing by label and Component ([f6bb7ec](https://github.com/jordansmall/spindrift/commit/f6bb7ec1ef67c05cd48e44aa4133ce5b702d904d)), closes [#7333](https://github.com/jordansmall/spindrift/issues/7333)
* **sdk:** add the tracker.matchesRoute per-issue route read ([cc44f85](https://github.com/jordansmall/spindrift/commit/cc44f858138cf314a9358a3884328f8fb81b42cf)), closes [#7335](https://github.com/jordansmall/spindrift/issues/7335)
* **sdk:** carry the Issue route on filing and the labeled read ([68d146d](https://github.com/jordansmall/spindrift/commit/68d146d56d8b4610f489376f7d43e0ec7f366520)), closes [#7333](https://github.com/jordansmall/spindrift/issues/7333)


### Bug Fixes

* **jira:** carry Jira's reason on a refused request ([0b5f7c2](https://github.com/jordansmall/spindrift/commit/0b5f7c21275473ccedddbffb362785251d170832)), closes [#7336](https://github.com/jordansmall/spindrift/issues/7336)
* **jira:** give the Connector module a dedup key ([df9646e](https://github.com/jordansmall/spindrift/commit/df9646ed9659a9b85b37aec3f540bd34d5ee782a)), closes [#7417](https://github.com/jordansmall/spindrift/issues/7417)

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
