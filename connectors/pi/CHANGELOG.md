# Changelog

## [0.2.0](https://github.com/jordansmall/spindrift/compare/pi-v0.1.1...pi-v0.2.0) (2026-11-28)


### ⚠ BREAKING CHANGES

* **connectors:** Connector flakes no longer export flakeModules.default and core.lib.connectorModule is gone; register a Connector by adding its connectorData output to spindrift.connectorData.
* **driver:** the SDK's driver.Strategy.SessionFlags returns a driver.SessionFlag instead of a string, and the driver.sessionFlags result is {flag, value} instead of {flags}.

### Features

* **connectors:** register the claude, opencode and pi Drivers as data ([64601ef](https://github.com/jordansmall/spindrift/commit/64601efe156d381cd20b44fc7c1ab4f7a70e4a86)), closes [#7559](https://github.com/jordansmall/spindrift/issues/7559)
* **driver:** answer driver.sessionFlags as a flag and a value ([f475053](https://github.com/jordansmall/spindrift/commit/f4750538566e44c9c1eb34b0b823771b2a9e11a8)), closes [#7507](https://github.com/jordansmall/spindrift/issues/7507)


### Code Refactoring

* **connectors:** drop the flake-module registration path ([cc88d2e](https://github.com/jordansmall/spindrift/commit/cc88d2e506394742c94394806726e4ea351843ff)), closes [#7562](https://github.com/jordansmall/spindrift/issues/7562)

## [0.1.1](https://github.com/jordansmall/spindrift/compare/pi-v0.1.0...pi-v0.1.1) (2026-11-23)


### Features

* **connectors:** make pi a Connector subflake ([51a65ae](https://github.com/jordansmall/spindrift/commit/51a65aeaf461a86a05c34daaf9d1681e6e707470)), closes [#6875](https://github.com/jordansmall/spindrift/issues/6875)
* **connectors:** port the pi Driver's host half to the SDK ([79e98a6](https://github.com/jordansmall/spindrift/commit/79e98a60b01ef4e20474703bf6597efe07c21f49)), closes [#6875](https://github.com/jordansmall/spindrift/issues/6875)
