package pi

// SessionFlags renders nothing: pi's session resume is not wired yet, so every
// Pass starts cold and the caller falls back to the cold-context fix flow.
func SessionFlags(_, _, _, _ string) string { return "" }
