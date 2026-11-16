package github

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/backend/backendrows"
	"spindrift.dev/launcher/internal/connectorhost"
	"spindrift.dev/launcher/internal/forge"
)

// primaryWrites is every tracker write a primary row routes, each driven
// through the production execClient. seed prepares the issue the write acts on.
var primaryWrites = []struct {
	name  string
	seed  forge.Issue
	write func(c *execClient) error
	// landed reports what the scripted backend then holds for issue "7".
	landed func(i *restIssue) bool
}{
	{"TransitionState", forge.Issue{Number: "7", Labels: []string{testLabels.Dispatchable}},
		func(c *execClient) error { return c.TransitionState("7", forge.Dispatchable, forge.InProgress) },
		func(i *restIssue) bool {
			return slices.Contains(i.labels, testLabels.InProgress) && !slices.Contains(i.labels, testLabels.Dispatchable)
		}},
	{"CompleteVerdict", forge.Issue{Number: "7", Labels: []string{testLabels.InProgress}},
		func(c *execClient) error { return c.CompleteVerdict("7", forge.Recommend) },
		func(i *restIssue) bool {
			return !slices.Contains(i.labels, testLabels.InProgress) && slices.Contains(i.labels, forge.ResearchVerdictLabels().Label(forge.Recommend))
		}},
	{"AddLabels", forge.Issue{Number: "7"},
		func(c *execClient) error { return c.AddLabels("7", []string{"agent-butler-finding"}) },
		func(i *restIssue) bool { return slices.Contains(i.labels, "agent-butler-finding") }},
	{"Comment", forge.Issue{Number: "7"},
		func(c *execClient) error { return c.Comment("7", "hello from the host") },
		func(i *restIssue) bool { return slices.Contains(i.comments, "hello from the host") }},
	{"CreateLabel", forge.Issue{Number: "7"},
		func(c *execClient) error { return c.CreateLabel("fresh-label", "a new label", "ededed") },
		nil},
}

func primaryWriteClient() *execClient {
	return NewExecClient(connectorRepo, testLabels, WithVerdictLabels(forge.ResearchVerdictLabels()))
}

// Under a primary row each tracker write reaches the Connector's backend and
// runs no gh.
func TestPrimaryRow_TrackerWritesGoThroughTheConnector(t *testing.T) {
	for _, tc := range primaryWrites {
		t.Run(tc.name, func(t *testing.T) {
			h, _, calls, _ := primaryHarness(t, useGithubRowPrimary)
			h.SeedIssue(tc.seed)

			if err := tc.write(primaryWriteClient()); err != nil {
				t.Fatalf("%s: %v", tc.name, err)
			}

			if n := calls(); n != 0 {
				t.Errorf("%s ran gh %d times under a primary row", tc.name, n)
			}
			h.backend.mu.Lock()
			defer h.backend.mu.Unlock()
			if tc.landed != nil && !tc.landed(h.backend.find("7")) {
				t.Errorf("%s did not reach the backend: %+v", tc.name, h.backend.find("7"))
			}
			if tc.name == "CreateLabel" && !slices.Contains(h.backend.labels, "fresh-label") {
				t.Errorf("CreateLabel did not reach the backend: labels %v", h.backend.labels)
			}
		})
	}
}

// A claim on an issue already in progress is refused by the Connector path
// with the same sentinel the native claim returns, and without gh.
func TestPrimaryRow_ClaimOfAClaimedIssueIsRefused(t *testing.T) {
	h, _, calls, _ := primaryHarness(t, useGithubRowPrimary)
	h.SeedIssue(forge.Issue{Number: "7", Labels: []string{testLabels.InProgress}})

	err := primaryWriteClient().TransitionState("7", forge.Dispatchable, forge.InProgress)

	if !errors.Is(err, forge.ErrAlreadyClaimed) {
		t.Errorf("TransitionState of a claimed issue = %v, want ErrAlreadyClaimed", err)
	}
	if n := calls(); n != 0 {
		t.Errorf("the refused claim ran gh %d times", n)
	}
}

// Without a primary row the same writes still run gh.
func TestTrackerWrites_StayOnGhWithoutAPrimaryRow(t *testing.T) {
	for rowName, useRow := range map[string]func(*testing.T, string){"plain": useGithubRowAt, "shadow": useGithubRowShadow} {
		for _, tc := range primaryWrites {
			t.Run(rowName+"/"+tc.name, func(t *testing.T) {
				h, _, calls, _ := primaryHarness(t, useRow)
				h.SeedIssue(tc.seed)

				_ = tc.write(primaryWriteClient())

				if calls() == 0 {
					t.Errorf("%s under a %s row never reached gh", tc.name, rowName)
				}
			})
		}
	}
}

// A Connector that fails to load fails each primary write with the load error,
// never reaching gh.
func TestPrimaryRow_LoadFailureFailsTheWrite(t *testing.T) {
	for _, tc := range primaryWrites {
		t.Run(tc.name, func(t *testing.T) {
			_, _, calls, _ := primaryHarness(t, useGithubRowPrimary)
			boom := errors.New("github connector failed to load")
			old := loadRowConnector
			t.Cleanup(func() { loadRowConnector = old })
			loadRowConnector = func(context.Context, backendrows.Row, backendrows.Options) (*connectorhost.Connector, error) {
				return nil, boom
			}

			if err := tc.write(primaryWriteClient()); !errors.Is(err, boom) {
				t.Errorf("err = %v, want the load error", err)
			}
			if n := calls(); n != 0 {
				t.Errorf("the failed write ran gh %d times", n)
			}
		})
	}
}

// A write outside the row's Routes is refused by the host: it fails with that
// refusal and never falls back to gh.
func TestPrimaryRow_RefusedRouteFailsTheWrite(t *testing.T) {
	for _, tc := range primaryWrites {
		t.Run(tc.name, func(t *testing.T) {
			h, _, calls, _ := primaryHarness(t, func(t *testing.T, host string) {
				applyGithubRow(t, host, func(r *backendrows.Row) {
					r.Primary = true
					r.Routes = r.Routes[:0:0]
					for _, rt := range allRoutes(t, host) {
						if !strings.Contains(rt.Path, "/issues") && !strings.Contains(rt.Path, "/labels") {
							r.Routes = append(r.Routes, rt)
						}
					}
				})
			})
			h.SeedIssue(tc.seed)

			if err := tc.write(primaryWriteClient()); !errors.Is(err, connectorhost.ErrRouteRefused) {
				t.Errorf("err = %v, want the route refusal", err)
			}
			if n := calls(); n != 0 {
				t.Errorf("the refused write ran gh %d times", n)
			}
		})
	}
}

// A Connector that does not advertise what a write needs fails it with the
// named capability error instead of reaching the nil native tracker or gh.
func TestPrimaryRow_MissingCapabilityFailsTheWrite(t *testing.T) {
	for _, tc := range []struct{ write, capName string }{
		{"TransitionState", "tracker.editLabels"},
		{"TransitionState", "tracker.transition"},
		{"CompleteVerdict", "tracker.editLabels"},
		{"CompleteVerdict", "tracker.transition"},
		{"AddLabels", "tracker.editLabels"},
		{"Comment", "tracker.comment"},
		{"CreateLabel", "tracker.labels"},
	} {
		t.Run(tc.write+"/"+tc.capName, func(t *testing.T) {
			h, _, calls, _ := primaryHarness(t, useGithubRowPrimary)
			withoutCapability(t, tc.capName)
			matched := false
			for _, w := range primaryWrites {
				if w.name != tc.write {
					continue
				}
				matched = true
				h.SeedIssue(w.seed)
				if err := w.write(primaryWriteClient()); !errors.Is(err, errNoCapability) {
					t.Errorf("err = %v, want errNoCapability", err)
				}
			}
			if !matched {
				t.Fatalf("no primary write named %q", tc.write)
			}
			if n := calls(); n != 0 {
				t.Errorf("the unsupported write ran gh %d times", n)
			}
		})
	}
}

// A lost claim race is core policy, not a Connector fault, so it leaves the
// primary log empty; any other failed write is recorded.
func TestNotePrimary_SkipsALostClaimRace(t *testing.T) {
	_, _, _, logDir := primaryHarness(t, useGithubRowPrimary)
	row := backendrows.Row{Primary: true}

	claimed := fmt.Errorf("tracker: issue 7: %w", forge.ErrAlreadyClaimed)
	if got := notePrimary(row, "transition an issue", claimed); got != claimed {
		t.Errorf("notePrimary = %v, want the error unchanged", got)
	}
	if logs := primaryFiles(t, logDir, ".log"); len(logs) != 0 {
		t.Errorf("a lost claim race wrote primary logs %v", logs)
	}

	_ = notePrimary(row, "transition an issue", errors.New("boom"))
	if logs := primaryFiles(t, logDir, ".log"); len(logs) != 1 {
		t.Errorf("a failed write wrote primary logs %v, want one", logs)
	}
}
