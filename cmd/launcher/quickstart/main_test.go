package main

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestForceFlagUsage_NoBakPromise(t *testing.T) {
	if !strings.Contains(forceFlagUsage, "backing each up") {
		t.Errorf("forceFlagUsage = %q, want it to mention backing each up", forceFlagUsage)
	}
	if strings.Contains(forceFlagUsage, "*.bak") {
		t.Errorf("forceFlagUsage = %q, must not promise a fixed *.bak backup name", forceFlagUsage)
	}
}

func TestParseRemoteHostSlug(t *testing.T) {
	tests := []struct {
		name      string
		remoteURL string
		wantHost  string
		wantSlug  string
	}{
		{
			name:      "scp-like ssh",
			remoteURL: "git@codeberg.org:owner/repo.git",
			wantHost:  "codeberg.org",
			wantSlug:  "owner/repo",
		},
		{
			name:      "ssh:// scheme",
			remoteURL: "ssh://git@codeberg.org/owner/repo.git",
			wantHost:  "codeberg.org",
			wantSlug:  "owner/repo",
		},
		{
			name:      "https scheme",
			remoteURL: "https://codeberg.org/owner/repo.git",
			wantHost:  "codeberg.org",
			wantSlug:  "owner/repo",
		},
		{
			name:      "https scheme without .git suffix",
			remoteURL: "https://codeberg.org/owner/repo",
			wantHost:  "codeberg.org",
			wantSlug:  "owner/repo",
		},
		{
			name:      "https scheme with dotted host",
			remoteURL: "https://git.example.com/team/proj.git",
			wantHost:  "git.example.com",
			wantSlug:  "team/proj",
		},
		{
			name:      "scp-like ssh github",
			remoteURL: "git@github.com:jordansmall/spindrift.git",
			wantHost:  "github.com",
			wantSlug:  "jordansmall/spindrift",
		},
		{
			name:      "too many path segments",
			remoteURL: "https://codeberg.org/owner/repo/extra",
			wantHost:  "",
			wantSlug:  "",
		},
		{
			name:      "empty input",
			remoteURL: "",
			wantHost:  "",
			wantSlug:  "",
		},
		{
			name:      "unparseable input",
			remoteURL: "not a url",
			wantHost:  "",
			wantSlug:  "",
		},
		{
			name:      "https scheme with explicit port",
			remoteURL: "https://git.example.com:3000/owner/repo.git",
			wantHost:  "git.example.com",
			wantSlug:  "owner/repo",
		},
		{
			name:      "https scheme with explicit port, no .git suffix",
			remoteURL: "https://git.example.com:3000/owner/repo",
			wantHost:  "git.example.com",
			wantSlug:  "owner/repo",
		},
		{
			name:      "scp-like ssh github still parses",
			remoteURL: "git@github.com:owner/repo.git",
			wantHost:  "github.com",
			wantSlug:  "owner/repo",
		},
		{
			name:      "https scheme github still parses",
			remoteURL: "https://github.com/owner/repo.git",
			wantHost:  "github.com",
			wantSlug:  "owner/repo",
		},
		{
			name:      "ssh:// scheme codeberg still parses",
			remoteURL: "ssh://git@codeberg.org/owner/repo.git",
			wantHost:  "codeberg.org",
			wantSlug:  "owner/repo",
		},
		{
			name:      "ssh:// scheme with explicit port",
			remoteURL: "ssh://git@git.example.com:2222/owner/repo",
			wantHost:  "git.example.com",
			wantSlug:  "owner/repo",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotHost, gotSlug := parseRemoteHostSlug(tt.remoteURL)
			if gotHost != tt.wantHost || gotSlug != tt.wantSlug {
				t.Errorf("parseRemoteHostSlug(%q) = (%q, %q), want (%q, %q)",
					tt.remoteURL, gotHost, gotSlug, tt.wantHost, tt.wantSlug)
			}
		})
	}
}

// TestHostEnvironment_InsideGitWorkTree covers issue #2567: the finish-line
// git-add reminder relies on hostEnvironment.InsideGitWorkTree correctly
// distinguishing a real git work tree from a plain directory.
func TestHostEnvironment_InsideGitWorkTree(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}

	workTree := t.TempDir()
	if err := exec.Command("git", "init", workTree).Run(); err != nil {
		t.Fatalf("git init %s: %v", workTree, err)
	}
	if got := (hostEnvironment{}).InsideGitWorkTree(workTree); !got {
		t.Errorf("InsideGitWorkTree(%q) = false, want true for a git-init'd dir", workTree)
	}

	plainDir := t.TempDir()
	// t.TempDir() is not guaranteed to sit outside every git work tree (e.g.
	// if TMPDIR is itself under a repo checkout) — set GIT_CEILING_DIRECTORIES
	// so `git rev-parse --is-inside-work-tree` cannot walk up past plainDir's
	// parent and find an ancestor .git, keeping this assertion deterministic.
	t.Setenv("GIT_CEILING_DIRECTORIES", filepath.Dir(plainDir))
	if got := (hostEnvironment{}).InsideGitWorkTree(plainDir); got {
		t.Errorf("InsideGitWorkTree(%q) = true, want false for a dir that was never git-init'd", plainDir)
	}
}
