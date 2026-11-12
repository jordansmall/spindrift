// Package connectortracker builds core's tracker reads (the forge.IssueTracker
// list and issue reads and, when advertised, forge.CommentLister,
// forge.NativeDepsReader and forge.BlockersLister) from a tracker Connector,
// over the 0.2 tracker primitives in sdk/tracker. Every write stays with the
// native adapter, which also stays authoritative for every optional capability
// the Connector does not serve: the result is a stack of forge.Unwrapper layers
// over the native adapter, so forge.As reaches those capabilities through it.
package connectortracker

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"spindrift.dev/launcher/internal/connectorhost"
	"spindrift.dev/launcher/internal/dedupmarker"
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
	// Dialect names the tracker deployment's API dialect, "" for a tracker with
	// one. It is core config, so a Connector never guesses a deployment.
	Dialect string `json:"dialect"`
}

// Lifecycle resolves a Dispatch state to the selectors a Connector lists by.
// A state may have a label, a native status, both, or neither.
type Lifecycle struct {
	Labels   forge.DispatchLabels
	Statuses map[forge.DispatchState]string
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

		// ClosedAt is RFC3339, "" when the issue is open or the date is unknown.
		// Core ignores it and ClosedNotPlanned for an open issue.
		ClosedAt         string `json:"closedAt"`
		ClosedNotPlanned bool   `json:"closedNotPlanned"`
		// DedupMarker is the raw dedup marker line from the tracker's side store,
		// "" when there is none. It is never part of Body.
		DedupMarker string `json:"dedupMarker"`
	}
	wireComment struct {
		Author          string `json:"author"`
		CreatedAt       string `json:"createdAt"`
		Body            string `json:"body"`
		Minimized       bool   `json:"minimized"`
		MinimizedReason string `json:"minimizedReason"`
		Association     string `json:"association"`
		// ResearchMarker reports that the comment carries the research marker in
		// a side store; Body excludes it.
		ResearchMarker bool `json:"researchMarker"`
	}
	listIssuesParams struct {
		Target Target `json:"target"`
		Label  string `json:"label"`
		Status string `json:"status"`
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
	// CapBlocks: the Connector serves tracker.blocks.
	CapBlocks = "tracker.blocks"
	// CapComments: the Connector serves tracker.comments and attests that each
	// comment's minimized and minimizedReason are faithful. forge.IssueText
	// drops minimized comments on that flag, and some trackers (GitHub REST)
	// cannot report it, so core trusts no Connector's comments without it.
	CapComments = "tracker.comments"
	// CapProjectKeyIDs: every id the Connector returns is a project key and a
	// number ("ENG-12"). Absent, ids are decimal integers.
	CapProjectKeyIDs = "tracker.ids.projectKey"
)

// projectKeyID is the projectKey grammar: ids become branch names, so the
// shape is closed rather than sanitised.
var projectKeyID = regexp.MustCompile(`^[A-Z][A-Z0-9_]*-[1-9][0-9]*$`)

// reads serves the list and issue reads from a Connector and leaves every write
// to the embedded native adapter. It is the base layer; the optional layers
// stack on it.
type reads struct {
	forge.IssueTracker
	call      guestCall
	lifecycle Lifecycle
	target    Target
	ids       idGrammar
	// v02 is false for a 0.1 Connector, which has no status selector.
	v02 bool
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
func (t *reads) StateLabels() forge.DispatchLabels { return t.lifecycle.Labels }

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
		comments[i] = forge.Comment{
			Author: c.Author, CreatedAt: c.CreatedAt, Body: c.Body,
			Minimized: c.Minimized, MinimizedReason: c.MinimizedReason, Association: c.Association,
		}
		if c.ResearchMarker {
			comments[i].Body = forge.ResearchMarker + "\n" + c.Body
		}
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
func (l nativeDepsLayer) NativeDepsOf(num string) ([]string, error) {
	return l.r.idList(CapNativeDeps, num)
}

// blocksLayer serves forge.BlockersLister from the Connector. It exists only
// when the Connector advertises CapBlocks; otherwise the lookup falls through
// to the native adapter's lister, if it has one.
type blocksLayer struct {
	forge.IssueTracker
	r *reads
}

func (l blocksLayer) Unwrap() forge.IssueTracker { return l.IssueTracker }

// BlocksOf returns the issues num blocks, deduplicated in the guest's order.
func (l blocksLayer) BlocksOf(num string) ([]forge.Dependency, error) {
	ids, err := l.r.idList(CapBlocks, num)
	if err != nil {
		return nil, err
	}
	return forge.WithSource(ids, forge.DepSourceNative), nil
}

// idList calls an {ids} method for num. An id outside the Connector's id
// grammar fails closed, as in issue().
func (t *reads) idList(method, num string) ([]string, error) {
	var res struct {
		IDs []string `json:"ids"`
	}
	if err := t.call(context.Background(), method, numberedParams{t.target, num}, &res); err != nil {
		return nil, err
	}
	var ids []string
	seen := map[string]bool{}
	for _, id := range res.IDs {
		if !t.ids.valid(id) {
			return nil, fmt.Errorf("%w: %s id %q is not %s", ErrAnswer, method, id, t.ids.name)
		}
		if !seen[id] {
			seen[id] = true
			ids = append(ids, id)
		}
	}
	return ids, nil
}

var (
	_ forge.Unwrapper        = (*reads)(nil)
	_ forge.FullyPaginated   = (*reads)(nil)
	_ forge.LabeledTracker   = (*reads)(nil)
	_ forge.Unwrapper        = commentsLayer{}
	_ forge.CommentLister    = commentsLayer{}
	_ forge.Unwrapper        = nativeDepsLayer{}
	_ forge.NativeDepsReader = nativeDepsLayer{}
	_ forge.Unwrapper        = blocksLayer{}
	_ forge.BlockersLister   = blocksLayer{}
)

// New builds core's tracker reads from a tracker Connector. writes keeps
// every write (transitions, verdicts, comments, labels, probes), which never
// cross the protocol, and answers every optional capability the Connector does
// not serve (see CapComments, CapNativeDeps and CapBlocks). Look capabilities
// up with forge.As. Calls run under context.Background() because IssueTracker
// methods carry no context.
//
// A 0.1 Connector predates the status selector, tracker.blocks and project-key
// ids, so core ignores those capabilities for it and never sends a status.
func New(c *connectorhost.Connector, writes forge.IssueTracker, lifecycle Lifecycle, target Target) (forge.IssueTracker, error) {
	if !slices.Contains(c.Capabilities, "tracker") {
		return nil, fmt.Errorf("%w: capabilities %v lack \"tracker\"", ErrNotTracker, c.Capabilities)
	}
	return newTracker(c.Call, writes, lifecycle, target, c.Capabilities, speaksV02(c.ProtocolVersion)), nil
}

// speaksV02 reports whether a protocol version is 0.2 or later; one the
// handshake would refuse reports false.
func speaksV02(version string) bool {
	minor, ok := connectorhost.ProtocolMinor(version)
	return ok && minor >= 2
}

func newTracker(call guestCall, writes forge.IssueTracker, lifecycle Lifecycle, target Target, caps []string, v02 bool) forge.IssueTracker {
	if !v02 {
		caps = slices.DeleteFunc(slices.Clone(caps), func(c string) bool {
			return c == CapBlocks || c == CapProjectKeyIDs
		})
	}
	ids := decimalIDs
	if slices.Contains(caps, CapProjectKeyIDs) {
		ids = projectKeyIDs
	}
	r := &reads{IssueTracker: writes, call: call, lifecycle: lifecycle, target: target, ids: ids, v02: v02}
	var it forge.IssueTracker = r
	if slices.Contains(caps, CapComments) {
		it = commentsLayer{it, r}
	}
	if slices.Contains(caps, CapNativeDeps) {
		it = nativeDepsLayer{it, r}
	}
	if slices.Contains(caps, CapBlocks) {
		it = blocksLayer{it, r}
	}
	return it
}

func (t *reads) ListIssues(state forge.DispatchState) ([]forge.Issue, error) {
	label, status := t.lifecycle.Labels.Label(state), t.lifecycle.Statuses[state]
	if !t.v02 && status != "" {
		if label == "" {
			// A 0.1 guest would ignore the status and list every open issue.
			return nil, fmt.Errorf("%w: a 0.1 connector cannot list by status %q", connectorhost.ErrProtocolUnsupported, status)
		}
		status = ""
	}
	if label == "" && status == "" {
		// Both empty means every open issue to the Connector; a state with no
		// selector is empty, as in native jira's stateJQL.
		return nil, nil
	}
	return t.list(label, status)
}

func (t *reads) ListOpenIssues() ([]forge.Issue, error) {
	return t.list("", "")
}

func (t *reads) list(label, status string) ([]forge.Issue, error) {
	var res struct {
		Issues []wireIssue `json:"issues"`
	}
	p := listIssuesParams{Target: t.target, Label: label, Status: status, Limit: forge.ResultPageLimit}
	if err := t.call(context.Background(), "tracker.listIssues", p, &res); err != nil {
		return nil, err
	}
	issues := make([]forge.Issue, len(res.Issues))
	for i, w := range res.Issues {
		var err error
		if issues[i], err = w.issue(t.ids); err != nil {
			return nil, err
		}
	}
	// Numbers were validated by issue().
	sort.Slice(issues, func(i, j int) bool { return t.ids.less(issues[i].Number, issues[j].Number) })
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
	got, err := w.issue(t.ids)
	if err != nil {
		return forge.Issue{}, err
	}
	if got.Number != num {
		return forge.Issue{}, fmt.Errorf("%w: asked for issue %s, got %s", ErrAnswer, num, got.Number)
	}
	return got, nil
}

// idGrammar is the shape of every id a Connector returns. Ids become branch
// names, so core checks them all against one grammar chosen from the
// Connector's capabilities.
type idGrammar struct {
	name  string
	valid func(string) bool
	// less is a stable id order standing in for creation order: decimal ids
	// numerically; project keys by project, then number. A key's number
	// reflects when it was keyed, so a moved issue sorts away from where
	// native jira's "order by created" puts it.
	less func(a, b string) bool
}

var decimalIDs = idGrammar{
	name: "a positive integer",
	valid: func(s string) bool {
		n, err := strconv.Atoi(s)
		return err == nil && n >= 1 && strconv.Itoa(n) == s
	},
	less: func(a, b string) bool {
		na, _ := strconv.Atoi(a)
		nb, _ := strconv.Atoi(b)
		return na < nb
	},
}

var projectKeyIDs = idGrammar{
	name:  "a project key and number (ENG-12)",
	valid: projectKeyID.MatchString,
	less: func(a, b string) bool {
		pa, na := splitKey(a)
		pb, nb := splitKey(b)
		if pa != pb {
			return pa < pb
		}
		return na < nb
	},
}

// splitKey splits a valid project-key id into its prefix and number.
func splitKey(id string) (string, int) {
	i := strings.LastIndexByte(id, '-')
	n, _ := strconv.Atoi(id[i+1:])
	return id[:i], n
}

// issue converts a wire issue, failing closed on a state it does not know or a
// number outside the id grammar (numbers become branch names).
func (w wireIssue) issue(g idGrammar) (forge.Issue, error) {
	if !g.valid(w.Number) {
		return forge.Issue{}, fmt.Errorf("%w: issue number %q is not %s", ErrAnswer, w.Number, g.name)
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
	iss := forge.Issue{
		Number:   w.Number,
		Title:    w.Title,
		Body:     dedupmarker.Append(w.Body, w.DedupMarker),
		State:    state,
		Labels:   w.Labels,
		Priority: forge.ResolvePriority(w.Labels),
	}
	if state == forge.IssueClosed {
		// A malformed date leaves ClosedAt zero rather than failing the read.
		iss.ClosedAt, _ = time.Parse(time.RFC3339, w.ClosedAt)
		iss.ClosedNotPlanned = w.ClosedNotPlanned
	}
	return iss, nil
}
