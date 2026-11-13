// Package connectortracker builds core's tracker reads (the forge.IssueTracker
// list and issue reads and, when advertised, forge.CommentLister,
// forge.NativeDepsReader, forge.BlockersLister, forge.IssueSearcher, the Probe
// read, forge.StatusMappingChecker, forge.IssueTypeChecker and
// forge.DemandCounter) from a tracker Connector, over the 0.2 tracker
// primitives in sdk/tracker. A Connector that advertises the transition and
// label-edit primitives performs the writes, while core keeps every lifecycle
// decision (claim checks, the settled-label strip, fallback and refusal,
// verdicts); any other write stays with the native adapter, which also stays
// authoritative for every optional capability the protocol does not carry: the
// result is a stack of forge.Unwrapper layers over the native adapter, so
// forge.As reaches those capabilities through it. The check and demand
// capabilities are the exception: they come only from the Connector, and are
// absent when it omits them.
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
//
// Statuses and IssueType feed the check capabilities (CheckMappedStatuses,
// CheckIssueType): a caller selecting a Connector tracker for doctor fills them.
// IssueType is also the type tracker.fileIssue files under, so a caller filing
// through the Connector must fill it too.
type Lifecycle struct {
	Labels   forge.DispatchLabels
	Statuses map[forge.DispatchState]string
	// IssueType is the issue type core files issues as (JIRA_ISSUE_TYPE), which
	// the createMeta check measures the project against.
	IssueType string
	// VerdictLabels names the label each research verdict lands as.
	VerdictLabels forge.VerdictLabels
	// Resolutions is the resolution a transition into a mapped state sets; a
	// state absent leaves it to the tracker's workflow default.
	Resolutions map[forge.DispatchState]string
	// StatusOnly is the status-only lifecycle: an unavailable or unmapped
	// transition is refused instead of swapped for a label, and the claim check
	// reads the native status alone.
	StatusOnly bool
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
		Target Target `json:"target"`
		Label  string `json:"label"`
		Status string `json:"status"`
		Limit  int    `json:"limit"`
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
	issueRequest struct {
		Title       string   `json:"title"`
		Body        string   `json:"body"`
		Labels      []string `json:"labels"`
		DedupMarker string   `json:"dedupMarker"`
		IssueType   string   `json:"issueType"`
	}
	fileIssueParams struct {
		Target Target `json:"target"`
		issueRequest
	}
	createMetaParams struct {
		Target    Target `json:"target"`
		IssueType string `json:"issueType"`
	}
	countParams struct {
		Target Target `json:"target"`
		Label  string `json:"label"`
		Status string `json:"status"`
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
	// CapProbe: the Connector serves tracker.probe, one authenticated read whose
	// HTTP status core maps to a Probe result.
	CapProbe = "tracker.probe"
	// CapStatuses: the Connector serves tracker.statuses, the project's workflow
	// status names.
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
	// CapSearchIssues: the Connector serves tracker.searchIssues.
	CapSearchIssues = "tracker.searchIssues"
	// CapTransition: the Connector serves tracker.transition. With CapEditLabels
	// it performs core's transitions and verdicts.
	CapTransition = "tracker.transition"
	// CapEditLabels: the Connector serves tracker.editLabels.
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

// reads serves the list and issue reads from a Connector and leaves every write
// to the embedded native adapter, which the write layers override. It is the base
// layer; the optional layers stack on it.
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

// A layer for a check or demand capability exists only when the Connector
// advertises it; reads hides the native adapter's version of each but Probe
// (which falls back to native), so an absent capability is absent rather than
// answered from the native side.

// probeLayer serves Probe from the Connector's one authenticated read.
type probeLayer struct {
	forge.IssueTracker
	r *reads
}

func (l probeLayer) Unwrap() forge.IssueTracker { return l.IssueTracker }

// Probe maps the read's HTTP status as the native adapters map theirs: auth
// failures stay distinguishable, anything else means the repo is unreachable.
func (l probeLayer) Probe() (string, error) {
	var res struct {
		Status int `json:"status"`
	}
	if err := l.r.call(context.Background(), CapProbe, targetParams{l.r.target}, &res); err != nil {
		return "", fmt.Errorf("%w: %w", forge.ErrRepoNotFound, err)
	}
	switch {
	case res.Status >= 200 && res.Status < 300:
		return l.r.target.Repo, nil
	case res.Status == 401 || res.Status == 403:
		return "", fmt.Errorf("%w: probe of %s answered %d", forge.ErrAuthFailure, l.r.target.Repo, res.Status)
	default:
		return "", fmt.Errorf("%w: probe of %s answered %d", forge.ErrRepoNotFound, l.r.target.Repo, res.Status)
	}
}

// statusesLayer serves forge.StatusMappingChecker.
type statusesLayer struct {
	forge.IssueTracker
	r *reads
}

func (l statusesLayer) Unwrap() forge.IssueTracker { return l.IssueTracker }

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
	if err := l.r.call(context.Background(), CapStatuses, targetParams{l.r.target}, &res); err != nil {
		return nil, err
	}
	return forge.MissingStatuses(mapped, res.Statuses), nil
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
	if err := forge.IssueTypeNotOffered(l.r.target.Repo, issueType, res.IssueTypes); err != nil {
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

// CountReady counts the set ListIssues(Dispatchable) selects; a state with no
// selector is empty, not every open issue.
func (l countLayer) CountReady(bool) (int, error) {
	label, status := l.r.lifecycle.Labels.Label(forge.Dispatchable), l.r.lifecycle.Statuses[forge.Dispatchable]
	if label == "" && status == "" {
		return 0, nil
	}
	var res struct {
		Count int `json:"count"`
	}
	if err := l.r.call(context.Background(), CapCount, countParams{l.r.target, label, status}, &res); err != nil {
		return 0, err
	}
	if res.Count < 0 {
		return 0, fmt.Errorf("%w: tracker.count answered %d", ErrAnswer, res.Count)
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
	var res struct {
		Seconds int `json:"seconds"`
	}
	if err := call(context.Background(), CapCountInterval, struct{}{}, &res); err != nil {
		return 0, err
	}
	if res.Seconds < 1 || res.Seconds > int(maxCountInterval/time.Second) {
		return 0, fmt.Errorf("%w: %s answered %d seconds, want 1 to %d", ErrAnswer, CapCountInterval, res.Seconds, int(maxCountInterval/time.Second))
	}
	return time.Duration(res.Seconds) * time.Second, nil
}

// Hides implements forge.CapabilityHider for the capabilities the protocol
// owns: the native adapter's own must never answer for a Connector that omits
// them.
func (t *reads) Hides(capability any) bool {
	switch capability.(type) {
	case *forge.DemandCounter, *forge.IssueTypeChecker, *forge.StatusMappingChecker, *forge.PriorClaimStateReader:
		return true
	}
	return false
}

// searchLayer serves forge.IssueSearcher from the Connector. It exists only
// when the Connector advertises CapSearchIssues; otherwise the lookup falls
// through to the native adapter's searcher, if it has one.
type searchLayer struct {
	forge.IssueTracker
	r *reads
}

func (l searchLayer) Unwrap() forge.IssueTracker { return l.IssueTracker }

// SearchIssues returns the guest's matches in its order, at most limit: the
// guest is asked for limit but is not trusted to honour it, nor the state and
// label filters (hits outside them are dropped). A bad number or
// state fails closed, as in issue().
func (l searchLayer) SearchIssues(text string, state forge.IssueState, label string, limit int) ([]forge.Issue, error) {
	var res struct {
		Issues []wireIssue `json:"issues"`
	}
	p := searchIssuesParams{Target: l.r.target, Text: text, State: state.Wire(), Label: label, Limit: limit}
	if err := l.r.call(context.Background(), CapSearchIssues, p, &res); err != nil {
		return nil, err
	}
	res.Issues = res.Issues[:min(len(res.Issues), max(limit, 0))]
	var issues []forge.Issue
	for _, w := range res.Issues {
		iss, err := w.issue(l.r.ids)
		if err != nil {
			return nil, err
		}
		// The guest is not trusted to have applied the filters: a hit outside
		// the requested state or label would be treated as a suppressing match.
		if iss.State != state || (label != "" && !slices.Contains(iss.Labels, label)) {
			continue
		}
		issues = append(issues, iss)
	}
	return issues, nil
}

var (
	_ forge.Unwrapper             = (*reads)(nil)
	_ forge.FullyPaginated        = (*reads)(nil)
	_ forge.LabeledTracker        = (*reads)(nil)
	_ forge.Unwrapper             = commentsLayer{}
	_ forge.CommentLister         = commentsLayer{}
	_ forge.Unwrapper             = nativeDepsLayer{}
	_ forge.NativeDepsReader      = nativeDepsLayer{}
	_ forge.Unwrapper             = blocksLayer{}
	_ forge.BlockersLister        = blocksLayer{}
	_ forge.CapabilityHider       = (*reads)(nil)
	_ forge.Unwrapper             = probeLayer{}
	_ forge.Unwrapper             = statusesLayer{}
	_ forge.StatusMappingChecker  = statusesLayer{}
	_ forge.Unwrapper             = createMetaLayer{}
	_ forge.IssueTypeChecker      = createMetaLayer{}
	_ forge.Unwrapper             = countLayer{}
	_ forge.DemandCounter         = countLayer{}
	_ forge.Unwrapper             = priorClaimLayer{}
	_ forge.PriorClaimStateReader = priorClaimLayer{}
	_ forge.Unwrapper             = searchLayer{}
	_ forge.IssueSearcher         = searchLayer{}
)

// New builds core's tracker from a tracker Connector. writes answers every
// write the Connector does not perform: transitions and verdicts unless it
// advertises both CapTransition and CapEditLabels, the label registry unless
// CapLabels, comments unless CapComment, issue filing unless CapFileIssue, and
// issue edits always. It also answers every optional capability the protocol
// does not carry (see CapComments, CapNativeDeps, CapBlocks and
// CapSearchIssues); Probe stays with it unless the Connector advertises
// CapProbe. The doctor and demand checks (CapStatuses, CapCreateMeta, CapCount)
// and recover's prior-claim read (CapRemovedLabels) are never answered
// natively.
// Look capabilities up with forge.As. Calls run under context.Background()
// because IssueTracker methods carry no context.
//
// A 0.1 Connector predates the status selector, tracker.blocks and project-key
// ids, so core ignores those capabilities for it and never sends a status.
func New(c *connectorhost.Connector, writes forge.IssueTracker, lifecycle Lifecycle, target Target) (forge.IssueTracker, error) {
	if !slices.Contains(c.Capabilities, "tracker") {
		return nil, fmt.Errorf("%w: capabilities %v lack \"tracker\"", ErrNotTracker, c.Capabilities)
	}
	return newTracker(c.Call, writes, lifecycle, target, c.Capabilities, speaksV02(c.ProtocolVersion))
}

// speaksV02 reports whether a protocol version is 0.2 or later; one the
// handshake would refuse reports false.
func speaksV02(version string) bool {
	minor, ok := connectorhost.ProtocolMinor(version)
	return ok && minor >= 2
}

func newTracker(call guestCall, writes forge.IssueTracker, lifecycle Lifecycle, target Target, caps []string, v02 bool) (forge.IssueTracker, error) {
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
		interval, err := readCountInterval(call)
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
	if slices.Contains(caps, CapTransition) && slices.Contains(caps, CapEditLabels) {
		it = transitionLayer{it, r}
	}
	if slices.Contains(caps, CapLabels) {
		it = labelsLayer{it, r}
	}
	if slices.Contains(caps, CapComment) {
		it = commentPostLayer{it, r}
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
	got, _, err := t.readIssue(num)
	return got, err
}

// readIssue is Issue plus the raw workflow status the write layers decide on.
func (t *reads) readIssue(num string) (forge.Issue, string, error) {
	var w wireIssue
	if err := t.call(context.Background(), "tracker.issue", numberedParams{t.target, num}, &w); err != nil {
		return forge.Issue{}, "", err
	}
	got, err := w.issue(t.ids)
	if err != nil {
		return forge.Issue{}, "", err
	}
	if got.Number != num {
		return forge.Issue{}, "", fmt.Errorf("%w: asked for issue %s, got %s", ErrAnswer, num, got.Number)
	}
	return got, w.Status, nil
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
