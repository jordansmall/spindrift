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

	"spindrift.dev/launcher/internal/backend"
	"spindrift.dev/launcher/internal/connectorhost"
	"spindrift.dev/launcher/internal/forge"
)

var testLabels = forge.DispatchLabels{Dispatchable: "ready-for-agent"}

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
	_, err := New(c, &forge.IssueTrackerFake{}, testLabels, testTarget)
	if !errors.Is(err, ErrNotTracker) {
		t.Fatalf("err = %v, want ErrNotTracker", err)
	}
}

func TestTrackerListIssuesSortsAndResolves(t *testing.T) {
	call, calls := stubGuest(t, map[string]string{"tracker.listIssues": `{"issues":[
		{"number":"10","title":"b","state":"open","labels":["priority:high"]},
		{"number":"9","title":"a","state":"open","labels":[]}]}`})
	tr := newTracker(call, &forge.IssueTrackerFake{}, testLabels, testTarget, nil)

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
		"target": map[string]any{"api": "https://api.example", "repo": "o/r"},
		"label":  "ready-for-agent", "limit": float64(forge.ResultPageLimit),
	}}
	if !reflect.DeepEqual((*calls)[0], want) {
		t.Errorf("call = %+v, want %+v", (*calls)[0], want)
	}
}

func TestTrackerListOpenIssuesSendsNoLabel(t *testing.T) {
	call, calls := stubGuest(t, map[string]string{"tracker.listIssues": `{"issues":[]}`})
	tr := newTracker(call, &forge.IssueTrackerFake{}, testLabels, testTarget, nil)
	if _, err := tr.ListOpenIssues(); err != nil {
		t.Fatal(err)
	}
	if l := (*calls)[0].params["label"]; l != "" {
		t.Errorf("label = %v, want empty", l)
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
			tr := newTracker(call, &forge.IssueTrackerFake{}, testLabels, testTarget, nil)
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
	tr := newTracker(call, &forge.IssueTrackerFake{}, testLabels, testTarget, nil)
	if _, err := tr.Issue("4"); !errors.Is(err, ErrAnswer) {
		t.Fatalf("err = %v, want ErrAnswer", err)
	}
}

func TestTrackerIssueClosed(t *testing.T) {
	call, _ := stubGuest(t, map[string]string{"tracker.issue": `{"number":"4","title":"t","body":"b","state":"closed","labels":["x"]}`})
	tr := newTracker(call, &forge.IssueTrackerFake{}, testLabels, testTarget, nil)
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
	tr := newTracker(call, &forge.IssueTrackerFake{}, testLabels, testTarget, nil)
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
	tr := newTracker(call, &forge.IssueTrackerFake{}, testLabels, testTarget, nil)

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

func (richNative) BodyGrammar() forge.BodyGrammar { return forge.BodyGrammarLocal }
func (richNative) WalksAllPages() bool            { return true }

func newNative() *forge.IssueTrackerFake { return forge.NewFake(testLabels).IssueTrackerFake }

func TestTrackerKeepsNativeCapabilitiesVisible(t *testing.T) {
	call, _ := stubGuest(t, nil)
	tr := newTracker(call, richNative{newNative()}, testLabels, testTarget, []string{CapComments, CapNativeDeps})

	c := forge.ResolveCapabilities(nil, tr, backend.Descriptor{}, backend.Descriptor{})
	if c.HostPostedCommenter == nil || c.IssueLabeler == nil || c.DemandCounter == nil || c.BlockersLister == nil {
		t.Errorf("native capabilities hidden: %+v", c)
	}
	if c.BodyGrammarer == nil || c.BodyGrammarer.BodyGrammar() != forge.BodyGrammarLocal {
		t.Errorf("BodyGrammarer = %v, want the native's", c.BodyGrammarer)
	}
}

func TestTrackerShadowsNativePaginationAndLabels(t *testing.T) {
	call, _ := stubGuest(t, nil)
	tr := newTracker(call, richNative{newNative()}, forge.DispatchLabels{Dispatchable: "other"}, testTarget, nil)

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
	tr := newTracker(call, native, testLabels, testTarget, []string{CapComments, CapNativeDeps})
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
	tr := newTracker(call, native, testLabels, testTarget, nil)

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
	tr := newTracker(call, bareTracker{native}, testLabels, testTarget, nil)
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
	tr := newTracker(call, native, testLabels, testTarget, []string{CapComments})

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

	without := newTracker(call, native, testLabels, testTarget, nil)
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

	bare := newTracker(call, bareTracker{native}, testLabels, testTarget, nil)
	if _, ok := forge.As[forge.NativeDepsReader](bare); ok {
		t.Error("NativeDepsReader present without the capability or a native reader")
	}

	with := newTracker(call, native, testLabels, testTarget, []string{CapNativeDeps})
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

func TestNativeDepsRefusesBadIDs(t *testing.T) {
	for _, bad := range []string{"abc", "0", "-3", "1/../x", "007", ""} {
		call, _ := stubGuest(t, map[string]string{"tracker.nativeDeps": `{"ids":["3",` + strconv.Quote(bad) + `]}`})
		tr := newTracker(call, newNative(), testLabels, testTarget, []string{CapNativeDeps})
		r, ok := forge.As[forge.NativeDepsReader](tr)
		if !ok {
			t.Fatal("tracker with the capability lacks NativeDepsReader")
		}
		if ids, err := r.NativeDepsOf("9"); !errors.Is(err, ErrAnswer) {
			t.Errorf("id %q: ids = %v, err = %v; want ErrAnswer", bad, ids, err)
		}
	}
}
