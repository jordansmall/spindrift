// Package unixsocket is the authoritative home for AF_UNIX sun_path length
// limits (issue #3104): sockaddr_un.sun_path is a fixed-size byte array,
// capped at 104 bytes on Darwin and 108 bytes on Linux, so any caller that
// binds or connects a unix domain socket needs to check a candidate path
// against this cap before the kernel does, since a bare over-limit bind
// fails with an EINVAL that names neither the cap nor the path.
package unixsocket

import "runtime"

// sunPathCap returns the sockaddr_un.sun_path capacity for goos. Linux is the
// only platform besides Darwin spindrift targets, so it's the default case
// rather than a "linux" match -- this also makes the function testable for
// both branches without depending on which OS the test binary actually runs
// on.
func sunPathCap(goos string) int {
	if goos == "darwin" {
		return 104
	}
	return 108
}

// Cap returns the sockaddr_un.sun_path capacity for the current OS, for a
// caller (e.g. ListenAndServe's error message) that needs the number itself,
// not just the TooLong verdict.
func Cap() int {
	return sunPathCap(runtime.GOOS)
}

// TooLong reports whether path is too long to bind as a unix domain socket
// on this OS: the kernel needs sun_path's last byte for its own NUL
// terminator, so a path must be strictly shorter than Cap().
func TooLong(path string) bool {
	return len(path) >= Cap()
}
