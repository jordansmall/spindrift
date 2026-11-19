package github

import (
	"context"
	"testing"

	"spindrift.dev/launcher/internal/connectorhost"
	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/hostop"
	"spindrift.dev/launcher/internal/readshadow"
)

// The read shadow compares the Connector's dispatchable list with native's at
// canonical types, strictly. Native decodes `gh issue list --json
// number,title,labels`, so Body and State come back empty; the Connector must
// list the same projection or every sampled ListIssues diverges.
func TestConnector_DispatchableListMatchesNativeProjection(t *testing.T) {
	conn := newComposedHarness(t)
	native := newGithubHarness(t)

	dispatch := testLabels.Label(forge.Dispatchable)
	for _, iss := range []forge.Issue{
		{Number: "7", Title: "seven", Body: "body of seven", Labels: []string{dispatch, "agent-priority-high"}},
		{Number: "3", Title: "three", Body: "body of three", Labels: []string{dispatch}},
		{Number: "5", Title: "not dispatchable", Body: "body of five", Labels: []string{"bug"}},
	} {
		conn.SeedIssue(iss)
		native.SeedIssue(iss)
	}

	var ops []hostop.SpindriftOp
	sh := readshadow.New(readshadow.Config{
		Backend: "github", Every: 1,
		Emit: func(op hostop.SpindriftOp) { ops = append(ops, op) },
		Open: func(context.Context, func(connectorhost.AuditEntry)) (readshadow.Reads, error) {
			return readshadow.Reads{Tracker: conn.tr}, nil
		},
	})
	got, err := sh.Tracker(native.tr).ListIssues(forge.Dispatchable)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("native ListIssues = %+v, want issues 3 and 7", got)
	}
	sh.Close(context.Background())

	var sums []hostop.ShadowSummary
	for _, op := range ops {
		if op.ShadowSummary != nil {
			sums = append(sums, *op.ShadowSummary)
		}
		if op.Divergence != nil {
			t.Errorf("divergence: %+v", *op.Divergence)
		}
	}
	if len(sums) != 1 || sums[0].Compared < 1 || sums[0].Diverged != 0 {
		t.Fatalf("summaries = %+v, want one with Compared >= 1 and Diverged == 0", sums)
	}
}
