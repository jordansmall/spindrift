package connectortracker

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"

	"spindrift.dev/launcher/internal/backend"
	"spindrift.dev/launcher/internal/forge"
)

// mustTracker is newTracker for a Connector whose handshake the test controls.
func mustTracker(t *testing.T, call guestCall, writes forge.IssueTracker, lc Lifecycle, target Target, caps []string, v02 bool) forge.IssueTracker {
	t.Helper()
	tr, err := newTracker(call, writes, lc, target, caps, v02)
	if err != nil {
		t.Fatal(err)
	}
	return tr
}

func TestAbsentCheckCapabilitiesStayAbsent(t *testing.T) {
	call, calls := stubGuest(t, nil)
	tr := mustTracker(t, call, richNative{newNative()}, testLifecycle, testTarget, nil, true)
	c := forge.ResolveCapabilities(nil, tr, backend.Descriptor{}, backend.Descriptor{})
	if c.DemandCounter != nil || c.IssueTypeChecker != nil || c.StatusMappingChecker != nil {
		t.Errorf("absent capabilities resolved: %+v", c)
	}
	if _, err := tr.Probe(); err != nil || len(*calls) != 0 {
		t.Errorf("Probe = %v with %d calls; want the native probe and no call", err, len(*calls))
	}
}

func TestEachCheckCapabilityAppearsAlone(t *testing.T) {
	for capName, want := range map[string][3]bool{
		CapCount:      {true, false, false},
		CapCreateMeta: {false, true, false},
		CapStatuses:   {false, false, true},
	} {
		call, _ := stubGuest(t, map[string]string{"tracker.countInterval": `{"seconds":7}`})
		tr := mustTracker(t, call, richNative{newNative()}, testLifecycle, testTarget, []string{capName}, true)
		c := forge.ResolveCapabilities(nil, tr, backend.Descriptor{}, backend.Descriptor{})
		got := [3]bool{c.DemandCounter != nil, c.IssueTypeChecker != nil, c.StatusMappingChecker != nil}
		if got != want {
			t.Errorf("%s: resolved %v, want %v", capName, got, want)
		}
	}
}

func TestProbeStatusMapping(t *testing.T) {
	for _, tc := range []struct {
		name    string
		reply   string
		err     error
		wantErr error
		auth    bool
	}{
		{name: "200", reply: `{"status":200}`},
		{name: "204", reply: `{"status":204}`},
		{name: "401", reply: `{"status":401}`, wantErr: forge.ErrAuthFailure},
		{name: "403", reply: `{"status":403}`, wantErr: forge.ErrAuthFailure},
		{name: "404", reply: `{"status":404}`, wantErr: forge.ErrRepoNotFound},
		{name: "500", reply: `{"status":500}`, wantErr: forge.ErrRepoNotFound},
		{name: "transport", err: errors.New("boom"), wantErr: forge.ErrRepoNotFound},
	} {
		t.Run(tc.name, func(t *testing.T) {
			call := func(_ context.Context, method string, _, result any) error {
				if method != "tracker.probe" {
					t.Errorf("method = %s", method)
				}
				if tc.err != nil {
					return tc.err
				}
				return jsonInto(tc.reply, result)
			}
			tr := mustTracker(t, call, newNative(), testLifecycle, testTarget, []string{CapProbe}, true)
			got, err := tr.Probe()
			if tc.wantErr == nil {
				if err != nil || got != "o/r" {
					t.Fatalf("Probe = %q, %v; want o/r", got, err)
				}
				return
			}
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("err = %v, want %v", err, tc.wantErr)
			}
			if tc.wantErr == forge.ErrRepoNotFound && errors.Is(err, forge.ErrAuthFailure) {
				t.Errorf("err = %v also wraps ErrAuthFailure", err)
			}
		})
	}
}

func TestStatusesLayer(t *testing.T) {
	lc := Lifecycle{Labels: testLabels, Statuses: map[forge.DispatchState]string{
		forge.Dispatchable: "Ready", forge.InProgress: "Doing", forge.Complete: "",
	}}
	call, calls := stubGuest(t, map[string]string{"tracker.statuses": `{"statuses":["ready","To Do"]}`})
	tr := mustTracker(t, call, newNative(), lc, testTarget, []string{CapStatuses}, true)
	checker, ok := forge.As[forge.StatusMappingChecker](tr)
	if !ok {
		t.Fatal("no StatusMappingChecker")
	}
	missing, err := checker.CheckMappedStatuses()
	if err != nil || !reflect.DeepEqual(missing, []string{"Doing"}) {
		t.Fatalf("missing = %v, %v; want [Doing]", missing, err)
	}
	if p := (*calls)[0].params["target"]; !reflect.DeepEqual(p, map[string]any{"api": "https://api.example", "repo": "o/r", "dialect": ""}) {
		t.Errorf("target = %v", p)
	}
}

func TestStatusesLayerNothingMappedMakesNoCall(t *testing.T) {
	call, calls := stubGuest(t, nil)
	tr := mustTracker(t, call, newNative(), testLifecycle, testTarget, []string{CapStatuses}, true)
	checker, _ := forge.As[forge.StatusMappingChecker](tr)
	missing, err := checker.CheckMappedStatuses()
	if err != nil || missing != nil || len(*calls) != 0 {
		t.Errorf("missing = %v, %v, %d calls; want nil, nil, 0", missing, err, len(*calls))
	}
}

func TestCreateMetaLayer(t *testing.T) {
	lc := Lifecycle{Labels: testLabels, IssueType: "Task"}
	reply := `{"issueTypes":["Task","Bug"],"fields":[
		{"fieldId":"summary","name":"Summary","required":true,"hasDefaultValue":false},
		{"fieldId":"customfield_1","name":"Team","required":true,"hasDefaultValue":false},
		{"fieldId":"customfield_2","name":"Env","required":true,"hasDefaultValue":true},
		{"fieldId":"customfield_3","name":"Opt","required":false,"hasDefaultValue":false}]}`
	call, calls := stubGuest(t, map[string]string{"tracker.createMeta": reply})
	tr := mustTracker(t, call, newNative(), lc, testTarget, []string{CapCreateMeta}, true)
	checker, ok := forge.As[forge.IssueTypeChecker](tr)
	if !ok {
		t.Fatal("no IssueTypeChecker")
	}
	unfilled, err := checker.CheckIssueType()
	if err != nil || !reflect.DeepEqual(unfilled, []string{`"Team" (customfield_1)`}) {
		t.Fatalf("unfilled = %v, %v", unfilled, err)
	}
	if got := (*calls)[0].params["issueType"]; got != "Task" {
		t.Errorf("issueType = %v, want Task", got)
	}

	lc.IssueType = "Epic"
	tr = mustTracker(t, call, newNative(), lc, testTarget, []string{CapCreateMeta}, true)
	checker, _ = forge.As[forge.IssueTypeChecker](tr)
	if _, err := checker.CheckIssueType(); !errors.Is(err, forge.ErrUnknownIssueType) {
		t.Errorf("err = %v, want ErrUnknownIssueType", err)
	}
}

func TestCountLayer(t *testing.T) {
	lc := Lifecycle{Labels: testLabels, Statuses: map[forge.DispatchState]string{forge.Dispatchable: "Ready"}}
	call, calls := stubGuest(t, map[string]string{
		"tracker.countInterval": `{"seconds":90}`,
		"tracker.count":         `{"count":4}`,
	})
	tr := mustTracker(t, call, newNative(), lc, testTarget, []string{CapCount}, true)
	dc, ok := forge.As[forge.DemandCounter](tr)
	if !ok {
		t.Fatal("no DemandCounter")
	}
	if dc.ProbeInterval() != 90*time.Second {
		t.Errorf("ProbeInterval = %v", dc.ProbeInterval())
	}
	n, err := dc.CountReady(true)
	if err != nil || n != 4 {
		t.Fatalf("CountReady = %d, %v", n, err)
	}
	if _, err := dc.CountReady(false); err != nil {
		t.Fatal(err)
	}
	if len(*calls) != 3 || (*calls)[0].method != "tracker.countInterval" {
		t.Fatalf("calls = %v; want one interval read then two counts", *calls)
	}
	p := (*calls)[1].params
	if p["label"] != "ready-for-agent" || p["status"] != "Ready" {
		t.Errorf("count params = %v, want both selectors", p)
	}
}

func TestCountLayerNoSelectorMakesNoCall(t *testing.T) {
	call, calls := stubGuest(t, map[string]string{"tracker.countInterval": `{"seconds":5}`})
	tr := mustTracker(t, call, newNative(), Lifecycle{}, testTarget, []string{CapCount}, true)
	dc, _ := forge.As[forge.DemandCounter](tr)
	n, err := dc.CountReady(false)
	if err != nil || n != 0 || len(*calls) != 1 {
		t.Errorf("CountReady = %d, %v with %d calls; want 0, nil, only the interval read", n, err, len(*calls))
	}
}

func TestCountLayerRefusesNegativeCount(t *testing.T) {
	call, _ := stubGuest(t, map[string]string{"tracker.countInterval": `{"seconds":5}`, "tracker.count": `{"count":-1}`})
	tr := mustTracker(t, call, newNative(), testLifecycle, testTarget, []string{CapCount}, true)
	dc, _ := forge.As[forge.DemandCounter](tr)
	if _, err := dc.CountReady(false); !errors.Is(err, ErrAnswer) {
		t.Errorf("err = %v, want ErrAnswer", err)
	}
}

func TestCountIntervalBelowOneSecondRefused(t *testing.T) {
	call, _ := stubGuest(t, map[string]string{"tracker.countInterval": `{"seconds":0}`})
	_, err := newTracker(call, newNative(), testLifecycle, testTarget, []string{CapCount}, true)
	if !errors.Is(err, ErrAnswer) {
		t.Errorf("err = %v, want ErrAnswer", err)
	}
}

func TestCountIntervalOverflowRefused(t *testing.T) {
	call, _ := stubGuest(t, map[string]string{"tracker.countInterval": `{"seconds":10000000000}`})
	_, err := newTracker(call, newNative(), testLifecycle, testTarget, []string{CapCount}, true)
	if !errors.Is(err, ErrAnswer) {
		t.Errorf("err = %v, want ErrAnswer", err)
	}
}

func jsonInto(raw string, result any) error { return json.Unmarshal([]byte(raw), result) }
