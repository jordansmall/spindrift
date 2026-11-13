package connectortracker

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"reflect"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/dedupmarker"
	"spindrift.dev/launcher/internal/forge"
)

// writeGuest answers tracker.issue from issue (an error when issueErr is set),
// tracker.transition with transition (or transitionErr), and records every call.
type writeGuest struct {
	issue         string
	issueErr      error
	transition    string
	transitionErr error
	editErr       error
	commentErr    error
	fileURL       string
	fileErr       error
	calls         []recordedCall
}

func (g *writeGuest) call(_ context.Context, method string, params, result any) error {
	raw, _ := json.Marshal(params)
	var p map[string]any
	_ = json.Unmarshal(raw, &p)
	g.calls = append(g.calls, recordedCall{method, p})
	switch method {
	case CapTransition:
		if g.transitionErr != nil {
			return g.transitionErr
		}
		return json.Unmarshal([]byte(g.transition), result)
	case CapEditLabels:
		return g.editErr
	case "tracker.issue":
		if g.issueErr != nil {
			return g.issueErr
		}
		return json.Unmarshal([]byte(g.issue), result)
	case CapComment:
		return g.commentErr
	case CapFileIssue:
		if g.fileErr != nil {
			return g.fileErr
		}
		return json.Unmarshal([]byte(g.fileURL), result)
	case "tracker.listLabels":
		return json.Unmarshal([]byte(`{"labels":["a","b"]}`), result)
	case "tracker.createLabel":
		return json.Unmarshal([]byte(`{}`), result)
	}
	return errors.New("guest: no such method " + method)
}

func (g *writeGuest) methods() []string {
	var out []string
	for _, c := range g.calls {
		out = append(out, c.method)
	}
	return out
}

func (g *writeGuest) edits() []recordedCall {
	var out []recordedCall
	for _, c := range g.calls {
		if c.method == CapEditLabels {
			out = append(out, c)
		}
	}
	return out
}

func issueJSON(status string, labels ...string) string {
	b, _ := json.Marshal(map[string]any{"number": "7", "title": "t", "state": "open", "labels": append([]string{}, labels...), "status": status})
	return string(b)
}

var (
	writeCaps  = []string{CapTransition, CapEditLabels}
	hybridLC   = Lifecycle{Labels: forge.DispatchLabels{Dispatchable: "ready", InProgress: "wip", Complete: "done", Failed: "bad", Ambiguous: "amb"}}
	verdictLC  = forge.NewVerdictLabels(forge.VerdictLabel{Verdict: "recommend", Label: "rec"})
	statusMap  = map[forge.DispatchState]string{forge.InProgress: "In Progress", forge.Complete: "Done", forge.Failed: "Blocked"}
	resolution = map[forge.DispatchState]string{forge.Complete: "Fixed"}
)

func lifecycle(statusOnly bool) Lifecycle {
	lc := hybridLC
	lc.Statuses = statusMap
	lc.Resolutions = resolution
	lc.VerdictLabels = verdictLC
	lc.StatusOnly = statusOnly
	if statusOnly {
		lc.Labels = forge.DispatchLabels{}
	}
	return lc
}

func writeTracker(t *testing.T, g *writeGuest, lc Lifecycle, native forge.IssueTracker, caps []string) forge.IssueTracker {
	t.Helper()
	return mustTracker(t, g.call, native, lc, testTarget, caps, true)
}

func strs(v any) []string {
	var out []string
	for _, e := range v.([]any) {
		out = append(out, e.(string))
	}
	return out
}

func TestTransitionMovedStripsStaleLabels(t *testing.T) {
	g := &writeGuest{transition: `{"outcome":"moved","offered":[]}`}
	native := newNative()
	tr := writeTracker(t, g, lifecycle(false), native, writeCaps)
	if err := tr.TransitionState("7", forge.InProgress, forge.Complete); err != nil {
		t.Fatal(err)
	}
	if len(native.TransitionStateCalls) != 0 {
		t.Errorf("native writes used: %+v", native.TransitionStateCalls)
	}
	want := recordedCall{CapTransition, map[string]any{
		"target": map[string]any{"api": "https://api.example", "repo": "o/r", "dialect": ""},
		"number": "7", "state": "complete",
		"mapping": map[string]any{
			"in-progress": map[string]any{"status": "In Progress", "resolution": ""},
			"complete":    map[string]any{"status": "Done", "resolution": "Fixed"},
			"failed":      map[string]any{"status": "Blocked", "resolution": ""},
		},
	}}
	if !reflect.DeepEqual(g.calls[0], want) {
		t.Errorf("transition call = %+v, want %+v", g.calls[0], want)
	}
	e := g.edits()
	if len(e) != 1 || !reflect.DeepEqual(strs(e[0].params["remove"]), []string{"wip", "bad"}) || len(strs(e[0].params["add"])) != 0 {
		t.Errorf("edits = %+v, want one removal of wip and the stale failed label", e)
	}
}

func TestTransitionMovedPostStripFailureIsIgnored(t *testing.T) {
	g := &writeGuest{transition: `{"outcome":"moved","offered":[]}`, editErr: errors.New("boom")}
	tr := writeTracker(t, g, lifecycle(false), newNative(), writeCaps)
	if err := tr.TransitionState("7", forge.InProgress, forge.Complete); err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
}

func TestTransitionRequestCarriesStateAndMapping(t *testing.T) {
	g := &writeGuest{transition: `{"outcome":"moved","status":"Done","offered":[]}`}
	tr := writeTracker(t, g, lifecycle(false), newNative(), writeCaps)
	if err := tr.TransitionState("7", forge.InProgress, forge.Complete); err != nil {
		t.Fatal(err)
	}
	p := g.calls[0].params
	if p["state"] != "complete" {
		t.Errorf("state = %v, want complete", p["state"])
	}
	if _, resolved := p["status"]; resolved {
		t.Errorf("core still sends a resolved status: %v", p)
	}
	want := map[string]any{
		"in-progress": map[string]any{"status": "In Progress", "resolution": ""},
		"complete":    map[string]any{"status": "Done", "resolution": "Fixed"},
		"failed":      map[string]any{"status": "Blocked", "resolution": ""},
	}
	if !reflect.DeepEqual(p["mapping"], want) {
		t.Errorf("mapping = %v, want %v", p["mapping"], want)
	}
}

func TestTransitionStateNameIsDispatchStateString(t *testing.T) {
	g := &writeGuest{transition: `{"outcome":"moved","offered":[]}`, issue: issueJSON("Open")}
	tr := writeTracker(t, g, lifecycle(false), newNative(), writeCaps)
	if err := tr.TransitionState("7", forge.Dispatchable, forge.InProgress); err != nil {
		t.Fatal(err)
	}
	var state any
	for _, c := range g.calls {
		if c.method == CapTransition {
			state = c.params["state"]
		}
	}
	if state != "in-progress" {
		t.Errorf("state = %v, want in-progress", state)
	}
}

func TestTransitionMovedStripFailureWarnsOnStderr(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	orig := os.Stderr
	os.Stderr = w
	g := &writeGuest{transition: `{"outcome":"moved","offered":[]}`, editErr: errors.New("boom")}
	tr := writeTracker(t, g, lifecycle(false), newNative(), writeCaps)
	trErr := tr.TransitionState("7", forge.InProgress, forge.Complete)
	os.Stderr = orig
	_ = w.Close()
	out, _ := io.ReadAll(r)
	if trErr != nil {
		t.Fatalf("err = %v, want nil", trErr)
	}
	if !strings.Contains(string(out), "#7") || !strings.Contains(string(out), "boom") {
		t.Errorf("stderr = %q, want a warning naming the issue and the failure", out)
	}
}

func TestTransitionUnavailableHybridSwapsLabel(t *testing.T) {
	g := &writeGuest{transition: `{"outcome":"unavailable","offered":["X"]}`}
	tr := writeTracker(t, g, lifecycle(false), newNative(), writeCaps)
	if err := tr.TransitionState("7", forge.InProgress, forge.Complete); err != nil {
		t.Fatal(err)
	}
	e := g.edits()
	if len(e) != 1 || !reflect.DeepEqual(strs(e[0].params["add"]), []string{"done"}) || !reflect.DeepEqual(strs(e[0].params["remove"]), []string{"wip", "bad"}) {
		t.Errorf("edits = %+v, want add done, remove wip and bad", e)
	}
}

func TestTransitionUnmappedHybridSwapsLabel(t *testing.T) {
	g := &writeGuest{transition: `{"outcome":"unmapped","offered":[]}`}
	lc := lifecycle(false)
	lc.Statuses = nil
	tr := writeTracker(t, g, lc, newNative(), writeCaps)
	if err := tr.TransitionState("7", forge.InProgress, forge.Complete); err != nil {
		t.Fatal(err)
	}
	if m, _ := g.calls[0].params["mapping"].(map[string]any); len(m) != 0 {
		t.Errorf("mapping = %v, want empty when no state names a status", m)
	}
	e := g.edits()
	if len(e) != 1 || !reflect.DeepEqual(strs(e[0].params["add"]), []string{"done"}) {
		t.Errorf("edits = %+v, want add done", e)
	}
}

func TestTransitionNoStatusNoLabelErrors(t *testing.T) {
	g := &writeGuest{transition: `{"outcome":"unmapped","offered":[]}`}
	lc := lifecycle(false)
	lc.Statuses = nil
	lc.Labels = forge.DispatchLabels{}
	tr := writeTracker(t, g, lc, newNative(), writeCaps)
	err := tr.TransitionState("7", forge.InProgress, forge.Complete)
	if err == nil || !strings.Contains(err.Error(), "no status mapping or fallback label configured for state complete") {
		t.Fatalf("err = %v", err)
	}
	if len(g.edits()) != 0 {
		t.Errorf("edits = %+v, want none", g.edits())
	}
}

func TestTransitionUnavailableStatusOnlyRefuses(t *testing.T) {
	g := &writeGuest{transition: `{"outcome":"unavailable","status":"Done","offered":["Review","Closed"]}`, issue: issueJSON("Open")}
	tr := writeTracker(t, g, lifecycle(true), newNative(), writeCaps)
	err := tr.TransitionState("7", forge.InProgress, forge.Complete)
	var refused *forge.TransitionRefusedError
	if !errors.As(err, &refused) {
		t.Fatalf("err = %v, want *TransitionRefusedError", err)
	}
	want := forge.TransitionRefusedError{Issue: "7", From: "Open", To: "Done", Offered: []string{"Review", "Closed"}}
	if !reflect.DeepEqual(*refused, want) {
		t.Errorf("refusal = %+v, want %+v", *refused, want)
	}
	if len(g.edits()) != 0 {
		t.Errorf("edits = %+v, want none", g.edits())
	}
}

func TestTransitionUnmappedStatusOnlyRefuses(t *testing.T) {
	g := &writeGuest{transition: `{"outcome":"unmapped","offered":[]}`, issue: issueJSON("Open")}
	lc := lifecycle(true)
	lc.Statuses = nil
	tr := writeTracker(t, g, lc, newNative(), writeCaps)
	err := tr.TransitionState("7", forge.InProgress, forge.Complete)
	var refused *forge.TransitionRefusedError
	if !errors.As(err, &refused) {
		t.Fatalf("err = %v, want *TransitionRefusedError", err)
	}
	want := forge.TransitionRefusedError{Issue: "7", From: "Open", State: "complete"}
	if !reflect.DeepEqual(*refused, want) {
		t.Errorf("refusal = %+v, want %+v", *refused, want)
	}
	if len(g.edits()) != 0 {
		t.Errorf("edits = %+v, want none", g.edits())
	}
}

func TestTransitionRefusalSurvivesFailedStatusRead(t *testing.T) {
	g := &writeGuest{transition: `{"outcome":"unavailable","status":"Done","offered":["Review"]}`, issueErr: errors.New("down")}
	tr := writeTracker(t, g, lifecycle(true), newNative(), writeCaps)
	err := tr.TransitionState("7", forge.InProgress, forge.Complete)
	var refused *forge.TransitionRefusedError
	if !errors.As(err, &refused) || refused.From != "" || refused.To != "Done" {
		t.Fatalf("err = %v, want a refusal with empty From", err)
	}
}

func TestTransitionErrorPropagatesWithoutFallback(t *testing.T) {
	boom := errors.New("transition requires field")
	g := &writeGuest{transitionErr: boom}
	tr := writeTracker(t, g, lifecycle(false), newNative(), writeCaps)
	if err := tr.TransitionState("7", forge.InProgress, forge.Complete); !errors.Is(err, boom) {
		t.Fatalf("err = %v, want %v", err, boom)
	}
	if len(g.edits()) != 0 {
		t.Errorf("edits = %+v, want none", g.edits())
	}
}

func TestTransitionUnknownOutcomeIsAnswerError(t *testing.T) {
	g := &writeGuest{transition: `{"outcome":"sideways","offered":[]}`}
	tr := writeTracker(t, g, lifecycle(false), newNative(), writeCaps)
	if err := tr.TransitionState("7", forge.InProgress, forge.Complete); !errors.Is(err, ErrAnswer) {
		t.Fatalf("err = %v, want ErrAnswer", err)
	}
}

func TestClaimAlreadyClaimedByLabel(t *testing.T) {
	g := &writeGuest{transition: `{"outcome":"moved","offered":[]}`, issue: issueJSON("Open", "wip")}
	tr := writeTracker(t, g, lifecycle(false), newNative(), writeCaps)
	err := tr.TransitionState("7", forge.Dispatchable, forge.InProgress)
	if !errors.Is(err, forge.ErrAlreadyClaimed) {
		t.Fatalf("err = %v, want ErrAlreadyClaimed", err)
	}
	if got := g.methods(); !reflect.DeepEqual(got, []string{"tracker.issue"}) {
		t.Errorf("calls = %v, want only the issue read", got)
	}
}

func TestClaimAlreadyClaimedByNativeStatus(t *testing.T) {
	g := &writeGuest{transition: `{"outcome":"moved","offered":[]}`, issue: issueJSON("in progress")}
	tr := writeTracker(t, g, lifecycle(true), newNative(), writeCaps)
	err := tr.TransitionState("7", forge.Dispatchable, forge.InProgress)
	if !errors.Is(err, forge.ErrAlreadyClaimed) {
		t.Fatalf("err = %v, want ErrAlreadyClaimed", err)
	}
	if got := g.methods(); !reflect.DeepEqual(got, []string{"tracker.issue"}) {
		t.Errorf("calls = %v, want only the issue read", got)
	}
}

func TestClaimNativeStatusExemptsSharedFromStatus(t *testing.T) {
	g := &writeGuest{transition: `{"outcome":"moved","offered":[]}`, issue: issueJSON("In Progress")}
	lc := lifecycle(true)
	lc.Statuses = map[forge.DispatchState]string{forge.Dispatchable: "In Progress", forge.InProgress: "In Progress"}
	tr := writeTracker(t, g, lc, newNative(), writeCaps)
	if err := tr.TransitionState("7", forge.Dispatchable, forge.InProgress); err != nil {
		t.Fatalf("err = %v, want the claim to proceed", err)
	}
}

func TestClaimStatusOnlyIgnoresLabelClaim(t *testing.T) {
	g := &writeGuest{transition: `{"outcome":"moved","offered":[]}`, issue: issueJSON("Open", "wip")}
	lc := lifecycle(true)
	lc.Labels = hybridLC.Labels
	tr := writeTracker(t, g, lc, newNative(), writeCaps)
	if err := tr.TransitionState("7", forge.Dispatchable, forge.InProgress); err != nil {
		t.Fatalf("err = %v, want the label claim ignored", err)
	}
}

func TestClaimStripsSettledLabelsBeforeNativeClaim(t *testing.T) {
	g := &writeGuest{transition: `{"outcome":"moved","offered":[]}`, issue: issueJSON("Open", "done", "rec", "other")}
	tr := writeTracker(t, g, lifecycle(false), newNative(), writeCaps)
	if err := tr.TransitionState("7", forge.Dispatchable, forge.InProgress); err != nil {
		t.Fatal(err)
	}
	if got, want := g.methods(), []string{"tracker.issue", CapEditLabels, CapTransition, CapEditLabels}; !reflect.DeepEqual(got, want) {
		t.Fatalf("calls = %v, want %v", got, want)
	}
	strip := g.calls[1].params
	if got := strs(strip["remove"]); !reflect.DeepEqual(got, []string{"done", "rec"}) || len(strs(strip["add"])) != 0 {
		t.Errorf("strip = %+v, want remove [done rec]", strip)
	}
}

func TestClaimSettledStripFailureFailsClaim(t *testing.T) {
	g := &writeGuest{transition: `{"outcome":"moved","offered":[]}`, issue: issueJSON("Open", "done"), editErr: errors.New("boom")}
	tr := writeTracker(t, g, lifecycle(false), newNative(), writeCaps)
	err := tr.TransitionState("7", forge.Dispatchable, forge.InProgress)
	if err == nil || !strings.Contains(err.Error(), "strip settled labels before claim") {
		t.Fatalf("err = %v", err)
	}
	for _, m := range g.methods() {
		if m == CapTransition {
			t.Errorf("transition called after a failed strip: %v", g.methods())
		}
	}
}

func TestClaimWithoutNativeStatusSkipsSettledStrip(t *testing.T) {
	g := &writeGuest{transition: `{"outcome":"unmapped","offered":[]}`, issue: issueJSON("Open", "done")}
	lc := lifecycle(false)
	lc.Statuses = nil
	tr := writeTracker(t, g, lc, newNative(), writeCaps)
	if err := tr.TransitionState("7", forge.Dispatchable, forge.InProgress); err != nil {
		t.Fatal(err)
	}
	// The label swap is the only edit; its removal set already names the settled labels.
	if e := g.edits(); len(e) != 1 || !reflect.DeepEqual(strs(e[0].params["add"]), []string{"wip"}) {
		t.Errorf("edits = %+v, want the one label swap", e)
	}
}

func TestCompleteVerdictSwapsInProgressLabel(t *testing.T) {
	g := &writeGuest{issue: issueJSON("Open", "wip")}
	tr := writeTracker(t, g, lifecycle(false), newNative(), writeCaps)
	if err := tr.CompleteVerdict("7", "recommend"); err != nil {
		t.Fatal(err)
	}
	e := g.edits()
	if len(e) != 1 || !reflect.DeepEqual(strs(e[0].params["add"]), []string{"rec"}) || !reflect.DeepEqual(strs(e[0].params["remove"]), []string{"wip"}) {
		t.Errorf("edits = %+v, want add rec, remove wip", e)
	}
}

func TestCompleteVerdictGuardsMissingInProgressLabel(t *testing.T) {
	g := &writeGuest{issue: issueJSON("Open", "other")}
	tr := writeTracker(t, g, lifecycle(false), newNative(), writeCaps)
	err := tr.CompleteVerdict("7", "recommend")
	if err == nil || !strings.Contains(err.Error(), `expected "wip" label`) {
		t.Fatalf("err = %v", err)
	}
	if len(g.edits()) != 0 {
		t.Errorf("edits = %+v, want none", g.edits())
	}
}

func TestCompleteVerdictUnconfiguredMakesNoCall(t *testing.T) {
	g := &writeGuest{issue: issueJSON("Open", "wip")}
	tr := writeTracker(t, g, lifecycle(false), newNative(), writeCaps)
	if err := tr.CompleteVerdict("7", "reject"); err == nil {
		t.Fatal("want an error for an unconfigured verdict")
	}
	if len(g.calls) != 0 {
		t.Errorf("calls = %v, want none", g.methods())
	}
}

func TestCompleteVerdictWithoutInProgressLabelSkipsRead(t *testing.T) {
	g := &writeGuest{}
	lc := lifecycle(true)
	tr := writeTracker(t, g, lc, newNative(), writeCaps)
	if err := tr.CompleteVerdict("7", "recommend"); err != nil {
		t.Fatal(err)
	}
	if got := g.methods(); !reflect.DeepEqual(got, []string{CapEditLabels}) {
		t.Errorf("calls = %v, want one label edit", got)
	}
}

func TestLabelRegistryPassesThrough(t *testing.T) {
	g := &writeGuest{}
	tr := writeTracker(t, g, lifecycle(false), newNative(), []string{CapLabels})
	got, err := tr.ListLabels()
	if err != nil || !reflect.DeepEqual(got, []string{"a", "b"}) {
		t.Fatalf("ListLabels = %v, %v", got, err)
	}
	if err := tr.CreateLabel("x", "desc", "ff0000"); err != nil {
		t.Fatal(err)
	}
	p := g.calls[1].params
	if g.calls[1].method != "tracker.createLabel" || p["name"] != "x" || p["description"] != "desc" || p["color"] != "ff0000" {
		t.Errorf("createLabel call = %+v", g.calls[1])
	}
}

func TestWritesStayNativeWithoutCapabilities(t *testing.T) {
	for _, caps := range [][]string{nil, {CapTransition}, {CapEditLabels}} {
		g := &writeGuest{}
		native := newNative()
		tr := writeTracker(t, g, lifecycle(false), native, caps)
		if err := tr.TransitionState("7", forge.Dispatchable, forge.InProgress); err != nil {
			t.Fatal(err)
		}
		if err := tr.CompleteVerdict("7", "recommend"); err != nil {
			t.Fatal(err)
		}
		if len(native.TransitionStateCalls) != 1 || len(native.CompleteVerdictCalls) != 1 || len(g.calls) != 0 {
			t.Errorf("caps %v: native calls = %d/%d, guest calls = %v", caps, len(native.TransitionStateCalls), len(native.CompleteVerdictCalls), g.methods())
		}
	}
}

func postCaps(caps ...string) []string { return caps }

func (g *writeGuest) last() recordedCall { return g.calls[len(g.calls)-1] }

func TestCommentStripsFirstResearchMarkerAndFlagsIt(t *testing.T) {
	g := &writeGuest{}
	native := newNative()
	tr := writeTracker(t, g, lifecycle(false), native, postCaps(CapComment))
	body := forge.ResearchMarker + "\nverdict\n  " + forge.ResearchMarker + "  \nquoted"
	if err := tr.Comment("7", body); err != nil {
		t.Fatal(err)
	}
	c := g.last()
	if c.method != CapComment || c.params["number"] != "7" || c.params["research"] != true {
		t.Errorf("call = %+v", c)
	}
	if want := "verdict\n  " + forge.ResearchMarker + "  \nquoted"; c.params["body"] != want {
		t.Errorf("body = %q, want %q", c.params["body"], want)
	}
	if len(native.CommentCalls) != 0 {
		t.Errorf("native comment used: %+v", native.CommentCalls)
	}
}

func TestCommentWithoutMarkerPassesVerbatim(t *testing.T) {
	g := &writeGuest{}
	tr := writeTracker(t, g, lifecycle(false), newNative(), postCaps(CapComment))
	body := "plain\nbody " + forge.ResearchMarker + "\n"
	if err := tr.Comment("7", body); err != nil {
		t.Fatal(err)
	}
	if c := g.last(); c.params["body"] != body || c.params["research"] != false {
		t.Errorf("call = %+v", c)
	}
}

func TestCommentGuestErrorPropagates(t *testing.T) {
	g := &writeGuest{commentErr: errors.New("boom")}
	tr := writeTracker(t, g, lifecycle(false), newNative(), postCaps(CapComment))
	if err := tr.Comment("7", "x"); err == nil || !strings.Contains(err.Error(), "boom") {
		t.Errorf("err = %v", err)
	}
}

func TestPostIssueSplitsDedupMarkerAndSendsIssueType(t *testing.T) {
	g := &writeGuest{fileURL: `{"url":"https://j/browse/P-1"}`}
	lc := lifecycle(false)
	lc.IssueType = "Bug"
	tr := writeTracker(t, g, lc, newNative(), postCaps(CapFileIssue))
	filer, ok := forge.As[forge.HostPostedIssueFiler](tr)
	if !ok {
		t.Fatal("no filer")
	}
	marker := dedupmarker.Build([]string{"a", "b"})
	url, err := filer.PostIssue("title", dedupmarker.Append("desc", marker), []string{"l1"})
	if err != nil || url != "https://j/browse/P-1" {
		t.Fatalf("url, err = %q, %v", url, err)
	}
	p := g.last().params
	if g.last().method != CapFileIssue || p["title"] != "title" || p["body"] != "desc" || p["dedupMarker"] != marker || p["issueType"] != "Bug" || !reflect.DeepEqual(strs(p["labels"]), []string{"l1"}) {
		t.Errorf("call = %+v", g.last())
	}
}

func TestPostIssueNilLabelsSendEmptyList(t *testing.T) {
	g := &writeGuest{fileURL: `{"url":"u"}`}
	tr := writeTracker(t, g, lifecycle(false), newNative(), postCaps(CapFileIssue))
	filer, _ := forge.As[forge.HostPostedIssueFiler](tr)
	if _, err := filer.PostIssue("t", "b", nil); err != nil {
		t.Fatal(err)
	}
	if l, ok := g.last().params["labels"].([]any); !ok || len(l) != 0 {
		t.Errorf("labels = %#v, want []", g.last().params["labels"])
	}
}

func TestPostIssueEmptyURLIsAnswerError(t *testing.T) {
	g := &writeGuest{fileURL: `{"url":""}`}
	tr := writeTracker(t, g, lifecycle(false), newNative(), postCaps(CapFileIssue))
	filer, _ := forge.As[forge.HostPostedIssueFiler](tr)
	if _, err := filer.PostIssue("t", "b", nil); !errors.Is(err, ErrAnswer) {
		t.Errorf("err = %v, want ErrAnswer", err)
	}
}

func TestPostIssueGuestErrorPropagates(t *testing.T) {
	g := &writeGuest{fileErr: errors.New("boom")}
	tr := writeTracker(t, g, lifecycle(false), newNative(), postCaps(CapFileIssue))
	filer, _ := forge.As[forge.HostPostedIssueFiler](tr)
	if _, err := filer.PostIssue("t", "b", nil); err == nil || !strings.Contains(err.Error(), "boom") {
		t.Errorf("err = %v", err)
	}
}

func TestCommentAndFilingStayNativeWithoutCapabilities(t *testing.T) {
	g := &writeGuest{}
	fake := forge.NewFake(testLabels)
	fake.PostIssueURL = "native-url"
	tr := writeTracker(t, g, lifecycle(false), fake.AsIssueFiler(), nil)
	if err := tr.Comment("7", "hi"); err != nil {
		t.Fatal(err)
	}
	filer, ok := forge.As[forge.HostPostedIssueFiler](tr)
	if !ok {
		t.Fatal("native filer hidden")
	}
	if url, err := filer.PostIssue("t", "b", nil); err != nil || url != "native-url" {
		t.Errorf("url, err = %q, %v", url, err)
	}
	if len(fake.CommentCalls) != 1 || len(fake.PostIssueCalls) != 1 || len(g.calls) != 0 {
		t.Errorf("native %d/%d, guest %v", len(fake.CommentCalls), len(fake.PostIssueCalls), g.methods())
	}
}
