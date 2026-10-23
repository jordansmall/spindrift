package ecosystem

// GradleRetiredRouteKey is gradle's Strategy.RetiredRouteKey, exported because
// registryroutes never spells the key itself. It stays here, not in the gradle
// package, so registryroutes can read it without importing a Strategy.
const GradleRetiredRouteKey = "gradle-path"

// GoRetiredRouteKey is go's Strategy.RetiredRouteKey, kept here for the same
// reason as GradleRetiredRouteKey.
const GoRetiredRouteKey = "go-path"

// CargoRetiredRouteKey is cargo's Strategy.RetiredRouteKey, kept here for the
// same reason as GradleRetiredRouteKey.
const CargoRetiredRouteKey = "cargo-registries"

// CargoRouteRegistriesKey is the one key (besides "path") a
// [routes.ecosystems.cargo] block may carry. It stays here so registryroutes'
// routes-file parser can name it without importing a Strategy (ADR 0048).
const CargoRouteRegistriesKey = "registries"
