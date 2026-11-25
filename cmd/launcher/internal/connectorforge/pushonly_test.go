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
	"spindrift.dev/launcher/internal/gitexec"
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

func TestComposeHandsPlumbingTheDerivedCloneURLAndCredential(t *testing.T) {
	h := connectorforgetest.New(t)
	calls := h.PlumbingCalls()
	want := "https://" + h.Backend.Host + "/o/r.git"
	if len(calls) != 1 || calls[0].CloneURL != want || calls[0].Credential != h.Credential {
		t.Fatalf("plumbing calls = %v, want one for %q with the configured credential", calls, want)
	}
}

// Core derives the clone URL from the Git remote declaration and the Target
// repository, keeping a subpath.
func TestComposeDerivesTheCloneURLFromTheDeclaration(t *testing.T) {
	h := connectorforgetest.New(t)
	h.WebBase = "https://forge.example/sub/"
	if _, _, err := h.Compose(); err != nil {
		t.Fatalf("Compose = %v", err)
	}
	calls := h.PlumbingCalls()
	if got, want := calls[len(calls)-1].CloneURL, "https://forge.example/sub/o/r.git"; got != want {
		t.Errorf("plumbing clone URL = %q, want %q", got, want)
	}
}

func TestComposeRefusesAnUnderivableCloneURL(t *testing.T) {
	h := connectorforgetest.New(t)
	for name, remote := range map[string]string{
		"empty":       "",
		"http":        "http://forge.example",
		"no host":     "https://",
		"userinfo":    "https://u:p@forge.example",
		"query":       "https://forge.example?x=1",
		"fragment":    "https://forge.example#f",
		"not a URL":   "https://forge.example\x7f",
		"scheme-less": "forge.example",
	} {
		t.Run(name, func(t *testing.T) {
			before := len(h.PlumbingCalls())
			h.WebBase = remote
			_, _, err := h.Compose()
			if !errors.Is(err, connectorforge.ErrGitRemote) {
				t.Fatalf("Compose(Remote %q) = %v, want ErrGitRemote", remote, err)
			}
			if strings.Contains(err.Error(), "u:p") {
				t.Errorf("error leaks userinfo: %v", err)
			}
			if n := len(h.PlumbingCalls()); n != before {
				t.Errorf("plumbing built %d times for a refused remote, want 0", n-before)
			}
		})
	}
}

func TestCloneURLRefusesARepositoryThatIsNotOwnerSlashName(t *testing.T) {
	for _, repo := range []string{"", "r", "o/", "/r", "o/r/x", "./r", "o/..", "../r"} {
		if got, err := connectorforge.CloneURL("https://forge.example", repo); !errors.Is(err, connectorforge.ErrGitRemote) {
			t.Errorf("CloneURL(%q) = %q, %v, want ErrGitRemote", repo, got, err)
		}
	}
	got, err := connectorforge.CloneURL("https://forge.example/sub/", "o/r")
	if err != nil || got != "https://forge.example/sub/o/r.git" {
		t.Errorf("CloneURL = %q, %v, want the subpath kept and the trailing slash trimmed", got, err)
	}
}

func TestComposeRefusesATargetRepositoryThatIsNotOwnerSlashName(t *testing.T) {
	h := connectorforgetest.New(t)
	_, _, err := connectorforge.Compose(h.Conn, connectorforge.Target{Repo: "r"}, connectorforge.Git{
		WebBase:  "https://" + h.Backend.Host,
		Plumbing: func(string, gitexec.Credential) forge.CodeForge { t.Fatal("plumbing built"); return nil },
	})
	if !errors.Is(err, connectorforge.ErrGitRemote) {
		t.Fatalf("Compose = %v, want ErrGitRemote", err)
	}
}

// Probe reads liveness through a request the backend answers.
func TestProbeReachesTheBackend(t *testing.T) {
	h := connectorforgetest.New(t)
	before := h.Backend.BranchRequests()
	repo, err := h.Forge().Probe()
	if err != nil || repo != connectorforgetest.Repo {
		t.Fatalf("Probe = (%q, %v), want (%q, nil)", repo, err, connectorforgetest.Repo)
	}
	if n := h.Backend.BranchRequests() - before; n != 1 {
		t.Errorf("Probe sent %d backend branch requests, want 1", n)
	}
}

func TestProbeFailsWhenTheBackendIsUnreachable(t *testing.T) {
	h := connectorforgetest.New(t)
	if _, err := h.Unreachable().Probe(); err == nil {
		t.Fatal("Probe succeeded against a closed backend")
	}
}

// The PR URLs are pinned to the host core derived from the Git remote, not to
// the host the backend serves: an answer on the backend's host is refused.
func TestComposePinsPRURLsToTheDerivedHostNotTheBackendHost(t *testing.T) {
	const derived = "github.example"
	h := connectorforgetest.NewPR(t)
	h.WebBase = "https://" + derived
	h.SeedOpenPR("7")
	branch := forge.AgentBranchName(h.BranchPrefix(), "7")
	_, caps, err := h.Compose()
	if err != nil {
		t.Fatalf("Compose = %v", err)
	}
	if pr, ok, err := caps.PRForge.OpenPRForBranch(branch); err == nil {
		t.Fatalf("OpenPRForBranch = %+v, %v, want an answer on %s refused", pr, ok, h.Backend.Host)
	}

	h.Backend.SetWeb("https://" + derived + "/" + connectorforgetest.Repo)
	pr, ok, err := caps.PRForge.OpenPRForBranch(branch)
	if err != nil || !ok || pr.URL != h.Backend.PRURL(7) {
		t.Errorf("OpenPRForBranch = %+v, %v, %v, want the PR on %s accepted", pr, ok, err, derived)
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

type noted struct {
	method string
	err    error
}

// Git.Note sees each guest call that fails after retries and classification,
// by method; a failure Compose's own remote read hits is the caller's to
// record, so it is not noted twice.
func TestComposeNotesFailedCallsByMethod(t *testing.T) {
	var got []noted
	h := connectorforgetest.New(t)
	h.Note = func(method string, err error) { got = append(got, noted{method, err}) }
	cf, _, err := h.Compose()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := cf.BranchExists("agent/issue-9"); err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("a successful call noted %v", got)
	}

	h.Backend.FailBranchRequests(422)
	_, callErr := cf.BranchExists("agent/issue-9")

	if callErr == nil || len(got) != 1 || got[0].method != "forge.branchExists" || got[0].err != callErr {
		t.Errorf("noted %v for call error %v, want one forge.branchExists note of that error", got, callErr)
	}
}

func TestComposeNotesNothingForAnUnderivableRemote(t *testing.T) {
	var got []noted
	h := connectorforgetest.New(t)
	h.Note = func(method string, err error) { got = append(got, noted{method, err}) }
	h.WebBase = "http://" + h.Backend.Host

	if _, _, err := h.Compose(); !errors.Is(err, connectorforge.ErrGitRemote) {
		t.Fatalf("Compose = %v, want ErrGitRemote", err)
	}
	if len(got) != 0 {
		t.Errorf("Compose noted %v, want the caller to record its error", got)
	}
}

func TestComposeWithoutANoteStillFailsCalls(t *testing.T) {
	h := connectorforgetest.New(t)
	cf, _, err := h.Compose()
	if err != nil {
		t.Fatal(err)
	}
	h.Backend.FailBranchRequests(422)
	if _, err := cf.BranchExists("agent/issue-9"); err == nil {
		t.Error("BranchExists succeeded, want the failure")
	}
}
