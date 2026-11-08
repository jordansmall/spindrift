package inputdoc

// KnobSource answers LoadKnobs' per-kind reads, so the caller decides where a
// value comes from and how a malformed one falls back.
type KnobSource interface {
	// String serves string knobs, and bool knobs as String(key) != "".
	String(key string) string
	// PositiveInt serves int knobs whose intKind is "positive".
	PositiveInt(key string) int
	// NonnegInt serves every other int knob.
	NonnegInt(key string) int
	// NonnegFloat serves float knobs.
	NonnegFloat(key string) float64
	// Secret never consults the document: that would open a Launcher-input
	// secret-injection channel.
	Secret(key string) string
	// HostGitConfig serves the members carrying hostGitConfigKey.
	HostGitConfig(key string) string
}
