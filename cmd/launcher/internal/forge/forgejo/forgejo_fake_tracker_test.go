package forgejo_test

import (
	"encoding/json"
	"net/http"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"

	"spindrift.dev/launcher/internal/forge"
)

// fakeTracker is the fake's issue-tracker half: issues and the label registry
// of owner/repo, plus its owner's org labels. It shares fakeForgejo's lock and
// its index space (nextNum), since Forgejo numbers issues and PRs together.
type fakeTracker struct {
	issues    map[int]*fakeIssue
	labels    []fakeLabel // sticky: a label stays defined after every issue drops it
	orgLabels []fakeLabel
	// orgStatus is what the org-labels route answers when it is not 200: 404 for
	// a user-owned repository, 403 for a token without read:organization.
	orgStatus int
	nextLabel int
}

type fakeIssue struct {
	number   int
	title    string
	body     string
	state    string
	labels   []string
	deps     []string
	failDeps bool // the dependencies endpoint answers 500
	comments []fakeComment
}

type fakeComment struct{ author, createdAt, body string }

type fakeLabel struct {
	id   int
	name string
}

func newFakeTracker() fakeTracker {
	return fakeTracker{issues: map[int]*fakeIssue{}, orgStatus: http.StatusNotFound}
}

var (
	fakeIssuesRe      = regexp.MustCompile(`^/api/v1/repos/owner/repo/issues$`)
	fakeIssueRe       = regexp.MustCompile(`^/api/v1/repos/owner/repo/issues/([0-9]+)(?:/(comments|dependencies|blocks|labels))?$`)
	fakeRepoLabelsRe  = regexp.MustCompile(`^/api/v1/repos/owner/repo/labels$`)
	fakeOrgLabelsRe   = regexp.MustCompile(`^/api/v1/orgs/owner/labels$`)
	fakeLabelColourRe = regexp.MustCompile(`^#[0-9a-fA-F]{6}$`)
)

// defineLabel registers name in the sticky repo registry and returns its id.
// The caller holds f.mu.
func (f *fakeForgejo) defineLabel(name string) int {
	t := &f.trk
	for _, l := range t.labels {
		if l.name == name {
			return l.id
		}
	}
	t.nextLabel++
	t.labels = append(t.labels, fakeLabel{t.nextLabel, name})
	return t.nextLabel
}

// SeedIssue scripts an issue; its labels become defined, as on a real forge.
func (f *fakeForgejo) SeedIssue(iss forge.Issue) {
	f.mu.Lock()
	defer f.mu.Unlock()
	n, _ := strconv.Atoi(iss.Number)
	state := "open"
	if iss.State == forge.IssueClosed {
		state = "closed"
	}
	f.trk.issues[n] = &fakeIssue{number: n, title: iss.Title, body: iss.Body, state: state, labels: slices.Clone(iss.Labels)}
	for _, l := range iss.Labels {
		f.defineLabel(l)
	}
}

func (f *fakeForgejo) SeedNativeDeps(num string, ids []string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.trk.issues[atoi(num)].deps = ids
}

func (f *fakeForgejo) FailNativeDeps(num string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.trk.issues[atoi(num)].failDeps = true
}

func (f *fakeForgejo) SeedComments(num string, comments []forge.Comment) {
	f.mu.Lock()
	defer f.mu.Unlock()
	iss := f.trk.issues[atoi(num)]
	iss.comments = nil
	for _, c := range comments {
		iss.comments = append(iss.comments, fakeComment{c.Author, c.CreatedAt, c.Body})
	}
}

// seedOrgLabels scripts the owner's org labels; they are not in the repo
// registry, so only a union read sees them.
func (f *fakeForgejo) seedOrgLabels(names ...string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.trk.orgStatus = http.StatusOK
	for _, n := range names {
		f.trk.nextLabel++
		f.trk.orgLabels = append(f.trk.orgLabels, fakeLabel{f.trk.nextLabel, n})
	}
}

func (f *fakeForgejo) scriptOrgStatus(status int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.trk.orgStatus = status
}

func (f *fakeForgejo) issueState(num string) (state string, labels []string, ok bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	iss := f.trk.issues[atoi(num)]
	if iss == nil {
		return "", nil, false
	}
	return iss.state, slices.Clone(iss.labels), true
}

// atoi panics on a malformed number: the fake has no t, and a silent 0 would
// surface as a misleading 404.
func atoi(s string) int {
	n, err := strconv.Atoi(s)
	if err != nil {
		panic("forgejo fake: malformed issue number " + strconv.Quote(s))
	}
	return n
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func issueJSON(i *fakeIssue) map[string]any {
	labels := []map[string]any{}
	for _, l := range i.labels {
		labels = append(labels, map[string]any{"name": l})
	}
	return map[string]any{"number": i.number, "title": i.title, "body": i.body, "state": i.state, "labels": labels}
}

func labelsJSON(ls []fakeLabel) []map[string]any {
	out := []map[string]any{}
	for _, l := range ls {
		out = append(out, map[string]any{"id": l.id, "name": l.name})
	}
	return out
}

// serveTracker answers the issue and label routes and reports whether it
// owned the request; a comment on a pull is left to the PR routes.
func (f *fakeForgejo) serveTracker(w http.ResponseWriter, r *http.Request) bool {
	p := r.URL.Path
	switch {
	case fakeIssuesRe.MatchString(p):
		switch r.Method {
		case http.MethodGet:
			f.listIssues(w, r)
		case http.MethodPost:
			f.createIssue(w, r)
		default:
			return false
		}
	case fakeIssueRe.MatchString(p):
		m := fakeIssueRe.FindStringSubmatch(p)
		f.mu.Lock()
		iss := f.trk.issues[atoi(m[1])]
		_, isPull := f.pulls[m[1]]
		f.mu.Unlock()
		if isPull && iss == nil {
			return false
		}
		if iss == nil {
			http.NotFound(w, r)
			return true
		}
		return f.serveIssue(w, r, iss, m[2])
	case fakeRepoLabelsRe.MatchString(p):
		switch r.Method {
		case http.MethodGet:
			f.mu.Lock()
			out := labelsJSON(windowPage(r, slices.Clone(f.trk.labels)))
			f.mu.Unlock()
			writeJSON(w, http.StatusOK, out)
		case http.MethodPost:
			f.createLabel(w, r)
		default:
			return false
		}
	case r.Method == http.MethodGet && fakeOrgLabelsRe.MatchString(p):
		f.mu.Lock()
		status, out := f.trk.orgStatus, labelsJSON(windowPage(r, slices.Clone(f.trk.orgLabels)))
		f.mu.Unlock()
		if status != http.StatusOK {
			w.WriteHeader(status)
			return true
		}
		writeJSON(w, http.StatusOK, out)
	default:
		return false
	}
	return true
}

// serveIssue answers the routes under one existing issue, reporting whether
// the method was one it serves.
func (f *fakeForgejo) serveIssue(w http.ResponseWriter, r *http.Request, iss *fakeIssue, sub string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	switch {
	case sub == "" && r.Method == http.MethodGet:
		writeJSON(w, http.StatusOK, issueJSON(iss))
	case sub == "" && r.Method == http.MethodPatch:
		var body struct {
			State string `json:"state"`
		}
		json.NewDecoder(r.Body).Decode(&body)
		if body.State != "" {
			iss.state = body.State
		}
		writeJSON(w, http.StatusCreated, issueJSON(iss))
	case sub == "comments" && r.Method == http.MethodGet:
		// Like real Forgejo, the whole thread; page and limit are ignored.
		out := []map[string]any{}
		for _, c := range iss.comments {
			out = append(out, map[string]any{"user": map[string]any{"login": c.author}, "created_at": c.createdAt, "body": c.body})
		}
		writeJSON(w, http.StatusOK, out)
	case sub == "comments" && r.Method == http.MethodPost:
		var body struct {
			Body string `json:"body"`
		}
		json.NewDecoder(r.Body).Decode(&body)
		iss.comments = append(iss.comments, fakeComment{author: "bot", body: body.Body})
		writeJSON(w, http.StatusCreated, map[string]any{})
	case sub == "dependencies" && r.Method == http.MethodGet:
		if iss.failDeps {
			w.WriteHeader(http.StatusInternalServerError)
			return true
		}
		out := []map[string]any{}
		for _, id := range iss.deps {
			out = append(out, map[string]any{"number": atoi(id)})
		}
		writeJSON(w, http.StatusOK, windowPage(r, out))
	case sub == "blocks" && r.Method == http.MethodGet:
		writeJSON(w, http.StatusOK, []map[string]any{})
	case sub == "labels" && r.Method == http.MethodPut:
		var body struct {
			Labels []string `json:"labels"`
		}
		json.NewDecoder(r.Body).Decode(&body)
		iss.labels = body.Labels
		for _, l := range body.Labels {
			f.defineLabel(l)
		}
		writeJSON(w, http.StatusOK, labelsJSON(nil))
	default:
		return false
	}
	return true
}

// listIssues serves the open issues, newest first. Like real Forgejo it drops
// a labels filter naming no defined label and returns every issue.
func (f *fakeForgejo) listIssues(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	state := q.Get("state")
	if state == "" {
		state = "open"
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	var want []string
	for _, n := range strings.Split(q.Get("labels"), ",") {
		if n != "" && slices.ContainsFunc(f.trk.labels, func(l fakeLabel) bool { return l.name == n }) {
			want = append(want, n)
		}
	}
	var matched []*fakeIssue
	for _, i := range f.trk.issues {
		if state != "all" && i.state != state {
			continue
		}
		if !slices.ContainsFunc(want, func(n string) bool { return !slices.Contains(i.labels, n) }) {
			matched = append(matched, i)
		}
	}
	sort.Slice(matched, func(a, b int) bool { return matched[a].number > matched[b].number })
	w.Header().Set("X-Total-Count", strconv.Itoa(len(matched)))
	out := []map[string]any{}
	for _, i := range windowPage(r, matched) {
		out = append(out, issueJSON(i))
	}
	writeJSON(w, http.StatusOK, out)
}

// createIssue files an issue whose labels arrive as ids, numbering it from the
// index space the pulls share.
func (f *fakeForgejo) createIssue(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Title  string `json:"title"`
		Body   string `json:"body"`
		Labels []int  `json:"labels"`
	}
	json.NewDecoder(r.Body).Decode(&body)
	if body.Title == "" {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"message": "title is required"})
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.nextNum++
	iss := &fakeIssue{number: f.nextNum, title: body.Title, body: body.Body, state: "open", labels: []string{}}
	for _, id := range body.Labels {
		for _, l := range append(slices.Clone(f.trk.labels), f.trk.orgLabels...) {
			if l.id == id {
				iss.labels = append(iss.labels, l.name)
			}
		}
	}
	f.trk.issues[iss.number] = iss
	writeJSON(w, http.StatusCreated, map[string]any{
		"number": iss.number, "html_url": f.web + "/owner/repo/issues/" + strconv.Itoa(iss.number),
	})
}

func (f *fakeForgejo) createLabel(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name  string `json:"name"`
		Color string `json:"color"`
	}
	json.NewDecoder(r.Body).Decode(&body)
	f.mu.Lock()
	defer f.mu.Unlock()
	switch {
	case body.Name == "" || !fakeLabelColourRe.MatchString(body.Color):
		w.WriteHeader(http.StatusUnprocessableEntity)
	case slices.ContainsFunc(f.trk.labels, func(l fakeLabel) bool { return l.name == body.Name }):
		w.WriteHeader(http.StatusConflict)
	default:
		id := f.defineLabel(body.Name)
		writeJSON(w, http.StatusCreated, map[string]any{"id": id, "name": body.Name})
	}
}
