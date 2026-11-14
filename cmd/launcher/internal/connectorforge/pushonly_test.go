package connectorforge_test

import (
	"errors"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/connectorforge"
	"spindrift.dev/launcher/internal/connectorforge/connectorforgetest"
	"spindrift.dev/launcher/internal/connectorhost"
	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/forge/forgetest"
)

func TestComposedForgeCodeForgeContract(t *testing.T) {
	forgetest.RunCodeForgeContract(t, connectorforgetest.New(t))
}

func TestComposeResolvesPushOnlyFromTheHandshake(t *testing.T) {
	h := connectorforgetest.New(t)
	caps := h.Capabilities()
	if caps.PRForge != nil {
		t.Errorf("PRForge = %v, want nil for a push-only forge", caps.PRForge)
	}
	if caps.BranchDeleter == nil {
		t.Error("BranchDeleter is nil, want the composed forge")
	}
	if _, isPR := h.Forge().(forge.PRForge); isPR {
		t.Error("composed forge implements forge.PRForge")
	}
}

func TestComposeRequiresTheBranchesCapability(t *testing.T) {
	h := connectorforgetest.New(t)
	saved := h.Conn.Capabilities
	h.Conn.Capabilities = []string{"forge"}
	defer func() { h.Conn.Capabilities = saved }()
	if _, _, err := h.Compose(); !errors.Is(err, connectorforge.ErrNotForge) {
		t.Fatalf("Compose = %v, want ErrNotForge", err)
	}
	h.Conn.Capabilities = []string{"forge.branches"}
	if _, _, err := h.Compose(); !errors.Is(err, connectorforge.ErrPRForgeUnsupported) {
		t.Fatalf("Compose without forge.pushOnly = %v, want ErrPRForgeUnsupported", err)
	}
}

func TestBranchExistsAndDeleteBranchAgainstTheRealRepo(t *testing.T) {
	h := connectorforgetest.New(t)
	branch := h.SeedLandable("7")
	cf := h.Forge().(forge.BranchDeleter)
	be := h.Forge()

	if ok, err := be.BranchExists(branch); err != nil || !ok {
		t.Fatalf("BranchExists(%q) = (%v, %v), want (true, nil)", branch, ok, err)
	}
	if ok, err := be.BranchExists("agent/issue-404"); err != nil || ok {
		t.Fatalf("BranchExists(missing) = (%v, %v), want (false, nil)", ok, err)
	}
	if err := cf.DeleteBranch(branch, "main"); err != nil {
		t.Fatalf("DeleteBranch: %v", err)
	}
	if ok, err := be.BranchExists(branch); err != nil || ok {
		t.Fatalf("BranchExists after delete = (%v, %v), want (false, nil)", ok, err)
	}
}

func TestDeleteBranchRefusesBaseAndEmptyBeforeAnyBackendHit(t *testing.T) {
	h := connectorforgetest.New(t)
	cf := h.Forge().(forge.BranchDeleter)
	for _, branch := range []string{"main", "refs/heads/main", "", "-f"} {
		if err := cf.DeleteBranch(branch, "main"); err == nil {
			t.Errorf("DeleteBranch(%q) succeeded, want a refusal", branch)
		}
	}
	if n := h.Backend.BranchRequests(); n != 0 {
		t.Errorf("backend saw %d branch requests, want 0", n)
	}
}

func TestComposeHandsPlumbingTheCloneURLAndCredentialOnlyWhenTheRemoteIsAccepted(t *testing.T) {
	h := connectorforgetest.New(t)
	calls := h.PlumbingCalls()
	want := "https://" + h.Backend.Host + "/o/r.git"
	if len(calls) != 1 || calls[0].CloneURL != want || calls[0].Credential != h.Credential {
		t.Fatalf("plumbing calls = %v, want one for %q with the configured credential", calls, want)
	}

	hostile := connectorforgetest.New(t)
	hostile.Backend.SetRemote("https://evil.example/o/r.git", "https://"+hostile.Backend.Host+"/o/r")
	before := len(hostile.PlumbingCalls())
	if _, _, err := hostile.Compose(); !errors.Is(err, connectorforge.ErrRemoteHost) {
		t.Fatalf("Compose = %v, want ErrRemoteHost", err)
	}
	if n := len(hostile.PlumbingCalls()); n != before {
		t.Errorf("plumbing built %d times for a refused remote, want 0", n-before)
	}
}

func TestComposeRefusesAHostileRemote(t *testing.T) {
	const secret = "s3cr3t"
	cases := []struct {
		name       string
		clone, web func(host string) string
		want       error
	}{
		{"credentialed clone", func(h string) string { return "https://user:" + secret + "@" + h + "/o/r.git" },
			func(h string) string { return "https://" + h + "/o/r" }, connectorforge.ErrRemoteCredentials},
		{"credentialed web", func(h string) string { return "https://" + h + "/o/r.git" },
			func(h string) string { return "https://" + secret + "@" + h + "/o/r" }, connectorforge.ErrRemoteCredentials},
		{"undeclared clone host", func(string) string { return "https://evil.example/o/r.git" },
			func(h string) string { return "https://" + h + "/o/r" }, connectorforge.ErrRemoteHost},
		{"undeclared web host", func(h string) string { return "https://" + h + "/o/r.git" },
			func(string) string { return "https://evil.example/o/r" }, connectorforge.ErrRemoteHost},
		{"plain http", func(h string) string { return "http://" + h + "/o/r.git" },
			func(h string) string { return "https://" + h + "/o/r" }, connectorforge.ErrRemoteHost},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := connectorforgetest.New(t)
			host := h.Backend.Host
			h.Backend.SetRemote(tc.clone(host), tc.web(host))
			h.Audit.Reset()
			_, _, err := h.Compose()
			if !errors.Is(err, tc.want) {
				t.Fatalf("Compose = %v, want %v", err, tc.want)
			}
			audit := h.Audit.String()
			if !strings.Contains(audit, "refused") || !strings.Contains(audit, "forge.remote") {
				t.Errorf("audit = %q, want a refused entry naming forge.remote", audit)
			}
			if strings.Contains(err.Error(), secret) || strings.Contains(audit, secret) {
				t.Errorf("secret leaked: error %q, audit %q", err, audit)
			}
			// Probe re-resolves the identity and refuses the same way.
			if _, perr := h.Forge().Probe(); !errors.Is(perr, tc.want) {
				t.Errorf("Probe = %v, want %v", perr, tc.want)
			}
		})
	}
}

func TestConnectorErrorClassCoreObserves(t *testing.T) {
	t.Run("transient is retried", func(t *testing.T) {
		h := connectorforgetest.New(t)
		branch := h.SeedLandable("8")
		h.Backend.FailBranchRequests(503)
		ok, err := h.Forge().BranchExists(branch)
		if err != nil || !ok {
			t.Fatalf("BranchExists = (%v, %v), want (true, nil)", ok, err)
		}
		if n := h.Backend.BranchRequests(); n != 2 {
			t.Errorf("backend saw %d branch requests, want 2", n)
		}
		if s := h.Sleeps(); len(s) != 1 {
			t.Errorf("slept %v, want one backoff", s)
		}
	})
	t.Run("transient exhausted", func(t *testing.T) {
		h := connectorforgetest.New(t)
		h.Backend.FailBranchRequests(503, 503, 503, 503)
		_, err := h.Forge().BranchExists("agent/issue-9")
		if connectorhost.ErrorClass(err) != connectorhost.Transient {
			t.Fatalf("err = %v, want a transient error", err)
		}
		if n := h.Backend.BranchRequests(); n != 3 {
			t.Errorf("backend saw %d branch requests, want 3 attempts", n)
		}
	})
	t.Run("delete retried after a transient treats not-found as done", func(t *testing.T) {
		h := connectorforgetest.New(t)
		// The first DELETE may have landed before its 5xx came back, so the
		// retry's 404 is the delete having already happened.
		h.Backend.FailBranchRequests(503, 404)
		if err := h.Forge().(forge.BranchDeleter).DeleteBranch("agent/issue-9", "main"); err != nil {
			t.Fatalf("DeleteBranch = %v, want nil", err)
		}
		if n := h.Backend.BranchRequests(); n != 2 {
			t.Errorf("backend saw %d branch requests, want 2", n)
		}
		if s := h.Sleeps(); len(s) != 1 {
			t.Errorf("slept %v, want one backoff", s)
		}
	})
	t.Run("delete first-attempt not-found is an error", func(t *testing.T) {
		h := connectorforgetest.New(t)
		h.Backend.FailBranchRequests(404)
		err := h.Forge().(forge.BranchDeleter).DeleteBranch("agent/issue-9", "main")
		if connectorhost.ErrorClass(err) != connectorhost.NotFound {
			t.Fatalf("err = %v, want a not-found error", err)
		}
		if n := h.Backend.BranchRequests(); n != 1 {
			t.Errorf("backend saw %d branch requests, want 1", n)
		}
	})
	t.Run("permanent is not retried", func(t *testing.T) {
		h := connectorforgetest.New(t)
		h.Backend.FailBranchRequests(422)
		_, err := h.Forge().BranchExists("agent/issue-9")
		if connectorhost.ErrorClass(err) != connectorhost.Permanent {
			t.Fatalf("err = %v (class %q), want permanent", err, connectorhost.ErrorClass(err))
		}
		if n := h.Backend.BranchRequests(); n != 1 {
			t.Errorf("backend saw %d branch requests, want 1", n)
		}
		if s := h.Sleeps(); len(s) != 0 {
			t.Errorf("slept %v, want none", s)
		}
	})
	t.Run("rate limit maps to core's sentinel", func(t *testing.T) {
		h := connectorforgetest.New(t)
		h.Backend.FailBranchRequests(429)
		_, err := h.Forge().BranchExists("agent/issue-9")
		if !errors.Is(err, forge.ErrRateLimit) {
			t.Fatalf("err = %v, want forge.ErrRateLimit", err)
		}
		if n := h.Backend.BranchRequests(); n != 1 {
			t.Errorf("backend saw %d branch requests, want 1", n)
		}
	})
	t.Run("auth maps to core's sentinel", func(t *testing.T) {
		h := connectorforgetest.New(t)
		h.Backend.FailBranchRequests(401)
		_, err := h.Forge().BranchExists("agent/issue-9")
		if !errors.Is(err, forge.ErrAuthFailure) {
			t.Fatalf("err = %v, want forge.ErrAuthFailure", err)
		}
	})
}

func TestComposedForgeCodeForgeContract_RemoteEngine(t *testing.T) {
	forgetest.RunCodeForgeContract(t, connectorforgetest.NewRemote(t))
}
