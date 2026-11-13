package main

import (
	"context"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/backend"
	"spindrift.dev/launcher/internal/backend/backendrows"
	"spindrift.dev/launcher/internal/connectorforge"
	"spindrift.dev/launcher/internal/connectorhost"
	"spindrift.dev/launcher/internal/forge"
)

const shadowRowDoc = `[{"name":"github","module":"/nonexistent.wasm","credentialEnv":"SHADOW_TEST_TOKEN",
"routes":[{"method":"GET","host":"api.github.com","path":"/repos/{repo}/issues"}]
%s}]`

func useShadowRow(t *testing.T, shadow string) {
	t.Helper()
	t.Cleanup(backendrows.Snapshot())
	doc := strings.Replace(shadowRowDoc, "%s", shadow, 1)
	if err := backendrows.Use([]byte(doc), func(string) string { return "" }); err != nil {
		t.Fatal(err)
	}
}

func githubRow(t *testing.T) backendrows.Row {
	t.Helper()
	row, ok := backendrows.Lookup("github")
	if !ok {
		t.Fatal("no github backend row")
	}
	return row
}

// trackerOnlyWasm is a Connector module serving a tracker-only handshake, so
// Load succeeds and Close has a real runtime to release. It is the output of
// connectorhost's guest_test.go answeringGuest([]string{"tracker"}, "{}").
const trackerOnlyWasm = "0061736d010000000113046000006000017e60017f017f60027f7f017e030403010203050301000107430413636f6e6e6563746f725f68616e647368616b6500000f636f6e6e6563746f725f616c6c6f6300010e636f6e6e6563746f725f63616c6c0002066d656d6f727902000a1803040042360b06004180c0000b0a0042828080808080040b0b44020041000b367b226361706162696c6974696573223a5b22747261636b6572225d2c2270726f746f636f6c56657273696f6e223a22302e332e30227d004180200b027b7d"

// useTrackerOnlyConnector makes rs load the tracker-only module and returns a
// pointer to the Connector it loaded.
func useTrackerOnlyConnector(t *testing.T, rs *readShadows) **connectorhost.Connector {
	t.Helper()
	wasm, err := hex.DecodeString(trackerOnlyWasm)
	if err != nil {
		t.Fatal(err)
	}
	var conn *connectorhost.Connector
	rs.load = func(ctx context.Context, _ backendrows.Row, _ backendrows.Options) (*connectorhost.Connector, error) {
		var err error
		if conn, err = connectorhost.Load(ctx, wasm); err == nil {
			t.Cleanup(func() { _ = conn.Close(context.Background()) })
		}
		return conn, err
	}
	return &conn
}

func shadowLogs(t *testing.T, pwd, suffix string) []string {
	t.Helper()
	m, err := filepath.Glob(filepath.Join(pwd, ".spindrift", "logs", "shadow-github-*"+suffix))
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func TestReadShadowsLeaveNativeUntouchedWithoutAShadowRow(t *testing.T) {
	c := minimalValidConfig()
	for name, setup := range map[string]func(){
		"no row":          func() { t.Cleanup(backendrows.Snapshot()); _ = backendrows.Use(nil, func(string) string { return "" }) },
		"shadow disabled": func() { useShadowRow(t, "") },
	} {
		t.Run(name, func(t *testing.T) {
			setup()
			pwd := t.TempDir()
			rs := newReadShadows(pwd, io.Discard)
			it, cf := forge.IssueTracker(forge.NewFake()), forge.CodeForge(forge.NewFake())
			if got := rs.wrapTracker(c, it); got != it {
				t.Errorf("tracker = %T, want the native tracker", got)
			}
			if got := rs.wrapCodeForge(c, cf); got != cf {
				t.Errorf("code forge = %T, want the native code forge", got)
			}
			rs.close()
			if m := shadowLogs(t, pwd, ""); len(m) != 0 {
				t.Errorf("shadow files %v written with no shadow", m)
			}
		})
	}
}

func TestReadShadowsLayerOverNativeAndShareOnePerBackend(t *testing.T) {
	useShadowRow(t, `,"shadow":true,"shadowEvery":3`)
	c := minimalValidConfig()
	pwd := t.TempDir()
	rs := newReadShadows(pwd, io.Discard)
	nativeIT, nativeCF := forge.NewFake(), forge.NewFake()

	it := rs.wrapTracker(c, nativeIT)
	cf := rs.wrapCodeForge(c, nativeCF)

	if it == forge.IssueTracker(nativeIT) {
		t.Fatal("tracker is the native tracker, want a shadow layer")
	}
	if u, ok := it.(forge.Unwrapper); !ok || u.Unwrap() != forge.IssueTracker(nativeIT) {
		t.Errorf("tracker layer does not unwrap to the native tracker")
	}
	if _, ok := forge.As[forge.IssueCloser](it); !ok {
		t.Error("forge.As lost the native tracker's IssueCloser through the layer")
	}
	nativeCaps := forge.ResolveCapabilities(nativeCF, nativeIT, backend.Descriptor{}, backend.Descriptor{})
	caps := forge.ResolveCapabilities(cf, it, backend.Descriptor{}, backend.Descriptor{})
	if caps.PRForge == nil || caps.PRForge == nativeCaps.PRForge {
		t.Errorf("ResolveCapabilities PRForge = %T, want the shadow layer", caps.PRForge)
	}
	if (caps.BranchPusher == nil) != (nativeCaps.BranchPusher == nil) {
		t.Error("forge.AsCodeForge lost a native capability through the layer")
	}

	rs.close()
	logs := shadowLogs(t, pwd, ".log")
	if len(logs) != 1 {
		t.Fatalf("shadow logs = %v, want exactly one for the shared backend", logs)
	}
	b, err := os.ReadFile(logs[0])
	if err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(string(b), `"op":"shadow_summary"`); n != 1 {
		t.Errorf("shadow_summary lines = %d in %q, want 1", n, b)
	}
}

func TestReadShadowOpenFailsFastWithoutCredential(t *testing.T) {
	useShadowRow(t, `,"shadow":true,"shadowEvery":3`)
	t.Setenv("SHADOW_TEST_TOKEN", "")
	row := githubRow(t)
	rs := newReadShadows(t.TempDir(), io.Discard)
	_, err := rs.open(t.Context(), row, minimalValidConfig(), nil, nil, nil, "shadow-github-x.audit.jsonl")
	if err == nil || !strings.Contains(err.Error(), "SHADOW_TEST_TOKEN") {
		t.Fatalf("open error = %v, want one naming SHADOW_TEST_TOKEN", err)
	}
	if m := shadowLogs(t, rs.pwd, ".audit.jsonl"); len(m) != 0 {
		t.Errorf("audit file %v created before the credential check", m)
	}
}

func TestReadShadowOpenHandsLoadTheSubstitutedRowAndShadowOptions(t *testing.T) {
	useShadowRow(t, `,"shadow":true,"shadowEvery":3`)
	t.Setenv("SHADOW_TEST_TOKEN", "tok")
	row := githubRow(t)
	rs := newReadShadows(t.TempDir(), io.Discard)
	loadErr := errors.New("load refused")
	var gotRow backendrows.Row
	var gotOpts backendrows.Options
	rs.load = func(_ context.Context, r backendrows.Row, o backendrows.Options) (*connectorhost.Connector, error) {
		gotRow, gotOpts = r, o
		return nil, loadErr
	}
	_, err := rs.open(t.Context(), row, minimalValidConfig(), nil, nil, nil, "shadow-github-x.audit.jsonl")
	if !errors.Is(err, loadErr) {
		t.Fatalf("open error = %v, want the load error", err)
	}
	if got := gotRow.Routes[0].Path; got != "/repos/owner/repo/issues" {
		t.Errorf("route path = %q, want {repo} replaced by the repo slug", got)
	}
	if !gotOpts.Shadow {
		t.Error("Options.Shadow = false, want true")
	}
}

func TestReadShadowOpenClosesTheConnectorWhenASideCannotBeBuilt(t *testing.T) {
	useShadowRow(t, `,"shadow":true,"shadowEvery":3`)
	t.Setenv("SHADOW_TEST_TOKEN", "tok")
	rs := newReadShadows(t.TempDir(), io.Discard)
	conn := useTrackerOnlyConnector(t, rs)
	// The tracker-only Connector cannot serve the native forge's PR reads.
	reads, err := rs.open(t.Context(), githubRow(t), minimalValidConfig(), nil, forge.NewFake(), nil, "shadow-github-x.audit.jsonl")
	if !errors.Is(err, connectorforge.ErrNotForge) {
		t.Fatalf("open error = %v, want ErrNotForge", err)
	}
	if reads.Tracker != nil || reads.PR != nil || reads.Close != nil {
		t.Errorf("reads = %+v, want the zero value", reads)
	}
	if err := (*conn).Call(t.Context(), "m", nil, nil); !errors.Is(err, connectorhost.ErrUnusable) {
		t.Errorf("call after failed open = %v, want ErrUnusable: the Connector was left open", err)
	}
}

func TestReadShadowOpenWiresTheConnectorIntoReads(t *testing.T) {
	useShadowRow(t, `,"shadow":true,"shadowEvery":3`)
	t.Setenv("SHADOW_TEST_TOKEN", "tok")
	rs := newReadShadows(t.TempDir(), io.Discard)
	conn := useTrackerOnlyConnector(t, rs)
	reads, err := rs.open(t.Context(), githubRow(t), minimalValidConfig(), forge.NewFake(), nil, nil, "shadow-github-x.audit.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	if reads.Tracker == nil || reads.PR != nil {
		t.Fatalf("tracker=%v pr=%v, want only the tracker side", reads.Tracker, reads.PR)
	}
	if err := reads.Failed(); err != nil {
		t.Fatalf("Failed() = %v before any failure", err)
	}
	reads.Abort()
	if err := (*conn).Call(t.Context(), "m", nil, nil); err == nil {
		t.Fatal("call after Abort succeeded, want a failure")
	}
	if reads.Failed() == nil {
		t.Error("Failed() = nil after an aborted call, want the Connector's failure")
	}
	reads.Close()
	if err := (*conn).Call(t.Context(), "m", nil, nil); !errors.Is(err, connectorhost.ErrUnusable) {
		t.Errorf("call after Close = %v, want ErrUnusable", err)
	}
}

func TestReadShadowTargetsDeriveAPIAndWebHosts(t *testing.T) {
	useShadowRow(t, `,"shadow":true,"shadowEvery":3`)
	t.Setenv("GH_HOST", "")
	row := githubRow(t)
	api, web, _ := shadowTargets(row, minimalValidConfig())
	if api != "https://api.github.com" {
		t.Errorf("api = %q, want the Route table's host", api)
	}
	if web != "github.com" {
		t.Errorf("web host = %q, want the backend's web host", web)
	}
	t.Setenv("GH_HOST", "ghe.example.com")
	if _, web, _ = shadowTargets(row, minimalValidConfig()); web != "ghe.example.com" {
		t.Errorf("web host = %q, want GH_HOST's", web)
	}
}

func TestReadShadowSides(t *testing.T) {
	useShadowRow(t, `,"shadow":true,"shadowEvery":3`)
	t.Setenv("GH_HOST", "")
	row := githubRow(t)
	both := &connectorhost.Connector{Capabilities: []string{"tracker", "forge"}}
	trackerOnly := &connectorhost.Connector{Capabilities: []string{"tracker"}}
	c := minimalValidConfig()
	it, cf := forge.IssueTracker(forge.NewFake()), forge.CodeForge(forge.NewFake())

	t.Run("both sides", func(t *testing.T) {
		tr, pr, err := shadowSides(both, row, c, it, cf)
		if err != nil || tr == nil || pr == nil {
			t.Fatalf("tracker=%v pr=%v err=%v, want both sides and no error", tr, pr, err)
		}
	})
	t.Run("PR side cannot be built", func(t *testing.T) {
		if _, _, err := shadowSides(trackerOnly, row, c, it, cf); !errors.Is(err, connectorforge.ErrNotForge) {
			t.Fatalf("err = %v, want ErrNotForge", err)
		}
	})
	t.Run("no web host", func(t *testing.T) {
		noRepo := c
		noRepo.RepoSlug = ""
		if _, _, err := shadowSides(both, row, noRepo, it, cf); !errors.Is(err, connectorforge.ErrNoWebHost) {
			t.Fatalf("err = %v, want ErrNoWebHost", err)
		}
	})
	t.Run("native tracker absent", func(t *testing.T) {
		tr, pr, err := shadowSides(both, row, c, nil, cf)
		if err != nil || tr != nil || pr == nil {
			t.Fatalf("tracker=%v pr=%v err=%v, want only the PR side", tr, pr, err)
		}
	})
	t.Run("native code forge absent", func(t *testing.T) {
		tr, pr, err := shadowSides(both, row, c, it, nil)
		if err != nil || tr == nil || pr != nil {
			t.Fatalf("tracker=%v pr=%v err=%v, want only the tracker side", tr, pr, err)
		}
	})
}

func TestReadShadowTargetsFailWithAStatedCause(t *testing.T) {
	useShadowRow(t, `,"shadow":true,"shadowEvery":3`)
	row := githubRow(t)

	t.Run("no Route", func(t *testing.T) {
		row := row
		row.Routes = nil
		_, _, err := shadowTargets(row, minimalValidConfig())
		if err == nil || !strings.Contains(err.Error(), "no Route") {
			t.Fatalf("err = %v, want a no-Route cause", err)
		}
	})
	t.Run("web URL does not parse", func(t *testing.T) {
		t.Setenv("GH_HOST", "bad host")
		_, _, err := shadowTargets(row, minimalValidConfig())
		if err == nil || !strings.Contains(err.Error(), "does not parse") {
			t.Fatalf("err = %v, want a parse cause", err)
		}
		if _, _, err := shadowSides(&connectorhost.Connector{}, row, minimalValidConfig(), forge.NewFake(), nil); err == nil {
			t.Fatal("shadowSides succeeded, want the shadowTargets error")
		}
	})
}

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, errors.New("disk full") }

func TestReadShadowLogWriteFailureWarnsOnce(t *testing.T) {
	var warn strings.Builder
	w := &warnOnceWriter{w: failingWriter{}, name: "shadow-x.log", warn: &warn}
	for range 3 {
		if _, err := io.WriteString(w, "line\n"); err == nil {
			t.Fatal("write succeeded, want the underlying error")
		}
	}
	if n := strings.Count(warn.String(), "==> shadow:"); n != 1 {
		t.Errorf("warnings = %d in %q, want exactly 1", n, warn.String())
	}
	if !strings.Contains(warn.String(), "shadow-x.log") || !strings.Contains(warn.String(), "disk full") {
		t.Errorf("warning %q does not name the log and the error", warn.String())
	}
}
