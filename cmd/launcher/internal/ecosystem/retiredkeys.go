package ecosystem

// GradleRetiredRouteKey is gradle's Strategy.RetiredRouteKey, exported because
// registryroutes never spells the key itself. It stays here, not in the gradle
// package, so registryroutes can read it without importing a Strategy.
const GradleRetiredRouteKey = "gradle-path"
