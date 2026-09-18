package console

import (
	"errors"
	"reflect"
	"testing"

	"spindrift.dev/launcher/internal/forge"
)

// issueTrackerOnly wraps a forge.IssueTracker so its dynamic type exposes only
// that interface's method set, hiding optional interfaces such as
// forge.BlockersLister that the wrapped value's concrete type implements. It
// simulates a tracker shaped like the local adapter (issue #1744).
type issueTrackerOnly struct{ forge.IssueTracker }

// resolveEdgeRefs reuses resolveBlockerRef, so each ref's title and state come
// from the backlog list rather than a second fetch (issue #1744).
func TestResolveEdgeRefs_ResolvesFromFetch(t *testing.T) {
	f := forge.NewFake()
	all := []forge.Issue{{Number: "7", Title: "backlog title", State: forge.IssueOpen}}
	fetch := func(string) ([]forge.Dependency, error) {
		return []forge.Dependency{{ID: "7", Source: forge.DepSourceNative}}, nil
	}

	refs := resolveEdgeRefs(f, all, fetch, "1")

	want := []BlockerRef{{Number: "7", Source: forge.DepSourceNative, State: forge.IssueOpen, Title: "backlog title"}}
	if !reflect.DeepEqual(refs, want) {
		t.Errorf("resolveEdgeRefs = %v, want %v", refs, want)
	}
}

// A transient DepsOf/BlocksOf failure must not fail the whole modal load, so a
// fetch error resolves to no refs instead of propagating (issue #1744).
func TestResolveEdgeRefs_FetchErrorReturnsNil(t *testing.T) {
	f := forge.NewFake()
	fetch := func(string) ([]forge.Dependency, error) {
		return nil, errors.New("boom")
	}

	refs := resolveEdgeRefs(f, nil, fetch, "1")

	if refs != nil {
		t.Errorf("resolveEdgeRefs = %v, want nil on fetch error", refs)
	}
}

// Opening the detail modal must not rebuild a whole-backlog readiness graph,
// so the cmd makes one DepsOf call for the opened ticket and none for any
// other issue (issue #1744).
func TestOpenDetailModalCmd_ResolvesBlockedByFromDepsOfOnly(t *testing.T) {
	f := forge.NewFake()
	f.SetIssue(forge.Issue{Number: "42", Title: "t", State: forge.IssueOpen})
	f.SetIssue(forge.Issue{Number: "7", Title: "blocker", State: forge.IssueOpen})
	f.NativeDeps = map[string][]string{"42": {"7"}}
	all := []forge.Issue{{Number: "42", Title: "t", State: forge.IssueOpen}, {Number: "7", Title: "blocker", State: forge.IssueOpen}}

	msg, ok := openDetailModalCmd(f, all, "42")().(DetailModalLoadedMsg)
	if !ok {
		t.Fatal("openDetailModalCmd did not return a DetailModalLoadedMsg")
	}

	want := []BlockerRef{{Number: "7", Source: forge.DepSourceNative, State: forge.IssueOpen, Title: "blocker"}}
	if !reflect.DeepEqual(msg.BlockedBy, want) {
		t.Errorf("BlockedBy = %v, want %v", msg.BlockedBy, want)
	}
	if want := []string{"42"}; !reflect.DeepEqual(f.DepsOfCalls, want) {
		t.Errorf("DepsOfCalls = %v, want %v", f.DepsOfCalls, want)
	}
}

// On a tracker with no native reverse-dependency concept, the local adapter's
// shape, the cmd resolves Blocks to nil rather than erroring or falling back
// to a whole-backlog scan (issue #1744).
func TestOpenDetailModalCmd_BlocksEmptyWhenTrackerLacksBlockersLister(t *testing.T) {
	f := forge.NewFake()
	f.SetIssue(forge.Issue{Number: "42", Title: "t", Body: "body text", State: forge.IssueOpen})
	tracker := issueTrackerOnly{f}

	msg, ok := openDetailModalCmd(tracker, nil, "42")().(DetailModalLoadedMsg)
	if !ok {
		t.Fatal("openDetailModalCmd did not return a DetailModalLoadedMsg")
	}

	if msg.Err != nil {
		t.Fatalf("Err = %v, want nil", msg.Err)
	}
	if msg.Blocks != nil {
		t.Errorf("Blocks = %v, want nil (no BlockersLister)", msg.Blocks)
	}
}

// A blocker already loaded in the backlog list resolves its title and state
// with no Issue fetch at all (issue #1632).
func TestResolveBlockerRef_PrefersBacklogOverFetch(t *testing.T) {
	f := forge.NewFake()
	f.SetIssue(forge.Issue{Number: "7", Title: "fetched title", State: forge.IssueOpen})
	all := []forge.Issue{{Number: "7", Title: "backlog title", State: forge.IssueOpen}}

	ref := resolveBlockerRef(f, all, "7", forge.DepSourceNative)

	if ref.Title != "backlog title" {
		t.Errorf("Title = %q, want the backlog row's title, not a fetched one", ref.Title)
	}
	if len(f.IssueCalls) != 0 {
		t.Errorf("IssueCalls = %v, want no fetch for a blocker already in the backlog", f.IssueCalls)
	}
}

// A blocker missing from the backlog list, for example one already closed, is
// resolved with its own Issue fetch (issue #1632).
func TestResolveBlockerRef_FetchesWhenNotInBacklog(t *testing.T) {
	f := forge.NewFake()
	f.SetIssue(forge.Issue{Number: "7", Title: "closed blocker", State: forge.IssueClosed})

	ref := resolveBlockerRef(f, nil, "7", forge.DepSourceBody)

	if ref.Title != "closed blocker" {
		t.Errorf("Title = %q, want the fetched issue's title", ref.Title)
	}
	if ref.State != forge.IssueClosed {
		t.Errorf("State = %q, want closed", ref.State)
	}
	if len(f.IssueCalls) != 1 || f.IssueCalls[0] != "7" {
		t.Errorf("IssueCalls = %v, want exactly one call for #7", f.IssueCalls)
	}
}
