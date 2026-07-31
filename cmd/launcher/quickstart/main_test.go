package main

import "testing"

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
