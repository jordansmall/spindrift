package main

import (
	"context"
	"fmt"
	"os"

	"spindrift.dev/launcher/internal/ghapp"
)

// startTokenRefresh keeps GH_TOKEN alive across a long run when GitHub App
// credentials are configured (issue #1027). Every host-side gh call (CI poll,
// pr merge, label edit, final comment) and every later Box launch reads
// GH_TOKEN from the ambient environment, so re-minting an installation token
// and republishing it via os.Setenv covers them all with no per-call-site
// change.
//
// It mints an initial token synchronously — replacing whatever GH_TOKEN was
// minted once at workflow start — then launches a background refresher that
// re-mints before each ~1h expiry. Returns a stop func (always non-nil); the
// caller runs it during cleanup to halt the refresher.
//
// With no App creds it is a no-op: GH_TOKEN stays the fine-grained PAT or the
// workflow-minted token, so short runs and the PAT path are unaffected. A
// misconfigured App (key won't parse, or the initial mint fails) logs a
// warning and leaves the ambient token in place rather than aborting the run.
func startTokenRefresh(c config) func() {
	if c.agentWorkerAppID == "" || c.agentWorkerAppPrivateKey == "" {
		return func() {}
	}
	m, err := ghapp.NewMinter(ghapp.Config{
		AppID:         c.agentWorkerAppID,
		PrivateKeyPEM: c.agentWorkerAppPrivateKey,
		Repo:          c.repoSlug,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "!! GH_TOKEN refresh disabled: %v\n", err)
		return func() {}
	}
	ctx, cancel := context.WithCancel(context.Background())
	publish := func(tok string) { os.Setenv("GH_TOKEN", tok) }
	logf := func(format string, args ...any) { fmt.Fprintf(os.Stderr, format+"\n", args...) }
	if err := ghapp.Start(ctx, m, publish, logf); err != nil {
		cancel()
		fmt.Fprintf(os.Stderr, "!! GH_TOKEN refresh disabled: initial mint failed: %v\n", err)
		return func() {}
	}
	fmt.Println("GH_TOKEN refresh: re-minting short-lived App installation tokens for long runs")
	return cancel
}
