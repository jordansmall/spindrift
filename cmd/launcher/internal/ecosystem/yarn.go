package ecosystem

// yarnRow is the yarn ecosystem's Table entry. It carries a BindingEnvVar
// despite a nil EnvExports because npmRow's EnvExports (NpmFamilyBindings)
// renders all three npm-family vars, yarn's included, in one call.
var yarnRow = Row{
	Name:             "yarn",
	LockfileNames:    []string{"yarn.lock"},
	Classification:   "npm/pnpm/yarn",
	InTreeConfigPath: ".yarnrc.yml",
	BindingEnvVar:    "YARN_NPM_REGISTRY_SERVER",
}
