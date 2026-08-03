package forgejo_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"regexp"
	"sync"
	"testing"

	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/forge/forgejo"
	"spindrift.dev/launcher/internal/forge/forgetest"
)

var hostMediationCommentRe = regexp.MustCompile(`^/api/v1/repos/owner/repo/issues/([0-9]+)/comments$`)

// hostMediationHarness is a forgetest.HostMediationHarness backed by a real
// bare git repo (RelayBundle's genuine push target, mirroring
// newReadOnlyRelayHarness) plus a combined httptest server standing in for
// Forgejo's pulls-create and issue-comment REST endpoints.
type hostMediationHarness struct {
	t    *testing.T
	repo *forgetest.GitRepoFixture
	srv  *httptest.Server
	cf   forge.CodeForge
	tr   forge.IssueTracker

	mu         sync.Mutex
	relayedSHA map[string]string
	// comments maps a pre-registered issue number to its posted bodies, so
	// far. An issue number absent from this map 404s -- the fault case,
	// exactly like contract_test.go's forgejoHarness comment route.
	comments map[string][]string
}

func newHostMediationHarness(t *testing.T) *hostMediationHarness {
	t.Helper()
	t.Setenv("GIT_AUTHOR_NAME", "Test Bot")
	t.Setenv("GIT_AUTHOR_EMAIL", "bot@example.com")
	t.Setenv("GIT_COMMITTER_NAME", "Test Bot")
	t.Setenv("GIT_COMMITTER_EMAIL", "bot@example.com")

	repo := forgetest.NewGitRepoFixture(t, "main")
	h := &hostMediationHarness{
		t:          t,
		repo:       repo,
		relayedSHA: map[string]string{},
		comments:   map[string][]string{"801": nil},
	}
	h.srv = httptest.NewServer(http.HandlerFunc(h.handle))
	t.Cleanup(h.srv.Close)

	h.cf = forgejo.NewReadOnlyForgejoCodeForge(forgejo.ForgejoCodeForgeConfig{
		BaseURL:      h.srv.URL,
		Repo:         "owner/repo",
		Token:        "tok",
		BaseBranch:   "main",
		UserName:     "Test Bot",
		UserEmail:    "bot@example.com",
		BranchPrefix: "agent/issue-",
		GitRemoteURL: repo.Bare,
	})
	h.tr = forgejo.NewForgejoClient(forgejo.ForgejoConfig{
		BaseURL: h.srv.URL,
		Repo:    "owner/repo",
		Token:   "tok",
	})
	return h
}

func (h *hostMediationHarness) CodeForge() forge.CodeForge  { return h.cf }
func (h *hostMediationHarness) Tracker() forge.IssueTracker { return h.tr }

func (h *hostMediationHarness) handle(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.Method == http.MethodPost && r.URL.Path == "/api/v1/repos/owner/repo/pulls":
		var body struct {
			Head string `json:"head"`
		}
		json.NewDecoder(r.Body).Decode(&body)
		if body.Head == "fail-head" {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{
			"number":   999,
			"html_url": "https://forge.test/owner/repo/pulls/999",
		})
	case r.Method == http.MethodPost && hostMediationCommentRe.MatchString(r.URL.Path):
		num := hostMediationCommentRe.FindStringSubmatch(r.URL.Path)[1]
		h.mu.Lock()
		_, ok := h.comments[num]
		h.mu.Unlock()
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		var body struct {
			Body string `json:"body"`
		}
		json.NewDecoder(r.Body).Decode(&body)
		h.mu.Lock()
		h.comments[num] = append(h.comments[num], body.Body)
		h.mu.Unlock()
		w.WriteHeader(http.StatusCreated)
	default:
		http.NotFound(w, r)
	}
}

func (h *hostMediationHarness) SeedBundle(ref string) (outboxDir string) {
	outbox := h.t.TempDir()
	h.mu.Lock()
	h.relayedSHA[ref] = forgetest.SeedRelayBundle(h.t, h.repo.Bare, "main", outbox, ref)
	h.mu.Unlock()
	return outbox
}

func (h *hostMediationHarness) BundleLanded(ref string) bool {
	h.mu.Lock()
	want, ok := h.relayedSHA[ref]
	h.mu.Unlock()
	if !ok {
		return false
	}
	return forgetest.RevParse(h.t, h.repo.Bare, "refs/heads/"+ref) == want
}

func (h *hostMediationHarness) EmptyOutbox() (outboxDir string) {
	return h.t.TempDir()
}

func (h *hostMediationHarness) SeedDraftPRHead(failing bool) (head string) {
	if failing {
		return "fail-head"
	}
	return "agent/issue-hmpr1"
}

func (h *hostMediationHarness) SeedCommentTarget(failing bool) (num string) {
	if failing {
		return "999"
	}
	return "801"
}

func (h *hostMediationHarness) CommentPosted(num, body string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, b := range h.comments[num] {
		if b == body {
			return true
		}
	}
	return false
}

func TestReadOnlyForgejoCodeForge_HostMediationContract(t *testing.T) {
	forgetest.RunHostMediationContract(t, newHostMediationHarness(t))
}
