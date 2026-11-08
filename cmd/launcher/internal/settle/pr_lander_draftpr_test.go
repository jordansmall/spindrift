package settle

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/dispatch"
	"spindrift.dev/launcher/internal/dispatchrecord"
	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/hostop"
)

const draftPRRetryURL = testPR

var errDraftPR504 = errors.New("HTTP 504: 504 Gateway Timeout (https://api.github.com/graphql)")

// draftPRCases are the rows where the host opens a read-only Box's draft PR
// (issue #5146): a create that fails is retried, a PR the failed attempt made
// is adopted rather than duplicated, and one that never succeeds parks.
func draftPRCases() []prCase {
	g := &handoffRig{}
	merged := prSettlement(dispatchrecord.ReasonMerged)
	parked := Settlement{Verdict: ForReason(dispatchrecord.ReasonDraftPRFailed), Note: "draft PR create failed; work preserved in outbox"}
	complete := []string{"agent-complete"}
	failed := []string{"agent-failed"}
	flipThenMerge := []string{"MarkReady:" + testPR, "Merge:" + testPR}
	errLookup := errors.New("lookup blip")
	attempt := func(n, of int, label Status, err error) Step {
		return handoffStep(label, fmt.Sprintf("attempt=%d/%d  !! %v", n, of, err))
	}
	opensPR := func(fc *forge.Fake) func(int) {
		return func(int) { fc.SetPR(fc.AgentBranch("1"), forge.PR{URL: testPR}) }
	}
	// The create on call makes the PR, as a gateway timeout that fired after the
	// forge made it would.
	createdOnCall := func(fc *forge.Fake, call int, mark func()) {
		fc.On("CreateDraftPR", func(n int) {
			if n == call {
				fc.SetPR(fc.AgentBranch("1"), forge.PR{URL: testPR})
				if mark != nil {
					mark()
				}
			}
		})
	}
	withoutIntent := func(fc *forge.Fake) { fc.CommitSubjectsResult = []string{"feat: add widget"} }
	reconstructed := handoffResult("ready")
	reconstructed.PRIntent, reconstructed.PRIntentFound = "", false
	announced := []string{"This PR was reconstructed host-side"}
	handedOff := func(c prCase) prCase {
		c.shape, c.noPR = shapeReadOnly, true
		if c.result == nil {
			c.result = handoffResult("ready")
		}
		c.want, c.wantLabels, c.wantNoLabels, c.wantMerged, c.wantCalls = merged, complete, failed, testPR, flipThenMerge
		c.wantNoStatus = append(c.wantNoStatus, StatusDraftPRFailed)
		return c
	}
	parks := func(c prCase) prCase {
		c.shape, c.noPR = shapeReadOnly, true
		if c.result == nil {
			c.result = handoffResult("ready")
		}
		c.want, c.prClosed, c.wantLabels = parked, true, failed
		c.wantComments = []string{"draft PR create failed: HTTP 504"}
		c.wantNoLabels = []string{"agent-in-progress", "agent-complete"}
		return c
	}
	var logPath string
	var draftFailureLog checkFn = func(t *testing.T, fc *forge.Fake, _ *dispatch.Fake) {
		t.Helper()
		for _, c := range fc.CommentCalls {
			if strings.Contains(c.Body, "bundle relay failed") {
				t.Errorf("a draft-PR create failure must not claim the relay failed: %q", c.Body)
			}
			if strings.Contains(c.Body, "draft PR create failed: draft PR create failed") {
				t.Errorf("comment repeats the error prefix: %q", c.Body)
			}
		}
		raw, err := os.ReadFile(logPath)
		if err != nil {
			t.Fatal(err)
		}
		lines := strings.Split(strings.TrimSuffix(string(raw), "\n"), "\n")
		if len(lines) != 2 {
			t.Fatalf("log = %q, want the prior line plus exactly one appended op", raw)
		}
		var ev hostop.Event
		if err := json.Unmarshal([]byte(lines[1]), &ev); err != nil || ev.SpindriftOp == nil || ev.SpindriftOp.Settled == nil {
			t.Fatalf("appended line %q is not a dispatch_settled op: %v", lines[1], err)
		}
		if got := ev.SpindriftOp.Settled; got.State != forge.Failed.String() || got.Reason != dispatchrecord.ReasonDraftPRFailed {
			t.Errorf("settled = %+v, want state=failed reason=%q", got, dispatchrecord.ReasonDraftPRFailed)
		}
	}

	cases := []prCase{
		handedOff(prCase{
			name:      "a draft PR create that fails transiently is retried, then opens",
			setup:     g.readOnly(3, func(_ *testing.T, fc *forge.Fake, _ *Config) { fc.CreateDraftPRErrs = []error{errDraftPR504, nil} }),
			wantSteps: []Step{attempt(1, 3, StatusDraftPRRetry, errDraftPR504)},
			check:     draftPRCalls(2),
		}),
		// A gateway timeout can fire after the forge made the PR: the retry must
		// adopt it instead of creating a second one.
		handedOff(prCase{
			name: "a draft PR create timeout that made the PR adopts it",
			setup: g.readOnly(3, func(_ *testing.T, fc *forge.Fake, _ *Config) {
				fc.CreateDraftPRErrs = []error{errDraftPR504}
				fc.On("CreateDraftPR", opensPR(fc))
			}),
			wantSteps: []Step{handoffStep(StatusDraftPRAdopted, testPR)},
			check:     draftPRCalls(1),
		}),
		handedOff(prCase{
			name: "a failed lookup after a create timeout falls through to a re-create",
			setup: g.readOnly(3, func(_ *testing.T, fc *forge.Fake, _ *Config) {
				fc.CreateDraftPRErrs = []error{errDraftPR504}
				fc.OpenPRForBranchErr = errLookup
			}),
			wantSteps: []Step{handoffStep(StatusDraftPRLookupFailed, "!! lookup blip")},
			check:     draftPRCalls(2),
		}),
		// A 504 on the final attempt can still have made the PR (#5172): the
		// lookup after the last failure must adopt it rather than let the caller park.
		handedOff(prCase{
			name: "a PR made by the final create attempt is adopted by the final lookup",
			setup: g.readOnly(2, func(_ *testing.T, fc *forge.Fake, _ *Config) {
				fc.CreateDraftPRErr = errDraftPR504
				createdOnCall(fc, 3, nil)
			}),
			wantSteps: []Step{handoffStep(StatusDraftPRAdopted, testPR)},
			check:     draftPRCalls(3),
		}),
		parks(prCase{
			name: "a zero retry budget creates once, then looks up the branch before parking",
			setup: g.readOnly(0, func(_ *testing.T, fc *forge.Fake, _ *Config) {
				fc.CreateDraftPRErrs = []error{errDraftPR504}
				fc.OpenPRForBranchErrs = []error{nil}
			}),
			wantSteps: []Step{attempt(1, 1, StatusDraftPRFinalLookup, errDraftPR504)},
			check: allOf(draftPRCalls(1), func(t *testing.T, fc *forge.Fake, _ *dispatch.Fake) {
				// The fake drains one OpenPRForBranchErrs entry per lookup.
				if got := len(fc.OpenPRForBranchErrs); got != 0 {
					t.Errorf("unrun lookups = %d, want 0: the final lookup must run even with Policy.Max=0", got)
				}
			}),
		}),
		parks(prCase{
			name: "a failed final lookup parks with the create's own error",
			setup: g.readOnly(0, func(_ *testing.T, fc *forge.Fake, _ *Config) {
				fc.CreateDraftPRErr = errDraftPR504
				fc.OpenPRForBranchErr = errLookup
			}),
			wantSteps: []Step{attempt(1, 1, StatusDraftPRFinalLookup, errDraftPR504), handoffStep(StatusDraftPRLookupFailed, "!! lookup blip")},
			check:     draftPRCalls(1),
		}),
		// Pins issue #5144: Policy.Max+1 attempts, then an agent-failed park that
		// names the forge error and keeps the Box's bundle for `spindrift recover`.
		parks(prCase{
			name: "a draft PR create that never succeeds parks failed with the bundle preserved",
			setup: g.readOnly(2, func(t *testing.T, fc *forge.Fake, c *Config) {
				g.bundled(t, fc, c)
				fc.CreateDraftPRErr = errDraftPR504
				logPath = filepath.Join(t.TempDir(), "issue-1.log")
				if err := os.WriteFile(logPath, []byte("existing\n"), 0o644); err != nil {
					t.Fatal(err)
				}
				c.LogPath = func(string) string { return logPath }
			}),
			dsetup:    func(d *dispatch.Fake, _ *forge.Fake) { d.RecordIDResult = "work:1@t" },
			wantSteps: []Step{handoffStep(StatusDraftPRFailed, "!! draft PR create failed: "+errDraftPR504.Error())},
			check: allOf(relayCalls(1), draftPRCalls(3), g.bundle(bundleKept), draftFailureLog,
				func(t *testing.T, fc *forge.Fake, _ *dispatch.Fake) {
					t.Helper()
					if c := fc.CommentCalls[0].Body; !strings.Contains(c, "preserved in the outbox") || !strings.Contains(c, "spindrift recover 1") {
						t.Errorf("comment %q must name the preserved bundle and `spindrift recover 1`", c)
					}
				}),
		}),
		// A stop during the create's backoff is an abandon, not a failure: no
		// comment, no park, and no lookup or re-create after the stop.
		{
			name: "a stop during a draft PR create's backoff abandons without a re-create", shape: shapeReadOnly, noPR: true,
			result: handoffResult("ready"),
			setup: g.readOnly(3, func(_ *testing.T, fc *forge.Fake, _ *Config) {
				fc.CreateDraftPRErr = errDraftPR504
				fc.On("CreateDraftPR", opensPR(fc))
				g.onSleep = func() { g.stop() }
			}),
			wire: g.arm, abandoned: true, check: draftPRCalls(1),
		},
		// The final create makes the PR and the stop lands before its backoff:
		// the lookup after the stop must not run, so nothing is adopted (#5172).
		{
			name: "a stop before the final lookup abandons with no retry budget", shape: shapeReadOnly, noPR: true,
			result: handoffResult("ready"),
			setup: g.readOnly(0, func(_ *testing.T, fc *forge.Fake, _ *Config) {
				fc.CreateDraftPRErr = errDraftPR504
				fc.OpenPRForBranchErrs = make([]error, 1) // one tripwire entry per lookup
			}),
			wire:      func(fc *forge.Fake, d *dispatch.Fake, mark func()) { g.arm(fc, d, mark); createdOnCall(fc, 1, mark) },
			abandoned: true, wantNoStatus: []Status{StatusDraftPRAdopted},
			check: allOf(draftPRCalls(1), func(t *testing.T, fc *forge.Fake, _ *dispatch.Fake) {
				if got := len(fc.OpenPRForBranchErrs); got != 1 {
					t.Errorf("unrun lookups = %d, want 1: the final lookup ran after the stop", got)
				}
			}),
		},
		{
			name: "a stop before the final lookup abandons after the retry budget", shape: shapeReadOnly, noPR: true,
			result: handoffResult("ready"),
			setup: g.readOnly(2, func(_ *testing.T, fc *forge.Fake, _ *Config) {
				fc.CreateDraftPRErr = errDraftPR504
				fc.OpenPRForBranchErrs = make([]error, 3)
			}),
			wire:      func(fc *forge.Fake, d *dispatch.Fake, mark func()) { g.arm(fc, d, mark); createdOnCall(fc, 3, mark) },
			abandoned: true, wantNoStatus: []Status{StatusDraftPRAdopted},
			check: allOf(draftPRCalls(3), func(t *testing.T, fc *forge.Fake, _ *dispatch.Fake) {
				if got := len(fc.OpenPRForBranchErrs); got != 1 {
					t.Errorf("unrun lookups = %d, want 1: the final lookup ran after the stop", got)
				}
			}),
		},
		// A PR the host made itself with reconstructed text and then adopted after a
		// failed attempt carries the reconstructed text, so the hand-off is announced
		// (issue #5173); created stays false.
		handedOff(prCase{
			name: "a reconstructed draft PR adopted after a create timeout is announced", result: reconstructed,
			setup: g.readOnly(3, func(_ *testing.T, fc *forge.Fake, _ *Config) {
				withoutIntent(fc)
				fc.CreateDraftPRErrs = []error{errDraftPR504}
				fc.On("CreateDraftPR", opensPR(fc))
			}),
			wantComments: announced, wantStepsLike: []Step{handoffStep(StatusReconstructed, "note=")},
		}),
		// The same when the lookup fails and the adapter's already-exists backstop
		// adopts the PR on the re-create.
		handedOff(prCase{
			name: "a reconstructed draft PR adopted by the already-exists backstop is announced", result: reconstructed,
			setup: g.readOnly(3, func(_ *testing.T, fc *forge.Fake, _ *Config) {
				withoutIntent(fc)
				fc.CreateDraftPRErrs = []error{errDraftPR504}
				fc.OpenPRForBranchErrs = []error{errLookup}
				fc.CreateDraftPRAdoptHead, fc.CreateDraftPRAdoptedURL = fc.AgentBranch("1"), testPR
			}),
			wantComments: announced, wantStepsLike: []Step{handoffStep(StatusReconstructed, "note=")},
		}),
		handedOff(prCase{
			name: "a PR-intent draft PR adopted after a create timeout has nothing to announce",
			setup: g.readOnly(3, func(_ *testing.T, fc *forge.Fake, _ *Config) {
				fc.CreateDraftPRErrs = []error{errDraftPR504}
				fc.On("CreateDraftPR", opensPR(fc))
			}),
			wantNoStatus: []Status{StatusReconstructed},
		}),
		// A first-attempt created=false is a pre-existing PR, which never received
		// the reconstructed text.
		handedOff(prCase{
			name: "a reconstructed hand-off onto a PR that already existed is not announced", result: reconstructed,
			setup: g.readOnly(3, func(_ *testing.T, fc *forge.Fake, _ *Config) {
				withoutIntent(fc)
				fc.CreateDraftPRAdoptHead, fc.CreateDraftPRAdoptedURL = fc.AgentBranch("1"), testPR
			}),
			wantNoStatus: []Status{StatusReconstructed}, check: draftPRCalls(1),
		}),
	}
	return cases
}
