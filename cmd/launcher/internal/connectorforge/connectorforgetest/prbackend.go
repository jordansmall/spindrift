package connectorforgetest

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os/exec"
	"sort"
	"strconv"
	"strings"
)

// prStore is the scripted PR half of the Backend, guarded by Backend.mu.
type prStore struct {
	pulls       []*pull
	statusID    int
	logs        map[int]string
	protected   map[string]bool
	allowRebase bool
	requests    int
	merges      int
}

type pull struct {
	number     int
	head, base string
	draft      bool
	merged     bool
	closed     bool
	enqueued   bool
	behind     int
	files      []string
	comments   []string
	mergeSHA   string
	statuses   []status
	// sequence scripts the rollup state per status request, in order; once it is
	// exhausted the state is empty. steady answers every request when no
	// sequence is scripted.
	sequence  []string
	sequenced bool
	steady    string
}

type status struct {
	ID          int    `json:"id"`
	Context     string `json:"context"`
	State       string `json:"state"`
	Description string `json:"description"`
	TargetURL   string `json:"target_url"`
}

// PRView is a snapshot of one scripted PR.
type PRView struct {
	Head, Base  string
	Draft       bool
	Merged      bool
	Closed      bool
	Enqueued    bool
	MergeCommit string
	Comments    []string
}

// Requests is how many requests of any kind the backend has seen.
func (b *Backend) Requests() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.prs.requests
}

// MergeRequests is how many requests the backend has seen whose path ends in
// /merge, on any path: a refused undeclared merge must never be counted here.
func (b *Backend) MergeRequests() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.prs.merges
}

// AddPR scripts an open PR number from head into base, ready or draft.
func (b *Backend) AddPR(number int, head, base string, draft bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.prs.pulls = append(b.prs.pulls, &pull{number: number, head: head, base: base, draft: draft})
}

// PRURL is the web URL the backend serves for PR number.
func (b *Backend) PRURL(number int) string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.web + "/pull/" + strconv.Itoa(number)
}

// PR snapshots PR number, or fails the test if it is unknown.
func (b *Backend) PR(number int) PRView {
	b.mu.Lock()
	defer b.mu.Unlock()
	p := b.pullLocked(number)
	if p == nil {
		panic(fmt.Sprintf("connectorforgetest: no PR %d", number))
	}
	return PRView{
		Head: p.head, Base: p.base, Draft: p.draft, Merged: p.merged, Closed: p.closed,
		Enqueued: p.enqueued, MergeCommit: p.mergeSHA, Comments: append([]string(nil), p.comments...),
	}
}

func (b *Backend) edit(number int, fn func(*pull)) {
	b.mu.Lock()
	defer b.mu.Unlock()
	p := b.pullLocked(number)
	if p == nil {
		panic(fmt.Sprintf("connectorforgetest: no PR %d", number))
	}
	fn(p)
}

// SetCheckStates scripts the rollup states successive status requests for PR
// number answer, then the empty state.
func (b *Backend) SetCheckStates(number int, states []string) {
	b.edit(number, func(p *pull) { p.sequence, p.sequenced = append([]string(nil), states...), true })
}

// SetSteadyState makes every status request for PR number answer state.
func (b *Backend) SetSteadyState(number int, state string) {
	b.edit(number, func(p *pull) { p.steady = state })
}

// AddStatus adds a commit status to PR number's head and returns its id.
func (b *Backend) AddStatus(number int, context, state, description, targetURL string) int {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.prs.statusID++
	id := b.prs.statusID
	p := b.pullLocked(number)
	p.statuses = append(p.statuses, status{id, context, state, description, targetURL})
	return id
}

// SetJobLog scripts the log the backend serves for status id's job.
func (b *Backend) SetJobLog(id int, log string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.prs.logs == nil {
		b.prs.logs = map[int]string{}
	}
	b.prs.logs[id] = log
}

// SetBehind scripts how many commits PR number's base has that its head lacks.
func (b *Backend) SetBehind(number, commits int) {
	b.edit(number, func(p *pull) { p.behind = commits })
}

// SetFiles scripts the files PR number changes.
func (b *Backend) SetFiles(number int, files []string) {
	b.edit(number, func(p *pull) { p.files = files })
}

// SetAutoMergeAllowed scripts the repository's allow_rebase setting.
func (b *Backend) SetAutoMergeAllowed(allowed bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.prs.allowRebase = allowed
}

// SetProtected scripts whether branch is protected.
func (b *Backend) SetProtected(branch string, protected bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.prs.protected == nil {
		b.prs.protected = map[string]bool{}
	}
	b.prs.protected[branch] = protected
}

func (b *Backend) pullLocked(number int) *pull {
	for _, p := range b.prs.pulls {
		if p.number == number {
			return p
		}
	}
	return nil
}

// headSHA is the head branch's real commit when the bare repo has it, so a
// landed PR is real, and a stable fake otherwise.
func (b *Backend) headSHA(p *pull) string {
	out, err := exec.Command("git", "-C", b.bare, "rev-parse", "--verify", "--quiet", "refs/heads/"+p.head).Output()
	if err != nil {
		return fmt.Sprintf("%040x", p.number)
	}
	return strings.TrimSpace(string(out))
}

func (b *Backend) wirePull(p *pull) map[string]any {
	state := "open"
	if p.closed || p.merged {
		state = "closed"
	}
	var merge any
	if p.mergeSHA != "" {
		merge = p.mergeSHA
	}
	return map[string]any{
		"number": p.number, "html_url": b.web + "/pull/" + strconv.Itoa(p.number),
		"state": state, "merged": p.merged, "draft": p.draft, "mergeable": !p.merged && !p.closed,
		"merge_commit_sha": merge,
		"head":             map[string]string{"ref": p.head, "sha": b.headSHA(p)},
		"base":             map[string]string{"ref": p.base},
	}
}

func reply(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

// servePR answers the forgejo pull, commit-status, compare, comment and job-log
// routes under the repository; rel is the path after /repos/<repo>.
func (b *Backend) servePR(w http.ResponseWriter, r *http.Request, rel string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	seg := strings.Split(strings.TrimPrefix(rel, "/"), "/")
	switch {
	case rel == "/pulls" && r.Method == http.MethodGet:
		out := []map[string]any{}
		pulls := append([]*pull(nil), b.prs.pulls...)
		sort.Slice(pulls, func(i, j int) bool { return pulls[i].number > pulls[j].number })
		for _, p := range pulls {
			out = append(out, b.wirePull(p))
		}
		reply(w, http.StatusOK, out)
	case rel == "/pulls" && r.Method == http.MethodPost:
		var in struct {
			Head, Base string
			Draft      bool
		}
		if json.NewDecoder(r.Body).Decode(&in) != nil {
			http.Error(w, "bad body", http.StatusBadRequest)
			return
		}
		n := 1
		for _, p := range b.prs.pulls {
			n = max(n, p.number+1)
		}
		p := &pull{number: n, head: in.Head, base: in.Base, draft: in.Draft}
		b.prs.pulls = append(b.prs.pulls, p)
		reply(w, http.StatusCreated, b.wirePull(p))
	case len(seg) >= 2 && seg[0] == "pulls":
		b.servePull(w, r, seg[1], strings.Join(seg[2:], "/"))
	case len(seg) == 3 && seg[0] == "issues" && seg[2] == "comments" && r.Method == http.MethodPost:
		var in struct{ Body string }
		p := b.pullByName(seg[1])
		if p == nil || json.NewDecoder(r.Body).Decode(&in) != nil {
			http.NotFound(w, r)
			return
		}
		p.comments = append(p.comments, in.Body)
		reply(w, http.StatusCreated, map[string]any{})
	case seg[0] == "compare" && len(seg) > 1 && r.Method == http.MethodGet:
		head, _, _ := strings.Cut(strings.Join(seg[1:], "/"), "...")
		for _, p := range b.prs.pulls {
			if p.head == head {
				reply(w, http.StatusOK, map[string]int{"total_commits": p.behind})
				return
			}
		}
		http.NotFound(w, r)
	case len(seg) == 3 && seg[0] == "commits" && seg[2] == "status" && r.Method == http.MethodGet:
		b.serveStatus(w, r, seg[1])
	case len(seg) == 4 && seg[0] == "actions" && seg[1] == "jobs" && seg[3] == "logs" && r.Method == http.MethodGet:
		id, _ := strconv.Atoi(seg[2])
		log, ok := b.prs.logs[id]
		if !ok {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(log))
	default:
		http.NotFound(w, r)
	}
}

func (b *Backend) pullByName(number string) *pull {
	n, err := strconv.Atoi(number)
	if err != nil {
		return nil
	}
	return b.pullLocked(n)
}

func (b *Backend) servePull(w http.ResponseWriter, r *http.Request, number, sub string) {
	p := b.pullByName(number)
	if p == nil {
		http.NotFound(w, r)
		return
	}
	switch {
	case sub == "" && r.Method == http.MethodGet:
		reply(w, http.StatusOK, b.wirePull(p))
	case sub == "" && r.Method == http.MethodPatch:
		var in struct{ Draft bool }
		if json.NewDecoder(r.Body).Decode(&in) != nil {
			http.Error(w, "bad body", http.StatusBadRequest)
			return
		}
		p.draft = in.Draft
		reply(w, http.StatusOK, b.wirePull(p))
	case sub == "files" && r.Method == http.MethodGet:
		out := []map[string]string{}
		for _, f := range p.files {
			out = append(out, map[string]string{"filename": f})
		}
		reply(w, http.StatusOK, out)
	case sub == "merge" && r.Method == http.MethodPost:
		b.mergeLocked(w, r, p)
	default:
		http.NotFound(w, r)
	}
}

// mergeLocked merges p, or with merge_when_checks_succeed only enqueues it. A
// draft or already-closed PR is refused as a real forge refuses it.
func (b *Backend) mergeLocked(w http.ResponseWriter, r *http.Request, p *pull) {
	var in struct {
		WhenChecksSucceed bool `json:"merge_when_checks_succeed"`
	}
	if json.NewDecoder(r.Body).Decode(&in) != nil {
		http.Error(w, "bad body", http.StatusBadRequest)
		return
	}
	if p.draft || p.merged || p.closed {
		http.Error(w, "pull request is not mergeable", http.StatusMethodNotAllowed)
		return
	}
	if in.WhenChecksSucceed {
		p.enqueued = true
		w.WriteHeader(http.StatusOK)
		return
	}
	sha := b.headSHA(p)
	// Fast-forward the base in the real repo when the head carries it, so Landed
	// is true; a head the base has moved past merges in name only.
	if exec.Command("git", "-C", b.bare, "merge-base", "--is-ancestor", "refs/heads/"+p.base, sha).Run() == nil {
		_ = exec.Command("git", "-C", b.bare, "update-ref", "refs/heads/"+p.base, sha).Run()
	}
	p.merged, p.mergeSHA = true, sha
	w.WriteHeader(http.StatusOK)
}

func (b *Backend) serveStatus(w http.ResponseWriter, r *http.Request, sha string) {
	for _, p := range b.prs.pulls {
		if b.headSHA(p) != sha {
			continue
		}
		state := p.steady
		switch {
		case p.sequenced:
			state = ""
			if len(p.sequence) > 0 {
				state, p.sequence = p.sequence[0], p.sequence[1:]
			}
		case state == "":
			for _, s := range p.statuses {
				if s.State == "failure" || s.State == "error" {
					state = "failure"
				}
			}
		}
		statuses := p.statuses
		if statuses == nil {
			statuses = []status{}
		}
		reply(w, http.StatusOK, map[string]any{"state": state, "statuses": statuses})
		return
	}
	http.NotFound(w, r)
}
