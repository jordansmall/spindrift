package forge

import (
	"errors"
	"net/http"
	"reflect"
	"strings"
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

// Methods the bypass guard does not treat as reads. A method on the Fake that
// is in none of these sets, and has no Set or As prefix, must cross the
// Connector, so a newly added read is covered the moment it exists.
var (
	// fakeNativeWrites mutate the Fake's state natively: the tracker writes await
	// the tracker Connector (#6753), and RelayBundle is git in core, not a forge
	// HTTP primitive.
	fakeNativeWrites = setOf("TransitionState", "CompleteVerdict", "Comment", "CreateLabel",
		"AddLabels", "RecordLanding", "RecordLandingPass", "CloseIssue", "CloseMergedIssue",
		"FlagAbandoned", "PostIssue", "RelayBundle")
	// fakeForgeWrites are the forge writes, each with the HTTP method its Route
	// must be audited under. They cross the Connector like reads do.
	fakeForgeWrites = map[string]string{
		"EnqueueAutoMerge": http.MethodPost, "MarkReady": http.MethodPatch,
		"MarkDraft": http.MethodPatch, "CommentPR": http.MethodPost,
		"Merge": http.MethodPost, "Rebase": http.MethodPost, "CreateDraftPR": http.MethodPost,
	}
	// pendingForgeWrites are forge writes not yet converted to cross the
	// Connector; the guard skips them until they are.
	pendingForgeWrites = setOf("EnqueueAutoMerge", "MarkReady", "MarkDraft", "CommentPR", "CreateDraftPR")
	// fakeConfig answer from configuration, with no state worth a fetch.
	fakeConfig = setOf("ProbeInterval", "StateLabels", "WalksAllPages",
		"On", "Fire") // On and Fire are fakehook.Hooks: test scripting, not state
)

func setOf(names ...string) map[string]bool {
	m := map[string]bool{}
	for _, n := range names {
		m[n] = true
	}
	return m
}

// guardTarget is one value whose exported methods the guard walks, built fresh
// around a given Fake so each read starts from clean audit state.
type guardTarget struct {
	name string
	of   func(f *Fake) any
}

// guardTargets are *Fake, *RunURLFake, and the value each As* shape method
// returns. The shapes are discovered by reflection and are the only route to
// the host-mediation backings, which are unexported on *Fake.
func guardTargets() []guardTarget {
	ts := []guardTarget{{"Fake", func(f *Fake) any { return f }}}
	ft := reflect.TypeOf(NewFake())
	for i := range ft.NumMethod() {
		m := ft.Method(i)
		if !strings.HasPrefix(m.Name, "As") || m.Type.NumIn() != 1 || m.Type.NumOut() != 1 {
			continue
		}
		ts = append(ts, guardTarget{m.Name, func(f *Fake) any {
			return reflect.ValueOf(f).Method(i).Call(nil)[0].Interface()
		}})
	}
	return ts
}

// guardArg synthesizes a plausible argument: "1" and "b" are what the seeded
// Fake knows, so most reads succeed, though only crossing is asserted.
func guardArg(t reflect.Type) reflect.Value {
	if t.Kind() == reflect.String {
		return reflect.ValueOf("1").Convert(t)
	}
	return reflect.Zero(t)
}

func seededFake(labels ...DispatchLabels) *Fake {
	f := NewFake(labels...)
	f.SetIssue(Issue{Number: "1", Title: "a", Labels: []string{"ready"}})
	f.SetPR("b", PR{URL: "1"})
	return f
}

// TestFakeCallsCrossTheConnector is the bypass guard: every exported method of
// the Fake that is not a scripting, native-write, or config method is a read or
// a forge write, and must cross the Connector at least once, sent on a declared
// Route; a forge write under its own HTTP method. A new read that answers
// natively fails here without anyone listing it.
func TestFakeCallsCrossTheConnector(t *testing.T) {
	routes := map[string]bool{}
	for _, r := range fakeRoutes {
		routes[r.Method+" "+r.Host+" "+r.Path] = true
	}
	seen := map[string]bool{}
	targets := append(guardTargets(), guardTarget{"RunURLFake", nil})
	for _, tg := range targets {
		build := func() (*Fake, any) {
			if tg.of == nil {
				r := NewRunURLFake(DispatchLabels{Dispatchable: "ready"})
				return r.Fake, r
			}
			f := seededFake(DispatchLabels{Dispatchable: "ready"})
			return f, tg.of(f)
		}
		_, proto := build()
		pt := reflect.TypeOf(proto)
		for i := range pt.NumMethod() {
			name := pt.Method(i).Name
			seen[name] = true
			if strings.HasPrefix(name, "Set") || strings.HasPrefix(name, "As") || fakeNativeWrites[name] || pendingForgeWrites[name] || fakeConfig[name] {
				continue
			}
			t.Run(tg.name+"."+name, func(t *testing.T) {
				f, target := build()
				if tg.of == nil {
					f.SetIssue(Issue{Number: "1", Title: "a", Labels: []string{"ready"}})
					f.SetPR("b", PR{URL: "1"})
				}
				reads := observeReads(t, f)
				m := reflect.ValueOf(target).MethodByName(name)
				args := make([]reflect.Value, m.Type().NumIn())
				for j := range args {
					args[j] = guardArg(m.Type().In(j))
				}
				func() {
					defer func() {
						if r := recover(); r != nil {
							t.Fatalf("%s panicked: %v", name, r)
						}
					}()
					m.Call(args) // the return is ignored: only the crossing is asserted
				}()
				got := reads()
				if len(got) == 0 {
					t.Fatalf("%s.%s never crossed the Connector: it answers natively", tg.name, name)
				}
				for _, a := range got {
					if a.Decision != connectorhost.DecisionSent || !routes[a.Route] {
						t.Errorf("%s.%s audit = %+v, want a sent call on a declared Route", tg.name, name, a)
					}
					if want, ok := fakeForgeWrites[name]; ok && a.Method != want {
						t.Errorf("%s.%s crossed as %s, want %s", tg.name, name, a.Method, want)
					}
				}
			})
		}
	}
	for name := range fakeForgeWrites {
		if !seen[name] {
			t.Errorf("%s is classified as a forge write but no Fake method has that name", name)
		}
	}
	for _, set := range []map[string]bool{fakeNativeWrites, pendingForgeWrites, fakeConfig} {
		for name := range set {
			if !seen[name] {
				t.Errorf("%s is classified as a non-read but no Fake method has that name", name)
			}
		}
	}
}

// A read on an undeclared path is refused by the host, which closes the shared
// Connector; the next read, on another Fake, must still succeed.
func TestFakeRefusedReadDoesNotPoisonTheSharedConnector(t *testing.T) {
	var out string
	err := NewFake().core.read("/tracker/isue", &out, func() (any, error) { return "x", nil })
	if err == nil {
		t.Fatal("read on an undeclared path succeeded, want a route refusal")
	}
	f := NewFake()
	f.SetIssue(Issue{Number: "1"})
	if _, err := f.Issue("1"); err != nil {
		t.Fatalf("read after a refused one: %v", err)
	}
}

func TestFakeReadSurfacesServePanicAndEncodeFailure(t *testing.T) {
	c := NewFake().core
	var out string
	err := c.read(pathIssue, &out, func() (any, error) { panic("boom") })
	if err == nil || !strings.Contains(err.Error(), "boom") {
		t.Errorf("panic read err = %v, want the panic value", err)
	}
	err = c.read(pathIssue, &out, func() (any, error) { return make(chan int), nil })
	if err == nil || !strings.Contains(err.Error(), "unsupported type") {
		t.Errorf("unencodable read err = %v, want the encode error", err)
	}
	if err := NewFake().core.read(pathIssue, &out, func() (any, error) { return "ok", nil }); err != nil || out != "ok" {
		t.Errorf("read after both = %q, %v, want it to succeed on the same Connector", out, err)
	}
}

func TestAuditSinkRejectsAMalformedQuery(t *testing.T) {
	if _, err := (auditSink{}).Write([]byte(`{"query":"%zz"}`)); err == nil {
		t.Error("audit line with an unparsable query was accepted")
	}
}

func TestFakePRAndCheckReadsKeepScriptedErrorIdentity(t *testing.T) {
	sentinel := errors.New("scripted")
	f := NewFake()
	f.OpenPRForBranchErr = sentinel
	if _, _, err := f.OpenPRForBranch("b"); !errors.Is(err, sentinel) {
		t.Errorf("OpenPRForBranch err = %v, want the scripted sentinel", err)
	}
	f.FailureDetailErr = sentinel
	if _, err := f.FailureDetail("u"); !errors.Is(err, sentinel) {
		t.Errorf("FailureDetail err = %v, want the scripted sentinel", err)
	}
}

func TestFakeForgeReadsReturnScriptedState(t *testing.T) {
	f := NewFake()
	f.SetPR("b", PR{URL: "u1"})
	pr, ok, err := f.OpenPRForBranch("b")
	if err != nil || !ok || pr.URL != "u1" {
		t.Fatalf("OpenPRForBranch = %+v, %v, %v", pr, ok, err)
	}
	if u, ok, _ := f.PRForBranch("b"); !ok || u != "u1" {
		t.Errorf("PRForBranch = %q, %v", u, ok)
	}
	if _, ok, _ := f.OpenPRForBranch("none"); ok {
		t.Error("OpenPRForBranch(none) found a PR")
	}
	if _, err := f.PRState("missing"); err == nil {
		t.Error("PRState(missing) = nil error, want not found")
	}
	f.SetCheckStates("u1", []RollupState{StateSuccess})
	if s, _ := f.CheckState("u1"); s != StateSuccess {
		t.Errorf("CheckState = %q, want success", s)
	}
}

func TestFakeLabelAndBranchReadsObserveWrites(t *testing.T) {
	f := NewFake()
	if err := f.CreateLabel("bug", "", ""); err != nil {
		t.Fatal(err)
	}
	if got, err := f.ListLabels(); err != nil || len(got) != 1 || got[0] != "bug" {
		t.Errorf("ListLabels after CreateLabel = %v, %v", got, err)
	}
	f.SetBranchExists("b", true)
	if ok, _ := f.BranchExists("b"); !ok {
		t.Error("BranchExists(b) = false after SetBranchExists")
	}
}

func TestFakeProbeKeepsScriptedErrorIdentity(t *testing.T) {
	f := NewFake()
	f.ProbeErr = ErrAuthFailure
	for name, probe := range map[string]func() (string, error){
		"Fake": f.Probe, "IssueTrackerFake": f.IssueTrackerFake.Probe, "CodeForgeFake": f.CodeForgeFake.Probe,
	} {
		if _, err := probe(); !errors.Is(err, ErrAuthFailure) {
			t.Errorf("%s.Probe err = %v, want ErrAuthFailure", name, err)
		}
	}
}

func TestFakeCheckRunCrossesTheConnectorForStateAndRunURL(t *testing.T) {
	const url = "u"
	f := NewRunURLFake()
	reads := observeReads(t, f.Fake)
	f.SetCheckStates(url, []RollupState{StateSuccess})
	f.SetRunURLs(url, []string{"run-1"})
	state, run, err := f.CheckRun(url)
	if err != nil || state != StateSuccess || run != "run-1" {
		t.Fatalf("CheckRun = %q, %q, %v", state, run, err)
	}
	var paths []string
	for _, a := range reads() {
		paths = append(paths, a.Path)
	}
	if len(paths) != 2 || paths[0] != "/forge/checks" || paths[1] != "/forge/runURL" {
		t.Errorf("CheckRun crossed %v, want checks then runURL", paths)
	}
	if _, run, _ := f.CheckRun(url); run != "" {
		t.Errorf("drained run URL = %q, want empty", run)
	}
}

func TestFakePRMetadataReadsObserveWrites(t *testing.T) {
	const url = "u"
	f := NewFake()
	f.SetPR("b", PR{URL: url})
	if err := f.Merge(url); err != nil {
		t.Fatal(err)
	}
	if s, err := f.PRState(url); err != nil || s != PRMerged {
		t.Errorf("PRState after Merge = %q, %v, want merged", s, err)
	}
	if _, ok, _ := f.OpenPRForBranch("b"); ok {
		t.Error("OpenPRForBranch still reports a merged PR")
	}

	f.SetMergeableState(url, MergeableMergeable)
	f.SetMergeableStates(url, []MergeableState{MergeableConflicting, MergeableUnknown})
	for i, want := range []MergeableState{MergeableConflicting, MergeableUnknown, MergeableMergeable} {
		if got, _ := f.Mergeable(url); got != want {
			t.Errorf("Mergeable #%d = %q, want %q", i, got, want)
		}
	}

	f.SetDraft(url, true)
	if !f.IsDraft(url) {
		t.Error("IsDraft = false after SetDraft(true)")
	}
	if err := f.MarkReady(url); err != nil {
		t.Fatal(err)
	}
	if f.IsDraft(url) {
		t.Error("IsDraft = true after MarkReady")
	}
	if err := f.MarkDraft(url); err != nil {
		t.Fatal(err)
	}
	if !f.IsDraft(url) {
		t.Error("IsDraft = false after MarkDraft")
	}

	f.SetNeedsUpdate(url, true)
	f.SetPRFiles(url, []string{"a", "b"})
	f.SetMergeCommit(url, "abc")
	f.SetHeadCommitSHAs(url, []string{"s1", "s2"})
	f.AutoMergeAllowed = true
	if stale, _ := f.NeedsUpdate(url); !stale {
		t.Error("NeedsUpdate = false after SetNeedsUpdate(true)")
	}
	if files, _ := f.ListPRFiles(url); len(files) != 2 || files[1] != "b" {
		t.Errorf("ListPRFiles = %v", files)
	}
	if sha, _ := f.MergeCommit(url); sha != "abc" {
		t.Errorf("MergeCommit = %q", sha)
	}
	for _, want := range []string{"s1", "s2", "fake-sha-1"} {
		if got, _ := f.HeadCommitSHA(url); got != want {
			t.Errorf("HeadCommitSHA = %q, want %q", got, want)
		}
	}
	if ok, _ := f.CanAutoMerge(); !ok {
		t.Error("CanAutoMerge = false after AutoMergeAllowed = true")
	}
}

func TestFakePRMetadataReadsKeepScriptedErrorIdentity(t *testing.T) {
	sentinel := errors.New("scripted")
	f := NewFake()
	f.NeedsUpdateErr, f.PRFilesErr, f.HeadCommitSHAErr = sentinel, sentinel, sentinel
	f.MergeCommitErr, f.AutoMergeErr, f.MergeableErr = sentinel, sentinel, sentinel
	for name, err := range map[string]error{
		"NeedsUpdate":   second(f.NeedsUpdate("u")),
		"ListPRFiles":   second(f.ListPRFiles("u")),
		"HeadCommitSHA": second(f.HeadCommitSHA("u")),
		"MergeCommit":   second(f.MergeCommit("u")),
		"CanAutoMerge":  second(f.CanAutoMerge()),
		"Mergeable":     second(f.Mergeable("u")),
	} {
		if !errors.Is(err, sentinel) {
			t.Errorf("%s err = %v, want the scripted sentinel", name, err)
		}
	}
}

func second[T any](_ T, err error) error { return err }

func TestFakeForgeWritesCrossTheConnectorAndKeepScriptedErrors(t *testing.T) {
	sentinel := errors.New("scripted")
	f := NewFake()
	f.SetPR("b", PR{URL: "u"})
	f.RebaseErrs = []error{sentinel, nil}
	if err := f.Rebase("u"); !errors.Is(err, sentinel) {
		t.Errorf("Rebase err = %v, want the scripted sentinel", err)
	}
	if err := f.Rebase("u"); err != nil || len(f.RebasedURLs) != 2 {
		t.Errorf("Rebase #2 = %v, RebasedURLs = %v", err, f.RebasedURLs)
	}
	f.MergeErr = sentinel
	if err := f.Merge("u"); !errors.Is(err, sentinel) {
		t.Errorf("Merge err = %v, want the scripted sentinel", err)
	}
	if s, _ := f.PRState("u"); s == PRMerged {
		t.Error("a failed Merge marked the PR merged")
	}
}

// A write on an undeclared route is refused and retires the shared Connector;
// the next write, on another Fake, must still succeed and record its state.
func TestFakeRefusedWriteDoesNotPoisonTheSharedConnector(t *testing.T) {
	err := NewFake().core.call(http.MethodPost, "/forge/mrge", nil, func() (any, error) { return nil, nil })
	if err == nil {
		t.Fatal("write on an undeclared path succeeded, want a route refusal")
	}
	f := NewFake()
	if err := f.Merge("u"); err != nil || f.Merged != "u" {
		t.Fatalf("Merge after a refused write = %v, Merged = %q", err, f.Merged)
	}
}
