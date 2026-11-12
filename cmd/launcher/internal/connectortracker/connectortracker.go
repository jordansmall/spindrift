// Package connectortracker builds core's tracker reads (the forge.IssueTracker
// list and issue reads and, when advertised, forge.CommentLister and
// forge.NativeDepsReader) from a tracker Connector, over the provisional 0.y
// tracker primitives in sdk/tracker. Every write stays with the native adapter,
// which also stays authoritative for every optional capability the Connector
// does not serve: the result is a stack of forge.Unwrapper layers over the
// native adapter, so forge.As reaches those capabilities through it.
package connectortracker

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strconv"
	"strings"

	"spindrift.dev/launcher/internal/connectorhost"
	"spindrift.dev/launcher/internal/forge"
)

// ErrNotTracker marks a Connector that does not advertise the "tracker"
// capability.
var ErrNotTracker = errors.New("connector is not a tracker")

// ErrAnswer marks a Connector answer the core refuses (a bad issue number or
// state, or an issue other than the one asked for), as opposed to a protocol
// failure, which wraps connectorhost.ErrCall.
var ErrAnswer = errors.New("connector answer refused")

// Target is the repository a tracker Connector reads.
type Target struct {
	API  string `json:"api"`
	Repo string `json:"repo"`
}

// Routes returns a copy of a tracker Connector's Route table with the
// repository named in each path. A table spells the repository as "{repo}"
// because the slug is runtime config, not something a Connector's package can
// know; the result goes to connectorhost.LoadNetwork, which validates it.
func Routes(table []connectorhost.Route, repo string) []connectorhost.Route {
	out := slices.Clone(table)
	for i := range out {
		out[i].Path = strings.ReplaceAll(out[i].Path, "{repo}", repo)
	}
	return out
}

// The shapes the tracker adapter decodes from the guest. The Connector returns
// raw fields; every policy decision is made here.
type (
	wireIssue struct {
		Number string   `json:"number"`
		Title  string   `json:"title"`
		Body   string   `json:"body"`
		State  string   `json:"state"`
		Labels []string `json:"labels"`
	}
	wireComment struct {
		Author          string `json:"author"`
		CreatedAt       string `json:"createdAt"`
		Body            string `json:"body"`
		Minimized       bool   `json:"minimized"`
		MinimizedReason string `json:"minimizedReason"`
		Association     string `json:"association"`
	}
	listIssuesParams struct {
		Target Target `json:"target"`
		Label  string `json:"label"`
		Limit  int    `json:"limit"`
	}
	numberedParams struct {
		Target Target `json:"target"`
		Number string `json:"number"`
	}
)

// guestCall is the Connector.Call signature, a seam so the adapter's policy
// runs against a stub guest.
type guestCall func(ctx context.Context, method string, params, result any) error

// Capabilities a tracker Connector advertises beyond "tracker".
const (
	// CapNativeDeps: the Connector serves tracker.nativeDeps.
	CapNativeDeps = "tracker.nativeDeps"
	// CapComments: the Connector serves tracker.comments and attests that each
	// comment's minimized and minimizedReason are faithful. forge.IssueText
	// drops minimized comments on that flag, and some trackers (GitHub REST)
	// cannot report it, so core trusts no Connector's comments without it.
	CapComments = "tracker.comments"
)

// reads serves the list and issue reads from a Connector and leaves every write
// to the embedded native adapter. It is the base layer; the optional layers
// stack on it.
type reads struct {
	forge.IssueTracker
	call   guestCall
	labels forge.DispatchLabels
	target Target
}

// Unwrap implements forge.Unwrapper, so forge.As finds the native adapter's own
// optional capabilities behind this layer.
func (t *reads) Unwrap() forge.IssueTracker { return t.IssueTracker }

// WalksAllPages implements forge.FullyPaginated. It shadows the native
// adapter's answer, which describes the native list, not the Connector's: one
// page capped at forge.ResultPageLimit.
func (t *reads) WalksAllPages() bool { return false }

// StateLabels implements forge.LabeledTracker. It shadows the native adapter's
// labels with the family the Connector's lists are resolved through.
func (t *reads) StateLabels() forge.DispatchLabels { return t.labels }

// commentsLayer serves forge.CommentLister from the Connector. It exists only
// when the Connector advertises CapComments; otherwise the lookup falls through
// to the native adapter's lister, whose minimized handling core already trusts.
type commentsLayer struct {
	forge.IssueTracker
	r *reads
}

func (l commentsLayer) Unwrap() forge.IssueTracker { return l.IssueTracker }

// Comments returns the comments oldest first, as the Connector reports them.
func (l commentsLayer) Comments(num string) ([]forge.Comment, error) {
	var res struct {
		Comments []wireComment `json:"comments"`
	}
	if err := l.r.call(context.Background(), CapComments, numberedParams{l.r.target, num}, &res); err != nil {
		return nil, err
	}
	comments := make([]forge.Comment, len(res.Comments))
	for i, c := range res.Comments {
		comments[i] = forge.Comment(c)
	}
	return comments, nil
}

// nativeDepsLayer serves forge.NativeDepsReader from the Connector. It exists
// only when the Connector advertises CapNativeDeps; otherwise the lookup falls
// through to the native adapter's reader, if it has one.
type nativeDepsLayer struct {
	forge.IssueTracker
	r *reads
}

func (l nativeDepsLayer) Unwrap() forge.IssueTracker { return l.IssueTracker }

// NativeDepsOf returns the ids num blocks on, deduplicated in the guest's order.
// An id that is not a positive decimal integer fails closed, as in issue().
func (l nativeDepsLayer) NativeDepsOf(num string) ([]string, error) {
	var res struct {
		IDs []string `json:"ids"`
	}
	if err := l.r.call(context.Background(), CapNativeDeps, numberedParams{l.r.target, num}, &res); err != nil {
		return nil, err
	}
	var deps []string
	seen := map[string]bool{}
	for _, id := range res.IDs {
		if !isIssueNumber(id) {
			return nil, fmt.Errorf("%w: native dependency id %q is not a positive integer", ErrAnswer, id)
		}
		if !seen[id] {
			seen[id] = true
			deps = append(deps, id)
		}
	}
	return deps, nil
}

var (
	_ forge.Unwrapper        = (*reads)(nil)
	_ forge.FullyPaginated   = (*reads)(nil)
	_ forge.LabeledTracker   = (*reads)(nil)
	_ forge.Unwrapper        = commentsLayer{}
	_ forge.CommentLister    = commentsLayer{}
	_ forge.Unwrapper        = nativeDepsLayer{}
	_ forge.NativeDepsReader = nativeDepsLayer{}
)

// New builds core's tracker reads from a tracker Connector. writes keeps
// every write (transitions, verdicts, comments, labels, probes), which never
// cross the protocol, and answers every optional capability the Connector does
// not serve (see CapComments and CapNativeDeps). Look capabilities up with
// forge.As. Calls run under context.Background() because IssueTracker methods
// carry no context.
func New(c *connectorhost.Connector, writes forge.IssueTracker, labels forge.DispatchLabels, target Target) (forge.IssueTracker, error) {
	if !slices.Contains(c.Capabilities, "tracker") {
		return nil, fmt.Errorf("%w: capabilities %v lack \"tracker\"", ErrNotTracker, c.Capabilities)
	}
	return newTracker(c.Call, writes, labels, target, c.Capabilities), nil
}

func newTracker(call guestCall, writes forge.IssueTracker, labels forge.DispatchLabels, target Target, caps []string) forge.IssueTracker {
	r := &reads{IssueTracker: writes, call: call, labels: labels, target: target}
	var it forge.IssueTracker = r
	if slices.Contains(caps, CapComments) {
		it = commentsLayer{it, r}
	}
	if slices.Contains(caps, CapNativeDeps) {
		it = nativeDepsLayer{it, r}
	}
	return it
}

func (t *reads) ListIssues(state forge.DispatchState) ([]forge.Issue, error) {
	return t.list(t.labels.Label(state))
}

func (t *reads) ListOpenIssues() ([]forge.Issue, error) {
	return t.list("")
}

func (t *reads) list(label string) ([]forge.Issue, error) {
	var res struct {
		Issues []wireIssue `json:"issues"`
	}
	p := listIssuesParams{Target: t.target, Label: label, Limit: forge.ResultPageLimit}
	if err := t.call(context.Background(), "tracker.listIssues", p, &res); err != nil {
		return nil, err
	}
	issues := make([]forge.Issue, len(res.Issues))
	for i, w := range res.Issues {
		var err error
		if issues[i], err = w.issue(); err != nil {
			return nil, err
		}
	}
	// Numbers were validated as decimal integers by issue().
	sort.Slice(issues, func(i, j int) bool {
		ni, _ := strconv.Atoi(issues[i].Number)
		nj, _ := strconv.Atoi(issues[j].Number)
		return ni < nj
	})
	// The guest is asked for Limit but is not trusted to honour it.
	issues = issues[:min(len(issues), forge.ResultPageLimit)]
	forge.WarnPageMayTruncateBacklog("tracker connector list", len(issues))
	return issues, nil
}

func (t *reads) Issue(num string) (forge.Issue, error) {
	var w wireIssue
	if err := t.call(context.Background(), "tracker.issue", numberedParams{t.target, num}, &w); err != nil {
		return forge.Issue{}, err
	}
	got, err := w.issue()
	if err != nil {
		return forge.Issue{}, err
	}
	if got.Number != num {
		return forge.Issue{}, fmt.Errorf("%w: asked for issue %s, got %s", ErrAnswer, num, got.Number)
	}
	return got, nil
}

// isIssueNumber reports whether s is a positive decimal integer in canonical
// form (no sign, no leading zeros).
func isIssueNumber(s string) bool {
	n, err := strconv.Atoi(s)
	return err == nil && n >= 1 && strconv.Itoa(n) == s
}

// issue converts a wire issue, failing closed on a state it does not know or a
// number that is not a positive decimal integer (numbers become branch names).
func (w wireIssue) issue() (forge.Issue, error) {
	if !isIssueNumber(w.Number) {
		return forge.Issue{}, fmt.Errorf("%w: issue number %q is not a positive integer", ErrAnswer, w.Number)
	}
	var state forge.IssueState
	switch w.State {
	case "open":
		state = forge.IssueOpen
	case "closed":
		state = forge.IssueClosed
	default:
		return forge.Issue{}, fmt.Errorf("%w: issue %s has unknown state %q", ErrAnswer, w.Number, w.State)
	}
	return forge.Issue{
		Number:   w.Number,
		Title:    w.Title,
		Body:     w.Body,
		State:    state,
		Labels:   w.Labels,
		Priority: forge.ResolvePriority(w.Labels),
	}, nil
}
