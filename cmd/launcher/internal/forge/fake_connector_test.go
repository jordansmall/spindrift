package forge

import (
	"errors"
	"sync"
	"testing"

	"spindrift.dev/launcher/internal/connectorhost"
)

// observeReads records the host's audit entry for each read f sends through the
// Connector, for the rest of the test.
func observeReads(t *testing.T, f *Fake) func() []connectorhost.AuditEntry {
	t.Helper()
	var mu sync.Mutex
	var got []connectorhost.AuditEntry
	observers.Store(f.core, func(e connectorhost.AuditEntry) {
		mu.Lock()
		defer mu.Unlock()
		got = append(got, e)
	})
	t.Cleanup(func() { observers.Delete(f.core) })
	return func() []connectorhost.AuditEntry {
		mu.Lock()
		defer mu.Unlock()
		return append([]connectorhost.AuditEntry(nil), got...)
	}
}

func TestFakeTrackerReadsCrossTheConnector(t *testing.T) {
	f := NewFake(DispatchLabels{Dispatchable: "ready"})
	f.SetIssue(Issue{Number: "1", Title: "a", Labels: []string{"ready"}})
	reads := observeReads(t, f)

	if _, err := f.Issue("1"); err != nil {
		t.Fatal(err)
	}
	got := reads()
	if len(got) != 1 {
		t.Fatalf("Issue crossed the Connector %d times, want 1", len(got))
	}
	if a := got[0]; a.Route != "GET fake.invalid /tracker" || a.Path != "/tracker/issue" || a.Decision != "sent" {
		t.Errorf("audit = %+v, want a sent read on route GET fake.invalid /tracker", a)
	}
}

func TestFakeTrackerReadKeepsScriptedErrorIdentity(t *testing.T) {
	sentinel := errors.New("scripted")
	f := NewFake()
	f.IssueErrByNum = map[string]error{"7": sentinel}
	if _, err := f.Issue("7"); !errors.Is(err, sentinel) {
		t.Errorf("Issue err = %v, want the scripted sentinel", err)
	}
	f.ListIssuesErr = sentinel
	if _, err := f.ListIssues(Dispatchable); !errors.Is(err, sentinel) {
		t.Errorf("ListIssues err = %v, want the scripted sentinel", err)
	}
}

func TestFakeTrackerReadsObserveWrites(t *testing.T) {
	f := NewFake(DispatchLabels{Dispatchable: "ready", InProgress: "wip"})
	f.SetIssue(Issue{Number: "1", Labels: []string{"ready"}})
	if err := f.TransitionState("1", Dispatchable, InProgress); err != nil {
		t.Fatal(err)
	}
	iss, err := f.Issue("1")
	if err != nil || len(iss.Labels) != 1 || iss.Labels[0] != "wip" {
		t.Fatalf("Issue = %+v, %v, want the transition's label", iss, err)
	}
	if got, _ := f.ListIssues(Dispatchable); len(got) != 0 {
		t.Errorf("ListIssues(Dispatchable) = %+v, want none after the claim", got)
	}
	if got, _ := f.ListIssues(InProgress); len(got) != 1 {
		t.Errorf("ListIssues(InProgress) = %+v, want the claimed issue", got)
	}
}

func TestFakeTrackerManyReadsShareOneConnector(t *testing.T) {
	f := NewFake()
	f.SetIssue(Issue{Number: "1"})
	for i := range 400 {
		if _, err := f.Issue("1"); err != nil {
			t.Fatalf("read %d: %v", i, err)
		}
	}
}
