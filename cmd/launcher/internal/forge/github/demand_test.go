package github

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"spindrift.dev/launcher/internal/forge"
)

// apiLog returns the fake gh's one-line-per-issues-call record: path,
// If-None-Match value ("-" when absent), and the status it answered.
func apiLog(t *testing.T) []string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(os.Getenv("STATE_DIR"), "api.log"))
	if err != nil {
		t.Fatalf("read api.log: %v", err)
	}
	return strings.Split(strings.TrimSpace(string(b)), "\n")
}

func logEntry(t *testing.T, line string) (path, inm, status string) {
	t.Helper()
	f := strings.Fields(line)
	if len(f) != 3 {
		t.Fatalf("api.log line %q, want 3 fields", line)
	}
	return f[0], f[1], f[2]
}

func seedReady(t *testing.T, h *githubHarness, num string) {
	t.Helper()
	h.SeedIssue(forge.Issue{Number: num, Title: "ready " + num})
	if err := h.tr.TransitionState(num, forge.Untriaged, forge.Dispatchable); err != nil {
		t.Fatalf("TransitionState(%s): %v", num, err)
	}
}

func countReady(t *testing.T, h *githubHarness, fresh bool) int {
	t.Helper()
	n, err := h.tr.(forge.DemandCounter).CountReady(fresh)
	if err != nil {
		t.Fatalf("CountReady(%v): %v", fresh, err)
	}
	return n
}

func TestCountReady_UnchangedProbeIs304(t *testing.T) {
	h := newGithubHarness(t)
	seedReady(t, h, "10")
	seedReady(t, h, "11")

	if got := countReady(t, h, false); got != 2 {
		t.Fatalf("first count = %d, want 2", got)
	}
	if got := countReady(t, h, false); got != 2 {
		t.Fatalf("second count = %d, want cached 2", got)
	}

	log := apiLog(t)
	if len(log) != 2 {
		t.Fatalf("api.log = %v, want 2 calls", log)
	}
	path, inm, status := logEntry(t, log[0])
	if inm != "-" || status != "200" {
		t.Errorf("first call inm=%s status=%s, want - 200", inm, status)
	}
	for _, want := range []string{"sort=updated", "per_page=100", "state=open", "labels=ready-for-agent"} {
		if !strings.Contains(path, want) {
			t.Errorf("path %q lacks %q", path, want)
		}
	}
	_, inm, status = logEntry(t, log[1])
	if inm == "-" || status != "304" {
		t.Errorf("second call inm=%s status=%s, want an etag and 304", inm, status)
	}
}

func TestCountReady_OldIssueGainingLabelIsCounted(t *testing.T) {
	h := newGithubHarness(t)
	h.SeedIssue(forge.Issue{Number: "1", Title: "old"})
	seedReady(t, h, "50")

	if got := countReady(t, h, false); got != 1 {
		t.Fatalf("count = %d, want 1", got)
	}
	if err := h.tr.TransitionState("1", forge.Untriaged, forge.Dispatchable); err != nil {
		t.Fatal(err)
	}
	if got := countReady(t, h, false); got != 2 {
		t.Fatalf("count after relabel = %d, want 2", got)
	}

	log := apiLog(t)
	_, inm, status := logEntry(t, log[1])
	if inm == "-" || status != "200" {
		t.Errorf("relabel probe inm=%s status=%s, want conditional request answered 200", inm, status)
	}
}

func TestCountReady_FreshSkipsConditionalHeader(t *testing.T) {
	h := newGithubHarness(t)
	seedReady(t, h, "10")

	countReady(t, h, false)
	if got := countReady(t, h, true); got != 1 {
		t.Fatalf("fresh count = %d, want 1", got)
	}
	_, inm, status := logEntry(t, apiLog(t)[1])
	if inm != "-" || status != "200" {
		t.Errorf("fresh call inm=%s status=%s, want - 200", inm, status)
	}
	// The fresh read refreshed the cache, so the next probe is conditional again.
	countReady(t, h, false)
	_, inm, status = logEntry(t, apiLog(t)[2])
	if inm == "-" || status != "304" {
		t.Errorf("post-fresh call inm=%s status=%s, want an etag and 304", inm, status)
	}
}

func TestCountReady_PullRequestsNotCounted(t *testing.T) {
	got, err := countNonPRItems([]byte(`[{"number":1},{"number":2,"pull_request":{"url":"x"}},{"number":3}]`))
	if err != nil || got != 2 {
		t.Fatalf("countNonPRItems = %d, %v; want 2, nil", got, err)
	}
	if _, err := countNonPRItems([]byte(`{"message":"nope"}`)); err == nil {
		t.Error("non-array body decoded without error")
	}
}

func TestCountReady_FailureIsError(t *testing.T) {
	h := newGithubHarness(t)
	t.Setenv("PATH", t.TempDir()) // no gh at all
	if _, err := h.tr.(forge.DemandCounter).CountReady(false); err == nil {
		t.Fatal("CountReady with no gh binary succeeded")
	}
}

func TestParseGhInclude(t *testing.T) {
	cases := []struct {
		name       string
		in         string
		wantStatus int
		wantEtag   string
		wantBody   string
		wantErr    bool
	}{
		{"crlf 200", "HTTP/2.0 200 OK\r\nEtag: \"abc\"\r\nContent-Type: x\r\n\r\n[1]", 200, `"abc"`, "[1]", false},
		{"lf only", "HTTP/2.0 200 OK\nEtag: W/\"abc\"\n\n[]", 200, `W/"abc"`, "[]", false},
		{"lowercase header", "HTTP/1.1 200 OK\r\netag: \"z\"\r\n\r\n[]", 200, `"z"`, "[]", false},
		{"304 no body", "HTTP/2.0 304 Not Modified\r\nEtag: \"abc\"\r\n\r\n", 304, `"abc"`, "", false},
		{"304 headers only", "HTTP/2.0 304 Not Modified\r\nEtag: \"abc\"\r\n", 304, `"abc"`, "", false},
		{"no etag", "HTTP/2.0 200 OK\r\n\r\n[]", 200, "", "[]", false},
		{"empty", "", 0, "", "", true},
		{"not http", "[]", 0, "", "", true},
		{"bad status", "HTTP/2.0 abc OK\r\n\r\n", 0, "", "", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r, err := parseGhInclude([]byte(c.in))
			if (err != nil) != c.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, c.wantErr)
			}
			if c.wantErr {
				return
			}
			if r.status != c.wantStatus || r.etag != c.wantEtag || string(r.body) != c.wantBody {
				t.Errorf("got %d %q %q, want %d %q %q", r.status, r.etag, r.body, c.wantStatus, c.wantEtag, c.wantBody)
			}
		})
	}
}

// url.QueryEscape encodes a space as "+", which the fake must decode back.
func TestCountReady_LabelWithSpace(t *testing.T) {
	h := newGithubHarness(t)
	labels := testLabels
	labels.Dispatchable = "ready for agent"
	h.tr = NewExecClient("owner/repo", labels, "agent/issue-")
	seedReady(t, h, "1")
	if got := countReady(t, h, false); got != 1 {
		t.Fatalf("CountReady = %d, want 1", got)
	}
}

func TestCountReady_ErrorCarriesHTTPStatus(t *testing.T) {
	h := newGithubHarness(t)
	t.Setenv("FAKE_GH_ISSUES_FAIL_STATUS", "500")
	_, err := h.tr.(forge.DemandCounter).CountReady(false)
	if err == nil {
		t.Fatal("CountReady against a 500 succeeded")
	}
	if !strings.Contains(err.Error(), "HTTP status 500") {
		t.Errorf("error %q lacks the HTTP status", err)
	}
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		t.Errorf("error %q no longer unwraps to *exec.ExitError", err)
	}
}

func rateLimitedCountReady(t *testing.T, headers string) *forge.RateLimitError {
	t.Helper()
	h := newGithubHarness(t)
	t.Setenv("FAKE_GH_ISSUES_RATE_LIMIT_HEADERS", headers)
	_, err := h.tr.(forge.DemandCounter).CountReady(false)
	if !errors.Is(err, forge.ErrRateLimit) {
		t.Fatalf("CountReady error %v does not match ErrRateLimit", err)
	}
	if !strings.Contains(err.Error(), "HTTP status 403") {
		t.Errorf("error %q lacks the HTTP status", err)
	}
	var rl *forge.RateLimitError
	if !errors.As(err, &rl) {
		t.Fatalf("error %v is not a *RateLimitError", err)
	}
	return rl
}

func TestCountReady_RateLimitResetFromHeaders(t *testing.T) {
	rl := rateLimitedCountReady(t, "X-Ratelimit-Remaining: 0\nX-Ratelimit-Reset: 1900000000")
	if want := time.Unix(1900000000, 0); !rl.Reset.Equal(want) {
		t.Errorf("Reset = %v, want %v", rl.Reset, want)
	}
}

func TestCountReady_RateLimitRetryAfter(t *testing.T) {
	before := time.Now()
	rl := rateLimitedCountReady(t, "Retry-After: 90")
	if rl.Reset.Before(before.Add(90*time.Second)) || rl.Reset.After(time.Now().Add(90*time.Second)) {
		t.Errorf("Reset = %v, want about 90s after %v", rl.Reset, before)
	}
}

func TestCountReady_RateLimitWithoutSignalHasZeroReset(t *testing.T) {
	// X-Ratelimit-Reset is sent on every response, so it only counts at zero remaining.
	rl := rateLimitedCountReady(t, "X-Ratelimit-Remaining: 12\nX-Ratelimit-Reset: 1900000000")
	if !rl.Reset.IsZero() {
		t.Errorf("Reset = %v, want zero", rl.Reset)
	}
}
