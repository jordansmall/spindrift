package main

// knobLookup serves the row knobs from c's already-resolved fields,
// so a Strategy's ReadKnobs sees what the launcher resolved, not the process
// environment. It must serve these already-parsed values (bools via
// FormatBool), never raw env: the schema treats any non-empty BWRAP_UNSHARE_NET
// as true, but the generated ReadKnobs uses ParseBool, so a raw
// BWRAP_UNSHARE_NET=0 would silently stop forcing isolation. rowKnobs is
// generated (rowknobs_gen.go).
func (c config) knobLookup(env string) (string, bool) {
	if field, ok := rowKnobs[env]; ok {
		return field(c), true
	}
	return "", false
}
