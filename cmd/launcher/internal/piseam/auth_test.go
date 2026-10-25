//go:build integration

package piseam

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/homelayout"
)

// envFilesEnv carries the pi Driver's envFiles in the DRIVER_ENV_FILES wire
// format, rendered from lib/drivers/pi.nix so the seam cannot drift from the
// row the Box reads.
const envFilesEnv = "SPINDRIFT_PI_ENV_FILES"

// A bump must re-run this seam's checks against ADR 0009's pi amendment and
// update it.
func TestPiVersion(t *testing.T) {
	const want = "1.1.0"
	out, err := exec.Command(piPath(t), "--version").Output()
	if err != nil {
		t.Fatalf("pi --version: %v", err)
	}
	if got := strings.TrimSpace(string(out)); got != want {
		t.Errorf("pi --version = %q, want %q", got, want)
	}
}

func authJSON(key string) map[string]any {
	return map[string]any{stubProvider: map[string]any{"type": "api_key", "key": key}}
}

func authHeader(t *testing.T, s *stub) string {
	t.Helper()
	hs := s.Headers()
	if len(hs) != 1 {
		t.Fatalf("stub got %d requests, want 1", len(hs))
	}
	return hs[0].Get("Authorization")
}

func TestPiAuthJSON(t *testing.T) {
	const (
		key   = "auth-json-key"
		decoy = "home-decoy-key"
	)

	// A custom Provider in models.json with no apiKey takes its key from the
	// agent dir's auth.json: the shape PI_AUTH_CONTENT would have to write.
	t.Run("agent dir auth.json supplies a custom Provider's key", func(t *testing.T) {
		s := newStub(t, textReply("ok"))
		dirs := newPiDirs(t)
		writeJSON(t, filepath.Join(dirs.agentDir, "auth.json"), authJSON(key))
		r := runPiWith(t, s, piOpts{noAPIKey: true, dirs: &dirs}, "hi")
		if r.err != nil || r.stderr != "" {
			t.Fatalf("pi exit = %v, stderr = %q, want a clean run", r.err, r.stderr)
		}
		if got := authHeader(t, s); got != "Bearer "+key {
			t.Errorf("Authorization = %q, want %q", got, "Bearer "+key)
		}
	})

	// With PI_CODING_AGENT_DIR set, $HOME/.pi/agent is not consulted.
	t.Run("PI_CODING_AGENT_DIR wins over HOME/.pi/agent", func(t *testing.T) {
		s := newStub(t, textReply("ok"))
		dirs := newPiDirs(t)
		homeAgent := filepath.Join(dirs.home, ".pi", "agent")
		if err := os.MkdirAll(homeAgent, 0o700); err != nil {
			t.Fatal(err)
		}
		writeJSON(t, filepath.Join(homeAgent, "auth.json"), authJSON(decoy))
		writeJSON(t, filepath.Join(dirs.agentDir, "auth.json"), authJSON(key))
		r := runPiWith(t, s, piOpts{noAPIKey: true, dirs: &dirs}, "hi")
		if r.err != nil {
			t.Fatalf("pi exit = %v, stderr = %q", r.err, r.stderr)
		}
		if got := authHeader(t, s); got != "Bearer "+key {
			t.Errorf("Authorization = %q, want %q (the decoy under HOME must not be used)", got, "Bearer "+key)
		}
	})

	// Control for the test above: with no key in the agent dir pi refuses to
	// call the Provider, so the decoy going unused is not a missed request.
	t.Run("HOME/.pi/agent auth.json alone is not read", func(t *testing.T) {
		s := newStub(t)
		dirs := newPiDirs(t)
		homeAgent := filepath.Join(dirs.home, ".pi", "agent")
		if err := os.MkdirAll(homeAgent, 0o700); err != nil {
			t.Fatal(err)
		}
		writeJSON(t, filepath.Join(homeAgent, "auth.json"), authJSON(decoy))
		r := runPiWith(t, s, piOpts{noAPIKey: true, dirs: &dirs}, "hi")
		if r.err == nil || !strings.Contains(r.stderr, "No API key found for "+stubProvider) {
			t.Errorf("pi exit = %v, stderr = %q, want a failure naming the missing key", r.err, r.stderr)
		}
		if n := len(s.Requests()); n != 0 {
			t.Errorf("stub got %d requests, want 0", n)
		}
	})

	// pi neither warns about nor tightens a group/world-readable auth.json.
	t.Run("auth.json mode is not enforced", func(t *testing.T) {
		s := newStub(t, textReply("ok"))
		dirs := newPiDirs(t)
		p := filepath.Join(dirs.agentDir, "auth.json")
		writeJSON(t, p, authJSON(key))
		if err := os.Chmod(p, 0o666); err != nil {
			t.Fatal(err)
		}
		r := runPiWith(t, s, piOpts{noAPIKey: true, dirs: &dirs}, "hi")
		if r.err != nil || r.stderr != "" {
			t.Fatalf("pi exit = %v, stderr = %q, want a clean run", r.err, r.stderr)
		}
		if got := authHeader(t, s); got != "Bearer "+key {
			t.Errorf("Authorization = %q, want %q", got, "Bearer "+key)
		}
		if st, err := os.Stat(p); err != nil || st.Mode().Perm() != 0o666 {
			t.Errorf("auth.json after run: %v, %v; want mode 0666 untouched", st, err)
		}
	})
}

// writePiEnvFiles runs the Box's WriteEnvFiles over the pi Driver's envFiles
// with env as the environment, skipping the test when the spec is not rendered.
func writePiEnvFiles(t *testing.T, home string, env map[string]string) {
	t.Helper()
	spec := os.Getenv(envFilesEnv)
	if spec == "" {
		t.Skipf("%s unset: nix/checks/go.nix renders it from the pi Driver's envFiles", envFilesEnv)
	}
	files, err := homelayout.ParseEnvFiles(spec)
	if err != nil {
		t.Fatalf("parse %s=%q: %v", envFilesEnv, spec, err)
	}
	if err := homelayout.WriteEnvFiles(home, files, func(name string) string { return env[name] }); err != nil {
		t.Fatalf("WriteEnvFiles: %v", err)
	}
}

// assertPrivateFile fails the test unless path holds exactly want at mode 0600.
func assertPrivateFile(t *testing.T, path, want string) {
	t.Helper()
	name := filepath.Base(path)
	if st, err := os.Stat(path); err != nil || st.Mode().Perm() != 0o600 {
		t.Errorf("%s: %v, %v; want mode 0600", name, st, err)
	}
	if got, err := os.ReadFile(path); err != nil || string(got) != want {
		t.Errorf("%s = %q, %v; want %q", name, got, err, want)
	}
}

// The path the Box takes for PI_AUTH_CONTENT: the Driver's envFiles write it
// into $HOME/.pi/agent/auth.json, which pi reads with PI_CODING_AGENT_DIR unset.
func TestPiAuthContentReachesPi(t *testing.T) {
	const key = "auth-content-key"
	b, err := json.Marshal(authJSON(key))
	if err != nil {
		t.Fatal(err)
	}
	content := string(b)

	dirs := newPiDirs(t)
	dirs.agentDir = filepath.Join(dirs.home, ".pi", "agent")
	writePiEnvFiles(t, dirs.home, map[string]string{"PI_AUTH_CONTENT": content})

	s := newStub(t, textReply("ok"))
	r := runPiWith(t, s, piOpts{
		args:            sessionArgs("initial"),
		noAPIKey:        true,
		defaultAgentDir: true,
		dirs:            &dirs,
	}, "hi")
	if r.err != nil {
		t.Fatalf("pi exit = %v, stderr = %q", r.err, r.stderr)
	}

	if got := authHeader(t, s); got != "Bearer "+key {
		t.Errorf("Authorization = %q, want %q", got, "Bearer "+key)
	}
	assertPrivateFile(t, filepath.Join(dirs.agentDir, "auth.json"), content)
	for _, a := range r.argv {
		if strings.Contains(a, key) {
			t.Errorf("pi argv holds the key: %q", a)
		}
	}
}

// The path the Box takes for PI_MODELS_CONTENT: the Driver's envFiles write it
// into $HOME/.pi/agent/models.json, which pi reads with PI_CODING_AGENT_DIR
// unset. The harness writes no models.json of its own, so the Provider can only
// come from the written file.
func TestPiModelsContentReachesPi(t *testing.T) {
	const (
		answer = "reply via written models.json"
		key    = "models-content-key"
	)
	s := newStub(t, textReply(answer))
	b, err := json.Marshal(stubModelsJSON(s.URL, key, false))
	if err != nil {
		t.Fatal(err)
	}
	content := string(b)

	dirs := newPiDirs(t)
	dirs.agentDir = filepath.Join(dirs.home, ".pi", "agent")
	writePiEnvFiles(t, dirs.home, map[string]string{"PI_MODELS_CONTENT": content})

	r := runPiWith(t, s, piOpts{noModels: true, defaultAgentDir: true, dirs: &dirs}, "hi")
	if r.err != nil {
		t.Fatalf("pi exit = %v, stderr = %q", r.err, r.stderr)
	}
	if got := finalAssistantText(r.events); got != answer {
		t.Errorf("final assistant text = %q, want %q\nstdout:\n%s", got, answer, r.stdout)
	}
	if got := authHeader(t, s); got != "Bearer "+key {
		t.Errorf("Authorization = %q, want %q", got, "Bearer "+key)
	}
	assertPrivateFile(t, filepath.Join(dirs.agentDir, "models.json"), content)
}
