package butler

import (
	"errors"
	"strings"
)

// ErrNoChores is the "BUTLER_CHORES names no Chore" condition the daemon's
// butler gate and butlerPreflight share; each caller wraps it with its own
// prefix.
var ErrNoChores = errors.New("no chores enabled (BUTLER_CHORES is empty)")

// Chores parses BUTLER_CHORES (schema key butlerChores): a space-separated
// list of enabled Chore names. The empty string parses to an empty slice.
func Chores(list string) []string {
	return strings.Fields(list)
}
