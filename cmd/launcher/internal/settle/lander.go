package settle

import (
	"spindrift.dev/launcher/internal/dispatch"
	"spindrift.dev/launcher/internal/passmanifest"
)

// lander lands a settled branch once delivery has put it on the Code Forge.
// New picks one from the Code Forge's capabilities, so "is there a PR forge?"
// is asked once: the push-only lander pushes and records and never rejoins the
// PR tail.
type lander interface {
	land(d dispatch.Dispatcher, num string, gen uint64, ref string, requireRegistration bool, passes []passmanifest.Entry) (landingResult, string, error)
	// landingRef turns a ready outcome into the ref land takes; ok is false
	// when the run is already settled (abandoned, or a failed host handoff).
	landingRef(d dispatch.Dispatcher, num string, gen uint64, result dispatch.Result) (ref string, ok bool)
	// latchOpenPR records num's open PR on its settled record.
	latchOpenPR(num string)
	// settleReportedMerge settles an off-script status=merged outcome.
	settleReportedMerge(num string)
	// salvage settles a run cut short whose evidence says its work finished;
	// true means the run is settled.
	salvage(d dispatch.Dispatcher, num string, gen uint64, result dispatch.Result) bool
	// landRelayed lands a branch the Box relayed; false means nothing landed.
	landRelayed(d dispatch.Dispatcher, num string, gen uint64, sit Situation, result dispatch.Result) (relayed bool, err error)
}
