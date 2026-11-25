package connectorforge_test

import (
	"testing"

	"spindrift.dev/launcher/internal/connectorforge/connectorforgetest"
)

// One token authorizes Connector requests and core's git alike, and both read
// it at each use: a token rotated mid-run reaches the next request and the
// next push, and the stale one no longer does.
// Serial: WithGitOverHTTPS -> gitexectest.ServeTLS t.Setenv GIT_SSL_CAINFO, and the token env.
func TestRotatedTokenReachesTheNextConnectorRequestAndPush(t *testing.T) {
	const env = "FORGEJO_TOKEN"
	t.Setenv(env, "token-a")
	h := connectorforgetest.New(t, connectorforgetest.WithGitOverHTTPS(), connectorforgetest.WithTokenEnv(env))
	first, second := h.SeedLandable("9201"), h.SeedLandable("9202")
	h.AdvanceBase()

	if _, err := h.Forge().BranchExists(first); err != nil {
		t.Fatalf("BranchExists with the original token: %v", err)
	}
	if err := h.Forge().Rebase(first); err != nil {
		t.Fatalf("Rebase with the original token: %v", err)
	}

	// The backend rotates first, so the process still holds the stale token.
	h.Backend.SetToken("token-b")
	if _, err := h.Forge().BranchExists(second); err == nil {
		t.Error("BranchExists with the stale token succeeded, want a refusal")
	}
	if err := h.Forge().Rebase(second); err == nil {
		t.Error("Rebase with the stale token succeeded, want a refusal")
	}
	if h.Rebased("9202") {
		t.Fatal("a push with the stale token reached the repository")
	}

	t.Setenv(env, "token-b")
	if _, err := h.Forge().BranchExists(second); err != nil {
		t.Errorf("BranchExists after the rotation: %v", err)
	}
	if err := h.Forge().Rebase(second); err != nil {
		t.Errorf("Rebase after the rotation: %v", err)
	}
	if !h.Rebased("9202") {
		t.Error("the push after the rotation never reached the repository")
	}
}
