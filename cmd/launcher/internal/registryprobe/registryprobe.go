// Package registryprobe defines the exit codes driver-exec's probe-registry-socket
// and probe-registry-tcp verbs report their capability verdict with (issue #3120).
// A driver-exec too old to know those verbs exits 1, or 2 on a flag error, so the
// verdict codes are values it cannot produce: any other exit code means the verb
// never ran, not that it ran and answered no.
package registryprobe

const (
	// ExitCapable reports that the capability under test is present.
	ExitCapable = 90

	// ExitIncapable reports that the capability was tested and found absent.
	ExitIncapable = 91
)
