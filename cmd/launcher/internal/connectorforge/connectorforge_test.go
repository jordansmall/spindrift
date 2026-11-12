package connectorforge

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"spindrift.dev/launcher/internal/connectorhost"
	"spindrift.dev/launcher/internal/forge"
)

var testTarget = Target{API: "https://api.example", Repo: "o/r"}

const testWebHost = "github.com"

const prURL7 = "https://github.com/o/r/pull/7"

type recordedCall struct {
	method string
	params map[string]any
}

// stubGuest answers each method with the JSON in replies and records the calls.
func stubGuest(t *testing.T, replies map[string]string) (guestCall, *[]recordedCall) {
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

// failingWrites is the native adapter's role: the five reads must never reach
// it. Mergeable stands in for every method the wrapper leaves to it.
type failingWrites struct {
	forge.PRForge
	t *testing.T
}

func (w failingWrites) OpenPRForBranch(string) (forge.PR, bool, error) {
	w.t.Errorf("OpenPRForBranch reached the native adapter")
	return forge.PR{}, false, nil
}
func (w failingWrites) PRForBranch(string) (string, bool, error) {
	w.t.Errorf("PRForBranch reached the native adapter")
	return "", false, nil
}
func (w failingWrites) PRState(string) (forge.PRState, error) {
	w.t.Errorf("PRState reached the native adapter")
	return "", nil
}
func (w failingWrites) CheckState(string) (forge.RollupState, error) {
	w.t.Errorf("CheckState reached the native adapter")
	return "", nil
}
func (w failingWrites) FailureDetail(string) (string, error) {
	w.t.Errorf("FailureDetail reached the native adapter")
	return "", nil
}
func (w failingWrites) Mergeable(string) (forge.MergeableState, error) {
	return "native", nil
}

func newTestForge(t *testing.T, replies map[string]string) (forge.PRForge, *[]recordedCall) {
	call, calls := stubGuest(t, replies)
	return newForge(call, failingWrites{t: t}, testTarget, testWebHost), calls
}

func TestNewRequiresCapability(t *testing.T) {
	c := &connectorhost.Connector{Capabilities: []string{"tracker"}}
	_, err := New(c, failingWrites{t: t}, testTarget, testWebHost)
	if !errors.Is(err, ErrNotForge) {
		t.Fatalf("err = %v, want ErrNotForge", err)
	}
}

func TestNewRefusesEmptyWebHost(t *testing.T) {
	c := &connectorhost.Connector{Capabilities: []string{"forge"}}
	_, err := New(c, failingWrites{t: t}, testTarget, "")
	if !errors.Is(err, ErrNoWebHost) {
		t.Fatalf("err = %v, want ErrNoWebHost", err)
	}
}

func TestUnownedMethodsDelegateToWrites(t *testing.T) {
	f, calls := newTestForge(t, nil)
	got, err := f.Mergeable(prURL7)
	if err != nil || got != "native" {
		t.Fatalf("Mergeable = %q, %v; want the native answer", got, err)
	}
	if len(*calls) != 0 {
		t.Errorf("guest called for a write-side method: %+v", *calls)
	}
}

func TestOpenPRForBranchPicksFirstOpen(t *testing.T) {
	f, calls := newTestForge(t, map[string]string{"forge.prsForBranch": `{"prs":[
		{"number":"9","url":"https://github.com/o/r/pull/9","state":"closed","merged":true},
		{"number":"8","url":"https://github.com/o/r/pull/8","state":"open","merged":false},
		{"number":"7","url":"https://github.com/o/r/pull/7","state":"open","merged":false}]}`})
	pr, ok, err := f.OpenPRForBranch("agent/issue-1")
	if err != nil || !ok || pr.URL != "https://github.com/o/r/pull/8" {
		t.Fatalf("got %+v, %v, %v; want PR 8", pr, ok, err)
	}
	want := recordedCall{"forge.prsForBranch", map[string]any{
		"target": map[string]any{"api": "https://api.example", "repo": "o/r"},
		"branch": "agent/issue-1",
	}}
	if !reflect.DeepEqual((*calls)[0], want) {
		t.Errorf("call = %+v, want %+v", (*calls)[0], want)
	}
}

func TestOpenPRForBranchOnlyClosedOrEmpty(t *testing.T) {
	for name, reply := range map[string]string{
		"closed": `{"prs":[{"number":"9","url":"https://github.com/o/r/pull/9","state":"closed","merged":false}]}`,
		"empty":  `{"prs":[]}`,
	} {
		t.Run(name, func(t *testing.T) {
			f, _ := newTestForge(t, map[string]string{"forge.prsForBranch": reply})
			pr, ok, err := f.OpenPRForBranch("b")
			if err != nil || ok || pr != (forge.PR{}) {
				t.Fatalf("got %+v, %v, %v; want zero, false, nil", pr, ok, err)
			}
		})
	}
}

func TestPRForBranchPicksFirstOfAnyState(t *testing.T) {
	f, _ := newTestForge(t, map[string]string{"forge.prsForBranch": `{"prs":[
		{"number":"9","url":"https://github.com/o/r/pull/9","state":"closed","merged":true},
		{"number":"8","url":"https://github.com/o/r/pull/8","state":"open","merged":false}]}`})
	url, ok, err := f.PRForBranch("b")
	if err != nil || !ok || url != "https://github.com/o/r/pull/9" {
		t.Fatalf("got %q, %v, %v; want PR 9", url, ok, err)
	}

	f, _ = newTestForge(t, map[string]string{"forge.prsForBranch": `{"prs":[]}`})
	url, ok, err = f.PRForBranch("b")
	if err != nil || ok || url != "" {
		t.Fatalf("empty: got %q, %v, %v; want zero, false, nil", url, ok, err)
	}
}

func TestBranchReadsFailClosedOnAnyBadPR(t *testing.T) {
	good := `{"number":"8","url":"https://github.com/o/r/pull/8","state":"open","merged":false}`
	bad := map[string]string{
		"zero number":       `{"number":"0","url":"https://github.com/o/r/pull/0","state":"open","merged":false}`,
		"padded number":     `{"number":"07","url":"https://github.com/o/r/pull/07","state":"open","merged":false}`,
		"signed number":     `{"number":"+7","url":"https://github.com/o/r/pull/7","state":"open","merged":false}`,
		"url mismatch":      `{"number":"7","url":"https://github.com/o/r/pull/9","state":"open","merged":false}`,
		"foreign repo":      `{"number":"7","url":"https://github.com/x/y/pull/7","state":"open","merged":false}`,
		"foreign host":      `{"number":"7","url":"https://attacker.example/o/r/pull/7","state":"open","merged":false}`,
		"userinfo":          `{"number":"7","url":"https://github.com@attacker.example/o/r/pull/7","state":"open","merged":false}`,
		"port":              `{"number":"7","url":"https://github.com:443/o/r/pull/7","state":"open","merged":false}`,
		"trailing dot host": `{"number":"7","url":"https://github.com./o/r/pull/7","state":"open","merged":false}`,
		"http url":          `{"number":"7","url":"http://github.com/o/r/pull/7","state":"open","merged":false}`,
		"unknown state":     `{"number":"7","url":"https://github.com/o/r/pull/7","state":"draft","merged":false}`,
		"merged but open":   `{"number":"7","url":"https://github.com/o/r/pull/7","state":"open","merged":true}`,
		"empty state":       `{"number":"7","url":"https://github.com/o/r/pull/7","state":"","merged":false}`,
		"url with query":    `{"number":"7","url":"https://github.com/o/r/pull/7?x=1","state":"open","merged":false}`,
		"url with suffix":   `{"number":"7","url":"https://github.com/o/r/pull/7/files","state":"open","merged":false}`,
		"url with newline":  "{\"number\":\"7\",\"url\":\"https://github.com/o/r/pull/7\\n\",\"state\":\"open\",\"merged\":false}",
	}
	for name, pr := range bad {
		// The bad PR trails the good one: every PR is validated, not just the pick.
		reply := `{"prs":[` + good + `,` + pr + `]}`
		t.Run(name, func(t *testing.T) {
			f, _ := newTestForge(t, map[string]string{"forge.prsForBranch": reply})
			if _, _, err := f.OpenPRForBranch("b"); !errors.Is(err, ErrAnswer) {
				t.Errorf("OpenPRForBranch err = %v, want ErrAnswer", err)
			}
			if _, _, err := f.PRForBranch("b"); !errors.Is(err, ErrAnswer) {
				t.Errorf("PRForBranch err = %v, want ErrAnswer", err)
			}
		})
	}
}

func TestPRStateMapsStates(t *testing.T) {
	for name, tc := range map[string]struct {
		reply string
		want  forge.PRState
	}{
		"open":   {`{"number":"7","url":"https://github.com/o/r/pull/7","state":"open","merged":false}`, forge.PROpen},
		"merged": {`{"number":"7","url":"https://github.com/o/r/pull/7","state":"closed","merged":true}`, forge.PRMerged},
		"closed": {`{"number":"7","url":"https://github.com/o/r/pull/7","state":"closed","merged":false}`, forge.PRClosed},
	} {
		t.Run(name, func(t *testing.T) {
			f, calls := newTestForge(t, map[string]string{"forge.pr": tc.reply})
			got, err := f.PRState(prURL7)
			if err != nil || got != tc.want {
				t.Fatalf("PRState = %q, %v; want %q", got, err, tc.want)
			}
			want := recordedCall{"forge.pr", map[string]any{
				"target": map[string]any{"api": "https://api.example", "repo": "o/r"},
				"number": "7",
			}}
			if !reflect.DeepEqual((*calls)[0], want) {
				t.Errorf("call = %+v, want %+v", (*calls)[0], want)
			}
		})
	}
}

func TestPRStateFailsClosed(t *testing.T) {
	f, calls := newTestForge(t, map[string]string{
		"forge.pr": `{"number":"8","url":"https://github.com/o/r/pull/8","state":"open","merged":false}`,
	})
	if _, err := f.PRState(prURL7); !errors.Is(err, ErrAnswer) {
		t.Errorf("answer for another PR: err = %v, want ErrAnswer", err)
	}
	for name, url := range map[string]string{
		"http":              "http://github.com/o/r/pull/7",
		"foreign repo":      "https://github.com/x/y/pull/7",
		"foreign host":      "https://attacker.example/o/r/pull/7",
		"userinfo":          "https://github.com@attacker.example/o/r/pull/7",
		"port":              "https://github.com:443/o/r/pull/7",
		"trailing dot host": "https://github.com./o/r/pull/7",
		"issue path":        "https://github.com/o/r/issues/7",
		"zero":              "https://github.com/o/r/pull/0",
		"leading zero":      "https://github.com/o/r/pull/07",
		"not a number":      "https://github.com/o/r/pull/x",
		"trailing":          "https://github.com/o/r/pull/7/files",
		"empty":             "",
	} {
		before := len(*calls)
		_, err := f.PRState(url)
		if err == nil {
			t.Errorf("%s: PRState(%q) succeeded, want error", name, url)
		}
		if len(*calls) != before {
			t.Errorf("%s: guest called for a malformed URL", name)
		}
	}
	// Repo comparison is case-insensitive.
	f, _ = newTestForge(t, map[string]string{
		"forge.pr": `{"number":"7","url":"https://github.com/O/R/pull/7","state":"open","merged":false}`,
	})
	if got, err := f.PRState("https://github.com/O/r/pull/7"); err != nil || got != forge.PROpen {
		t.Errorf("mixed case: got %q, %v; want OPEN", got, err)
	}
}

func TestPRStateRejectsBadAnswer(t *testing.T) {
	for name, reply := range map[string]string{
		"unknown state": `{"number":"7","url":"https://github.com/o/r/pull/7","state":"weird","merged":false}`,
		"merged open":   `{"number":"7","url":"https://github.com/o/r/pull/7","state":"open","merged":true}`,
		"url mismatch":  `{"number":"7","url":"https://github.com/o/r/pull/8","state":"open","merged":false}`,
	} {
		t.Run(name, func(t *testing.T) {
			f, _ := newTestForge(t, map[string]string{"forge.pr": reply})
			if _, err := f.PRState(prURL7); !errors.Is(err, ErrAnswer) {
				t.Errorf("err = %v, want ErrAnswer", err)
			}
		})
	}
}

func TestCheckStateMapsRollup(t *testing.T) {
	for state, want := range map[string]forge.RollupState{
		"":         forge.StateNone,
		"SUCCESS":  forge.StateSuccess,
		"PENDING":  forge.StatePending,
		"EXPECTED": forge.StateExpected,
		"FAILURE":  forge.StateFailure,
		"ERROR":    forge.StateError,
	} {
		f, calls := newTestForge(t, map[string]string{"forge.checks": `{"state":"` + state + `","checks":[]}`})
		got, err := f.CheckState(prURL7)
		if err != nil || got != want {
			t.Errorf("CheckState(%q) = %q, %v; want %q", state, got, err, want)
		}
		if (*calls)[0].method != "forge.checks" || (*calls)[0].params["number"] != "7" {
			t.Errorf("call = %+v", (*calls)[0])
		}
	}
	for _, state := range []string{"success", "NONE", "WEIRD"} {
		f, _ := newTestForge(t, map[string]string{"forge.checks": `{"state":"` + state + `","checks":[]}`})
		if _, err := f.CheckState(prURL7); !errors.Is(err, ErrAnswer) {
			t.Errorf("CheckState(%q) err = %v, want ErrAnswer", state, err)
		}
	}
	f, _ := newTestForge(t, nil)
	if _, err := f.CheckState("https://github.com/x/y/pull/7"); err == nil {
		t.Error("CheckState accepted a foreign-repo URL")
	}
}

func rollupJSON(t *testing.T, checks []wireCheck) string {
	t.Helper()
	b, err := json.Marshal(wireRollup{State: "FAILURE", Checks: checks})
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestFailureDetailFiltersAndRenders(t *testing.T) {
	checks := []wireCheck{
		{Kind: "CheckRun", Name: "build", Conclusion: "SUCCESS", Summary: "ok"},
		{Kind: "CheckRun", Name: "lint", Conclusion: "FAILURE", Summary: "bad style"},
		{Kind: "CheckRun", Name: "skip", Conclusion: "SKIPPED"},
		{Kind: "CheckRun", Name: "neutral", Conclusion: "NEUTRAL"},
		{Kind: "CheckRun", Name: "slow", Conclusion: "TIMED_OUT"},
		{Kind: "CheckRun", Name: "stop", Conclusion: "CANCELLED"},
		{Kind: "CheckRun", Name: "gate", Conclusion: "ACTION_REQUIRED"},
		{Kind: "CheckRun", Name: "boot", Conclusion: "STARTUP_FAILURE"},
		{Kind: "StatusContext", Name: "ci/legacy", Conclusion: "ERROR", Summary: "boom"},
		{Kind: "StatusContext", Name: "ci/ok", Conclusion: "SUCCESS"},
		{Kind: "StatusContext", Name: "ci/wait", Conclusion: "PENDING"},
		{Kind: "StatusContext", Name: "ci/cancelled", Conclusion: "CANCELLED"}, // not a status state
		{Kind: "CheckRun", Name: "cr-error", Conclusion: "ERROR"},              // not a check-run conclusion
		{Kind: "Other", Name: "mystery", Conclusion: "FAILURE"},
	}
	f, _ := newTestForge(t, map[string]string{"forge.checks": rollupJSON(t, checks)})
	got, err := f.FailureDetail(prURL7)
	if err != nil {
		t.Fatal(err)
	}
	want := forge.RenderFailureDetail([]forge.FailureDetailEntry{
		{Name: "lint", State: "FAILURE", Summary: "bad style"},
		{Name: "slow", State: "TIMED_OUT"},
		{Name: "stop", State: "CANCELLED"},
		{Name: "gate", State: "ACTION_REQUIRED"},
		{Name: "boot", State: "STARTUP_FAILURE"},
		{Name: "ci/legacy", State: "ERROR", Summary: "boom"},
	})
	if got != want {
		t.Errorf("FailureDetail =\n%s\nwant\n%s", got, want)
	}
	if !strings.HasPrefix(got, "lint: FAILURE\nbad style\n---\n") {
		t.Errorf("rendering drifted: %q", got)
	}
}

func TestFailureDetailEmptyWhenNothingFails(t *testing.T) {
	f, _ := newTestForge(t, map[string]string{"forge.checks": rollupJSON(t, []wireCheck{
		{Kind: "CheckRun", Name: "build", Conclusion: "SUCCESS"},
		{Kind: "StatusContext", Name: "ci", Conclusion: "PENDING"},
	})})
	got, err := f.FailureDetail(prURL7)
	if err != nil || got != "" {
		t.Fatalf("FailureDetail = %q, %v; want empty", got, err)
	}
}

func TestFailureDetailIsCappedByCore(t *testing.T) {
	huge := strings.Repeat("x", 10*forge.MaxFailureDetailBytes)
	f, _ := newTestForge(t, map[string]string{"forge.checks": rollupJSON(t, []wireCheck{
		{Kind: "CheckRun", Name: "build", Conclusion: "FAILURE", Summary: huge},
	})})
	got, err := f.FailureDetail(prURL7)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) > forge.MaxFailureDetailBytes {
		t.Errorf("len = %d, want <= %d", len(got), forge.MaxFailureDetailBytes)
	}
}

func TestGuestErrorsPropagate(t *testing.T) {
	f, _ := newTestForge(t, nil) // the stub guest has no methods
	if _, _, err := f.OpenPRForBranch("b"); err == nil || errors.Is(err, ErrAnswer) {
		t.Errorf("OpenPRForBranch err = %v, want the guest's error", err)
	}
	if _, err := f.PRState(prURL7); err == nil {
		t.Error("PRState swallowed the guest error")
	}
	if _, err := f.CheckState(prURL7); err == nil {
		t.Error("CheckState swallowed the guest error")
	}
	if _, err := f.FailureDetail(prURL7); err == nil {
		t.Error("FailureDetail swallowed the guest error")
	}
}

func TestFailureDetailCarriesTheFailingJobLog(t *testing.T) {
	f, calls := newTestForge(t, map[string]string{
		"forge.checks": rollupJSON(t, []wireCheck{
			{Kind: "CheckRun", Name: "build", Conclusion: "SUCCESS", JobID: "1"},
			{Kind: "CheckRun", Name: "unit", Conclusion: "FAILURE", Summary: "exit 1", JobID: "42"},
			{Kind: "CheckRun", Name: "app", Conclusion: "FAILURE"}, // not a workflow job
		}),
		"forge.jobLog": `{"log":"--- FAIL: TestX\nboom\n"}`,
	})
	got, err := f.FailureDetail(prURL7)
	if err != nil {
		t.Fatal(err)
	}
	want := forge.RenderFailureDetail([]forge.FailureDetailEntry{
		{Name: "unit", State: "FAILURE", Summary: "exit 1", Log: "--- FAIL: TestX\nboom\n"},
		{Name: "app", State: "FAILURE"},
	})
	if got != want {
		t.Errorf("FailureDetail =\n%s\nwant\n%s", got, want)
	}
	var logCalls []recordedCall
	for _, c := range *calls {
		if c.method == "forge.jobLog" {
			logCalls = append(logCalls, c)
		}
	}
	wantCall := recordedCall{"forge.jobLog", map[string]any{
		"target": map[string]any{"api": "https://api.example", "repo": "o/r"},
		"jobId":  "42",
		"tail":   float64(forge.JobLogKeepBytes),
	}}
	if len(logCalls) != 1 || !reflect.DeepEqual(logCalls[0], wantCall) {
		t.Errorf("jobLog calls = %v, want [%v]", logCalls, wantCall)
	}
}

func TestFailureDetailSurvivesAnUnreadableLog(t *testing.T) {
	// The stub guest has no forge.jobLog, as with a Connector that predates it
	// or a log the forge refuses.
	f, _ := newTestForge(t, map[string]string{"forge.checks": rollupJSON(t, []wireCheck{
		{Kind: "CheckRun", Name: "unit", Conclusion: "FAILURE", Summary: "exit 1", JobID: "42"},
	})})
	got, err := f.FailureDetail(prURL7)
	if err != nil {
		t.Fatal(err)
	}
	if want := "unit: FAILURE\nexit 1\n---"; got != want {
		t.Errorf("FailureDetail = %q, want %q", got, want)
	}
}

func TestFailureDetailCapsAHostileLog(t *testing.T) {
	huge, _ := json.Marshal(strings.Repeat("x\n", 10*forge.MaxFailureDetailBytes))
	f, _ := newTestForge(t, map[string]string{
		"forge.checks": rollupJSON(t, []wireCheck{{Kind: "CheckRun", Name: "unit", Conclusion: "FAILURE", JobID: "42"}}),
		"forge.jobLog": `{"log":` + string(huge) + `}`,
	})
	got, err := f.FailureDetail(prURL7)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) > forge.MaxFailureDetailBytes {
		t.Errorf("len = %d, want <= %d", len(got), forge.MaxFailureDetailBytes)
	}
}

func TestFailureDetailSkipsLogsOnceTheBudgetIsSpent(t *testing.T) {
	old := jobLogsBudget
	jobLogsBudget = -time.Second
	t.Cleanup(func() { jobLogsBudget = old })
	f, calls := newTestForge(t, map[string]string{
		"forge.checks": rollupJSON(t, []wireCheck{
			{Kind: "CheckRun", Name: "unit", Conclusion: "FAILURE", Summary: "exit 1", JobID: "42"},
		}),
		"forge.jobLog": `{"log":"boom\n"}`,
	})
	got, err := f.FailureDetail(prURL7)
	if err != nil {
		t.Fatal(err)
	}
	if want := "unit: FAILURE\nexit 1\n---"; got != want {
		t.Errorf("FailureDetail = %q, want %q", got, want)
	}
	for _, c := range *calls {
		if c.method == "forge.jobLog" {
			t.Errorf("forge.jobLog called after the budget was spent: %+v", c)
		}
	}
}
