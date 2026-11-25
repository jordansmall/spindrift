// Package connectortracker builds core's forge.IssueTracker, and each optional
// read capability the Connector advertises, from a tracker Connector over the
// sdk/tracker primitives. A Connector advertising transition and label-edit
// performs the writes while core keeps every lifecycle decision (claim checks,
// the settled-label strip, fallback and refusal, verdicts). The result is a
// stack of forge.Unwrapper layers, one per advertised capability: a core method
// the Connector does not serve fails with connectorhost.ErrProtocolUnsupported,
// and an optional capability it omits is absent from forge.As.
package connectortracker

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"

	"spindrift.dev/launcher/internal/connectorcall"
	"spindrift.dev/launcher/internal/connectorhost"
	"spindrift.dev/launcher/internal/dedupmarker"
	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/retry"
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
// A state may have a label, a native status, both, or neither. Statuses and
// IssueType feed doctor's check capabilities; IssueType is also what
// tracker.fileIssue files under.
type Lifecycle struct {
	Labels   forge.DispatchLabels
	Statuses map[forge.DispatchState]string
	// IssueType is the issue type core files issues as (JIRA_ISSUE_TYPE), which
	// the createMeta check measures the project against.
	IssueType string
	// WebHost is the tracker's web host (host[:port]) a filed issue's URL must
	// be on; GitHub's differs from its API host. Empty refuses every filed URL.
	WebHost string
	// VerdictLabels names the label each research verdict lands as.
	VerdictLabels forge.VerdictLabels
	// Resolutions is the resolution a transition into a mapped state sets; a
	// state absent leaves it to the tracker's workflow default.
	Resolutions map[forge.DispatchState]string
	// StatusOnly is the status-only lifecycle: an unavailable or unmapped
	// transition is refused instead of swapped for a label, and the claim check
	// reads the native status alone.
	StatusOnly bool
	// Route narrows the queue lists, the demand count and the labeled backlog
	// read, and is stamped on filed issues; nil is the whole scope.
	Route forge.Route
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
		// Status is the tracker's workflow status name, "" for a tracker
		// without one.
		Status string `json:"status"`
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
		Target Target     `json:"target"`
		Label  string     `json:"label"`
		Status string     `json:"status"`
		Limit  int        `json:"limit"`
		Route  []Selector `json:"route,omitempty"`
	}
	labeledParams struct {
		Target Target     `json:"target"`
		Label  string     `json:"label"`
		State  string     `json:"state"`
		Limit  int        `json:"limit"`
		Cursor string     `json:"cursor,omitempty"`
		Route  []Selector `json:"route,omitempty"`
	}
	searchIssuesParams struct {
		Target Target `json:"target"`
		Text   string `json:"text"`
		State  string `json:"state"`
		Label  string `json:"label"`
		Limit  int    `json:"limit"`
	}
	numberedParams struct {
		Target Target `json:"target"`
		Number string `json:"number"`
	}
	targetParams struct {
		Target Target `json:"target"`
	}
	editLabelsParams struct {
		Target Target   `json:"target"`
		Number string   `json:"number"`
		Add    []string `json:"add"`
		Remove []string `json:"remove"`
	}
	createLabelParams struct {
		Target      Target `json:"target"`
		Name        string `json:"name"`
		Description string `json:"description"`
		Color       string `json:"color"`
	}
	StatusTarget struct {
		Status     string `json:"status"`
		Resolution string `json:"resolution"`
	}
	transitionRequest struct {
		State   string                  `json:"state"`
		Mapping map[string]StatusTarget `json:"mapping"`
	}
	transitionResult struct {
		Outcome string   `json:"outcome"`
		Status  string   `json:"status"`
		Offered []string `json:"offered"`
	}
	transitionParams struct {
		Target Target `json:"target"`
		Number string `json:"number"`
		transitionRequest
	}
	commentRequest struct {
		Body     string `json:"body"`
		Research bool   `json:"research"`
	}
	commentParams struct {
		Target Target `json:"target"`
		Number string `json:"number"`
		commentRequest
	}
	// Selector mirrors the sdk's Selector: one Issue route term.
	Selector struct {
		Kind  string `json:"kind"`
		Value string `json:"value"`
	}
	issueRequest struct {
		Title       string     `json:"title"`
		Body        string     `json:"body"`
		Labels      []string   `json:"labels"`
		DedupMarker string     `json:"dedupMarker"`
		IssueType   string     `json:"issueType"`
		Route       []Selector `json:"route,omitempty"`
	}
	fileIssueParams struct {
		Target Target `json:"target"`
		issueRequest
	}
	closeIssueParams struct {
		Target Target `json:"target"`
		Number string `json:"number"`
	}
	createMetaParams struct {
		Target    Target `json:"target"`
		IssueType string `json:"issueType"`
	}
	matchesRouteParams struct {
		Target Target     `json:"target"`
		Number string     `json:"number"`
		Route  []Selector `json:"route"`
	}
	countParams struct {
		Target Target     `json:"target"`
		Label  string     `json:"label"`
		Status string     `json:"status"`
		Limit  int        `json:"limit"`
		Fresh  bool       `json:"fresh"`
		Route  []Selector `json:"route,omitempty"`
	}
	wireCreateMeta struct {
		IssueTypes []string          `json:"issueTypes"`
		Fields     []wireCreateField `json:"fields"`
	}
	wireCreateField struct {
		ID              string `json:"fieldId"`
		Name            string `json:"name"`
		Required        bool   `json:"required"`
		HasDefaultValue bool   `json:"hasDefaultValue"`
	}
)

// guestCall is the Connector.Call signature, a seam so the adapter's policy
// runs against a stub guest.
type guestCall = connectorcall.Call

// Capabilities a tracker Connector advertises beyond "tracker".
const (
	CapNativeDeps = "tracker.nativeDeps"
	CapBlocks     = "tracker.blocks"
	// CapComments: the Connector serves tracker.comments and attests that each
	// comment's minimized and minimizedReason are faithful. forge.IssueText
	// drops minimized comments on that flag, and some trackers (GitHub REST)
	// cannot report it, so core trusts no Connector's comments without it.
	CapComments = "tracker.comments"
	// CapProjectKeyIDs: every id the Connector returns is a project key and a
	// number ("ENG-12"). Absent, ids are decimal integers.
	CapProjectKeyIDs = "tracker.ids.projectKey"
	// CapRoute: the Connector ANDs a route into every routed read and stamps it
	// on the issue it files. Absent, core refuses a route unsent, since a
	// Connector without it would drop the field and answer the whole scope.
	// It also serves tracker.matchesRoute, the one-issue form of the route.
	CapRoute = "tracker.route"
	// CapProbe: the Connector serves tracker.probe, one authenticated read whose
	// HTTP status core maps to a Probe result.
	CapProbe = "tracker.probe"
	// CapStatuses: the Connector serves tracker.statuses, the project's workflow
	// status names and, when the tracker reports them, each one's category.
	CapStatuses = "tracker.statuses"
	// CapCreateMeta: the Connector serves tracker.createMeta, the project's issue
	// types and the create fields of one.
	CapCreateMeta = "tracker.createMeta"
	// CapCount: the Connector serves tracker.count and tracker.countInterval,
	// the ready-demand count and its probe cadence.
	CapCount = "tracker.count"
	// CapCountInterval is the method that serves the probe cadence; it rides on
	// CapCount rather than being advertised itself.
	CapCountInterval = "tracker.countInterval"
	// CapRemovedLabels: the Connector serves tracker.removedLabels, the label
	// names removed from an issue, oldest first. Core maps them to a lifecycle
	// state for recover; the Connector knows nothing of that.
	CapRemovedLabels = "tracker.removedLabels"
	CapSearchIssues  = "tracker.searchIssues"
	// CapLabeled: the Connector serves tracker.labeled, the open or closed
	// issues carrying one label, newest first, with the close date and
	// not-planned flag on closed ones. Core's finding dedup scan reads it.
	CapLabeled = "tracker.labeled"
	// CapTransition: the Connector serves tracker.transition. With CapEditLabels
	// it performs core's transitions and verdicts.
	CapTransition = "tracker.transition"
	CapEditLabels = "tracker.editLabels"
	// CapLabels: the Connector serves tracker.listLabels and tracker.createLabel,
	// its label registry.
	CapLabels = "tracker.labels"
	// CapListLabels and CapCreateLabel are the methods behind CapLabels.
	CapListLabels  = "tracker.listLabels"
	CapCreateLabel = "tracker.createLabel"
	// CapComment: the Connector serves tracker.comment, posting canonical
	// Markdown and storing the research marker as the tracker can.
	CapComment = "tracker.comment"
	// CapFileIssue: the Connector serves tracker.fileIssue, filing a host
	// finding and answering its browse URL.
	CapFileIssue = "tracker.fileIssue"
	// CapCloseIssue: the Connector serves tracker.closeIssue, closing an open
	// issue as completed.
	CapCloseIssue = "tracker.closeIssue"
)

// The outcomes of tracker.transition that are not an error.
const (
	transitionMoved       = "moved"
	transitionUnavailable = "unavailable"
	transitionUnmapped    = "unmapped"
)

// projectKeyID is the projectKey grammar: ids become branch names, so the
// shape is closed rather than sanitised.
var projectKeyID = regexp.MustCompile(`^[A-Z][A-Z0-9_]*-[1-9][0-9]*$`)

// reads serves the list and issue reads from a Connector. It is the base
// layer; the optional layers stack on it. The core methods no read serves fail
// as unadvertised until a layer for their capability overrides them.
type reads struct {
	call      guestCall
	lifecycle Lifecycle
	target    Target
	ids       idGrammar
	// v02 is false for a 0.1 Connector, which has no status selector.
	v02 bool
	// routes records CapRoute from the handshake.
	routes bool
	// route is lifecycle.Route in wire form; omitempty keeps an unrouted
	// payload unchanged.
	route []Selector
}

// routeParam is the route to send, refused for a Connector not advertising
// CapRoute, which would drop the unknown field and answer the whole scope.
func (t *reads) routeParam() ([]Selector, error) {
	if len(t.route) > 0 && !t.routes {
		return nil, fmt.Errorf("%w: connector does not advertise %q; it cannot narrow by issue route", connectorhost.ErrProtocolUnsupported, CapRoute)
	}
	return t.route, nil
}

// MatchesRoute implements forge.RouteMatcher. An empty route matches every
// issue, so it asks the Connector nothing.
func (t *reads) MatchesRoute(num string) (bool, error) {
	if len(t.route) == 0 {
		return true, nil
	}
	route, err := t.routeParam()
	if err != nil {
		return false, err
	}
	var res struct {
		Matches bool `json:"matches"`
	}
	if err := t.call(context.Background(), "tracker.matchesRoute", matchesRouteParams{Target: t.target, Number: num, Route: route}, &res); err != nil {
		return false, err
	}
	return res.Matches, nil
}

// unadvertised is the error a core method returns when the Connector does not
// advertise caps.
func unadvertised(caps ...string) error {
	return fmt.Errorf("%w: the method needs the connector to advertise \"%s\"", connectorhost.ErrProtocolUnsupported, strings.Join(caps, "\" and \""))
}

func (*reads) TransitionState(string, forge.DispatchState, forge.DispatchState) error {
	return unadvertised(CapTransition, CapEditLabels)
}
func (*reads) CompleteVerdict(string, forge.Verdict) error {
	return unadvertised(CapTransition, CapEditLabels)
}
func (*reads) Comment(string, string) error     { return unadvertised(CapComment) }
func (*reads) Probe() (string, error)           { return "", unadvertised(CapProbe) }
func (*reads) ListLabels() ([]string, error)    { return nil, unadvertised(CapLabels) }
func (*reads) CreateLabel(_, _, _ string) error { return unadvertised(CapLabels) }

// WalksAllPages implements forge.FullyPaginated: one page capped at
// forge.ResultPageLimit.
func (t *reads) WalksAllPages() bool { return false }

// StateLabels implements forge.LabeledTracker: the family the Connector's lists
// are resolved through.
func (t *reads) StateLabels() forge.DispatchLabels { return t.lifecycle.Labels }

// commentsLayer serves forge.CommentLister from the Connector. It exists only
// when the Connector advertises CapComments.
type commentsLayer struct {
	forge.IssueTracker
	r *reads
}

func (l commentsLayer) Unwrap() forge.IssueTracker { return l.IssueTracker }

// Comments returns the comments oldest first.
func (l commentsLayer) Comments(num string) ([]forge.Comment, error) {
	var res wireComments
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
// only when the Connector advertises CapNativeDeps.
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
// when the Connector advertises CapBlocks.
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

// idList calls an {ids} method for num.
func (t *reads) idList(method, num string) ([]string, error) {
	res := wireIDs{method: method}
	if err := t.call(context.Background(), method, numberedParams{t.target, num}, &res); err != nil {
		return nil, err
	}
	return res.IDs, nil
}

// probeLayer serves Probe from the Connector's one authenticated read.
type probeLayer struct {
	forge.IssueTracker
	r *reads
}

func (l probeLayer) Unwrap() forge.IssueTracker { return l.IssueTracker }

// Probe maps the read's HTTP status as the native adapters map theirs: auth
// failures stay distinguishable, anything else means the repo is unreachable.
func (l probeLayer) Probe() (string, error) {
	var res wireProbeStatus
	if err := l.r.call(context.Background(), CapProbe, targetParams{l.r.target}, &res); err != nil {
		if errors.As(err, new(probeStatusError)) {
			return "", err
		}
		return "", fmt.Errorf("%w: %w", forge.ErrRepoNotFound, err)
	}
	return l.r.target.Repo, nil
}

// statusesLayer serves forge.StatusMappingChecker and
// forge.StatusCategoryReader.
type statusesLayer struct {
	forge.IssueTracker
	r *reads
}

func (l statusesLayer) Unwrap() forge.IssueTracker { return l.IssueTracker }

// read decodes the tracker.statuses reply into res. Each capability decodes
// only its own key, so a malformed key one of them never reads cannot fail it.
func (l statusesLayer) read(res any) error {
	return l.r.call(context.Background(), CapStatuses, targetParams{l.r.target}, res)
}

func (l statusesLayer) CheckMappedStatuses() ([]string, error) {
	var mapped []string
	for _, s := range l.r.lifecycle.Statuses {
		if s != "" {
			mapped = append(mapped, s)
		}
	}
	if len(mapped) == 0 {
		return nil, nil
	}
	var res struct {
		Statuses []string `json:"statuses"`
	}
	if err := l.read(&res); err != nil {
		return nil, err
	}
	return forge.MissingStatuses(mapped, res.Statuses), nil
}

func (l statusesLayer) MappedStatusCategory(state forge.DispatchState) (string, error) {
	name := l.r.lifecycle.Statuses[state]
	if name == "" {
		return "", nil
	}
	var res struct {
		Categories map[string]string `json:"categories"`
	}
	if err := l.read(&res); err != nil {
		return "", err
	}
	// Exact case first, then the smallest folded match: a reply naming both
	// "Done" and "DONE" must not resolve by map iteration order.
	if category, ok := res.Categories[name]; ok {
		return category, nil
	}
	var found string
	for status := range res.Categories {
		if strings.EqualFold(status, name) && (found == "" || status < found) {
			found = status
		}
	}
	if found == "" {
		return "", nil
	}
	return res.Categories[found], nil
}

// createMetaLayer serves forge.IssueTypeChecker.
type createMetaLayer struct {
	forge.IssueTracker
	r *reads
}

func (l createMetaLayer) Unwrap() forge.IssueTracker { return l.IssueTracker }

func (l createMetaLayer) CheckIssueType() ([]string, error) {
	issueType := l.r.lifecycle.IssueType
	var res wireCreateMeta
	if err := l.r.call(context.Background(), CapCreateMeta, createMetaParams{l.r.target, issueType}, &res); err != nil {
		return nil, err
	}
	fields := make([]forge.CreateField, len(res.Fields))
	for i, f := range res.Fields {
		fields[i] = forge.CreateField{ID: f.ID, Name: f.Name, Required: f.Required, HasDefault: f.HasDefaultValue}
	}
	return forge.UnfilledCreateFields(fields), nil
}

// countLayer serves forge.DemandCounter.
type countLayer struct {
	forge.IssueTracker
	r        *reads
	interval time.Duration
}

func (l countLayer) Unwrap() forge.IssueTracker { return l.IssueTracker }

func (l countLayer) ProbeInterval() time.Duration { return l.interval }

// CountReady counts the set ListIssues(Dispatchable) selects, never past
// forge.ResultPageLimit (the most issues core uses; the Connector gets it as
// limit and may stop early, core clamps regardless). fresh is forwarded so the
// Connector can bypass a conditional-request cache. A state with no
// selector is empty, not every open issue.
func (l countLayer) CountReady(fresh bool) (int, error) {
	label, status := l.r.lifecycle.Labels.Label(forge.Dispatchable), l.r.lifecycle.Statuses[forge.Dispatchable]
	if label == "" && status == "" {
		return 0, nil
	}
	route, err := l.r.routeParam()
	if err != nil {
		return 0, err
	}
	var res wireCount
	if err := l.r.call(context.Background(), CapCount, countParams{Target: l.r.target, Label: label, Status: status, Limit: forge.ResultPageLimit, Fresh: fresh, Route: route}, &res); err != nil {
		return 0, err
	}
	return res.Count, nil
}

// maxCountInterval bounds the cadence a Connector may ask for.
const maxCountInterval = 24 * time.Hour

// priorClaimLayer serves forge.PriorClaimStateReader from the removed labels.
type priorClaimLayer struct {
	forge.IssueTracker
	r *reads
}

func (l priorClaimLayer) Unwrap() forge.IssueTracker { return l.IssueTracker }

// PriorClaimState is the most recent removal of the Complete or Failed label,
// the claim's strip of it being the only trace of the state before it.
func (l priorClaimLayer) PriorClaimState(num string) (forge.DispatchState, bool, error) {
	var res struct {
		Labels []string `json:"labels"`
	}
	if err := l.r.call(context.Background(), CapRemovedLabels, numberedParams{l.r.target, num}, &res); err != nil {
		return forge.Untriaged, false, err
	}
	labels := l.r.lifecycle.Labels
	prior, found := forge.Untriaged, false
	for _, name := range res.Labels {
		switch {
		case name != "" && name == labels.Complete:
			prior, found = forge.Complete, true
		case name != "" && name == labels.Failed:
			prior, found = forge.Failed, true
		}
	}
	return prior, found, nil
}

// readCountInterval reads the Connector's demand-probe cadence once, at
// construction; a sub-second cadence would hammer the tracker, and a cadence
// past maxCountInterval is an implausible answer (and would overflow a
// time.Duration).
func readCountInterval(call guestCall) (time.Duration, error) {
	var res wireInterval
	if err := call(context.Background(), CapCountInterval, struct{}{}, &res); err != nil {
		return 0, err
	}
	return time.Duration(res.Seconds) * time.Second, nil
}

// searchLayer serves forge.IssueSearcher from the Connector. It exists only
// when the Connector advertises CapSearchIssues.
type searchLayer struct {
	forge.IssueTracker
	r *reads
}

func (l searchLayer) Unwrap() forge.IssueTracker { return l.IssueTracker }

// SearchIssues returns the guest's matches in its order, at most limit: the
// guest is asked for limit but is not trusted to honour it, nor the state and
// label filters.
func (l searchLayer) SearchIssues(text string, state forge.IssueState, label string, limit int) ([]forge.Issue, error) {
	var res wireIssues
	p := searchIssuesParams{Target: l.r.target, Text: text, State: state.Wire(), Label: label, Limit: limit}
	if err := l.r.call(context.Background(), CapSearchIssues, p, &res); err != nil {
		return nil, err
	}
	var issues []forge.Issue
	for _, w := range res.Issues {
		issues = append(issues, w.issue())
	}
	return issues, nil
}

// labeledLayer serves forge.LabeledBacklogLister from the Connector. It exists
// only when the Connector advertises CapLabeled.
type labeledLayer struct {
	forge.IssueTracker
	r *reads
}

func (l labeledLayer) Unwrap() forge.IssueTracker { return l.IssueTracker }

// labeledMaxPages bounds one label's cursor walk so a guest minting fresh
// cursors forever cannot hold the scan open; labeledMaxBytes bounds what the
// walk may hold, as the host's output cap only bounds one page. Variables so a
// test can lower them.
var (
	labeledMaxPages = 1000
	labeledMaxBytes = 64 << 20
)

// labeledAnswer is a tracker.labeled reply. It decodes inside the call, so a
// malformed one passes through the Connector runtime's failure note like any
// other Connector failure, and records the answer's size for the walk's byte
// budget.
type labeledAnswer struct {
	Issues []wireIssue `json:"issues"`
	Next   string      `json:"next"`
	size   int
}

func (a *labeledAnswer) UnmarshalJSON(b []byte) error {
	type plain labeledAnswer
	if err := json.Unmarshal(b, (*plain)(a)); err != nil {
		return fmt.Errorf("%w: tracker.labeled: %w", ErrAnswer, err)
	}
	a.size = len(b)
	return nil
}

// ListIssuesWithLabels merges one guest read per label, as the native adapter
// does; walkLabeled reads one label.
func (l labeledLayer) ListIssuesWithLabels(state forge.IssueState, labels []string) ([]forge.Issue, error) {
	if state != forge.IssueOpen && state != forge.IssueClosed {
		return nil, fmt.Errorf("connector tracker: unsupported issue state %q", state)
	}
	source := "connector tracker.labeled " + state.Wire()
	return forge.MergeLabeledIssues("connector tracker", labels, func(label string) ([]forge.Issue, error) {
		return l.walkLabeled(state, source, label)
	})
}

// walkLabeled walks one label by cursor until next is empty, so a Connector
// that predates paging gets one call. A page is kept whole whatever the limit.
// A repeated cursor, or a page of only already-read issues, fails the label
// closed rather than loop; passing the page or byte bound keeps what was read
// and warns, as an error would drop the whole label.
func (l labeledLayer) walkLabeled(state forge.IssueState, source, label string) ([]forge.Issue, error) {
	route, err := l.r.routeParam()
	if err != nil {
		return nil, err
	}
	var issues []forge.Issue
	sent := map[[sha256.Size]byte]bool{}
	seen := map[string]bool{}
	bytesRead := 0
	cursor := ""
	for page := 1; ; page++ {
		sent[sha256.Sum256([]byte(cursor))] = true
		p := labeledParams{Target: l.r.target, Label: label, State: state.Wire(), Limit: forge.DedupScanLimit, Cursor: cursor, Route: route}
		var ans labeledAnswer
		if err := l.r.call(context.Background(), CapLabeled, p, &ans); err != nil {
			return nil, err
		}
		bytesRead += ans.size
		// An empty page is not a replay: a github REST page can hold only pull
		// requests, which that Connector skips, so it answers none with a next.
		// The page and byte bounds still stop a guest that loops on them.
		fresh := len(ans.Issues) == 0
		for _, w := range ans.Issues {
			iss := w.issue()
			fresh = fresh || !seen[iss.Number]
			seen[iss.Number] = true
			issues = append(issues, iss)
		}
		if ans.Next == "" {
			if page == 1 {
				forge.WarnDedupScanMayTruncate(source, label, len(issues))
			}
			return issues, nil
		}
		if sent[sha256.Sum256([]byte(ans.Next))] {
			return nil, fmt.Errorf("%w: tracker.labeled returned on page %d a cursor it had returned already; refusing to loop", ErrAnswer, page)
		}
		// New rows mid-walk legitimately shift an offset-paged tracker by a
		// row or two; a page with nothing new is a guest replaying one.
		if !fresh {
			return nil, fmt.Errorf("%w: tracker.labeled returned page %d with only issues already read; refusing to loop", ErrAnswer, page)
		}
		if page == labeledMaxPages {
			forge.WarnDedupScanStopped(source, label, "page", page, len(issues))
			return issues, nil
		}
		if bytesRead >= labeledMaxBytes {
			forge.WarnDedupScanStopped(source, label, "byte", page, len(issues))
			return issues, nil
		}
		cursor = ans.Next
	}
}

var (
	_ forge.FullyPaginated        = (*reads)(nil)
	_ forge.LabeledTracker        = (*reads)(nil)
	_ forge.RouteMatcher          = (*reads)(nil)
	_ forge.Unwrapper             = commentsLayer{}
	_ forge.CommentLister         = commentsLayer{}
	_ forge.Unwrapper             = nativeDepsLayer{}
	_ forge.NativeDepsReader      = nativeDepsLayer{}
	_ forge.Unwrapper             = blocksLayer{}
	_ forge.BlockersLister        = blocksLayer{}
	_ forge.Unwrapper             = probeLayer{}
	_ forge.Unwrapper             = statusesLayer{}
	_ forge.StatusMappingChecker  = statusesLayer{}
	_ forge.StatusCategoryReader  = statusesLayer{}
	_ forge.Unwrapper             = createMetaLayer{}
	_ forge.IssueTypeChecker      = createMetaLayer{}
	_ forge.Unwrapper             = countLayer{}
	_ forge.DemandCounter         = countLayer{}
	_ forge.Unwrapper             = priorClaimLayer{}
	_ forge.PriorClaimStateReader = priorClaimLayer{}
	_ forge.Unwrapper             = searchLayer{}
	_ forge.IssueSearcher         = searchLayer{}
	_ forge.Unwrapper             = labeledLayer{}
	_ forge.LabeledBacklogLister  = labeledLayer{}
)

// New builds core's tracker from a tracker Connector. A core IssueTracker method
// whose capability the Connector does not advertise fails with
// connectorhost.ErrProtocolUnsupported (transitions and verdicts need both
// CapTransition and CapEditLabels; the label registry CapLabels; comments
// CapComment; Probe CapProbe), so a caller first checks the Connector
// advertises every capability it uses. Every optional capability it omits is
// absent from forge.As. Calls run under context.Background() because
// IssueTracker methods carry no context.
//
// A 0.1 Connector predates the status selector, tracker.blocks and project-key
// ids, so core ignores those capabilities for it and never sends a status.
//
// clock paces the retry backoff; the zero value sleeps for real.
func New(c *connectorhost.Connector, lifecycle Lifecycle, target Target, clock retry.Clock) (forge.IssueTracker, error) {
	return NewCalling(c, c.Call, lifecycle, target, clock)
}

// NewCalling is New with calls routed through call instead of c.Call, for a
// caller that owns the Connector's lifetime and reloads it behind call; c
// supplies the handshake. A zero clock sleeps for real.
func NewCalling(c *connectorhost.Connector, call connectorcall.Call, lifecycle Lifecycle, target Target, clock retry.Clock) (forge.IssueTracker, error) {
	if !slices.Contains(c.Capabilities, "tracker") {
		return nil, fmt.Errorf("%w: capabilities %v lack \"tracker\"", ErrNotTracker, c.Capabilities)
	}
	return newTracker(connectorcall.Classify(call, clock, c.Capabilities), lifecycle, target, c.Capabilities, speaksV02(c.ProtocolVersion))
}

// speaksV02 reports whether a protocol version is 0.2 or later; one the
// handshake would refuse reports false.
func speaksV02(version string) bool {
	minor, ok := connectorhost.ProtocolMinor(version)
	return ok && minor >= 2
}

func wireRoute(r forge.Route) []Selector {
	var out []Selector
	for _, s := range r {
		out = append(out, Selector{Kind: s.Kind, Value: s.Value})
	}
	return out
}

func newTracker(call guestCall, lifecycle Lifecycle, target Target, caps []string, v02 bool) (forge.IssueTracker, error) {
	if !v02 {
		caps = slices.DeleteFunc(slices.Clone(caps), func(c string) bool {
			return c == CapBlocks || c == CapProjectKeyIDs || c == CapRoute
		})
	}
	ids := decimalIDs
	if slices.Contains(caps, CapProjectKeyIDs) {
		ids = projectKeyIDs
	}
	r := &reads{lifecycle: lifecycle, target: target, ids: ids, v02: v02, routes: slices.Contains(caps, CapRoute), route: wireRoute(lifecycle.Route)}
	r.call = connectorcall.Checked(call, trackerChecks(r))
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
	if slices.Contains(caps, CapProbe) {
		it = probeLayer{it, r}
	}
	if slices.Contains(caps, CapStatuses) {
		it = statusesLayer{it, r}
	}
	if slices.Contains(caps, CapCreateMeta) {
		it = createMetaLayer{it, r}
	}
	if slices.Contains(caps, CapCount) {
		interval, err := readCountInterval(r.call)
		if err != nil {
			return nil, err
		}
		it = countLayer{it, r, interval}
	}
	if slices.Contains(caps, CapRemovedLabels) {
		it = priorClaimLayer{it, r}
	}
	if slices.Contains(caps, CapSearchIssues) {
		it = searchLayer{it, r}
	}
	if slices.Contains(caps, CapLabeled) {
		it = labeledLayer{it, r}
	}
	if slices.Contains(caps, CapTransition) && slices.Contains(caps, CapEditLabels) {
		it = transitionLayer{it, r}
	}
	if slices.Contains(caps, CapEditLabels) {
		it = labelAddLayer{it, r}
	}
	if slices.Contains(caps, CapLabels) {
		it = labelsLayer{it, r}
	}
	if slices.Contains(caps, CapComment) {
		it = commentPostLayer{it, r}
	}
	if slices.Contains(caps, CapCloseIssue) {
		it = closerLayer{it, r}
	}
	if slices.Contains(caps, CapFileIssue) {
		it = filerLayer{it, r}
	}
	return it, nil
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
		// selector matches nothing.
		return nil, nil
	}
	route, err := t.routeParam()
	if err != nil {
		return nil, err
	}
	return t.list(label, status, route)
}

func (t *reads) ListOpenIssues() ([]forge.Issue, error) {
	return t.list("", "", nil)
}

func (t *reads) list(label, status string, route []Selector) ([]forge.Issue, error) {
	var res wireIssues
	p := listIssuesParams{Target: t.target, Label: label, Status: status, Limit: forge.ResultPageLimit, Route: route}
	if err := t.call(context.Background(), "tracker.listIssues", p, &res); err != nil {
		return nil, err
	}
	issues := make([]forge.Issue, len(res.Issues))
	for i, w := range res.Issues {
		issues[i] = w.issue()
	}
	// The check validated the numbers.
	sort.Slice(issues, func(i, j int) bool { return t.ids.less(issues[i].Number, issues[j].Number) })
	// The guest is asked for Limit but is not trusted to honour it.
	issues = issues[:min(len(issues), forge.ResultPageLimit)]
	forge.WarnPageMayTruncateBacklog("tracker connector list", len(issues))
	return issues, nil
}

func (t *reads) Issue(num string) (forge.Issue, error) {
	got, _, err := t.readIssue(num)
	return got, err
}

// readIssue is Issue plus the raw workflow status the write layers decide on.
func (t *reads) readIssue(num string) (forge.Issue, string, error) {
	var w wireIssue
	if err := t.call(context.Background(), "tracker.issue", numberedParams{t.target, num}, &w); err != nil {
		return forge.Issue{}, "", err
	}
	return w.issue(), w.Status, nil
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
	// its creation time would put it.
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

// readKind is which read a wire issue answers.
type readKind int

const (
	singleRead readKind = iota
	// listRead may leave state empty, as the github Connector's list projection does.
	listRead
)

// check fails closed on a state it does not know or a number outside the id
// grammar (numbers become branch names).
func (w wireIssue) check(g idGrammar, read readKind) error {
	if !g.valid(w.Number) {
		return fmt.Errorf("%w: issue number %q is not %s", ErrAnswer, w.Number, g.name)
	}
	if w.State != "open" && w.State != "closed" && (w.State != "" || read != listRead) {
		return fmt.Errorf("%w: issue %s has unknown state %q", ErrAnswer, w.Number, w.State)
	}
	if err := checkLabels(w.Labels); err != nil {
		return fmt.Errorf("issue %s: %w", w.Number, err)
	}
	return nil
}

// issue converts a wire issue its check accepted.
func (w wireIssue) issue() forge.Issue {
	var state forge.IssueState
	switch w.State {
	case "open":
		state = forge.IssueOpen
	case "closed":
		state = forge.IssueClosed
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
	return iss
}

// cleanText reports whether s is non-empty and free of control characters, so
// text a Connector supplies cannot forge lines in a transcript or log.
func cleanText(s string) bool {
	return s != "" && strings.IndexFunc(s, unicode.IsControl) < 0
}

// checkLabels refuses a label name core could not safely act on or echo.
func checkLabels(labels []string) error {
	for _, l := range labels {
		if !cleanText(l) {
			return fmt.Errorf("%w: empty or control-character label name %q", ErrAnswer, l)
		}
	}
	return nil
}

// HostOf is the host[:port] of a tracker's base URL, for Lifecycle.WebHost;
// "" when the URL does not parse, which refuses every filed URL.
func HostOf(baseURL string) string {
	u, err := url.Parse(baseURL)
	if err != nil {
		return ""
	}
	return u.Host
}
