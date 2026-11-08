package inputdoc

// FlagEntry maps a runtime knob to its CLI flag. nix/regen.nix generates the
// SchemaFlags table from lib/env-schema.nix, via lib/renderers.nix's
// renderFlagTableGo.
type FlagEntry struct {
	Env   string // SCREAMING_SNAKE_CASE
	Flag  string // kebab-case, no leading dashes
	Alias string // kebab-case, no leading dashes; empty when absent
	Kind  string // "string", "int", or "bool"
	Doc   string
	Dflt  string // empty when there is none
	Group string
	// canonical placed flake path, e.g. git.merge.policy (Driver-valued knobs:
	// agents.drivers.<name>.*); empty for a non-flakeOption knob
	SettingsPath    string
	DeprecatedAlias string // empty unless the canonical flag was renamed (ADR 0037 Pass 2)
	Choices         []string
	// LauncherIgnores marks a knob the launcher generates a flag for (1:1
	// flake-option/flag parity, #3567) but never itself reads; only
	// cmd/launcher/daemon consumes it, via the input document (#3698).
	LauncherIgnores bool
	// EmptyDisables marks a string knob whose doc gives "" its own meaning
	// (e.g. disables the limit); see EmptyIsSetting.
	EmptyDisables bool
	// BoxHostDerived marks a knob the schema declares both hostDerived and
	// boxEnv (issue #5329). A test-only parity marker: no production path
	// reads it; TestDispatchConfig_ResolveEnv_EveryBoxHostDerivedKnobGetsSnapshot
	// pins it, and runtime forwarding reads BOX_ENV_VARS. Schema-drift's
	// hostGitConfigKeyMismatch makes it equivalent to HostGitConfigKey != "";
	// it stays as defence in depth, so that test still catches a keyless
	// host-derived row in a regen that never ran schema-drift.
	BoxHostDerived bool
	// HostGitConfigKey is the host git config key (git config --get) a
	// BoxHostDerived knob falls back to; read by cmd/launcher's
	// launcherKnobSource.HostGitConfig.
	HostGitConfigKey string
}

// EmptyIsSetting is the one rule for whether an empty env value is a real
// setting rather than "unset" (which falls back to the default): true iff the
// knob is bool kind (scanSchemaFlags encodes a bool's explicit off as
// set-but-empty) or EmptyDisables. The knob resolver applies it to explicit
// flags, AmbientKnobError to env, and Document.Setting to a document value;
// never re-derive this.
func (e FlagEntry) EmptyIsSetting() bool {
	return e.Kind == "bool" || e.EmptyDisables
}

// Argv renders the flag with value as command-line tokens: a bool is a single
// "--flag=value" token so a following token is never read as its value.
func (e FlagEntry) Argv(value string) []string {
	if e.Kind == "bool" {
		return []string{"--" + e.Flag + "=" + value}
	}
	return []string{"--" + e.Flag, value}
}

// SecretKnob is a knob the schema marks secret = true, so it gets no inline
// value flag: callers supply it through the environment, --<FileFlag>, or
// --<CmdFlag>. nix/regen.nix generates SecretKnobs from lib/env-schema.nix,
// via lib/renderers.nix's renderFlagTableGo.
type SecretKnob struct {
	Env      string
	Doc      string
	FileFlag string // kebab-case, no leading dashes
	CmdFlag  string // kebab-case, no leading dashes
}

// SchemaFlag returns the SchemaFlags row whose env key is key.
func SchemaFlag(key string) (FlagEntry, bool) {
	for _, e := range SchemaFlags {
		if e.Env == key {
			return e, true
		}
	}
	return FlagEntry{}, false
}
