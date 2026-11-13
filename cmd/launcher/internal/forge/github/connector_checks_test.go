package github

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/forge"
)

// noGH puts a gh on PATH that fails every call and records it, and returns a
// function reporting how many times it was run: the Connector-backed paths
// reach GitHub through the host's http_request call, never the gh CLI.
func noGH(t *testing.T) (calls func() int) {
	t.Helper()
	dir := prependFakeGH(t, "exit 99")
	return func() int {
		m, _ := filepath.Glob(filepath.Join(dir, "call-*.txt"))
		return len(m)
	}
}

// ghProbeStub scripts the gh the native Probe runs: `auth status` fails
// without a credential, `repo view` fails for a repository gh cannot see.
func ghProbeStub(t *testing.T, status int) {
	t.Helper()
	prependFakeGH(t, `case "$*" in
"auth status")
	[ `+strconv.Itoa(status)+` = 401 ] && { echo "You are not logged in" >&2; exit 1; }
	exit 0 ;;
"repo view"*)
	[ `+strconv.Itoa(status)+` = 404 ] && { echo "Could not resolve to a Repository" >&2; exit 1; }
	printf 'owner/repo\n' ;;
esac`)
}

func errClass(err error) [3]bool {
	return [3]bool{err == nil, errors.Is(err, forge.ErrAuthFailure), errors.Is(err, forge.ErrRepoNotFound)}
}

func TestConnector_ProbeMatchesNative(t *testing.T) {
	for _, status := range []int{200, 401, 404} {
		h := newConnectorHarness(t)
		if status != 200 {
			h.backend.listStatus = status
		}
		calls := noGH(t)
		gotSlug, gotErr := h.tr.Probe()
		if n := calls(); n != 0 {
			t.Errorf("status %d: the Connector's Probe ran gh %d times", status, n)
		}

		ghProbeStub(t, status)
		wantSlug, wantErr := NewExecClient(connectorRepo, testLabels).Probe()

		if gotSlug != wantSlug || errClass(gotErr) != errClass(wantErr) {
			t.Errorf("status %d: Connector Probe = (%q, %v), native (%q, %v)", status, gotSlug, gotErr, wantSlug, wantErr)
		}
		if status == 200 && (gotErr != nil || gotSlug != connectorRepo) {
			t.Errorf("good credential: Probe = (%q, %v), want (%q, nil)", gotSlug, gotErr, connectorRepo)
		}
		if status == 401 && !errors.Is(gotErr, forge.ErrAuthFailure) {
			t.Errorf("auth failure: Probe err = %v, want forge.ErrAuthFailure", gotErr)
		}
		if status == 404 && !errors.Is(gotErr, forge.ErrRepoNotFound) {
			t.Errorf("missing repo: Probe err = %v, want forge.ErrRepoNotFound", gotErr)
		}
	}
}

// A demand count is the same number whether the native adapter reads it through
// gh or the Connector through the host: the Dispatchable issues, pull requests
// excluded, however many items sit in other states.
func TestConnector_CountReadyMatchesNative(t *testing.T) {
	type item struct {
		num    string
		labels []string
		pull   bool
	}
	ready, active := []string{testLabels.Dispatchable}, []string{testLabels.InProgress}
	for _, fx := range []struct {
		name  string
		items []item
		want  int
	}{
		{"none", nil, 0},
		{"mixed", []item{
			{"1", ready, false}, {"2", ready, true}, {"3", active, false},
			{"4", ready, false}, {"5", nil, false}, {"6", ready, false},
		}, 3},
	} {
		t.Run(fx.name, func(t *testing.T) {
			h := newConnectorHarness(t)
			calls := noGH(t)
			for _, it := range fx.items {
				h.SeedIssue(forge.Issue{Number: it.num, Title: it.num, Labels: it.labels})
				h.mutate(it.num, func(i *restIssue) { i.pull = it.pull })
			}
			dc, ok := forge.As[forge.DemandCounter](h.tr)
			if !ok {
				t.Fatal("Connector tracker does not serve CountReady: it must advertise tracker.count")
			}
			got, err := dc.CountReady(false)
			if err != nil || got != fx.want {
				t.Fatalf("Connector CountReady = %d, %v; want %d", got, err, fx.want)
			}
			if n := calls(); n != 0 {
				t.Errorf("the Connector's CountReady ran gh %d times", n)
			}
			if h.backend.unexpected != nil {
				t.Errorf("unexpected backend requests: %v", h.backend.unexpected)
			}

			n := newGithubHarness(t)
			for _, it := range fx.items {
				n.SeedIssue(forge.Issue{Number: it.num, Title: it.num, Labels: it.labels})
				if it.pull {
					if err := os.WriteFile(filepath.Join(n.issueDir(it.num), "pull_request"), nil, 0o644); err != nil {
						t.Fatal(err)
					}
				}
			}
			if want := countReady(t, n, false); got != want {
				t.Errorf("Connector CountReady = %d, native %d", got, want)
			}
		})
	}
}

func TestConnector_CountReadyFailsWhenTheListDoes(t *testing.T) {
	h := newConnectorHarness(t)
	h.backend.listStatus = 500
	dc, _ := forge.As[forge.DemandCounter](h.tr)
	if _, err := dc.CountReady(false); err == nil {
		t.Fatal("CountReady against a 500 succeeded")
	}
}

func TestConnector_ProbeIntervalMatchesNative(t *testing.T) {
	h := newConnectorHarness(t)
	dc, ok := forge.As[forge.DemandCounter](h.tr)
	if !ok {
		t.Fatal("Connector tracker does not serve the demand count")
	}
	if got := dc.ProbeInterval(); got != demandProbeInterval {
		t.Errorf("Connector ProbeInterval = %v, native %v", got, demandProbeInterval)
	}
}

// recover reads the terminal label a claim stripped from the timeline. The
// Connector returns the removed names and core picks the state, so the answer
// must be the native adapter's, which filters the same timeline through gh.
func TestConnector_PriorClaimStateMatchesNative(t *testing.T) {
	for _, fx := range []struct {
		name    string
		removed []string
		want    forge.DispatchState
		found   bool
	}{
		{"complete last", []string{testLabels.Failed, testLabels.Complete}, forge.Complete, true},
		{"failed last", []string{testLabels.Complete, testLabels.Failed}, forge.Failed, true},
		{"complete then claim strip", []string{testLabels.Complete, testLabels.Dispatchable}, forge.Complete, true},
		{"none found", []string{testLabels.Dispatchable, "bug"}, 0, false},
		{"no removals", nil, 0, false},
	} {
		t.Run(fx.name, func(t *testing.T) {
			h := newConnectorHarness(t)
			h.SeedIssue(forge.Issue{Number: "7", Title: "t"})
			h.mutate("7", func(i *restIssue) { i.unlabeled = fx.removed })
			calls := noGH(t)
			pr, ok := forge.As[forge.PriorClaimStateReader](h.tr)
			if !ok {
				t.Fatal("Connector tracker does not serve PriorClaimState: it must advertise tracker.removedLabels")
			}
			got, found, err := pr.PriorClaimState("7")
			if err != nil || found != fx.found || (found && got != fx.want) {
				t.Fatalf("Connector PriorClaimState = %v, %v, %v; want %v, %v", got, found, err, fx.want, fx.found)
			}
			if n := calls(); n != 0 {
				t.Errorf("the Connector's PriorClaimState ran gh %d times", n)
			}
			if h.backend.unexpected != nil {
				t.Errorf("unexpected backend requests: %v", h.backend.unexpected)
			}

			// The native adapter's gh --jq has already reduced the timeline to
			// the removed names, one a line.
			out := strings.Join(fx.removed, "\\n")
			prependFakeGH(t, `case "$*" in *timeline*) printf '`+out+`\n' ;; esac`)
			wantState, wantFound, err := NewExecClient(connectorRepo, testLabels).PriorClaimState("7")
			if err != nil || found != wantFound || (found && got != wantState) {
				t.Errorf("Connector PriorClaimState = (%v, %v), native (%v, %v, %v)", got, found, wantState, wantFound, err)
			}
		})
	}
}

func TestConnector_PriorClaimStateFailsWhenTheTimelineDoes(t *testing.T) {
	h := newConnectorHarness(t)
	pr, _ := forge.As[forge.PriorClaimStateReader](h.tr)
	if _, found, err := pr.PriorClaimState("99"); err == nil || found {
		t.Fatalf("PriorClaimState of a missing issue = %v, %v; want an error", found, err)
	}
}
