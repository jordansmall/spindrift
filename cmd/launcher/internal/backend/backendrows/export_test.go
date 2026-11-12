package backendrows

// Reset drops every applied row, for a test that must not leak them.
func Reset() {
	mu.Lock()
	defer mu.Unlock()
	applied = map[string]Row{}
}
