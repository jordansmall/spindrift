// Package unixsocket owns the AF_UNIX sun_path length limit (issue #3104):
// sun_path holds 104 bytes on Darwin and 108 on Linux. Check a candidate path
// before binding, because an over-limit bind fails with an EINVAL that names
// neither the cap nor the path.
package unixsocket

import "runtime"

// sunPathCap takes goos as an argument so tests can cover both branches
// whatever OS the test binary runs on. Darwin and Linux are the only
// platforms spindrift targets, so Linux is the default case rather than a
// "linux" match.
func sunPathCap(goos string) int {
	if goos == "darwin" {
		return 104
	}
	return 108
}

// Cap returns the sun_path capacity for the current OS, for a caller that
// needs the number itself rather than the TooLong verdict.
func Cap() int {
	return sunPathCap(runtime.GOOS)
}

// TooLong reports whether path is too long to bind on this OS. The kernel
// needs sun_path's last byte for its NUL terminator, so a path must be
// strictly shorter than Cap().
func TooLong(path string) bool {
	return len(path) >= Cap()
}
