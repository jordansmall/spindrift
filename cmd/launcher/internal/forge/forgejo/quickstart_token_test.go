package forgejo_test

import (
	"bytes"
	"errors"
	"fmt"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/backendkit"
	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/forge/forgejo"
	"spindrift.dev/launcher/internal/forge/rest"
)

const rerun = "nix run example#quickstart"

func acquire(pasted string, probe func(string) error) (token, output string, err error) {
	var out bytes.Buffer
	token, err = forgejo.Strategy.AcquireToken(
		backendkit.Shared{RepoSlug: "o/r"},
		mapLookup(map[string]string{"FORGEJO_BASE_URL": "https://forge.example"}),
		backendkit.TokenPrompt{
			Out:          &out,
			PromptMasked: func(string) string { return pasted },
			Probe:        probe,
			RerunCmd:     rerun,
		})
	return token, out.String(), err
}

func TestAcquireToken_ValidTokenIsProbed(t *testing.T) {
	var probed string
	token, out, err := acquire("tok", func(tok string) error { probed = tok; return nil })
	if err != nil || token != "tok" || probed != "tok" {
		t.Fatalf("got %q probed %q, %v", token, probed, err)
	}
	if !strings.Contains(out, "ok: Forgejo token validated via live API ping") {
		t.Errorf("output = %q", out)
	}
}

func TestAcquireToken_Failures(t *testing.T) {
	cases := []struct {
		name  string
		paste string
		probe error
		want  []string
		is    error
	}{
		{"blank", "", nil, []string{"no Forgejo token provided", rerun}, nil},
		{"auth", "t", forge.ErrAuthFailure, []string{"rejected by the API at https://forge.example", "git remote set-url", rerun}, forge.ErrAuthFailure},
		{"status", "t", rest.StatusError{Status: 503}, []string{"HTTP status 503", "check the Forgejo instance's health/logs", `repo slug "o/r"`, rerun}, nil},
		{"decode", "t", rest.DecodeError{}, []string{"could not be parsed", "version/API compatibility", rerun}, nil},
		{"not found", "t", forge.ErrNotFound, []string{"unreachable or the repo slug is wrong", "check both", rerun}, forge.ErrNotFound},
		{"other", "t", errors.New("dial tcp"), []string{"unreachable or the repo slug is wrong", rerun}, nil},
		{"not found wrapping status", "t", fmt.Errorf("w: %w: %w", forge.ErrNotFound, rest.StatusError{Status: 404}), []string{"unreachable or the repo slug is wrong"}, forge.ErrNotFound},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, _, err := acquire(c.paste, func(string) error { return c.probe })
			if err == nil {
				t.Fatal("want error")
			}
			for _, w := range c.want {
				if !strings.Contains(err.Error(), w) {
					t.Errorf("error %q missing %q", err, w)
				}
			}
			if c.is != nil && !errors.Is(err, c.is) {
				t.Errorf("error %v does not wrap %v", err, c.is)
			}
		})
	}
}

func TestKnobsFromRemote(t *testing.T) {
	for remote, want := range map[string]string{
		"https://codeberg.org/o/r.git": "https://codeberg.org",
		"git@forge.example:o/r.git":    "https://forge.example",
		"https://forge.example/o/r":    "https://forge.example",
	} {
		got := forgejo.Strategy.KnobsFromRemote(remote)
		if len(got) != 1 || got["FORGEJO_BASE_URL"] != want {
			t.Errorf("KnobsFromRemote(%q) = %v, want FORGEJO_BASE_URL=%s", remote, got, want)
		}
	}
	if got := forgejo.Strategy.KnobsFromRemote(""); got != nil {
		t.Errorf("empty remote = %v, want nil", got)
	}
}
