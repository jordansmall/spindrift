package main

import (
	"bytes"
	"io"
	"strings"
	"testing"
)

// TestReadOnlyTokenGate_ReadWriteIsNoOp verifies that checkReadOnlyTokenGate
// never calls introspect nor inspects BOX_GH_TOKEN when
// BOX_FORGE_AND_ISSUE_ACCESS is read-write (the default) -- read-write must
// stay a complete no-op, mirroring checkReadOnlyCapabilityGate's own
// read-write short-circuit.
func TestReadOnlyTokenGate_ReadWriteIsNoOp(t *testing.T) {
	c := minimalValidConfig()
	c.boxForgeAndIssueAccess = "read-write"
	introspect := func(token, repoSlug string) (tokenIntrospectionResult, error) {
		t.Fatal("introspect called under read-write")
		return tokenIntrospectionResult{}, nil
	}
	if err := checkReadOnlyTokenGate(c, introspect, io.Discard); err != nil {
		t.Errorf("checkReadOnlyTokenGate() with read-write = %v, want nil", err)
	}
}

// TestReadOnlyTokenGate_UnsetBoxTokenFails verifies the first fail-closed
// case: under read-only, an unset BOX_GH_TOKEN aborts startup with a named
// error rather than silently handing the Box no credential at all (which
// would leave the Box's own GH_TOKEN resolution falling through to the
// Launcher's, per boxGHTokenResolver).
func TestReadOnlyTokenGate_UnsetBoxTokenFails(t *testing.T) {
	c := minimalValidConfig()
	c.boxForgeAndIssueAccess = "read-only"
	t.Setenv("BOX_GH_TOKEN", "")
	introspect := func(token, repoSlug string) (tokenIntrospectionResult, error) {
		t.Fatal("introspect called with no Box token set")
		return tokenIntrospectionResult{}, nil
	}
	err := checkReadOnlyTokenGate(c, introspect, io.Discard)
	if err == nil {
		t.Fatal("checkReadOnlyTokenGate() = nil, want an error naming the missing BOX_GH_TOKEN")
	}
	if !strings.Contains(err.Error(), "BOX_GH_TOKEN") {
		t.Errorf("error should mention BOX_GH_TOKEN, got: %v", err)
	}
}

// TestReadOnlyTokenGate_DistinctNonWriteCapableTokenSucceeds verifies the
// happy path: a distinct Box token that introspection confirms carries no
// write scopes proceeds without error.
func TestReadOnlyTokenGate_DistinctNonWriteCapableTokenSucceeds(t *testing.T) {
	c := minimalValidConfig()
	c.boxForgeAndIssueAccess = "read-only"
	c.ghToken = "launcher-token"
	c.repoSlug = "owner/repo"
	t.Setenv("BOX_GH_TOKEN", "box-token")
	called := false
	introspect := func(token, repoSlug string) (tokenIntrospectionResult, error) {
		called = true
		if token != "box-token" || repoSlug != "owner/repo" {
			t.Errorf("introspect(%q, %q), want (%q, %q)", token, repoSlug, "box-token", "owner/repo")
		}
		return tokenIntrospectionResult{Introspectable: true, WriteCapable: false}, nil
	}
	if err := checkReadOnlyTokenGate(c, introspect, io.Discard); err != nil {
		t.Errorf("checkReadOnlyTokenGate() with a distinct non-write-capable token = %v, want nil", err)
	}
	if !called {
		t.Error("introspect was never called for a distinct Box token")
	}
}

// TestReadOnlyTokenGate_WriteCapableIntrospectableTokenFails verifies that a
// distinct Box token which introspection reports as write-capable (a classic
// PAT carrying `repo`, or a GitHub App installation token with a write
// permission) is rejected outright -- read-only never accepts a token that
// can be proven to write.
func TestReadOnlyTokenGate_WriteCapableIntrospectableTokenFails(t *testing.T) {
	c := minimalValidConfig()
	c.boxForgeAndIssueAccess = "read-only"
	c.ghToken = "launcher-token"
	t.Setenv("BOX_GH_TOKEN", "box-token")
	introspect := func(token, repoSlug string) (tokenIntrospectionResult, error) {
		return tokenIntrospectionResult{Introspectable: true, WriteCapable: true}, nil
	}
	err := checkReadOnlyTokenGate(c, introspect, io.Discard)
	if err == nil {
		t.Fatal("checkReadOnlyTokenGate() = nil, want an error naming the write-capable token")
	}
	if !strings.Contains(err.Error(), "write") {
		t.Errorf("error should mention the token's write capability, got: %v", err)
	}
}

// TestReadOnlyTokenGate_NonIntrospectableTokenWarnsAndSucceeds verifies the
// fine-grained-PAT case: GitHub exposes no endpoint to enumerate a
// fine-grained PAT's granted permissions, so checkReadOnlyTokenGate accepts
// it -- but only alongside a loud, visible warning rather than a silent
// pass, since the residual guarantee now rests entirely on the operator's
// own provisioning.
func TestReadOnlyTokenGate_NonIntrospectableTokenWarnsAndSucceeds(t *testing.T) {
	c := minimalValidConfig()
	c.boxForgeAndIssueAccess = "read-only"
	c.ghToken = "launcher-token"
	t.Setenv("BOX_GH_TOKEN", "box-token")
	introspect := func(token, repoSlug string) (tokenIntrospectionResult, error) {
		return tokenIntrospectionResult{Introspectable: false}, nil
	}
	var buf bytes.Buffer
	if err := checkReadOnlyTokenGate(c, introspect, &buf); err != nil {
		t.Errorf("checkReadOnlyTokenGate() with a non-introspectable token = %v, want nil", err)
	}
	if !strings.Contains(strings.ToUpper(buf.String()), "WARNING") {
		t.Errorf("want a loud warning printed for a non-introspectable token, got %q", buf.String())
	}
}

// TestReadOnlyTokenGate_EqualToLauncherTokenFails verifies the second
// fail-closed case: BOX_GH_TOKEN set but byte-for-byte identical to the
// Launcher's own GH_TOKEN is exactly the misconfiguration this gate exists
// to catch -- read-only must never silently collapse to full access.
func TestReadOnlyTokenGate_EqualToLauncherTokenFails(t *testing.T) {
	c := minimalValidConfig()
	c.boxForgeAndIssueAccess = "read-only"
	c.ghToken = "shared-token"
	t.Setenv("BOX_GH_TOKEN", "shared-token")
	introspect := func(token, repoSlug string) (tokenIntrospectionResult, error) {
		t.Fatal("introspect called with a Box token equal to the Launcher's")
		return tokenIntrospectionResult{}, nil
	}
	err := checkReadOnlyTokenGate(c, introspect, io.Discard)
	if err == nil {
		t.Fatal("checkReadOnlyTokenGate() = nil, want an error naming the shared token")
	}
	if !strings.Contains(err.Error(), "BOX_GH_TOKEN") {
		t.Errorf("error should mention BOX_GH_TOKEN, got: %v", err)
	}
}
