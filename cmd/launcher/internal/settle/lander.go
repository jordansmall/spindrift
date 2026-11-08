package settle

import (
	"errors"

	"spindrift.dev/launcher/internal/dispatch"
	"spindrift.dev/launcher/internal/passmanifest"
)

// lander lands a settled branch once delivery has put it on the Code Forge.
// New picks one from the Code Forge's capabilities, so "is there a PR forge?"
// is asked once: the push-only lander pushes and records and never rejoins the
// PR tail.
type lander interface {
	land(d dispatch.Dispatcher, num string, gen uint64, ref string, requireRegistration bool, passes []passmanifest.Entry) (landingResult, string, error)
}

// prLander watches CI on a PR, applies the merge mode to it, and verifies a
// merge against the PR's state. pr is the PR settle opened or adopted, never
// the Box's placeholder branch-name landing= value, so a host-mediated landing
// verifies against the real PR.
type prLander struct{ *Settle }

func (s prLander) land(d dispatch.Dispatcher, num string, gen uint64, pr string, requireRegistration bool, _ []passmanifest.Entry) (landingResult, string, error) {
	landing, reason, err := s.selfHealGate(d, num, gen, pr, requireRegistration)
	if landing == landingMerged && !errors.Is(err, errAbandoned) {
		s.verifyMerged(num, pr)
	}
	return landing, reason, err
}
