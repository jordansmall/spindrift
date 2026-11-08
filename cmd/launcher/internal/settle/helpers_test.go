package settle

import (
	"time"

	"spindrift.dev/launcher/internal/dispatch"
	"spindrift.dev/launcher/internal/dispatchrecord"
	"spindrift.dev/launcher/internal/forge"
)

func baseConfig() Config {
	return Config{
		CompleteLabel:     "agent-complete",
		FindingLabel:      "agent-review-finding",
		MergePollInterval: 1,   // The Clock below never sleeps for real, so this costs nothing.
		MergePollTimeout:  100, // Large enough for multi-poll tests.
		MergeMode:         "immediate",
		// settle.New() only falls back to dispatch.RealClock() when Clock.Sleep
		// is nil, so this non-sleeping clock keeps every baseConfig() caller off
		// real time. Tests that assert sleep durations overwrite it.
		Clock: dispatch.Clock{Now: time.Now, Sleep: func(time.Duration) {}},
	}
}

const testPR = "https://github.com/owner/repo/pull/42"

// testDispatchLabels mirrors lib/env-schema.nix and is pinned against the agent
// workflows by nix/checks/dispatch-labels.nix (issue #460).
var testDispatchLabels = forge.DispatchLabels{
	Dispatchable: "ready-for-agent",
	InProgress:   "agent-in-progress",
	Complete:     "agent-complete",
	Failed:       "agent-failed",
}

// recordingClock records every requested duration instead of sleeping, so a
// test can assert the backoff schedule without waiting for it.
func recordingClock() (*[]time.Duration, dispatch.Clock) {
	var sleeps []time.Duration
	return &sleeps, dispatch.Clock{Now: time.Now, Sleep: func(d time.Duration) { sleeps = append(sleeps, d) }}
}

// merged is the Settlement of a PR landing of testPR that merged; complete is
// the label set such a landing leaves on the issue.
var (
	merged   = prSettlement(dispatchrecord.ReasonMerged)
	complete = []string{"agent-complete"}
)

// stepAt is a progress step of issue 1 at the given landing: testPR for a PR
// landing, a branch before the hand-off has opened one.
func stepAt(landing string, status Status, detail string) Step {
	return Step{Key: "1", Landing: landing, Status: status, Detail: detail}
}

// openAfterMerge reports the PR as still OPEN after a nil Merge, as a forge
// whose merge is queued rather than applied would.
type openAfterMerge struct{ *forge.Fake }

func (o openAfterMerge) Merge(url string) error {
	if err := o.Fake.Merge(url); err != nil {
		return err
	}
	o.SetPRState(url, forge.PROpen)
	return nil
}
