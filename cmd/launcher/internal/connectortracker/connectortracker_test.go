package connectortracker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"spindrift.dev/launcher/internal/backend"
	"spindrift.dev/launcher/internal/connectorhost"
	"spindrift.dev/launcher/internal/dedupmarker"
	"spindrift.dev/launcher/internal/forge"
)

var testLabels = forge.DispatchLabels{Dispatchable: "ready-for-agent"}

var testLifecycle = Lifecycle{Labels: testLabels}

var testTarget = Target{API: "https://api.example", Repo: "o/r"}

type recordedCall struct {
	method string
	params map[string]any
}

// stubGuest answers each method with the JSON in replies and records the calls.
func stubGuest(t *testing.T, replies map[string]string) (func(context.Context, string, any, any) error, *[]recordedCall) {
	t.Helper()
	var calls []recordedCall
	return func(_ context.Context, method string, params, result any) error {
		raw, err := json.Marshal(params)
		if err != nil {
			t.Fatal(err)
		}
		var p map[string]any
		if err := json.Unmarshal(raw, &p); err != nil {
			t.Fatal(err)
		}
		calls = append(calls, recordedCall{method, p})
		reply, ok := replies[method]
		if !ok {
			return errors.New("guest: no such method " + method)
		}
		return json.Unmarshal([]byte(reply), result)
	}, &calls
}

func TestNewRequiresCapability(t *testing.T) {
	c := &connectorhost.Connector{Capabilities: []string{"driver"}}
	_, err := New(c, &forge.IssueTrackerFake{}, testLifecycle, testTarget)
	if !errors.Is(err, ErrNotTracker) {
		t.Fatalf("err = %v, want ErrNotTracker", err)
	}
}

func TestTrackerListIssuesSortsAndResolves(t *testing.T) {
	call, calls := stubGuest(t, map[string]string{"tracker.listIssues": `{"issues":[
		{"number":"10","title":"b","state":"open","labels":["priority:high"]},
		{"number":"9","title":"a","state":"open","labels":[]}]}`})
	tr := mustTracker(t, call, &forge.IssueTrackerFake{}, testLifecycle, testTarget, nil, true)

	got, err := tr.ListIssues(forge.Dispatchable)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Number != "9" || got[1].Number != "10" {
		t.Fatalf("issues = %+v, want ascending by number", got)
	}
	if got[1].Priority != forge.ResolvePriority([]string{"priority:high"}) || got[1].State != forge.IssueOpen {
		t.Errorf("issue 10 = %+v, want resolved priority and open state", got[1])
	}
	want := recordedCall{"tracker.listIssues", map[string]any{
		"target": map[string]any{"api": "https://api.example", "repo": "o/r", "dialect": ""},
		"label":  "ready-for-agent", "status": "", "limit": float64(forge.ResultPageLimit),
	}}
	if !reflect.DeepEqual((*calls)[0], want) {
		t.Errorf("call = %+v, want %+v", (*calls)[0], want)
	}
}

func TestTrackerListOpenIssuesSendsNoSelector(t *testing.T) {
	call, calls := stubGuest(t, map[string]string{"tracker.listIssues": `{"issues":[]}`})
	lc := Lifecycle{Labels: testLabels, Statuses: map[forge.DispatchState]string{forge.Dispatchable: "Ready"}}
	tr := mustTracker(t, call, &forge.IssueTrackerFake{}, lc, testTarget, nil, true)
	if _, err := tr.ListOpenIssues(); err != nil {
		t.Fatal(err)
	}
	p := (*calls)[0].params
	if p["label"] != "" || p["status"] != "" {
		t.Errorf("label = %v, status = %v; want both empty", p["label"], p["status"])
	}
}

func TestTrackerListIssuesSendsLabelAndStatus(t *testing.T) {
	call, calls := stubGuest(t, map[string]string{"tracker.listIssues": `{"issues":[]}`})
	lc := Lifecycle{Labels: testLabels, Statuses: map[forge.DispatchState]string{forge.Dispatchable: "Ready for Agent"}}
	tr := mustTracker(t, call, &forge.IssueTrackerFake{}, lc, testTarget, nil, true)
	if _, err := tr.ListIssues(forge.Dispatchable); err != nil {
		t.Fatal(err)
	}
	p := (*calls)[0].params
	if p["label"] != "ready-for-agent" || p["status"] != "Ready for Agent" {
		t.Errorf("label = %v, status = %v; want both selectors", p["label"], p["status"])
	}
}

func TestTrackerListIssuesStatusOnlyState(t *testing.T) {
	call, calls := stubGuest(t, map[string]string{"tracker.listIssues": `{"issues":[]}`})
	lc := Lifecycle{Statuses: map[forge.DispatchState]string{forge.Dispatchable: "Ready"}}
	tr := mustTracker(t, call, &forge.IssueTrackerFake{}, lc, testTarget, nil, true)
	if _, err := tr.ListIssues(forge.Dispatchable); err != nil {
		t.Fatal(err)
	}
	p := (*calls)[0].params
	if p["label"] != "" || p["status"] != "Ready" {
		t.Errorf("label = %v, status = %v; want status only", p["label"], p["status"])
	}
}

// A state with no selector must not widen to every open issue.
func TestTrackerListIssuesWithoutSelectorIsEmpty(t *testing.T) {
	call, calls := stubGuest(t, map[string]string{"tracker.listIssues": `{"issues":[{"number":"1","state":"open"}]}`})
	tr := mustTracker(t, call, &forge.IssueTrackerFake{}, Lifecycle{}, testTarget, nil, true)
	got, err := tr.ListIssues(forge.Dispatchable)
	if err != nil || len(got) != 0 || len(*calls) != 0 {
		t.Errorf("issues = %v, err = %v, guest calls = %v; want none and no call", got, err, *calls)
	}
}

func TestTrackerTargetCarriesDialect(t *testing.T) {
	call, calls := stubGuest(t, map[string]string{"tracker.listIssues": `{"issues":[]}`})
	tgt := Target{API: "https://j.example", Repo: "ENG", Dialect: "server"}
	tr := mustTracker(t, call, &forge.IssueTrackerFake{}, testLifecycle, tgt, nil, true)
	if _, err := tr.ListOpenIssues(); err != nil {
		t.Fatal(err)
	}
	want := map[string]any{"api": "https://j.example", "repo": "ENG", "dialect": "server"}
	if got := (*calls)[0].params["target"]; !reflect.DeepEqual(got, want) {
		t.Errorf("target = %v, want %v", got, want)
	}
}

func TestTrackerRejectsBadIssues(t *testing.T) {
	for name, body := range map[string]string{
		"state":  `{"number":"1","state":"merged"}`,
		"zero":   `{"number":"0","state":"open"}`,
		"signed": `{"number":"+1","state":"open"}`,
		"padded": `{"number":"01","state":"open"}`,
		"branch": `{"number":"1/../x","state":"open"}`,
	} {
		t.Run(name, func(t *testing.T) {
			call, _ := stubGuest(t, map[string]string{
				"tracker.issue":      body,
				"tracker.listIssues": `{"issues":[` + body + `]}`,
			})
			tr := mustTracker(t, call, &forge.IssueTrackerFake{}, testLifecycle, testTarget, nil, true)
			if _, err := tr.Issue("1"); !errors.Is(err, ErrAnswer) {
				t.Errorf("Issue err = %v, want ErrAnswer", err)
			}
			if _, err := tr.ListOpenIssues(); !errors.Is(err, ErrAnswer) {
				t.Errorf("ListOpenIssues err = %v, want ErrAnswer", err)
			}
		})
	}
}

func TestTrackerIssueRejectsOtherNumber(t *testing.T) {
	call, _ := stubGuest(t, map[string]string{"tracker.issue": `{"number":"5","state":"open"}`})
	tr := mustTracker(t, call, &forge.IssueTrackerFake{}, testLifecycle, testTarget, nil, true)
	if _, err := tr.Issue("4"); !errors.Is(err, ErrAnswer) {
		t.Fatalf("err = %v, want ErrAnswer", err)
	}
}

func TestTrackerIssueClosed(t *testing.T) {
	call, _ := stubGuest(t, map[string]string{"tracker.issue": `{"number":"4","title":"t","body":"b","state":"closed","labels":["x"]}`})
	tr := mustTracker(t, call, &forge.IssueTrackerFake{}, testLifecycle, testTarget, nil, true)
	got, err := tr.Issue("4")
	if err != nil {
		t.Fatal(err)
	}
	if got.State != forge.IssueClosed || got.Body != "b" || got.Title != "t" {
		t.Errorf("issue = %+v", got)
	}
}

func TestTrackerGuestErrorPassesThrough(t *testing.T) {
	call, _ := stubGuest(t, nil)
	tr := mustTracker(t, call, &forge.IssueTrackerFake{}, testLifecycle, testTarget, nil, true)
	if _, err := tr.Issue("1"); err == nil {
		t.Error("guest error swallowed")
	}
}

func TestRoutesNamesTheRepoWithoutMutatingTheTable(t *testing.T) {
	table := []connectorhost.Route{
		{Method: "GET", Host: "api.example", Path: "/repos/{repo}/issues"},
		{Method: "GET", Host: "api.example", Path: "/user"},
	}
	got := Routes(table, "owner/name")
	want := []connectorhost.Route{
		{Method: "GET", Host: "api.example", Path: "/repos/owner/name/issues"},
		{Method: "GET", Host: "api.example", Path: "/user"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Routes = %v, want %v", got, want)
	}
	if table[0].Path != "/repos/{repo}/issues" {
		t.Errorf("Routes mutated its input: %v", table)
	}
}

func TestTrackerListIssuesCapsAtPageLimit(t *testing.T) {
	var sb strings.Builder
	sb.WriteString(`{"issues":[`)
	n := forge.ResultPageLimit + 50
	for i := n; i >= 1; i-- {
		if i != n {
			sb.WriteByte(',')
		}
		fmt.Fprintf(&sb, `{"number":"%d","title":"t","state":"open","labels":[]}`, i)
	}
	sb.WriteString(`]}`)
	call, _ := stubGuest(t, map[string]string{"tracker.listIssues": sb.String()})
	tr := mustTracker(t, call, &forge.IssueTrackerFake{}, testLifecycle, testTarget, nil, true)

	got, err := tr.ListOpenIssues()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != forge.ResultPageLimit {
		t.Fatalf("got %d issues, want the page cap %d", len(got), forge.ResultPageLimit)
	}
	if got[0].Number != "1" || got[len(got)-1].Number != strconv.Itoa(forge.ResultPageLimit) {
		t.Errorf("kept %s..%s, want the lowest numbers 1..%d", got[0].Number, got[len(got)-1].Number, forge.ResultPageLimit)
	}
}

// richNative is a native adapter with optional capabilities the Connector
// layers must not hide. The Fake already supplies HostPostedCommenter,
// IssueLabeler, DemandCounter, BlockersLister, CommentLister and
// NativeDepsReader; these two it lacks or answers differently.
type richNative struct{ *forge.IssueTrackerFake }

func (richNative) CheckIssueType() ([]string, error)      { return nil, nil }
func (richNative) CheckMappedStatuses() ([]string, error) { return nil, nil }

func (richNative) BodyGrammar() forge.BodyGrammar { return forge.BodyGrammarLocal }
func (richNative) WalksAllPages() bool            { return true }

func newNative() *forge.IssueTrackerFake { return forge.NewFake(testLabels).IssueTrackerFake }

func TestTrackerKeepsNativeCapabilitiesVisible(t *testing.T) {
	call, _ := stubGuest(t, nil)
	tr := mustTracker(t, call, richNative{newNative()}, testLifecycle, testTarget, []string{CapComments, CapNativeDeps}, true)

	c := forge.ResolveCapabilities(nil, tr, backend.Descriptor{}, backend.Descriptor{})
	if c.HostPostedCommenter == nil || c.IssueLabeler == nil || c.BlockersLister == nil {
		t.Errorf("native capabilities hidden: %+v", c)
	}
	if c.DemandCounter != nil || c.IssueTypeChecker != nil || c.StatusMappingChecker != nil {
		t.Errorf("a capability the protocol owns fell back to native: %+v", c)
	}
	if c.BodyGrammarer == nil || c.BodyGrammarer.BodyGrammar() != forge.BodyGrammarLocal {
		t.Errorf("BodyGrammarer = %v, want the native's", c.BodyGrammarer)
	}
}

func TestTrackerShadowsNativePaginationAndLabels(t *testing.T) {
	call, _ := stubGuest(t, nil)
	tr := mustTracker(t, call, richNative{newNative()}, Lifecycle{Labels: forge.DispatchLabels{Dispatchable: "other"}}, testTarget, nil, true)

	fp, ok := forge.As[forge.FullyPaginated](tr)
	if !ok || fp.WalksAllPages() {
		t.Errorf("FullyPaginated = %v, %v; want the reads layer's WalksAllPages false", fp, ok)
	}
	lt, ok := forge.As[forge.LabeledTracker](tr)
	if !ok || lt.StateLabels().Dispatchable != "other" {
		t.Errorf("LabeledTracker = %v, %v; want the labels the Connector reads resolve through", lt, ok)
	}
}

func TestTrackerWritesReachNative(t *testing.T) {
	native := newNative()
	call, _ := stubGuest(t, nil)
	tr := mustTracker(t, call, native, testLifecycle, testTarget, []string{CapComments, CapNativeDeps}, true)
	if err := tr.TransitionState("3", forge.Dispatchable, forge.InProgress); err != nil {
		t.Fatal(err)
	}
	if len(native.TransitionStateCalls) != 1 {
		t.Errorf("native TransitionState calls = %v, want 1", native.TransitionStateCalls)
	}
}

func TestTrackerCommentsWithoutCapabilityComeFromNative(t *testing.T) {
	native := newNative()
	native.CommentsFor = map[string][]forge.Comment{"4": {{Author: "native", Body: "n"}}}
	call, calls := stubGuest(t, map[string]string{"tracker.comments": `{"comments":[{"author":"guest"}]}`})
	tr := mustTracker(t, call, native, testLifecycle, testTarget, nil, true)

	cl, ok := forge.As[forge.CommentLister](tr)
	if !ok {
		t.Fatal("no CommentLister: the native adapter's should show through")
	}
	got, err := cl.Comments("4")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Author != "native" || len(*calls) != 0 {
		t.Errorf("comments = %+v, guest calls = %v; want the native's with no guest call", got, *calls)
	}
}

func TestTrackerCommentsWithoutCapabilityAbsentWhenNativeHasNone(t *testing.T) {
	call, _ := stubGuest(t, nil)
	var native forge.IssueTracker = newNative()
	tr := mustTracker(t, call, bareTracker{native}, testLifecycle, testTarget, nil, true)
	if _, ok := forge.As[forge.CommentLister](tr); ok {
		t.Error("CommentLister present without the capability or a native lister")
	}
}

// bareTracker hides every optional capability of the tracker it wraps.
type bareTracker struct{ forge.IssueTracker }

func TestTrackerCommentsCapabilityServesFromConnectorAndDropsMinimized(t *testing.T) {
	call, calls := stubGuest(t, map[string]string{
		"tracker.issue": `{"number":"4","title":"t","body":"body","state":"open"}`,
		"tracker.comments": `{"comments":[
		{"author":"a","createdAt":"2026-01-01T00:00:00Z","body":"keep","association":"MEMBER"},
		{"author":"b","createdAt":"2026-01-02T00:00:00Z","body":"hidden","minimized":true,"minimizedReason":"spam","association":"NONE"}]}`})
	native := newNative()
	native.CommentsFor = map[string][]forge.Comment{"4": {{Author: "native", Body: "n"}}}
	tr := mustTracker(t, call, native, testLifecycle, testTarget, []string{CapComments}, true)

	cl, ok := forge.As[forge.CommentLister](tr)
	if !ok {
		t.Fatal("no CommentLister")
	}
	got, err := cl.Comments("4")
	if err != nil {
		t.Fatal(err)
	}
	want := []forge.Comment{
		{Author: "a", CreatedAt: "2026-01-01T00:00:00Z", Body: "keep", Association: "MEMBER"},
		{Author: "b", CreatedAt: "2026-01-02T00:00:00Z", Body: "hidden", Minimized: true, MinimizedReason: "spam", Association: "NONE"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("comments = %+v, want %+v", got, want)
	}
	if (*calls)[0].method != "tracker.comments" {
		t.Errorf("first guest call = %v, want tracker.comments", (*calls)[0].method)
	}

	var warn strings.Builder
	text, err := forge.IssueText(tr, "4", &warn)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(text, "keep") || strings.Contains(text, "hidden") || !strings.Contains(warn.String(), "spam") {
		t.Errorf("IssueText = %q, warn = %q; want the minimized comment dropped", text, warn.String())
	}
}

func TestTrackerNativeDepsFollowsCapability(t *testing.T) {
	call, calls := stubGuest(t, map[string]string{"tracker.nativeDeps": `{"ids":["3","5","3"]}`})
	native := newNative()
	native.NativeDeps = map[string][]string{"9": {"7"}}

	without := mustTracker(t, call, native, testLifecycle, testTarget, nil, true)
	r, ok := forge.As[forge.NativeDepsReader](without)
	if !ok {
		t.Fatal("without the capability the native reader should show through")
	}
	ids, err := r.NativeDepsOf("9")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(ids, []string{"7"}) || len(*calls) != 0 {
		t.Errorf("ids = %v, guest calls = %v; want the native's with no guest call", ids, *calls)
	}

	bare := mustTracker(t, call, bareTracker{native}, testLifecycle, testTarget, nil, true)
	if _, ok := forge.As[forge.NativeDepsReader](bare); ok {
		t.Error("NativeDepsReader present without the capability or a native reader")
	}

	with := mustTracker(t, call, native, testLifecycle, testTarget, []string{CapNativeDeps}, true)
	r, ok = forge.As[forge.NativeDepsReader](with)
	if !ok {
		t.Fatal("tracker with the capability lacks NativeDepsReader")
	}
	if ids, err = r.NativeDepsOf("9"); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(ids, []string{"3", "5"}) {
		t.Errorf("ids = %v, want the Connector's, deduped in order", ids)
	}
}

func TestTrackerBlocksFollowsCapability(t *testing.T) {
	call, calls := stubGuest(t, map[string]string{"tracker.blocks": `{"ids":["3","5","3"]}`})
	native := newNative()
	native.NativeDeps = map[string][]string{"7": {"9"}}

	without := mustTracker(t, call, native, testLifecycle, testTarget, nil, true)
	r, ok := forge.As[forge.BlockersLister](without)
	if !ok {
		t.Fatal("without the capability the native lister should show through")
	}
	deps, err := r.BlocksOf("9")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(deps, forge.WithSource([]string{"7"}, forge.DepSourceNative)) || len(*calls) != 0 {
		t.Errorf("deps = %v, guest calls = %v; want the native's with no guest call", deps, *calls)
	}

	bare := mustTracker(t, call, bareTracker{native}, testLifecycle, testTarget, nil, true)
	if _, ok := forge.As[forge.BlockersLister](bare); ok {
		t.Error("BlockersLister present without the capability or a native lister")
	}

	with := mustTracker(t, call, native, testLifecycle, testTarget, []string{CapBlocks}, true)
	r, ok = forge.As[forge.BlockersLister](with)
	if !ok {
		t.Fatal("tracker with the capability lacks BlockersLister")
	}
	if deps, err = r.BlocksOf("9"); err != nil {
		t.Fatal(err)
	}
	if want := forge.WithSource([]string{"3", "5"}, forge.DepSourceNative); !reflect.DeepEqual(deps, want) {
		t.Errorf("deps = %v, want %v (the Connector's, deduped in order, native-sourced)", deps, want)
	}
	if (*calls)[0].method != "tracker.blocks" {
		t.Errorf("method = %q, want tracker.blocks", (*calls)[0].method)
	}
}

func TestBlocksRefusesIDsOutsideGrammar(t *testing.T) {
	for _, bad := range []string{"abc", "0", "007", "ENG-3", ""} {
		call, _ := stubGuest(t, map[string]string{"tracker.blocks": `{"ids":["3",` + strconv.Quote(bad) + `]}`})
		tr := mustTracker(t, call, newNative(), testLifecycle, testTarget, []string{CapBlocks}, true)
		r, ok := forge.As[forge.BlockersLister](tr)
		if !ok {
			t.Fatal("tracker with the capability lacks BlockersLister")
		}
		if deps, err := r.BlocksOf("9"); !errors.Is(err, ErrAnswer) {
			t.Errorf("id %q: deps = %v, err = %v; want ErrAnswer", bad, deps, err)
		}
	}
	call, _ := stubGuest(t, map[string]string{"tracker.blocks": `{"ids":["ENG-3","OPS-4"]}`})
	tr := mustTracker(t, call, newNative(), testLifecycle, testTarget, []string{CapBlocks, CapProjectKeyIDs}, true)
	r, _ := forge.As[forge.BlockersLister](tr)
	if deps, err := r.BlocksOf("ENG-1"); err != nil || len(deps) != 2 {
		t.Errorf("projectKey: deps = %v, err = %v; want 2 ids", deps, err)
	}
}

func TestNativeDepsRefusesBadIDs(t *testing.T) {
	for _, bad := range []string{"abc", "0", "-3", "1/../x", "007", ""} {
		call, _ := stubGuest(t, map[string]string{"tracker.nativeDeps": `{"ids":["3",` + strconv.Quote(bad) + `]}`})
		tr := mustTracker(t, call, newNative(), testLifecycle, testTarget, []string{CapNativeDeps}, true)
		r, ok := forge.As[forge.NativeDepsReader](tr)
		if !ok {
			t.Fatal("tracker with the capability lacks NativeDepsReader")
		}
		if ids, err := r.NativeDepsOf("9"); !errors.Is(err, ErrAnswer) {
			t.Errorf("id %q: ids = %v, err = %v; want ErrAnswer", bad, ids, err)
		}
	}
}

func TestProjectKeyIDsRoundTrip(t *testing.T) {
	call, _ := stubGuest(t, map[string]string{"tracker.issue": `{"number":"ENG-12","title":"t","state":"open"}`})
	tr := mustTracker(t, call, newNative(), testLifecycle, testTarget, []string{CapProjectKeyIDs}, true)
	got, err := tr.Issue("ENG-12")
	if err != nil {
		t.Fatal(err)
	}
	if got.Number != "ENG-12" {
		t.Errorf("number = %q, want ENG-12", got.Number)
	}
}

func TestProjectKeyIDsRefuseOutsideGrammar(t *testing.T) {
	for _, bad := range []string{"eng-12", "ENG-012", "ENG-0", "ENG12", "12", "ENG-12/../x", ""} {
		body := `{"number":` + strconv.Quote(bad) + `,"state":"open"}`
		call, _ := stubGuest(t, map[string]string{
			"tracker.issue":      body,
			"tracker.listIssues": `{"issues":[` + body + `]}`,
		})
		tr := mustTracker(t, call, newNative(), testLifecycle, testTarget, []string{CapProjectKeyIDs}, true)
		if _, err := tr.Issue(bad); !errors.Is(err, ErrAnswer) {
			t.Errorf("Issue(%q) err = %v, want ErrAnswer", bad, err)
		}
		if _, err := tr.ListOpenIssues(); !errors.Is(err, ErrAnswer) {
			t.Errorf("list with %q err = %v, want ErrAnswer", bad, err)
		}
	}
}

func TestDecimalConnectorRefusesProjectKey(t *testing.T) {
	body := `{"number":"ENG-12","state":"open"}`
	call, _ := stubGuest(t, map[string]string{"tracker.issue": body, "tracker.listIssues": `{"issues":[` + body + `]}`})
	tr := mustTracker(t, call, newNative(), testLifecycle, testTarget, nil, true)
	if _, err := tr.Issue("ENG-12"); !errors.Is(err, ErrAnswer) {
		t.Errorf("Issue err = %v, want ErrAnswer", err)
	}
	if _, err := tr.ListOpenIssues(); !errors.Is(err, ErrAnswer) {
		t.Errorf("list err = %v, want ErrAnswer", err)
	}
}

func TestProjectKeyNativeDepsFollowGrammar(t *testing.T) {
	deps := func(caps []string, ids string) ([]string, error) {
		call, _ := stubGuest(t, map[string]string{"tracker.nativeDeps": `{"ids":` + ids + `}`})
		tr := mustTracker(t, call, newNative(), testLifecycle, testTarget, append([]string{CapNativeDeps}, caps...), true)
		r, ok := forge.As[forge.NativeDepsReader](tr)
		if !ok {
			t.Fatal("no NativeDepsReader")
		}
		return r.NativeDepsOf("ENG-1")
	}
	got, err := deps([]string{CapProjectKeyIDs}, `["ENG-3","OPS-4","ENG-3"]`)
	if err != nil || !reflect.DeepEqual(got, []string{"ENG-3", "OPS-4"}) {
		t.Errorf("ids = %v, err = %v; want ENG-3, OPS-4", got, err)
	}
	for _, bad := range []string{`["ENG-3","3"]`, `["eng-3"]`, `["ENG-03"]`} {
		if ids, err := deps([]string{CapProjectKeyIDs}, bad); !errors.Is(err, ErrAnswer) {
			t.Errorf("projectKey %s: ids = %v, err = %v; want ErrAnswer", bad, ids, err)
		}
	}
	if ids, err := deps(nil, `["ENG-3"]`); !errors.Is(err, ErrAnswer) {
		t.Errorf("decimal: ids = %v, err = %v; want ErrAnswer", ids, err)
	}
}

func TestProjectKeyListSortsByPrefixThenNumber(t *testing.T) {
	call, _ := stubGuest(t, map[string]string{"tracker.listIssues": `{"issues":[
		{"number":"ENG-10","state":"open"},
		{"number":"ENG-9","state":"open"},
		{"number":"ABC-3","state":"open"}]}`})
	tr := mustTracker(t, call, newNative(), testLifecycle, testTarget, []string{CapProjectKeyIDs}, true)
	got, err := tr.ListOpenIssues()
	if err != nil {
		t.Fatal(err)
	}
	var nums []string
	for _, i := range got {
		nums = append(nums, i.Number)
	}
	if want := []string{"ABC-3", "ENG-9", "ENG-10"}; !reflect.DeepEqual(nums, want) {
		t.Errorf("order = %v, want %v", nums, want)
	}
}

func TestTrackerRestoresDedupMarkerAsNativeDoes(t *testing.T) {
	marker := dedupmarker.Build([]string{"a", "b"})
	for name, body := range map[string]string{"with body": "prose", "empty body": ""} {
		call, _ := stubGuest(t, map[string]string{"tracker.issue": `{"number":"4","state":"open","body":` +
			strconv.Quote(body) + `,"dedupMarker":` + strconv.Quote(marker) + `}`})
		tr := mustTracker(t, call, newNative(), testLifecycle, testTarget, nil, true)
		got, err := tr.Issue("4")
		if err != nil {
			t.Fatal(err)
		}
		if want := dedupmarker.Append(body, marker); got.Body != want {
			t.Errorf("%s: body = %q, want %q", name, got.Body, want)
		}
	}
	call, _ := stubGuest(t, map[string]string{"tracker.issue": `{"number":"4","state":"open","body":"prose"}`})
	got, err := mustTracker(t, call, newNative(), testLifecycle, testTarget, nil, true).Issue("4")
	if err != nil || got.Body != "prose" {
		t.Errorf("no marker: body = %q, err = %v; want body untouched", got.Body, err)
	}
}

func TestTrackerClosedFields(t *testing.T) {
	read := func(body string) forge.Issue {
		t.Helper()
		call, _ := stubGuest(t, map[string]string{"tracker.issue": body})
		got, err := mustTracker(t, call, newNative(), testLifecycle, testTarget, nil, true).Issue("4")
		if err != nil {
			t.Fatal(err)
		}
		return got
	}
	closed := read(`{"number":"4","state":"closed","closedAt":"2026-03-04T05:06:07Z","closedNotPlanned":true}`)
	if want := time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC); !closed.ClosedAt.Equal(want) || !closed.ClosedNotPlanned {
		t.Errorf("closed = %+v, want ClosedAt %v and not planned", closed, want)
	}
	open := read(`{"number":"4","state":"open","closedAt":"2026-03-04T05:06:07Z","closedNotPlanned":true}`)
	if !open.ClosedAt.IsZero() || open.ClosedNotPlanned {
		t.Errorf("open = %+v, want closed fields ignored", open)
	}
	bad := read(`{"number":"4","state":"closed","closedAt":"yesterday","closedNotPlanned":true}`)
	if !bad.ClosedAt.IsZero() || !bad.ClosedNotPlanned {
		t.Errorf("malformed date = %+v, want zero ClosedAt and no error", bad)
	}
}

func TestTrackerRestoresResearchMarkerOnlyWhenFlagged(t *testing.T) {
	call, _ := stubGuest(t, map[string]string{"tracker.comments": `{"comments":[
		{"author":"bot","body":"verdict","researchMarker":true},
		{"author":"human","body":"verdict"}]}`})
	tr := mustTracker(t, call, newNative(), testLifecycle, testTarget, []string{CapComments}, true)
	cl, ok := forge.As[forge.CommentLister](tr)
	if !ok {
		t.Fatal("no CommentLister")
	}
	got, err := cl.Comments("4")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Body != forge.ResearchMarker+"\nverdict" || got[1].Body != "verdict" {
		t.Errorf("comments = %+v, want the marker only on the flagged one", got)
	}
}

func TestLegacyConnectorNeverReceivesStatus(t *testing.T) {
	call, calls := stubGuest(t, map[string]string{"tracker.listIssues": `{"issues":[]}`})
	statuses := map[forge.DispatchState]string{forge.Dispatchable: "Ready"}

	both := mustTracker(t, call, &forge.IssueTrackerFake{}, Lifecycle{Labels: testLabels, Statuses: statuses}, testTarget, nil, false)
	if _, err := both.ListIssues(forge.Dispatchable); err != nil {
		t.Fatal(err)
	}
	if p := (*calls)[0].params; p["label"] != "ready-for-agent" || p["status"] != "" {
		t.Errorf("label = %v, status = %v; want the label alone", p["label"], p["status"])
	}

	n := len(*calls)
	only := mustTracker(t, call, &forge.IssueTrackerFake{}, Lifecycle{Statuses: statuses}, testTarget, nil, false)
	if _, err := only.ListIssues(forge.Dispatchable); !errors.Is(err, connectorhost.ErrProtocolUnsupported) {
		t.Errorf("status-only state err = %v, want ErrProtocolUnsupported", err)
	}
	if len(*calls) != n {
		t.Errorf("a status-only state reached the guest")
	}
}

func TestLegacyConnectorIgnoresV02Capabilities(t *testing.T) {
	call, _ := stubGuest(t, map[string]string{"tracker.issue": `{"number":"ENG-1","state":"open"}`})
	tr := mustTracker(t, call, bareTracker{newNative()}, testLifecycle, testTarget, []string{CapBlocks, CapProjectKeyIDs}, false)
	if _, ok := forge.As[forge.BlockersLister](tr); ok {
		t.Error("a 0.1 Connector's tracker.blocks must be ignored")
	}
	if _, err := tr.Issue("ENG-1"); err == nil {
		t.Error("a 0.1 Connector's project-key ids must be refused")
	}
}

func TestSpeaksV02(t *testing.T) {
	for v, want := range map[string]bool{"0.1.0": false, "0.1.7": false, "0.2.0": true, "0.3.0": true, "1.0.0": false, "garbage": false} {
		if got := speaksV02(v); got != want {
			t.Errorf("speaksV02(%q) = %v, want %v", v, got, want)
		}
	}
}
