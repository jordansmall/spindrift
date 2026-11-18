//go:build darwin

package sandbox

import (
	"errors"
	"syscall"
)

// namespaceIdentity is macOS's per-boot random UUID. macOS has no pid
// namespaces, so only hosts sharing a remote runtime daemon need telling
// apart, and a hostname can repeat across them where this UUID cannot.
func namespaceIdentity() (string, error) {
	id, err := syscall.Sysctl("kern.bootsessionuuid")
	if err != nil {
		return "", err
	}
	if id == "" {
		return "", errors.New("empty kern.bootsessionuuid")
	}
	return id, nil
}
