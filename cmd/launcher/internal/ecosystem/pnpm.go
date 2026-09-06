package ecosystem

// pnpmRow is the pnpm ecosystem's Table entry (see yarnRow's doc comment for
// why this row carries a BindingEnvVar despite a nil EnvExports).
var pnpmRow = Row{
	Name:             "pnpm",
	LockfileNames:    []string{"pnpm-lock.yaml"},
	Classification:   "npm/pnpm/yarn",
	InTreeConfigPath: "pnpm-workspace.yaml",
	BindingEnvVar:    "pnpm_config_registry",
}
