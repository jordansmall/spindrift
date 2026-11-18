//go:build !linux && !darwin

package sandbox

import (
	"errors"
	"runtime"
)

// namespaceIdentity is unavailable here, so the sweep reaps nothing rather
// than guess which host's sandboxes are its own.
func namespaceIdentity() (string, error) {
	return "", errors.New("no namespace identity on " + runtime.GOOS)
}
