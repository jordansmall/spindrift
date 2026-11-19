package settle

import (
	"testing"
	"time"

	"spindrift.dev/launcher/internal/backend"
	"spindrift.dev/launcher/internal/dispatch"
	"spindrift.dev/launcher/internal/dispatchrecord"
	"spindrift.dev/launcher/internal/forge"
)

func baseConfig() Config {
	return Config{
		BranchPrefix:      testBranchPrefix,
		CompleteLabel:     "agent-complete",
		FindingLabel:      "agent-review-finding",
		MergePollInterval: 1,   // The Clock below never sleeps for real, so this costs nothing.
		MergePollTimeout:  100, // Large enough for multi-poll tests.
		MergeMode:         "immediate",
		BaseBranch:        "main",
		OutboxDir:         func(num string) string { return "/outbox/" + num },
		// settle.New() only falls back to dispatch.RealClock() when Clock.Sleep
		// is nil, so this non-sleeping clock keeps every baseConfig() caller off
		// real time. Tests that assert sleep durations overwrite it.
		Clock: dispatch.Clock{Now: time.Now, Sleep: func(time.Duration) {}},
	}
}

const testPR = "https://github.com/owner/repo/pull/42"

// markReadyFailedComment is the issue comment for a ready flip that failed with
// cause, leaving testPR a draft.
func markReadyFailedComment(cause error) string {
	return "marking " + testPR + " ready failed after green CI: " + cause.Error() + " — it is still a draft"
}

// testBranchPrefix is the BRANCH_PREFIX default; baseConfig sets it on Config.
const testBranchPrefix = "agent/issue-"

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
type openAfterMerge struct{ roFake }

func (o openAfterMerge) Merge(url string) error {
	if err := o.Fake.Merge(url); err != nil {
		return err
	}
	o.SetPRState(url, forge.PROpen)
	return nil
}

// row resolves a backend row by name, failing the test on an unknown name.
func row(t *testing.T, name string) backend.Descriptor {
	t.Helper()
	d, ok := backend.ByName(name)
	if !ok {
		t.Fatalf("no backend row %q", name)
	}
	return d
}

// githubRow is the real github row, passed as both the forge and tracker
// descriptor, so the github-shaped fixtures (AsNoLandingRecorder) track every
// row fact settle reads.
var githubRow, _ = backend.ByName("github")

// roFake is a *forge.Fake that keeps the host-mediation surface of
// AsGithubReadOnly while a wrapper overrides other methods: the Box never
// pushes, so every PR Lander forge relays bundles and opens the draft PR.
type roFake struct{ *forge.Fake }

func (r roFake) RelayBundle(outboxDir, ref string) error {
	return r.AsGithubReadOnly().(forge.BundleRelay).RelayBundle(outboxDir, ref)
}

func (r roFake) CreateDraftPR(title, body, base, head string) (string, bool, error) {
	return r.AsGithubReadOnly().(forge.DraftPRCreator).CreateDraftPR(title, body, base, head)
}

func (r roFake) CommitSubjects(outboxDir, base, ref string) ([]string, error) {
	return r.AsGithubReadOnly().(forge.BundleCommitSubjects).CommitSubjects(outboxDir, base, ref)
}

// roRunURL keeps host mediation on a RunURLFake, which AsGithubReadOnly would
// otherwise hide behind a wrapper that has no RunURL.
type roRunURL struct{ *forge.RunURLFake }

func (r roRunURL) RelayBundle(outboxDir, ref string) error {
	return roFake{r.Fake}.RelayBundle(outboxDir, ref)
}

func (r roRunURL) CreateDraftPR(title, body, base, head string) (string, bool, error) {
	return roFake{r.Fake}.CreateDraftPR(title, body, base, head)
}

func (r roRunURL) CommitSubjects(outboxDir, base, ref string) ([]string, error) {
	return roFake{r.Fake}.CommitSubjects(outboxDir, base, ref)
}
