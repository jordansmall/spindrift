package main

import (
	"io"
	"path/filepath"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/dispatch"
	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/passmanifest"
	"spindrift.dev/launcher/internal/settle"
)

// seedManifest writes num's outbox pass manifest, as the Box's review loop
// leaves it.
func (x *queueRecoverFixture) seedManifest(t *testing.T, num string, entries ...passmanifest.Entry) {
	t.Helper()
	passmanifest.Write(filepath.Join(dispatch.OutboxDirFor(x.dir, num), passmanifest.FileName), entries)
}

// A stale implement-pass ready on an agent-failed issue is human triage, not
// something recover lands (issue #5973), in queue and hand-run mode alike.
func TestRecoverQueueOne_UnreviewedReadyIsLeftUntouched(t *testing.T) {
	x := newQueueRecoverFixture(t)
	x.addFailed(t, "42", "ready")
	x.seedManifest(t, "42",
		passmanifest.Entry{Pass: 1, Kind: "implement", OutcomeFound: true},
		passmanifest.Entry{Pass: 2, Kind: "review"},
	)

	code, out := x.run(t)

	if code != 2 {
		t.Errorf("exit = %d, want 2", code)
	}
	x.assertUntouched(t)
	if x.fc.Merged != "" || len(x.fc.RelayBundleCalls) != 0 {
		t.Errorf("Merged = %q, relays = %v, want nothing landed", x.fc.Merged, x.fc.RelayBundleCalls)
	}
	if strings.Contains(out, "status=skipped") || strings.Contains(out, "land pass") {
		t.Errorf("stdout = %q, want queue mode silent (settle already commented)", out)
	}
}

func TestRecoverQueueOne_LandPassReadyStillLands(t *testing.T) {
	x := newQueueRecoverFixture(t)
	x.addFailed(t, "42", "ready")
	x.seedManifest(t, "42",
		passmanifest.Entry{Pass: 1, Kind: "implement", OutcomeFound: true},
		passmanifest.Entry{Pass: 2, Kind: "review"},
		passmanifest.Entry{Pass: 3, Kind: "land", OutcomeFound: true},
	)

	code, _ := x.run(t)

	if code != 0 || x.fc.Merged != testReconcilePR {
		t.Errorf("exit = %d, Merged = %q, want 0 and %q", code, x.fc.Merged, testReconcilePR)
	}
}

func TestCmdRecover_UnreviewedReadyReportsSkipNotNoOpenPR(t *testing.T) {
	x := newQueueRecoverFixture(t)
	x.addFailed(t, "42", "ready")
	x.seedManifest(t, "42", passmanifest.Entry{Pass: 1, Kind: "implement", OutcomeFound: true})
	lc := &launchContext{
		config:       x.c,
		pwd:          x.dir,
		issueTracker: x.fc,
		codeForge:    x.cf,
		factory:      testFactory(t, x.dir, nil),
		settle:       testNewSettle(x.c, x.fc, testWired(x.fc), x.cf),
		cleanup:      func() {},
	}

	var stdout, stderr strings.Builder
	got := cmdRecover(lc, "42", &stdout, &stderr)

	if got == 0 || x.fc.Merged != "" || len(x.fc.RelayBundleCalls) != 0 {
		t.Errorf("cmdRecover = %d, Merged = %q, relays = %v, want a non-zero exit and nothing landed", got, x.fc.Merged, x.fc.RelayBundleCalls)
	}
	if !strings.Contains(stdout.String(), "land pass") || strings.Contains(stdout.String(), "no open PR") {
		t.Errorf("stdout = %q, want the unreviewed skip note and no %q", stdout.String(), "no open PR")
	}
}

// openPRRecover hand-runs recover for #42 on a forge where the Box already
// opened its draft PR in-box, so recover takes the adopt path.
func (x *queueRecoverFixture) openPRRecover(t *testing.T) (string, error) {
	t.Helper()
	x.fc.SetPR(x.fc.AgentBranch("42"), forge.PR{URL: testReconcilePR})
	var out strings.Builder
	err := recoverByNumber(x.c, x.fc, x.fc, capsFor(x.fc, x.fc), x.dir, testFactory(t, x.dir, nil), newWorkSettle(x.c, x.fc, testWired(x.fc), x.fc), "42", &out, io.Discard)
	return out.String(), err
}

// A draft PR the Box opened before a crashed review pass must not be adopted
// on the strength of the implement pass's stale ready (issue #5973).
func TestCmdRecover_UnreviewedReadyWithOpenPRIsNotAdopted(t *testing.T) {
	x := newQueueRecoverFixture(t)
	x.addFailed(t, "42", "ready")
	x.seedManifest(t, "42",
		passmanifest.Entry{Pass: 1, Kind: "implement", OutcomeFound: true},
		passmanifest.Entry{Pass: 2, Kind: "review"},
	)

	out, err := x.openPRRecover(t)

	if err == nil {
		t.Error("recoverByNumber = nil, want the unreviewed decline")
	}
	if x.fc.Merged != "" {
		t.Errorf("Merged = %q, want nothing landed", x.fc.Merged)
	}
	if !strings.Contains(out, "status=skipped") || !strings.Contains(out, "land pass") {
		t.Errorf("stdout = %q, want a skipped note naming the land pass", out)
	}
	if containsLabel(x.labels(t, "42"), x.c.completeLabel) {
		t.Errorf("labels = %v, want no %s", x.labels(t, "42"), x.c.completeLabel)
	}
}

// A signal-killed run leaves no self-report; stopped in implement, its bundle
// is still not something recover lands (issue #5984).
func TestCmdRecover_NoReportStoppedBeforeLandIsSkipped(t *testing.T) {
	x := newQueueRecoverFixture(t)
	x.addFailed(t, "42", "")
	x.seedManifest(t, "42", passmanifest.Entry{Pass: 1, Kind: "implement"})
	lc := &launchContext{
		config:       x.c,
		pwd:          x.dir,
		issueTracker: x.fc,
		codeForge:    x.cf,
		factory:      testFactory(t, x.dir, nil),
		settle:       testNewSettle(x.c, x.fc, testWired(x.fc), x.cf),
		cleanup:      func() {},
	}

	var stdout, stderr strings.Builder
	got := cmdRecover(lc, "42", &stdout, &stderr)

	if got == 0 || x.fc.Merged != "" || len(x.fc.RelayBundleCalls) != 0 {
		t.Errorf("cmdRecover = %d, Merged = %q, relays = %v, want a non-zero exit and nothing landed", got, x.fc.Merged, x.fc.RelayBundleCalls)
	}
	if !strings.Contains(stdout.String(), settle.UnreviewedNote) {
		t.Errorf("stdout = %q, want the unreviewed skip note", stdout.String())
	}
}

// The stale-ready decline is about a success self-report; a run with none
// that opened its draft PR in-box and crashed in implement still goes to the
// adopt path's merge gate, as before issue #5984.
func TestCmdRecover_NoReportStoppedBeforeLandWithOpenPRIsNotDeclined(t *testing.T) {
	x := newQueueRecoverFixture(t)
	x.addFailed(t, "42", "")
	x.seedManifest(t, "42", passmanifest.Entry{Pass: 1, Kind: "implement"})

	out, err := x.openPRRecover(t)

	if strings.Contains(out, settle.UnreviewedNote) {
		t.Errorf("stdout = %q, want no unreviewed decline for an open PR with no success self-report", out)
	}
	if err != nil || x.fc.Merged != testReconcilePR || len(x.fc.MarkReadyCalls) != 1 {
		t.Errorf("err = %v, Merged = %q, MarkReadyCalls = %v, want the adopt path's gate to mark ready and merge %q", err, x.fc.Merged, x.fc.MarkReadyCalls, testReconcilePR)
	}
}

// A hand-run recover with no open PR, no success self-report, and a manifest
// stopped before the land pass names the unreviewed decline, not "no open PR"
// (issue #5984).
func TestCmdRecover_NoReportStoppedBeforeLandNoOpenPRNamesUnreviewed(t *testing.T) {
	cases := []struct {
		name    string
		entries []passmanifest.Entry
	}{
		{"implement-last", []passmanifest.Entry{{Pass: 1, Kind: "implement"}}},
		{"fix-last", []passmanifest.Entry{
			{Pass: 1, Kind: "implement", OutcomeFound: true},
			{Pass: 2, Kind: "review", Verdict: "REQUEST_CHANGES"},
			{Pass: 3, Kind: "fix"},
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			x := newQueueRecoverFixture(t)
			x.addFailed(t, "42", "")
			x.seedManifest(t, "42", tc.entries...)

			var out strings.Builder
			err := recoverByNumber(x.c, x.fc, x.fc, capsFor(x.fc, x.fc), x.dir, testFactory(t, x.dir, nil), newWorkSettle(x.c, x.fc, testWired(x.fc), x.cf), "42", &out, io.Discard)

			if err == nil || !strings.Contains(err.Error(), settle.UnreviewedNote) {
				t.Errorf("err = %v, want the unreviewed decline", err)
			}
			if !strings.Contains(out.String(), "status=skipped") || !strings.Contains(out.String(), settle.UnreviewedNote+"; needs human triage") {
				t.Errorf("stdout = %q, want the unreviewed skip line", out.String())
			}
			if strings.Contains(out.String(), "no open PR") || (err != nil && strings.Contains(err.Error(), "no open PR")) {
				t.Errorf("stdout = %q, err = %v, want no %q", out.String(), err, "no open PR")
			}
			if x.fc.Merged != "" || len(x.fc.RelayBundleCalls) != 0 {
				t.Errorf("Merged = %q, relays = %v, want nothing landed", x.fc.Merged, x.fc.RelayBundleCalls)
			}
			if !containsLabel(x.labels(t, "42"), x.c.failedLabel) || containsLabel(x.labels(t, "42"), x.c.completeLabel) {
				t.Errorf("labels = %v, want %s and not %s", x.labels(t, "42"), x.c.failedLabel, x.c.completeLabel)
			}
		})
	}
}

func TestCmdRecover_LandPassReadyWithOpenPRIsAdopted(t *testing.T) {
	x := newQueueRecoverFixture(t)
	x.addFailed(t, "42", "ready")
	x.seedManifest(t, "42",
		passmanifest.Entry{Pass: 1, Kind: "implement", OutcomeFound: true},
		passmanifest.Entry{Pass: 2, Kind: "review"},
		passmanifest.Entry{Pass: 3, Kind: "land", OutcomeFound: true},
	)

	out, err := x.openPRRecover(t)

	if err != nil || x.fc.Merged != testReconcilePR {
		t.Errorf("err = %v, Merged = %q, want nil and %q (stdout %q)", err, x.fc.Merged, testReconcilePR, out)
	}
}
