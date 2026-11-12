package ledger_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"spindrift.dev/launcher/internal/gitexec"
	"spindrift.dev/launcher/internal/gitexec/gitexectest"
	"spindrift.dev/launcher/internal/ledger"
)

const remoteToken = "s3cr3t-ledger-token-6860"

// assertTokenOutOfBand fails if the token reached a traced git argv or the
// scratch repo's persisted config, or if any traced argv carries URL userinfo.
func assertTokenOutOfBand(t *testing.T, traceFile string, scratches ...string) {
	t.Helper()
	b, err := os.ReadFile(traceFile)
	if err != nil || len(b) == 0 {
		t.Fatalf("GIT_TRACE empty or unreadable (%v): the tracer saw no git invocation", err)
	}
	trace := string(b)
	if strings.Contains(trace, remoteToken) {
		t.Errorf("git trace carries the token:\n%s", trace)
	}
	for _, line := range strings.Split(trace, "\n") {
		if strings.Contains(line, "http://") && strings.Contains(line, "@") {
			t.Errorf("traced git argv carries URL userinfo: %s", line)
		}
	}
	for _, s := range scratches {
		cfg, err := os.ReadFile(filepath.Join(s, "config"))
		if err != nil {
			t.Fatalf("read scratch config: %v", err)
		}
		if strings.Contains(string(cfg), remoteToken) {
			t.Errorf("scratch git config carries the token:\n%s", cfg)
		}
	}
}

// TestRemoteCredentialAuthenticatesEveryRemoteCall drives the sync fetch, the
// push, and the ls-remote a lost lease triggers against a basic-auth HTTP
// remote whose URL carries no userinfo: only the Credential authenticates.
func TestRemoteCredentialAuthenticatesEveryRemoteCall(t *testing.T) {
	setGitIdentityEnv(t)
	traceFile := filepath.Join(t.TempDir(), "trace.log")
	t.Setenv("GIT_TRACE", traceFile)
	url, bare := gitexectest.AuthRemote(t, "oauth2", remoteToken)
	cred := gitexec.Credential{Username: "oauth2", Password: remoteToken}

	scratch := t.TempDir()
	r, err := ledger.NewRemote(scratch, url, cred)
	if err != nil {
		t.Fatalf("NewRemote: %v", err)
	}
	commit, err := r.Append("chore-cred", "", ledger.State{Phase: ledger.Claimed}, time.Now())
	if err != nil {
		t.Fatalf("Append: %v", err)
	}
	if got := runGitOutput(t, bare, "rev-parse", ledger.RefPrefix+"chore-cred"); strings.TrimSpace(got) != commit {
		t.Fatalf("remote tip = %q, want %s", got, commit)
	}

	// A rival wins the ref, so r's lease fails and Append consults ls-remote.
	rivalScratch := t.TempDir()
	rival, err := ledger.NewRemote(rivalScratch, url, cred)
	if err != nil {
		t.Fatalf("rival NewRemote: %v", err)
	}
	if _, err := rival.Append("chore-cred", commit, ledger.State{Phase: ledger.Done}, time.Now()); err != nil {
		t.Fatalf("rival Append: %v", err)
	}
	if _, err := r.Append("chore-cred", commit, ledger.State{Phase: ledger.Claimed}, time.Now()); !errors.Is(err, ledger.ErrLostRace) {
		t.Fatalf("stale Append = %v, want ErrLostRace", err)
	}

	assertTokenOutOfBand(t, traceFile, scratch, rivalScratch)
}

func TestRemoteWrongCredentialErrorOmitsToken(t *testing.T) {
	setGitIdentityEnv(t)
	url, _ := gitexectest.AuthRemote(t, "oauth2", remoteToken)
	const wrong = "wrong-token-xyz"

	_, err := ledger.NewRemote(t.TempDir(), url, gitexec.Credential{Username: "oauth2", Password: wrong})
	if err == nil {
		t.Fatal("NewRemote with a wrong token: got nil error, want one")
	}
	for _, secret := range []string{wrong, remoteToken} {
		if strings.Contains(err.Error(), secret) {
			t.Fatalf("error carries a token: %v", err)
		}
	}
}
