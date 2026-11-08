package settle

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"spindrift.dev/launcher/internal/dispatch"
	"spindrift.dev/launcher/internal/dispatchrecord"
	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/seambundle"
)

func bundleExists(t *testing.T, outbox string) bool {
	t.Helper()
	_, err := os.Stat(filepath.Join(outbox, seambundle.FileName))
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("stat bundle: %v", err)
	}
	return err == nil
}

// bundleCases are the rows where the Box's seam.bundle outlives or does not
// outlive a landing (issue #4653): a finished issue must not leave a stale
// bundle behind, but only a settle that verified the merge may drop it; every
// other outcome keeps it as the evidence a human or `spindrift recover` works
// from.
func bundleCases() []prCase {
	g := &handoffRig{}
	failed := []string{"agent-failed"}
	// The leading Pending proves the checks registered, which Recover's open-PR arm waits for.
	registered := []forge.RollupState{forge.StatePending, green, green}
	flipThenMerge := []string{"MarkReady:" + testPR, "Merge:" + testPR}
	flipOnly := []string{"MarkReady:" + testPR}
	workflowFile := []string{".github/workflows/ci.yml"}
	bundled := g.setup(0, func(t *testing.T, fc *forge.Fake, c *Config) { g.bundled(t, fc, c) })
	landed := func(c prCase) prCase {
		c.entry, c.checks = entryAdopted, registered
		c.want, c.wantLabels, c.wantNoLabels, c.wantMerged, c.wantCalls = merged, complete, failed, testPR, flipThenMerge
		c.wantSteps = append(c.wantSteps, stepAt(testPR, StatusVerifiedMerged, ""))
		return c
	}
	adopted := func(reason string) Settlement { return Settlement{Verdict: ForReason(reason), PRURL: testPR} }

	return []prCase{
		landed(prCase{name: "adopted: a verified merge removes the bundle", wantStatuses: []Status{StatusAdopted, StatusVerifiedMerged}, setup: bundled, check: g.bundle(bundleDropped)}),
		{
			name: "adopted: a gate that ends terminal keeps the bundle", entry: entryAdopted, checks: []forge.RollupState{red},
			wantStatuses: []Status{StatusAdopted, StatusFailed},
			setup:        bundled, want: Settlement{Verdict: ForReason(dispatchrecord.ReasonCIRed), PRURL: testPR, Note: "ci-red: still red after exhausting 0 fix pass(es)"}, wantPRClosed: true, wantLabels: failed,
			check: g.bundle(bundleKept),
		},
		{
			name: "adopted: manual mode keeps the bundle", entry: entryAdopted, checks: registered, mergeMode: "manual",
			wantStatuses: []Status{StatusAdopted, StatusAgentComplete},
			setup:        bundled, want: adopted(dispatchrecord.ReasonManual), wantLabels: complete, wantCalls: flipOnly,
			check: g.bundle(bundleKept),
		},
		{
			name: "adopted: an enqueued auto-merge keeps the bundle", entry: entryAdopted, checks: registered, mergeMode: "auto",
			wantStatuses: []Status{StatusAdopted, StatusAutoMergeEnqueued},
			setup:        bundled, want: adopted(dispatchrecord.ReasonAutoMergeEnqueued), wantLabels: complete,
			wantCalls: []string{"MarkReady:" + testPR, "EnqueueAutoMerge:" + testPR}, check: g.bundle(bundleKept),
		},
		{
			name: "adopted: a merge guard hit keeps the bundle", entry: entryAdopted, checks: registered, guard: ".github/**",
			wantStatuses: []Status{StatusAdopted, StatusMergeGuardHit, StatusMergeableUnknown},
			files:        workflowFile, setup: bundled, want: adopted(dispatchrecord.ReasonMergeGuardHit), wantLabels: complete,
			wantCalls: flipOnly, check: g.bundle(bundleKept),
		},
		{
			name: "adopted: a blocked merge keeps the bundle", entry: entryAdopted, checks: registered,
			wantStatuses: []Status{StatusAdopted, StatusMergeBlocked},
			setup: g.setup(0, func(t *testing.T, fc *forge.Fake, c *Config) {
				g.bundled(t, fc, c)
				fc.MergeErr = errors.New("required review missing")
			}),
			want: adopted(dispatchrecord.ReasonMergeBlocked), wantLabels: complete, wantNoLabels: failed, wantCalls: flipThenMerge,
			wantComments: []string{"merge blocked after green CI: required review missing"}, check: g.bundle(bundleKept),
		},
		// The merge gate can go green and Merge return nil while the forge still
		// reports the PR unmerged; verifyMerged then fails the issue, and a failed
		// settle keeps its bundle.
		{
			name:         "adopted: a merge the forge does not report merged fails the settle and keeps the bundle",
			wantStatuses: []Status{StatusAdopted, StatusFailed},
			entry:        entryAdopted, checks: registered, setup: bundled,
			afterMerge: func(fc *forge.Fake) { fc.SetPRState(testPR, forge.PROpen) },
			want:       Settlement{Verdict: ForReason(dispatchrecord.ReasonMergeUnverified), PRURL: testPR, Note: "PR state is 'OPEN', expected MERGED"}, wantPRClosed: true, wantLabels: failed,
			wantMerged: testPR, wantCalls: flipThenMerge, wantSteps: []Step{stepAt(testPR, StatusFailed, "!! PR state is 'OPEN', expected MERGED")}, check: g.bundle(bundleKept),
		},
		// The outbox also holds the pass manifest, so only the bundle file goes.
		landed(prCase{
			name:         "adopted: a verified merge leaves the outbox's other files",
			wantStatuses: []Status{StatusAdopted, StatusVerifiedMerged},
			setup: g.setup(0, func(t *testing.T, fc *forge.Fake, c *Config) {
				g.bundled(t, fc, c)
				if err := os.WriteFile(filepath.Join(g.outbox, "other.json"), []byte("{}"), 0o644); err != nil {
					t.Fatal(err)
				}
			}),
			check: allOf(g.bundle(bundleDropped), func(t *testing.T, _ *forge.Fake, _ *dispatch.Fake) {
				if _, err := os.Stat(filepath.Join(g.outbox, "other.json")); err != nil {
					t.Errorf("unrelated outbox file must survive: %v", err)
				}
			}),
		}),
		// A missing bundle (e.g. a Box that wrote none) is a silent no-op, and an
		// unset OutboxDir never panics.
		landed(prCase{
			name: "adopted: a verified merge with no bundle in the outbox is silent", setup: g.setup(0, nil),
			wantStatuses: []Status{StatusAdopted, StatusVerifiedMerged},
		}),
		landed(prCase{
			name:         "adopted: a verified merge with an unset OutboxDir is silent",
			wantStatuses: []Status{StatusAdopted, StatusVerifiedMerged},
			setup:        func(_ *testing.T, _ *forge.Fake, c *Config) { c.OutboxDir = nil },
		}),
		// A failed removal is logged and must never fail the landing.
		landed(prCase{
			name:         "adopted: a bundle that cannot be removed is logged and the landing still merges",
			wantStatuses: []Status{StatusAdopted, StatusVerifiedMerged, StatusBundleRemoveFailed},
			setup: g.setup(0, func(t *testing.T, _ *forge.Fake, _ *Config) {
				// A non-empty directory in the bundle's place makes os.Remove fail.
				if err := os.MkdirAll(filepath.Join(g.outbox, seambundle.FileName, "child"), 0o755); err != nil {
					t.Fatal(err)
				}
			}),
			wantStepsLike: []Step{{Key: "1", Status: StatusBundleRemoveFailed, Detail: "!! remove "}},
		}),
		// A blocked hand-off never reaches the landing, so its bundle stays.
		{
			name: "a blocked hand-off keeps the bundle", shape: shapeReadOnly, noPR: true, result: handoffResult("blocked"),
			wantStatuses: []Status{detailOnly, StatusBlocked},
			setup:        g.readOnly(0, func(t *testing.T, fc *forge.Fake, c *Config) { g.bundled(t, fc, c) }),
			want:         Settlement{Verdict: ForReason(dispatchrecord.ReasonBlocked), PRURL: testPR, Note: "stuck"}, wantPRClosed: true, wantLabels: failed, wantComments: []string{"stuck"},
			check: g.bundle(bundleKept),
		},
		// Recover's relay arm lands through the same gate and drops the bundle too.
		{
			name: "a relayed branch that merges drops the bundle", shape: shapeReadOnly, noPR: true,
			wantStatuses: []Status{StatusAdopted, StatusVerifiedMerged},
			entry:        entryRelayed, checks: registered,
			setup: g.readOnly(0, func(t *testing.T, fc *forge.Fake, c *Config) { g.bundled(t, fc, c) }),
			want:  merged, wantLabels: complete, wantMerged: testPR, wantCalls: flipThenMerge, check: g.bundle(bundleDropped),
		},
	}
}
