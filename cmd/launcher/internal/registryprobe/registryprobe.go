// Package registryprobe defines the reserved process exit codes driver-exec's
// probe-registry-socket and probe-registry-tcp verbs use to report their
// capability verdict (issue #3120).
//
// An old driver-exec binary -- one built before those verbs existed -- has no
// idea "probe-registry-socket" is a verb at all: it falls through to the
// default flag-parsing path and exits 1 (or 2 on a flag error). If a verdict
// reused 0 or 1, that old binary's unrelated exit code would read as a real
// "incapable" answer, making launcher/image version drift indistinguishable
// from a genuine probe result. Reserving codes an old binary structurally
// cannot produce closes that gap: any exit code other than these two reserved
// values means the verb never ran as designed, not that it ran and said no.
package registryprobe

const (
	// ExitCapable is the exit code a probe verb returns when the capability
	// under test is present.
	ExitCapable = 90

	// ExitIncapable is the exit code a probe verb returns when the
	// capability under test was tested and found absent.
	ExitIncapable = 91
)
