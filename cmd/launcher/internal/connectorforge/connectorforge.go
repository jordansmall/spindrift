// Package connectorforge builds core's forge PR reads (the open-PR and
// any-state branch lookups, the PR state, the check rollup state and the
// failure detail of forge.PRForge) from a forge Connector, over the forge
// primitives in sdk/forge. New wraps a native adapter: every write stays with
// it, and so does every read the Connector has no primitive for (Mergeable,
// HeadCommitSHA, NeedsUpdate, ListPRFiles, CanAutoMerge). Compose instead
// builds a whole Code Forge from the Connector.
//
// The Connector returns raw fields; every policy decision is made here: PR URL
// validation, which conclusions count as failing, and rendering and capping the
// failure detail, so a hostile guest's text cannot outgrow
// forge.MaxFailureDetailBytes.
//
// Compose builds a Code Forge from a Connector advertising "forge.branches" and
// either "forge.pushOnly" (a push-only forge) or "forge.prs" (a PR forge whose
// merge, draft, comment and auto-merge writes the Connector performs): the
// Connector answers the forge questions and names the remote, and git runs in
// core's own plumbing with core's own credential, attached only after the
// remote's host is checked against the Connector's declared routes. Every
// idempotent Connector call retries a transient failure, and every call reads
// its error class as core's sentinels.
package connectorforge

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"path"
	"slices"
	"strconv"
	"strings"
	"time"

	"spindrift.dev/launcher/internal/connectorhost"
	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/retry"
)

// ErrNotForge marks a Connector that does not advertise the "forge"
// capability.
var ErrNotForge = errors.New("connector is not a forge")

// ErrNoWebHost marks a New call with no web host to pin PR URLs to.
var ErrNoWebHost = errors.New("no forge web host to pin PR URLs to")

// ErrAnswer marks a Connector answer the core refuses (a malformed PR, a PR
// other than the one asked for, or an unknown rollup state), as opposed to a
// protocol failure, which wraps connectorhost.ErrCall.
var ErrAnswer = errors.New("connector answer refused")

// Target is the repository a forge Connector reads.
type Target struct {
	API  string `json:"api"`
	Repo string `json:"repo"`
}

// The shapes the forge adapter decodes from the guest.
type (
	wireRemote struct {
		CloneURL string `json:"cloneUrl"`
		WebURL   string `json:"webUrl"`
	}
	wirePR struct {
		Number      string `json:"number"`
		URL         string `json:"url"`
		State       string `json:"state"`
		Merged      bool   `json:"merged"`
		Draft       bool   `json:"draft,omitempty"`
		Head        string `json:"head,omitempty"`
		HeadSHA     string `json:"headSha,omitempty"`
		Mergeable   string `json:"mergeable,omitempty"`
		MergeCommit string `json:"mergeCommit,omitempty"`
	}
	wireCheck struct {
		Kind       string `json:"kind"`
		Name       string `json:"name"`
		Conclusion string `json:"conclusion"`
		Summary    string `json:"summary"`
		JobID      string `json:"jobId,omitempty"`
	}
	wireGrant struct {
		Introspectable bool   `json:"introspectable"`
		WriteCapable   bool   `json:"writeCapable"`
		Reason         string `json:"reason,omitempty"`
	}
	wireRollup struct {
		State  string      `json:"state"`
		Checks []wireCheck `json:"checks"`
	}
	branchParams struct {
		Target Target `json:"target"`
		Branch string `json:"branch"`
	}
	numberedParams struct {
		Target Target `json:"target"`
		Number string `json:"number"`
	}
	jobLogParams struct {
		Target Target `json:"target"`
		JobID  string `json:"jobId"`
		Tail   int    `json:"tail"`
	}
)

// guestCall is the Connector.Call signature, a seam so the adapter's policy
// runs against a stub guest.
type guestCall func(ctx context.Context, method string, params, result any) error

// reads serves the PR reads from a Connector and leaves everything else to the
// embedded native adapter.
//
// Optional capabilities found by type assertion on the native adapter
// (forge.CIRunReporter and the like) are hidden behind this wrapper. Only the read
// shadow builds one in production, so no authoritative path loses them.
type reads struct {
	forge.PRForge
	call    guestCall
	target  Target
	webHost string
}

// New builds core's forge PR reads from a forge Connector. writes keeps every
// write and every read the Connector has no primitive for. Calls run under
// context.Background() because PRForge methods carry no context.
// webHost is the forge's web host (host[:port]) every PR URL must name exactly,
// matched case-insensitively with the port as written ("github.com:443" does
// not match "github.com").
func New(c *connectorhost.Connector, writes forge.PRForge, target Target, webHost string) (forge.PRForge, error) {
	if !slices.Contains(c.Capabilities, capForge) {
		return nil, fmt.Errorf("%w: capabilities %v lack \"forge\"", ErrNotForge, c.Capabilities)
	}
	if webHost == "" {
		return nil, ErrNoWebHost
	}
	return newForge(classedCall(c, retry.Clock{}), writes, target, webHost), nil
}

func newForge(call guestCall, writes forge.PRForge, target Target, webHost string) forge.PRForge {
	return &reads{PRForge: writes, call: call, target: target, webHost: webHost}
}

// isPRNumber reports whether s is a positive decimal integer in canonical form
// (no sign, no leading zeros).
func isPRNumber(s string) bool {
	n, err := strconv.Atoi(s)
	return err == nil && n >= 1 && strconv.Itoa(n) == s
}

// onWebHost reports whether u is a plain https URL on the target's web host:
// no userinfo, opaque part, query or fragment.
func (t *reads) onWebHost(u *url.URL) bool {
	return u.Scheme == "https" && strings.EqualFold(u.Host, t.webHost) && u.User == nil && u.Opaque == "" &&
		u.RawQuery == "" && !u.ForceQuery && u.Fragment == ""
}

// prNumber extracts the PR number from raw, which must be an https URL on the
// target's web host of the form /<owner>/<repo>/pull/<n> naming the target
// repository (host and repo case-insensitive, as forges are) with nothing else
// attached.
func (t *reads) prNumber(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("PR URL %q: %w", raw, err)
	}
	if !t.onWebHost(u) {
		return "", fmt.Errorf("PR URL %q is not a plain https URL on %s", raw, t.webHost)
	}
	parts := strings.Split(u.Path, "/")
	if len(parts) != 5 || parts[0] != "" || parts[3] != "pull" || !isPRNumber(parts[4]) ||
		!strings.EqualFold(parts[1]+"/"+parts[2], t.target.Repo) {
		return "", fmt.Errorf("PR URL %q is not /%s/pull/<n>", raw, t.target.Repo)
	}
	return parts[4], nil
}

// ownRunLink keeps a CI run link only when it is a plain https URL on the
// target repository's web host. The guest is untrusted and the link is
// rendered for operators to click, so a link off the target repo is dropped,
// not failed on. A backslash is refused: browsers read it as "/", which would
// escape the repository prefix.
func (t *reads) ownRunLink(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || !t.onWebHost(u) || strings.Contains(u.Path, `\`) {
		return ""
	}
	prefix := "/" + t.target.Repo + "/"
	if len(u.Path) <= len(prefix) || !strings.EqualFold(u.Path[:len(prefix)], prefix) || path.Clean(u.Path) != u.Path {
		return ""
	}
	return raw
}

// check validates a PR the guest returned: a canonical number, a URL naming
// that same number in the target repository, and a state/merged pair that is
// possible.
func (t *reads) check(w wirePR) (forge.PRState, error) {
	if !isPRNumber(w.Number) {
		return "", fmt.Errorf("%w: PR number %q is not a positive integer", ErrAnswer, w.Number)
	}
	n, err := t.prNumber(w.URL)
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrAnswer, err)
	}
	if n != w.Number {
		return "", fmt.Errorf("%w: PR %s has URL %q for PR %s", ErrAnswer, w.Number, w.URL, n)
	}
	switch {
	case w.State == "open" && !w.Merged:
		return forge.PROpen, nil
	case w.State == "closed" && w.Merged:
		return forge.PRMerged, nil
	case w.State == "closed":
		return forge.PRClosed, nil
	}
	return "", fmt.Errorf("%w: PR %s has state %q (merged %t)", ErrAnswer, w.Number, w.State, w.Merged)
}

// branchPRs asks the guest for every PR on branch and validates all of them.
func (t *reads) branchPRs(branch string) ([]wirePR, []forge.PRState, error) {
	var res struct {
		PRs []wirePR `json:"prs"`
	}
	if err := t.call(context.Background(), "forge.prsForBranch", branchParams{t.target, branch}, &res); err != nil {
		return nil, nil, err
	}
	states := make([]forge.PRState, len(res.PRs))
	for i, w := range res.PRs {
		var err error
		if states[i], err = t.check(w); err != nil {
			return nil, nil, err
		}
	}
	return res.PRs, states, nil
}

func (t *reads) OpenPRForBranch(branch string) (forge.PR, bool, error) {
	prs, states, err := t.branchPRs(branch)
	if err != nil {
		return forge.PR{}, false, err
	}
	for i, s := range states {
		if s == forge.PROpen {
			return forge.PR{URL: prs[i].URL}, true, nil
		}
	}
	return forge.PR{}, false, nil
}

func (t *reads) PRForBranch(branch string) (string, bool, error) {
	prs, _, err := t.branchPRs(branch)
	if err != nil || len(prs) == 0 {
		return "", false, err
	}
	return prs[0].URL, true, nil
}

// pr reads and validates the PR at prURL, which must be the PR the guest answers
// for.
func (t *reads) pr(prURL string) (wirePR, forge.PRState, error) {
	n, err := t.prNumber(prURL)
	if err != nil {
		return wirePR{}, "", err
	}
	var w wirePR
	if err := t.call(context.Background(), "forge.pr", numberedParams{t.target, n}, &w); err != nil {
		return wirePR{}, "", err
	}
	state, err := t.check(w)
	if err != nil {
		return wirePR{}, "", err
	}
	if w.Number != n {
		return wirePR{}, "", fmt.Errorf("%w: asked for PR %s, got %s", ErrAnswer, n, w.Number)
	}
	return w, state, nil
}

func (t *reads) PRState(prURL string) (forge.PRState, error) {
	_, state, err := t.pr(prURL)
	return state, err
}

func (t *reads) rollup(prURL string) (wireRollup, error) {
	n, err := t.prNumber(prURL)
	if err != nil {
		return wireRollup{}, err
	}
	var ro wireRollup
	err = t.call(context.Background(), "forge.checks", numberedParams{t.target, n}, &ro)
	return ro, err
}

func (t *reads) CheckState(prURL string) (forge.RollupState, error) {
	ro, err := t.rollup(prURL)
	if err != nil {
		return "", err
	}
	// Unlike the native adapter, which passes states through, fail closed on a
	// guest-supplied state outside the five known values.
	switch s := forge.RollupState(ro.State); s {
	case "":
		return forge.StateNone, nil
	case forge.StateSuccess, forge.StatePending, forge.StateExpected, forge.StateFailure, forge.StateError:
		return s, nil
	}
	return "", fmt.Errorf("%w: unknown rollup state %q", ErrAnswer, ro.State)
}

// The failing sets match the native github adapter's (ci_rollup.go): a CheckRun
// conclusion excludes SUCCESS, NEUTRAL and SKIPPED; a StatusContext fails on
// FAILURE or ERROR.
var (
	failingCheckRunConclusions = map[string]bool{
		"FAILURE":         true,
		"TIMED_OUT":       true,
		"CANCELLED":       true,
		"ACTION_REQUIRED": true,
		"STARTUP_FAILURE": true,
	}
	failingStatusContextStates = map[string]bool{
		"FAILURE": true,
		"ERROR":   true,
	}
)

// jobLogsBudget caps the total time one FailureDetail spends reading job logs,
// so a slow forge cannot stall PR settle; jobs left once it is spent get no
// log. A var so a test can shrink it.
var jobLogsBudget = 60 * time.Second

func (t *reads) FailureDetail(prURL string) (string, error) {
	ro, err := t.rollup(prURL)
	if err != nil {
		return "", err
	}
	deadline := time.Now().Add(jobLogsBudget)
	var entries []forge.FailureDetailEntry
	for _, c := range ro.Checks {
		var failing bool
		switch c.Kind {
		case "CheckRun":
			failing = failingCheckRunConclusions[c.Conclusion]
		case "StatusContext":
			failing = failingStatusContextStates[c.Conclusion]
		}
		if failing {
			entries = append(entries, forge.FailureDetailEntry{
				Name: c.Name, State: c.Conclusion, Summary: c.Summary, Log: t.jobLog(deadline, c.JobID),
			})
		}
	}
	return forge.RenderFailureDetail(entries), nil
}

// jobLog reads the tail of one job's log, or "" for no job, a Connector without
// forge.jobLog, or any failed read: the log only enriches a detail that is
// already in hand, and a caller treats an error as no detail at all. The guest
// is asked for a tail of forge.JobLogKeepBytes so a long log never crosses the
// output cap.
//
// The budget is checked before the call rather than carried as its context
// deadline: a deadline expiring mid-call closes the Connector, costing every
// later read for the sake of a log.
func (t *reads) jobLog(deadline time.Time, jobID string) string {
	if jobID == "" || !time.Now().Before(deadline) {
		return ""
	}
	var res struct {
		Log string `json:"log"`
	}
	p := jobLogParams{t.target, jobID, forge.JobLogKeepBytes}
	if err := t.call(context.Background(), "forge.jobLog", p, &res); err != nil {
		return ""
	}
	return res.Log
}
