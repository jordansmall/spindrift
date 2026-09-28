package chore

import "errors"

// ErrNoChores is the "BUTLER_CHORES names no Chore" condition the daemon's
// butler gate and butlerPreflight share; each caller wraps it with its own
// prefix.
var ErrNoChores = errors.New("no chores enabled (BUTLER_CHORES is empty)")
