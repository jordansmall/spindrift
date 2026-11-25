package github

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/backend/backendrows"
	"spindrift.dev/launcher/internal/connectorhost"
	"spindrift.dev/launcher/internal/dedupmarker"
	"spindrift.dev/launcher/internal/forge"
)

// routedWrites is every tracker write a routed row routes, each driven
// through the production execClient. seed prepares the issue the write acts on.
var routedWrites = []struct {
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
	{"CloseMergedIssue", forge.Issue{Number: "7"},
		func(c *execClient) error { return c.CloseMergedIssue("7") },
		func(i *restIssue) bool { return i.closed && !i.notPlanned }},
	{"PostIssue", forge.Issue{Number: "7"},
		func(c *execClient) error {
			_, err := c.PostIssue("a finding", "its body", []string{"agent-butler-finding"})
			return err
		},
		nil},
}

func connectorWriteClient(t *testing.T) *execClient {
	return newClient(t, connectorRepo, testLabels, WithVerdictLabels(forge.ResearchVerdictLabels()))
}

// Under a routed row each tracker write reaches the Connector's backend and
// runs no gh.
func TestConnectorRow_TrackerWritesGoThroughTheConnector(t *testing.T) {
	for _, tc := range routedWrites {
		t.Run(tc.name, func(t *testing.T) {
			h, _, calls, _ := routedLogHarness(t, useGithubRowAt)
			h.SeedIssue(tc.seed)

			if err := tc.write(connectorWriteClient(t)); err != nil {
				t.Fatalf("%s: %v", tc.name, err)
			}

			if n := calls(); n != 0 {
				t.Errorf("%s ran gh %d times under a routed row", tc.name, n)
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

// A host-filed finding lands in the Connector's backend with its labels, and
// the backlog read through the Connector returns the dedup marker the launcher
// appended, so finding dedup and butler promotion see it as a gh-filed one.
// settle's own sweep is not driven here: the backend go-test fileset leaves
// settle out (nix/checks/go.nix backendGoTestShared).
func TestConnectorRow_PostedIssueKeepsItsMarkerAndLabels(t *testing.T) {
	h, _, calls, _ := routedLogHarness(t, useGithubRowAt)
	c := connectorWriteClient(t)
	marker := dedupmarker.Build([]string{"pkg/a.go"})
	labels := []string{"agent-butler-finding", "ready-for-agent"}

	url, err := c.PostIssue("a finding", dedupmarker.Append("Finding detail.", marker), labels)
	if err != nil {
		t.Fatalf("PostIssue: %v", err)
	}

	backlog, err := c.ListIssuesWithLabels(forge.IssueOpen, []string{"agent-butler-finding"})
	if err != nil {
		t.Fatalf("ListIssuesWithLabels: %v", err)
	}
	if len(backlog) != 1 {
		t.Fatalf("backlog = %+v, want the one filed issue", backlog)
	}
	got := backlog[0]
	// The butler reads the filed issue's number off the URL's last path segment.
	if want := h.backend.web + "/issues/" + got.Number; url != want {
		t.Errorf("url = %q, want %q", url, want)
	}
	if terms := dedupmarker.Parse(got.Body); !slices.Equal(terms, []string{"pkg/a.go"}) {
		t.Errorf("backlog body %q parses to dedup terms %v, want [pkg/a.go]", got.Body, terms)
	}
	for _, l := range labels {
		if !slices.Contains(got.Labels, l) {
			t.Errorf("label %q missing from %v", l, got.Labels)
		}
	}
	if n := calls(); n != 0 {
		t.Errorf("filing ran gh %d times under a routed row", n)
	}
}

// A claim on an issue already in progress is refused by the Connector path
// with forge.ErrAlreadyClaimed, and without gh.
func TestConnectorRow_ClaimOfAClaimedIssueIsRefused(t *testing.T) {
	h, _, calls, _ := routedLogHarness(t, useGithubRowAt)
	h.SeedIssue(forge.Issue{Number: "7", Labels: []string{testLabels.InProgress}})

	err := connectorWriteClient(t).TransitionState("7", forge.Dispatchable, forge.InProgress)

	if !errors.Is(err, forge.ErrAlreadyClaimed) {
		t.Errorf("TransitionState of a claimed issue = %v, want ErrAlreadyClaimed", err)
	}
	if n := calls(); n != 0 {
		t.Errorf("the refused claim ran gh %d times", n)
	}
}

// A plain row routes the writes through the Connector, never gh.
func TestTrackerWrites_PlainRowRoutesWrites(t *testing.T) {
	for _, tc := range routedWrites {
		t.Run(tc.name, func(t *testing.T) {
			h, _, calls, _ := routedLogHarness(t, useGithubRowAt)
			h.SeedIssue(tc.seed)

			_ = tc.write(connectorWriteClient(t))

			if n := calls(); n != 0 {
				t.Errorf("%s under a plain row ran gh %d times", tc.name, n)
			}
		})
	}
}

// A Connector that fails to load fails each routed write with the load error,
// never reaching gh.
func TestPlainRow_LoadFailureFailsTheWrite(t *testing.T) {
	for _, tc := range routedWrites {
		t.Run(tc.name, func(t *testing.T) {
			_, _, calls, _ := routedLogHarness(t, useGithubRowAt)
			boom := errors.New("github connector failed to load")
			hookLoads(t, func(func() (*connectorhost.Connector, error)) (*connectorhost.Connector, error) { return nil, boom })

			if err := tc.write(connectorWriteClient(t)); !errors.Is(err, boom) {
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
func TestPlainRow_RefusedRouteFailsTheWrite(t *testing.T) {
	for _, tc := range routedWrites {
		t.Run(tc.name, func(t *testing.T) {
			h, _, calls, _ := routedLogHarness(t, func(t *testing.T, host string) {
				applyGithubRow(t, host, func(r *backendrows.Row) {
					r.Routes = r.Routes[:0:0]
					for _, rt := range allRoutes(t, host) {
						if !strings.Contains(rt.Path, "/issues") && !strings.Contains(rt.Path, "/labels") {
							r.Routes = append(r.Routes, rt)
						}
					}
				})
			})
			h.SeedIssue(tc.seed)

			if err := tc.write(connectorWriteClient(t)); !errors.Is(err, connectorhost.ErrRouteRefused) {
				t.Errorf("err = %v, want the route refusal", err)
			}
			if n := calls(); n != 0 {
				t.Errorf("the refused write ran gh %d times", n)
			}
		})
	}
}

// A Connector that does not advertise what a write needs fails it with the
// named capability error instead of reaching gh.
func TestConnectorRow_MissingCapabilityFailsTheWrite(t *testing.T) {
	for _, tc := range []struct{ write, capName string }{
		{"TransitionState", "tracker.editLabels"},
		{"TransitionState", "tracker.transition"},
		{"CompleteVerdict", "tracker.editLabels"},
		{"CompleteVerdict", "tracker.transition"},
		{"AddLabels", "tracker.editLabels"},
		{"Comment", "tracker.comment"},
		{"CreateLabel", "tracker.labels"},
		{"PostIssue", "tracker.fileIssue"},
		{"CloseMergedIssue", "tracker.closeIssue"},
	} {
		t.Run(tc.write+"/"+tc.capName, func(t *testing.T) {
			h, _, calls, _ := routedLogHarness(t, useGithubRowAt)
			withoutCapability(t, tc.capName)
			matched := false
			for _, w := range routedWrites {
				if w.name != tc.write {
					continue
				}
				matched = true
				h.SeedIssue(w.seed)
				if err := w.write(connectorWriteClient(t)); !errors.Is(err, errNoCapability) {
					t.Errorf("err = %v, want errNoCapability", err)
				}
			}
			if !matched {
				t.Fatalf("no routed write named %q", tc.write)
			}
			if n := calls(); n != 0 {
				t.Errorf("the unsupported write ran gh %d times", n)
			}
		})
	}
}

// A lost claim race is core policy, not a Connector fault, so it leaves the
// Connector record empty; any other failed write is recorded.
func TestNoteRouted_SkipsALostClaimRace(t *testing.T) {
	_, _, _, logDir := routedLogHarness(t, useGithubRowAt)

	claimed := fmt.Errorf("tracker: issue 7: %w", forge.ErrAlreadyClaimed)
	if got := noteRouted("transition an issue", claimed); got != claimed {
		t.Errorf("noteRouted = %v, want the error unchanged", got)
	}
	if logs := connectorFiles(t, logDir, ".log"); len(logs) != 0 {
		t.Errorf("a lost claim race wrote Connector logs %v", logs)
	}

	_ = noteRouted("transition an issue", errors.New("boom"))
	if logs := connectorFiles(t, logDir, ".log"); len(logs) != 1 {
		t.Errorf("a failed write wrote Connector logs %v, want one", logs)
	}
}
