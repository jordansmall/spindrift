package runnertest

// Lookup serves knobs from env, standing in for the launcher's knobLookup. It
// returns the bare func type so it needs no runner import (see doc.go); it is
// assignable to runner.KnobLookup.
func Lookup(env map[string]string) func(string) (string, bool) {
	return func(k string) (string, bool) { v, ok := env[k]; return v, ok }
}
