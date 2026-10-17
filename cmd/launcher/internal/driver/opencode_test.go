package driver

import (
	"bytes"
	"database/sql"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"spindrift.dev/launcher/internal/driver/driverkit"
	"spindrift.dev/launcher/internal/testutil"
)

func TestNewSelectsOpencodeByName(t *testing.T) {
	d, err := New("opencode")
	if err != nil {
		t.Fatalf("New(opencode): %v", err)
	}
	if d.Name() != "opencode" {
		t.Errorf("Name(): got %q, want %q", d.Name(), "opencode")
	}
}

// The raw sink must stay byte-exact, which is what the opencode heartbeat
// writer promises at the Driver seam (issue #2092).
func TestOpencodeDriverHeartbeatWriterForwardsRaw(t *testing.T) {
	d, err := New("opencode")
	if err != nil {
		t.Fatalf("New(opencode): %v", err)
	}

	var raw, out bytes.Buffer
	w := d.NewHeartbeatWriter(&raw, "77", &out, driverkit.RenderOptions{})

	ndjson := `{"type":"text","part":{"text":"doing the thing"}}` + "\n"
	if _, err := w.Write([]byte(ndjson)); err != nil {
		t.Fatalf("Write: %v", err)
	}

	if raw.String() != ndjson {
		t.Errorf("raw not byte-exact:\ngot:  %q\nwant: %q", raw.String(), ndjson)
	}
	if !strings.Contains(out.String(), "#77") {
		t.Errorf("heartbeat output missing issue prefix: %q", out.String())
	}
	if !strings.Contains(out.String(), "doing the thing") {
		t.Errorf("heartbeat output missing text event's first line: %q", out.String())
	}
}

// opencode's heartbeat carries no role attribution, so topLevelRole must
// change nothing (issue #2092).
func TestOpencodeDriverHeartbeatWriterIgnoresTopLevelRole(t *testing.T) {
	d, err := New("opencode")
	if err != nil {
		t.Fatalf("New(opencode): %v", err)
	}

	var raw, out bytes.Buffer
	w := d.NewHeartbeatWriter(&raw, "77", &out, driverkit.RenderOptions{TopLevelRole: "reviewer"})

	ndjson := `{"type":"text","part":{"text":"doing the thing"}}` + "\n"
	if _, err := w.Write([]byte(ndjson)); err != nil {
		t.Fatalf("Write: %v", err)
	}

	if raw.String() != ndjson {
		t.Errorf("raw not byte-exact:\ngot:  %q\nwant: %q", raw.String(), ndjson)
	}
	if !strings.Contains(out.String(), "#77") {
		t.Errorf("heartbeat output missing issue prefix: %q", out.String())
	}
}

func TestOpencodeDriverHeartbeatWriterForwardsOnModel(t *testing.T) {
	d, err := New("opencode")
	if err != nil {
		t.Fatalf("New(opencode): %v", err)
	}

	type call struct{ model, role string }
	var got []call
	opts := driverkit.RenderOptions{OnModel: func(model, role string) {
		got = append(got, call{model, role})
	}}
	var raw, out bytes.Buffer
	w := d.NewHeartbeatWriter(&raw, "77", &out, opts)

	ndjson := `{"type":"step_finish","part":{"modelID":"m1"}}` + "\n"
	if _, err := w.Write([]byte(ndjson)); err != nil {
		t.Fatalf("Write: %v", err)
	}

	if want := []call{{"m1", ""}}; !slices.Equal(got, want) {
		t.Errorf("OnModel calls = %v, want %v", got, want)
	}
}

// A non-zero exitCode is never resolved to 0, even when the log carries a
// valid outcome line (crash after the agent wrote its outcome).
func TestOpencodeDriverResolveExitNonZeroRC_KeptDespiteOutcome(t *testing.T) {
	d, err := New("opencode")
	if err != nil {
		t.Fatalf("New(opencode): %v", err)
	}

	dir := t.TempDir()
	logPath := filepath.Join(dir, "issue-1.log")
	line := `{"type":"text","part":{"text":"SPINDRIFT_OUTCOME issue=42 landing=https://example/pr/1 status=ready note=done"}}`
	if err := os.WriteFile(logPath, []byte(line+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := d.ResolveExit(logPath, 17)
	if err != nil {
		t.Fatalf("ResolveExit: %v", err)
	}
	if got != 17 {
		t.Errorf("ResolveExit: got %d, want 17", got)
	}
}

// opencode exits 0 even on a mid-run error, so the passed-in exitCode is 0
// here and the log's error event turns it into a failure (issue #2263).
func TestOpencodeDriverResolveExitErrorEvent_IsNonZero(t *testing.T) {
	d, err := New("opencode")
	if err != nil {
		t.Fatalf("New(opencode): %v", err)
	}

	dir := t.TempDir()
	logPath := filepath.Join(dir, "issue-1.log")
	lines := []string{
		`{"type":"text","part":{"text":"SPINDRIFT_OUTCOME issue=42 landing=https://example/pr/1 status=ready note=done"}}`,
		`{"type":"error","error":"boom"}`,
	}
	if err := os.WriteFile(logPath, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := d.ResolveExit(logPath, 0)
	if err != nil {
		t.Fatalf("ResolveExit: %v", err)
	}
	if got == 0 {
		t.Errorf("ResolveExit: got 0, want non-zero")
	}
}

func TestOpencodeDriverResolveExitMissingLog_PassesRCThrough(t *testing.T) {
	d, err := New("opencode")
	if err != nil {
		t.Fatalf("New(opencode): %v", err)
	}

	logPath := filepath.Join(t.TempDir(), "does-not-exist.log")
	got, err := d.ResolveExit(logPath, 0)
	if err != nil {
		t.Fatalf("ResolveExit: %v", err)
	}
	if got != 0 {
		t.Errorf("ResolveExit: got %d, want 0", got)
	}
}

// The error event between the two text events pins that RenderTranscript
// joins only text bodies.
func TestOpencodeDriverRenderTranscript(t *testing.T) {
	d, err := New("opencode")
	if err != nil {
		t.Fatalf("New(opencode): %v", err)
	}

	dir := t.TempDir()
	logPath := filepath.Join(dir, "issue-1.log")
	lines := []string{
		`{"type":"text","part":{"text":"Investigating the issue."}}`,
		`{"type":"error","error":"rate_limit_error"}`,
		`{"type":"text","part":{"text":"Filed a fix."}}`,
	}
	if err := os.WriteFile(logPath, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := d.RenderTranscript(logPath, driverkit.RenderOptions{})
	if err != nil {
		t.Fatalf("RenderTranscript: %v", err)
	}
	want := "[implementor] Investigating the issue.\n[implementor] Filed a fix.\n"
	if got != want {
		t.Errorf("RenderTranscript = %q, want %q", got, want)
	}
}

// openOpencodeStore creates a session store in a fresh temp dir, points
// OPENCODE_DB at it for the test, and returns it open.
func openOpencodeStore(t *testing.T, create string) *sql.DB {
	t.Helper()
	path := filepath.Join(t.TempDir(), "opencode.db")
	t.Setenv("OPENCODE_DB", path)
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if _, err := db.Exec(create); err != nil {
		t.Fatal(err)
	}
	return db
}

// seedOpencodeStore creates the session store from the committed opencode
// schema fixture and inserts rows. Seeding from the real DDL, not a
// hand-written stub, is what makes a column lookupSession queries going
// missing from opencode's schema fail a test.
func seedOpencodeStore(t *testing.T, rows [][5]any) *sql.DB {
	t.Helper()
	ddl, err := os.ReadFile(filepath.Join("opencode", "testdata", "session-schema.sql"))
	if err != nil {
		t.Fatal(err)
	}
	db := openOpencodeStore(t, string(ddl))
	// The NOT NULL columns lookupSession never reads still need values.
	if _, err := db.Exec(`INSERT INTO project (id, worktree, time_created, time_updated, sandboxes) VALUES ('prj', '/w', 0, 0, '[]')`); err != nil {
		t.Fatal(err)
	}
	for _, r := range rows {
		if _, err := db.Exec(`INSERT INTO session (id, project_id, slug, directory, version, time_updated, parent_id, title, time_created, time_archived) VALUES (?, 'prj', 's', '/w', 'v', 0, ?, ?, ?, ?)`, r[0], r[1], r[2], r[3], r[4]); err != nil {
			t.Fatal(err)
		}
	}
	return db
}

// The columns lookupSession queries (opencode/session.go) must exist in the
// fixture schema.
func TestOpencodeSchemaFixtureHasLookupColumns(t *testing.T) {
	db := seedOpencodeStore(t, nil)
	rows, err := db.Query(`SELECT name FROM pragma_table_info('session')`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	have := map[string]bool{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatal(err)
		}
		have[name] = true
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	for _, col := range []string{"id", "title", "parent_id", "time_archived", "time_created"} {
		if !have[col] {
			t.Errorf("session table in opencode/testdata/session-schema.sql has no %q column, which lookupSession queries", col)
		}
	}
}

func TestOpencodeSessionFlags(t *testing.T) {
	d, err := New("opencode")
	if err != nil {
		t.Fatalf("New(opencode): %v", err)
	}
	title := func(t *testing.T, slug, issue string) string {
		t.Helper()
		got := d.SessionFlags("initial", slug, issue, t.TempDir())
		v, ok := strings.CutPrefix(got, "--title ")
		if !ok || v == "" || strings.ContainsAny(v, " \t'\"") {
			t.Fatalf("initial = %q; want --title <single token>", got)
		}
		return v
	}

	t.Run("initial pins a stable per-issue title", func(t *testing.T) {
		a := title(t, "owner/repo", "7")
		if a != title(t, "owner/repo", "7") {
			t.Error("title not stable for the same slug/issue")
		}
		if a == title(t, "owner/repo", "8") || a == title(t, "other/repo", "7") {
			t.Error("title does not differ across issues/repos")
		}
	})

	t.Run("resume with no store is cold", func(t *testing.T) {
		t.Setenv("OPENCODE_DB", filepath.Join(t.TempDir(), "opencode.db"))
		if got := d.SessionFlags("resume", "owner/repo", "7", t.TempDir()); got != "" {
			t.Errorf("got %q; want empty", got)
		}
	})

	t.Run("resume picks the newest matching root session", func(t *testing.T) {
		want := title(t, "owner/repo", "7")
		seedOpencodeStore(t, [][5]any{
			{"ses_old", nil, want, 100, nil},
			{"ses_new", nil, want, 300, nil},
			{"ses_child", "ses_old", want, 400, nil},
			{"ses_archived", nil, want, 500, 600},
			{"ses_other", nil, title(t, "owner/repo", "8"), 700, nil},
		})
		if got := d.SessionFlags("resume", "owner/repo", "7", t.TempDir()); got != "--session ses_new" {
			t.Errorf("got %q; want --session ses_new", got)
		}
	})

	t.Run("resume with a store but no match is cold", func(t *testing.T) {
		seedOpencodeStore(t, [][5]any{
			{"ses_other", nil, title(t, "owner/repo", "8"), 100, nil},
			{"ses_child", "ses_other", title(t, "owner/repo", "7"), 200, nil},
		})
		if got := d.SessionFlags("resume", "owner/repo", "7", t.TempDir()); got != "" {
			t.Errorf("got %q; want empty", got)
		}
	})

	t.Run("resume with a malformed newest id is cold", func(t *testing.T) {
		seedOpencodeStore(t, [][5]any{
			{"ses_old", nil, title(t, "owner/repo", "7"), 100, nil},
			{"ses_a --share", nil, title(t, "owner/repo", "7"), 200, nil},
		})
		var got string
		stderr := testutil.CaptureStderr(t, func() {
			got = d.SessionFlags("resume", "owner/repo", "7", t.TempDir())
		})
		if got != "" {
			t.Errorf("got %q; want empty", got)
		}
		if !strings.Contains(stderr, "malformed session id") || strings.Contains(stderr, "--share") {
			t.Errorf("stderr = %q; want a malformed-id WARNING without the id", stderr)
		}
	})

	t.Run("resume warns on a query error but stays cold", func(t *testing.T) {
		openOpencodeStore(t, `CREATE TABLE session (id text PRIMARY KEY)`)
		var got string
		stderr := testutil.CaptureStderr(t, func() {
			got = d.SessionFlags("resume", "owner/repo", "7", t.TempDir())
		})
		if got != "" {
			t.Errorf("got %q; want empty", got)
		}
		if !strings.Contains(stderr, "WARNING") {
			t.Errorf("stderr = %q; want a WARNING", stderr)
		}
	})

	t.Run("resume with no match stays silent", func(t *testing.T) {
		seedOpencodeStore(t, nil)
		stderr := testutil.CaptureStderr(t, func() {
			d.SessionFlags("resume", "owner/repo", "7", t.TempDir())
		})
		if stderr != "" {
			t.Errorf("stderr = %q; want empty", stderr)
		}
	})

	t.Run("resume does not create the store", func(t *testing.T) {
		dir := filepath.Join(t.TempDir(), "session")
		t.Setenv("OPENCODE_DB", filepath.Join(dir, "opencode.db"))
		d.SessionFlags("resume", "owner/repo", "7", t.TempDir())
		if _, err := os.Stat(dir); !os.IsNotExist(err) {
			t.Errorf("resume created state at OPENCODE_DB: %v", err)
		}
	})

	t.Run("resume reads OPENCODE_DB, not a path under home", func(t *testing.T) {
		seedOpencodeStore(t, [][5]any{{"ses_env", nil, title(t, "owner/repo", "7"), 100, nil}})
		if got := d.SessionFlags("resume", "owner/repo", "7", t.TempDir()); got != "--session ses_env" {
			t.Errorf("got %q; want --session ses_env", got)
		}
	})

	t.Run("resume with OPENCODE_DB unset or empty is cold", func(t *testing.T) {
		seedOpencodeStore(t, [][5]any{{"ses_a", nil, title(t, "owner/repo", "7"), 100, nil}})
		t.Setenv("OPENCODE_DB", "")
		if got := d.SessionFlags("resume", "owner/repo", "7", t.TempDir()); got != "" {
			t.Errorf("empty: got %q; want empty", got)
		}
		os.Unsetenv("OPENCODE_DB")
		if got := d.SessionFlags("resume", "owner/repo", "7", t.TempDir()); got != "" {
			t.Errorf("unset: got %q; want empty", got)
		}
	})

	t.Run("unknown modes render nothing", func(t *testing.T) {
		seedOpencodeStore(t, [][5]any{{"ses_a", nil, title(t, "owner/repo", "7"), 100, nil}})
		for _, mode := range []string{"", "continue", "bogus"} {
			if got := d.SessionFlags(mode, "owner/repo", "7", t.TempDir()); got != "" {
				t.Errorf("SessionFlags(%q) = %q; want empty", mode, got)
			}
		}
	})
}

// The registered opencode Driver carries the error event's reset time to the
// retry hold, the same way claude's resetsAt does.
func TestOpencodeDriverClassifyTransientCarriesResetAt(t *testing.T) {
	d, err := New("opencode")
	if err != nil {
		t.Fatalf("New(opencode): %v", err)
	}
	logPath := filepath.Join(t.TempDir(), "box.log")
	line := `{"type":"error","timestamp":1760610000000,"error":{"name":"APIError","data":{"statusCode":429,"responseHeaders":{"retry-after":"3600"}}}}` + "\n"
	if err := os.WriteFile(logPath, []byte(line), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := d.ClassifyTransient(logPath)
	if err != nil {
		t.Fatalf("ClassifyTransient: %v", err)
	}
	if got.Class != Transient || got.Reason != RateLimit {
		t.Fatalf("Class/Reason: got %s/%s, want %s/%s", got.Class, got.Reason, Transient, RateLimit)
	}
	want := time.UnixMilli(1760610000000).Add(time.Hour).UTC()
	if got.ResetAt == nil || !got.ResetAt.Equal(want) {
		t.Errorf("ResetAt: got %v, want %v", got.ResetAt, want)
	}
}
