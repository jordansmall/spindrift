package main

import (
	"embed"
	"errors"
	"fmt"
	"html/template"
	"io/fs"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

//go:embed web
var webFS embed.FS

type server struct {
	checkout   string // the Daemon's work-tree root, which Child log paths are relative to
	statusPath string
	eventsPath string
	now        func() time.Time
	alive      func(pid int, host string) bool
	tmpl       *template.Template
	static     http.Handler
	poll       time.Duration // how often /events looks for file changes
}

func newServer(checkout, statusPath string) *server {
	staticFS, err := fs.Sub(webFS, "web/static")
	if err != nil {
		panic(err)
	}
	return &server{
		checkout:   checkout,
		statusPath: statusPath,
		eventsPath: filepath.Join(filepath.Dir(statusPath), eventsFileName),
		now:        time.Now,
		alive:      processAlive,
		tmpl: template.Must(template.New("index.html.tmpl").
			Funcs(template.FuncMap{"historyLimit": func() int { return historyLimit }}).
			ParseFS(webFS, "web/index.html.tmpl", "web/dispatch.html.tmpl")),
		poll:   defaultPoll,
		static: http.StripPrefix("/static/", http.FileServerFS(staticFS)),
	}
}

func (s *server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	switch {
	case r.URL.Path == "/":
		s.page(w)
	case r.URL.Path == "/dispatch":
		s.dispatch(w, r)
	case r.URL.Path == "/events":
		s.events(w, r)
	case r.URL.Path == "/log":
		s.serveLog(w, r)
	case strings.HasPrefix(r.URL.Path, "/static/"):
		s.static.ServeHTTP(w, r)
	default:
		http.NotFound(w, r)
	}
}

func (s *server) page(w http.ResponseWriter) {
	st, raw, err := readStatus(s.statusPath)
	v := s.viewFrom(st, raw, err)
	v.History, v.HistoryErr = readHistory(s.eventsPath)
	linkEntries(v.History, repoURLOf(st))
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	if err := s.tmpl.Execute(w, v); err != nil {
		// Headers are gone; all that is left is to cut the page short.
		writeRenderError(w, err)
	}
}

type viewMode int

const (
	// modeNone: no live Daemon. State, Reason and Written then carry its last
	// record, if any.
	modeNone  viewMode = iota
	modeError          // unreadable status file
	modeSkew           // status file at a schema this Dashboard does not understand
	modeLive
)

// revisionLen is how much of a commit hash a slot card shows.
const revisionLen = 12

type view struct {
	mode viewMode
	Note string // why the mode is none
	Err  string
	Raw  string

	State    string
	Reason   string // shown live and, as the last record, for a dead pid
	Halted   bool
	Host     string
	Pid      int
	Uptime   string
	Written  string
	Busy     int
	Total    int
	Kinds    []kindView
	Trackers []TrackerCheck
	Slots    []slotView

	History    []historyEntry // newest first; shown in every mode
	HistoryErr error          // an Events generation that exists but could not be read
}

// The template branches on these rather than on mode literals.
func (v view) Live() bool    { return v.mode == modeLive }
func (v view) Errored() bool { return v.mode == modeError }
func (v view) None() bool    { return v.mode == modeNone }
func (v view) Skewed() bool  { return v.mode == modeSkew }

type kindView struct {
	Kind        string
	NextCheck   string
	Jammed      bool
	JamUntil    string
	ReadyAtJam  string
	Ready       string
	ProbedAt    string
	NextProbe   string
	NextDue     string
	OnTipMove   bool
	AlsoTipMove bool
}

type slotView struct {
	Slot        int
	Phase       string
	Busy        bool
	Kind        string
	Subject     []subject
	Rev         string
	Elapsed     string
	ChildStart  string
	ChildStartN int
}

// viewFrom builds the view from one read of the status file, which a stream
// can run on its own cached read.
func (s *server) viewFrom(st *Status, raw []byte, err error) view {
	var skew *schemaError
	switch {
	// Before the generic error case: a skew is an error too, but gets its own banner.
	case errors.As(err, &skew):
		return view{mode: modeSkew, Err: skew.Error(), Raw: string(raw)}
	case err != nil:
		return view{mode: modeError, Err: err.Error(), Raw: string(raw)}
	case st == nil:
		return view{mode: modeNone}
	case !s.alive(st.Pid, st.Host):
		// The Daemon publishes its last state, a halt included, then exits and
		// leaves the file as a record of how the run ended, so show it.
		return view{mode: modeNone, Note: fmt.Sprintf(
			"pid %d on %s is no longer running; the last state it published is below.", st.Pid, st.Host),
			State: st.State, Reason: st.Reason, Halted: st.halted(), Written: st.Time}
	}

	now := s.now()
	v := view{
		mode:     modeLive,
		State:    st.State,
		Reason:   st.Reason,
		Halted:   st.halted(),
		Host:     st.Host,
		Pid:      st.Pid,
		Written:  st.Time,
		Total:    len(st.Slots),
		Trackers: st.Trackers,
	}
	if started, err := time.Parse(time.RFC3339, st.Started); err == nil {
		d := now.Sub(started)
		v.Uptime = formatDuration(d)
	} else {
		v.Uptime = "unknown"
	}
	for _, c := range st.Checks {
		k := kindView{
			Kind:       c.Kind,
			NextCheck:  c.NextCheck,
			Jammed:     c.Jammed,
			JamUntil:   c.JamUntil,
			ReadyAtJam: optInt(c.ReadyAtJam),
			Ready:      optInt(c.Ready),
			ProbedAt:   c.ProbedAt,
			NextProbe:  c.NextProbe,
			NextDue:    c.NextDue,
			OnTipMove:  c.NextDue == nextDueOnTipMove,
		}
		k.AlsoTipMove = c.NextDueOnTipMove && !k.OnTipMove
		v.Kinds = append(v.Kinds, k)
	}
	repo := repoURLOf(st)
	for _, sl := range st.Slots {
		if sl.Busy {
			v.Busy++
		}
		sv := slotView{Slot: sl.Slot, Phase: sl.Phase, Busy: sl.Busy, Kind: sl.Kind, Rev: shortRev(sl.Revision), Elapsed: "unknown", ChildStart: sl.ChildStart, ChildStartN: sl.ChildStartN}
		for _, is := range sl.Issues {
			sv.Subject = append(sv.Subject, issueSubject(is).linked(repo))
		}
		if sl.Chore != "" {
			sv.Subject = append(sv.Subject, subject{Label: sl.Chore})
		}
		if since, err := time.Parse(time.RFC3339, sl.Since); err == nil {
			d := now.Sub(since)
			sv.Elapsed = formatDuration(d)
		}
		v.Slots = append(v.Slots, sv)
	}
	return v
}

func optInt(p *int) string {
	if p == nil {
		return ""
	}
	return strconv.Itoa(*p)
}

func isIssueNumber(s string) bool {
	return s != "" && strings.Trim(s, "0123456789") == ""
}

// subject is what a Dispatch works on, shown as a chip. URL is set only for an
// issue the Target repo's web URL can reach; a Chore, whose Ledger lives in
// git, and a non-numeric tracker key stay plain.
type subject struct {
	Label string
	URL   string
	issue string // the issue number a URL is built from; empty for a Chore
}

func issueSubject(key string) subject {
	s := subject{Label: key}
	if isIssueNumber(key) {
		s.Label = "#" + key
		s.issue = key
	}
	return s
}

// subjectOf is an event's subject: its issue when it names one, else its Chore.
func subjectOf(issue, chore string) subject {
	if issue != "" {
		return issueSubject(issue)
	}
	return subject{Label: chore}
}

// linked returns s with its issue URL set; a Chore subject or a status that
// publishes no repo URL stays unlinked rather than getting a broken href.
func (s subject) linked(repoURL string) subject {
	if s.issue != "" && repoURL != "" {
		s.URL = strings.TrimRight(repoURL, "/") + "/issues/" + s.issue
	}
	return s
}

// repoURLOf is the web URL a status publishes, empty when there is none.
func repoURLOf(st *Status) string {
	if st == nil {
		return ""
	}
	return st.RepoURL
}

func seconds(d time.Duration) int64 {
	if d < 0 {
		return 0
	}
	return int64(d / time.Second)
}

// formatDuration renders d as "2d 2h 1m", "2h 0m 0s", "12m 3s" or "5s"; the
// largest unit drops a trailing seconds field once days appear.
func formatDuration(d time.Duration) string {
	n := seconds(d)
	days, h, m, sec := n/86400, n%86400/3600, n%3600/60, n%60
	switch {
	case days > 0:
		return fmt.Sprintf("%dd %dh %dm", days, h, m)
	case h > 0:
		return fmt.Sprintf("%dh %dm %ds", h, m, sec)
	case m > 0:
		return fmt.Sprintf("%dm %ds", m, sec)
	}
	return fmt.Sprintf("%ds", sec)
}
