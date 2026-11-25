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
	"spindrift.dev/launcher/internal/connectorprotocol/schematest"
	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/retry"
)

var testTarget = Target{API: "https://api.example", Repo: "o/r"}

const testWebHost = "github.com"

const prURL7 = "https://github.com/o/r/pull/7"

type recordedCall = schematest.Call

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
		calls = append(calls, recordedCall{Method: method, Params: p})
		reply, ok := replies[method]
		if !ok {
			return errors.New("guest: no such method " + method)
		}
		return json.Unmarshal([]byte(reply), result)
	}, &calls
}

func newTestForge(t *testing.T, replies map[string]string) (*reads, *[]recordedCall) {
	call, calls := stubGuest(t, replies)
	p := pushOnlyOver(auditStub{}, call, retry.Clock{}, testTarget)
	p.rd.webHost = testWebHost
	return p.rd, calls
}

func TestNewReaderRequiresCapability(t *testing.T) {
	c := &connectorhost.Connector{Capabilities: []string{"tracker"}}
	_, err := NewReader(c, testTarget, testWebHost)
	if !errors.Is(err, ErrNotForge) {
		t.Fatalf("err = %v, want ErrNotForge", err)
	}
}

func TestNewReaderRefusesEmptyWebHost(t *testing.T) {
	c := &connectorhost.Connector{Capabilities: []string{"forge"}}
	_, err := NewReader(c, testTarget, "")
	if !errors.Is(err, ErrNoWebHost) {
		t.Fatalf("err = %v, want ErrNoWebHost", err)
	}
}

func TestOpenPRForBranchPicksFirstOpen(t *testing.T) {
	replies := map[string]string{"forge.prsForBranch": `{"prs":[
		{"number":"9","url":"https://github.com/o/r/pull/9","state":"closed","merged":true},
		{"number":"8","url":"https://github.com/o/r/pull/8","state":"open","merged":false},
		{"number":"7","url":"https://github.com/o/r/pull/7","state":"open","merged":false}]}`}
	f, calls := newTestForge(t, replies)
	pr, ok, err := f.OpenPRForBranch("agent/issue-1")
	if err != nil || !ok || pr.URL != "https://github.com/o/r/pull/8" {
		t.Fatalf("got %+v, %v, %v; want PR 8", pr, ok, err)
	}
	want := recordedCall{Method: "forge.prsForBranch", Params: map[string]any{
		"target": map[string]any{"api": "https://api.example", "repo": "o/r"},
		"branch": "agent/issue-1",
	}}
	if !reflect.DeepEqual((*calls)[0], want) {
		t.Errorf("call = %+v, want %+v", (*calls)[0], want)
	}
	schematest.RequireConforms(t, *calls, replies)
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
			want := recordedCall{Method: "forge.pr", Params: map[string]any{
				"target": map[string]any{"api": "https://api.example", "repo": "o/r"},
				"number": "7",
			}}
			if !reflect.DeepEqual((*calls)[0], want) {
				t.Errorf("call = %+v, want %+v", (*calls)[0], want)
			}
			schematest.RequireConforms(t, *calls, map[string]string{"forge.pr": tc.reply})
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
		if (*calls)[0].Method != "forge.checks" || (*calls)[0].Params["number"] != "7" {
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
	replies := map[string]string{
		"forge.checks": rollupJSON(t, []wireCheck{
			{Kind: "CheckRun", Name: "build", Conclusion: "SUCCESS", JobID: "1"},
			{Kind: "CheckRun", Name: "unit", Conclusion: "FAILURE", Summary: "exit 1", JobID: "42"},
			{Kind: "CheckRun", Name: "app", Conclusion: "FAILURE"}, // not a workflow job
		}),
		"forge.jobLog": `{"log":"--- FAIL: TestX\nboom\n"}`,
	}
	f, calls := newTestForge(t, replies)
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
		if c.Method == "forge.jobLog" {
			logCalls = append(logCalls, c)
		}
	}
	wantCall := recordedCall{Method: "forge.jobLog", Params: map[string]any{
		"target":     map[string]any{"api": "https://api.example", "repo": "o/r"},
		"jobId":      "42",
		"tail":       float64(forge.JobLogKeepBytes),
		"errorLines": float64(forge.MaxLogExcerptBytes),
	}}
	if len(logCalls) != 1 || !reflect.DeepEqual(logCalls[0], wantCall) {
		t.Errorf("jobLog calls = %v, want [%v]", logCalls, wantCall)
	}
	schematest.RequireConforms(t, *calls, replies)
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
		if c.Method == "forge.jobLog" {
			t.Errorf("forge.jobLog called after the budget was spent: %+v", c)
		}
	}
}

// Forgejo names a PR's web page /<owner>/<repo>/pulls/<n>, where GitHub's is
// /pull/<n>; both name the target repository's PR.
func TestBranchReadsAcceptForgejoPullsPath(t *testing.T) {
	f, _ := newTestForge(t, map[string]string{"forge.prsForBranch": `{"prs":[{"number":"8","url":"https://github.com/o/r/pulls/8","state":"open","merged":false}]}`})
	pr, ok, err := f.OpenPRForBranch("b")
	if err != nil || !ok || pr.URL != "https://github.com/o/r/pulls/8" {
		t.Fatalf("OpenPRForBranch = (%+v, %v, %v), want the /pulls/ PR", pr, ok, err)
	}
}

// A forge served under a subpath keeps its PR URLs and run links under that
// prefix, matched by whole segments.
func TestURLChecksStripTheWebBasePath(t *testing.T) {
	r := &reads{target: testTarget, webHost: "example.org", webPath: "/forgejo"}
	for _, tc := range []struct {
		name, url string
		ok        bool
	}{
		{"own PR", "https://example.org/forgejo/o/r/pulls/3", true},
		{"own PR, mixed case", "https://Example.org/forgejo/O/R/pull/3", true},
		{"no prefix", "https://example.org/o/r/pulls/3", false},
		{"partial segment", "https://example.org/forgejoX/o/r/pulls/3", false},
		{"other prefix", "https://example.org/other/o/r/pulls/3", false},
		{"prefix only", "https://example.org/forgejo", false},
		{"doubled prefix", "https://example.org/forgejo/forgejo/o/r/pulls/3", false},
	} {
		t.Run("PR "+tc.name, func(t *testing.T) {
			n, err := r.prNumber(tc.url)
			if tc.ok != (err == nil) || (tc.ok && n != "3") {
				t.Fatalf("prNumber(%q) = %q, %v, want ok=%t", tc.url, n, err, tc.ok)
			}
		})
	}
	for _, tc := range []struct {
		name, url string
		ok        bool
	}{
		{"own run", "https://example.org/forgejo/o/r/actions/runs/1", true},
		{"no prefix", "https://example.org/o/r/actions/runs/1", false},
		{"partial segment", "https://example.org/forgejoX/o/r/actions/runs/1", false},
		{"escape", "https://example.org/forgejo/o/r/../../x/y/actions/runs/1", false},
		{"escape the prefix", "https://example.org/forgejo/../o/r/actions/runs/1", false},
	} {
		t.Run("run "+tc.name, func(t *testing.T) {
			want := ""
			if tc.ok {
				want = tc.url
			}
			if got := r.ownRunLink(tc.url); got != want {
				t.Fatalf("ownRunLink(%q) = %q, want %q", tc.url, got, want)
			}
		})
	}
}
