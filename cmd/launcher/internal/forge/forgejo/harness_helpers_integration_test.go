//go:build integration

package forgejo_test

import (
	"slices"
	"testing"
)

// TestForgejoHarnessLabels asserts the harness seeds both label families the
// dispatch lifecycle needs: the four triage/dispatch labels (mirrored from
// testLabels in contract_test.go) and the three research verdict labels
// (forge.ResearchVerdictLabels). Every color must be a bare 6-hex-digit
// string with no leading "#" — the adapter's CreateLabel prepends the "#"
// itself (forgejo.go:446), so a helper-supplied "#" would double up.
func TestForgejoHarnessLabels(t *testing.T) {
	labels := forgejoHarnessLabels()

	want := []string{
		testLabels.Dispatchable,
		testLabels.InProgress,
		testLabels.Complete,
		testLabels.Failed,
		"agent-research-recommend",
		"agent-research-reject",
		"agent-research-unclear",
	}

	got := make(map[string]string, len(labels))
	for _, l := range labels {
		got[l.Name] = l.Color
	}

	for _, name := range want {
		color, ok := got[name]
		if !ok {
			t.Errorf("forgejoHarnessLabels: missing label %q", name)
			continue
		}
		if len(color) != 6 {
			t.Errorf("forgejoHarnessLabels: label %q color %q is not 6 hex digits", name, color)
			continue
		}
		if color[0] == '#' {
			t.Errorf("forgejoHarnessLabels: label %q color %q must not have a leading #", name, color)
		}
		for _, c := range color {
			if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')) {
				t.Errorf("forgejoHarnessLabels: label %q color %q is not hex", name, color)
				break
			}
		}
	}

	if len(labels) != len(want) {
		t.Errorf("forgejoHarnessLabels: got %d labels, want %d", len(labels), len(want))
	}
}

// TestForgejoContainerRunArgsFixedPort asserts the fixed-port shape: the
// host port is published explicitly, and the throwaway-container flags
// (--rm, INSTALL_LOCK) are present.
func TestForgejoContainerRunArgsFixedPort(t *testing.T) {
	args := forgejoContainerRunArgs("podman", "spindrift-forgejo-harness", "codeberg.org/forgejo/forgejo:1.21", 49213)

	if !slices.Contains(args, "--rm") {
		t.Errorf("forgejoContainerRunArgs: missing --rm in %q", args)
	}
	if !slices.Contains(args, "-e") || !slices.Contains(args, "FORGEJO__security__INSTALL_LOCK=true") {
		t.Errorf("forgejoContainerRunArgs: missing INSTALL_LOCK=true in %q", args)
	}
	if !slices.Contains(args, "127.0.0.1:49213:3000") {
		t.Errorf("forgejoContainerRunArgs: expected fixed publish flag 127.0.0.1:49213:3000, got %q", args)
	}
	if !slices.Contains(args, "spindrift-forgejo-harness") {
		t.Errorf("forgejoContainerRunArgs: missing container name in %q", args)
	}
	if !slices.Contains(args, "codeberg.org/forgejo/forgejo:1.21") {
		t.Errorf("forgejoContainerRunArgs: missing image in %q", args)
	}
}

// TestForgejoContainerRunArgsEphemeralPort asserts hostPort==0 publishes
// without a host port, letting the runtime pick one.
func TestForgejoContainerRunArgsEphemeralPort(t *testing.T) {
	args := forgejoContainerRunArgs("docker", "spindrift-forgejo-harness", "codeberg.org/forgejo/forgejo:1.21", 0)

	if !slices.Contains(args, "127.0.0.1::3000") {
		t.Errorf("forgejoContainerRunArgs: expected ephemeral publish flag 127.0.0.1::3000, got %q", args)
	}
	if !slices.Contains(args, "--rm") {
		t.Errorf("forgejoContainerRunArgs: missing --rm in %q", args)
	}
	if !slices.Contains(args, "FORGEJO__security__INSTALL_LOCK=true") {
		t.Errorf("forgejoContainerRunArgs: missing INSTALL_LOCK=true in %q", args)
	}
}

// TestParseForgejoHostPortMultiline exercises a realistic `docker port
// <name> 3000` / `podman port <name> 3000` output: an ipv4 line, and an ipv6
// line that must not shadow the first parsed port.
func TestParseForgejoHostPortMultiline(t *testing.T) {
	out := "0.0.0.0:49153\n[::]:49153\n"
	port, err := parseForgejoHostPort(out)
	if err != nil {
		t.Fatalf("parseForgejoHostPort: unexpected error: %v", err)
	}
	if port != 49153 {
		t.Errorf("parseForgejoHostPort: got %d, want 49153", port)
	}
}

// TestParseForgejoHostPortSingleLine exercises a single 127.0.0.1 line.
func TestParseForgejoHostPortSingleLine(t *testing.T) {
	port, err := parseForgejoHostPort("127.0.0.1:41000\n")
	if err != nil {
		t.Fatalf("parseForgejoHostPort: unexpected error: %v", err)
	}
	if port != 41000 {
		t.Errorf("parseForgejoHostPort: got %d, want 41000", port)
	}
}

// TestParseForgejoHostPortEmpty asserts empty/unparseable input errors
// instead of silently returning a zero port.
func TestParseForgejoHostPortEmpty(t *testing.T) {
	if _, err := parseForgejoHostPort(""); err == nil {
		t.Error("parseForgejoHostPort: expected error for empty input, got nil")
	}
	if _, err := parseForgejoHostPort("   \n  \n"); err == nil {
		t.Error("parseForgejoHostPort: expected error for whitespace-only input, got nil")
	}
}

// TestForgejoAdminCreateArgs asserts the admin-bootstrap argv creates an
// admin user with a password that doesn't need changing on first login (the
// harness has no interactive terminal to satisfy a forced change).
func TestForgejoAdminCreateArgs(t *testing.T) {
	args := forgejoAdminCreateArgs("spindrift-admin", "s3cr3t-pass", "admin@example.invalid")

	if !slices.Contains(args, "--admin") {
		t.Errorf("forgejoAdminCreateArgs: missing --admin in %q", args)
	}
	if !slices.Contains(args, "--must-change-password=false") {
		t.Errorf("forgejoAdminCreateArgs: missing --must-change-password=false in %q", args)
	}
	if !slices.Contains(args, "spindrift-admin") {
		t.Errorf("forgejoAdminCreateArgs: missing username in %q", args)
	}
	if !slices.Contains(args, "s3cr3t-pass") {
		t.Errorf("forgejoAdminCreateArgs: missing password in %q", args)
	}
	if !slices.Contains(args, "admin@example.invalid") {
		t.Errorf("forgejoAdminCreateArgs: missing email in %q", args)
	}
}

// TestForgejoTokenGenArgs asserts the token-minting argv requests a raw,
// all-scopes token — raw so the harness can read the token straight off
// stdout without parsing table output.
func TestForgejoTokenGenArgs(t *testing.T) {
	args := forgejoTokenGenArgs("spindrift-admin")

	if !slices.Contains(args, "--raw") {
		t.Errorf("forgejoTokenGenArgs: missing --raw in %q", args)
	}
	if !slices.Contains(args, "--scopes") || !slices.Contains(args, "all") {
		t.Errorf("forgejoTokenGenArgs: missing --scopes all in %q", args)
	}
	if !slices.Contains(args, "spindrift-admin") {
		t.Errorf("forgejoTokenGenArgs: missing username in %q", args)
	}
}

// TestForgejoVersionURL asserts the version-endpoint URL is built correctly
// both with and without a trailing slash on baseURL.
func TestForgejoVersionURL(t *testing.T) {
	cases := []struct {
		baseURL string
		want    string
	}{
		{"http://127.0.0.1:49213", "http://127.0.0.1:49213/api/v1/version"},
		{"http://127.0.0.1:49213/", "http://127.0.0.1:49213/api/v1/version"},
	}
	for _, c := range cases {
		if got := forgejoVersionURL(c.baseURL); got != c.want {
			t.Errorf("forgejoVersionURL(%q) = %q, want %q", c.baseURL, got, c.want)
		}
	}
}
