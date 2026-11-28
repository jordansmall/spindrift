# Changelog

## [0.2.0](https://github.com/jordansmall/spindrift/compare/opencode-v0.1.2...opencode-v0.2.0) (2026-11-28)


### ⚠ BREAKING CHANGES

* **connectors:** Connector flakes no longer export flakeModules.default and core.lib.connectorModule is gone; register a Connector by adding its connectorData output to spindrift.connectorData.
* **driver:** the SDK's driver.Strategy.SessionFlags returns a driver.SessionFlag instead of a string, and the driver.sessionFlags result is {flag, value} instead of {flags}.

### Features

* **connectors:** register the claude, opencode and pi Drivers as data ([64601ef](https://github.com/jordansmall/spindrift/commit/64601efe156d381cd20b44fc7c1ab4f7a70e4a86)), closes [#7559](https://github.com/jordansmall/spindrift/issues/7559)
* **driver:** answer driver.sessionFlags as a flag and a value ([f475053](https://github.com/jordansmall/spindrift/commit/f4750538566e44c9c1eb34b0b823771b2a9e11a8)), closes [#7507](https://github.com/jordansmall/spindrift/issues/7507)


### Code Refactoring

* **connectors:** drop the flake-module registration path ([cc88d2e](https://github.com/jordansmall/spindrift/commit/cc88d2e506394742c94394806726e4ea351843ff)), closes [#7562](https://github.com/jordansmall/spindrift/issues/7562)

## [0.1.2](https://github.com/jordansmall/spindrift/compare/opencode-v0.1.1...opencode-v0.1.2) (2026-11-23)


### Documentation

* **go:** trim and correct comments in Go sources ([b5ea3c7](https://github.com/jordansmall/spindrift/commit/b5ea3c7b4ded1ce6d19cd14b0d02c1749b1530ab))


### Tests

* **connector/opencode:** make the header-merge seed deterministic ([1fc1445](https://github.com/jordansmall/spindrift/commit/1fc144548df72c108947f043a6df016631152738)), closes [#6874](https://github.com/jordansmall/spindrift/issues/6874)

## [0.1.1](https://github.com/jordansmall/spindrift/compare/opencode-v0.1.0...opencode-v0.1.1) (2026-11-21)


### Features

* **connector/opencode:** make opencode a Connector subflake ([29ce044](https://github.com/jordansmall/spindrift/commit/29ce0449f5d8b4d167052260043623fd6cedfa70)), closes [#6873](https://github.com/jordansmall/spindrift/issues/6873)
* **connector/opencode:** port the opencode Strategy to a Connector ([e10214d](https://github.com/jordansmall/spindrift/commit/e10214d351ff2b8f068b5673cac857dafb5b9d13)), closes [#6873](https://github.com/jordansmall/spindrift/issues/6873)


### Tests

* **connector/opencode:** re-sync the reference from native's fix ([a6fc0b8](https://github.com/jordansmall/spindrift/commit/a6fc0b83390750c7765fb3d183f007cdc08775cd)), closes [#7388](https://github.com/jordansmall/spindrift/issues/7388)
