// Package connectorforge builds core's forge PR reads (the open-PR and
// any-state branch lookups, the PR state, the check rollup state and the
// failure detail of forge.PRForge) from a forge Connector, over the
// provisional 0.y forge primitives in sdk/forge. Every write stays with the
// native adapter, which also keeps serving the reads the Connector has no
// primitive for (Mergeable, HeadCommitSHA, NeedsUpdate, ListPRFiles,
// CanAutoMerge).
//
// The Connector returns raw fields; every policy decision is made here: PR URL
// validation, which conclusions count as failing, and rendering and capping the
// failure detail, so a hostile guest's text cannot outgrow forge.MaxFailureDetailBytes.
package connectorforge

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strconv"
	"strings"

	"spindrift.dev/launcher/internal/connectorhost"
	"spindrift.dev/launcher/internal/forge"
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
	wirePR struct {
		Number string `json:"number"`
		URL    string `json:"url"`
		State  string `json:"state"`
		Merged bool   `json:"merged"`
	}
	wireCheck struct {
		Kind       string `json:"kind"`
		Name       string `json:"name"`
		Conclusion string `json:"conclusion"`
		Summary    string `json:"summary"`
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
)

// guestCall is the Connector.Call signature, a seam so the adapter's policy
// runs against a stub guest.
type guestCall func(ctx context.Context, method string, params, result any) error

// reads serves the PR reads from a Connector and leaves everything else to the
// embedded native adapter.
//
// Optional capabilities found by type assertion on the native adapter
// (forge.CIRunReporter and the like) are hidden behind this wrapper. Nothing in
// production builds one yet; connectortracker.New is likewise only called from
// tests today.
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
	if !slices.Contains(c.Capabilities, "forge") {
		return nil, fmt.Errorf("%w: capabilities %v lack \"forge\"", ErrNotForge, c.Capabilities)
	}
	if webHost == "" {
		return nil, ErrNoWebHost
	}
	return newForge(c.Call, writes, target, webHost), nil
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

// prNumber extracts the PR number from raw, which must be an https URL on the
// target's web host of the form /<owner>/<repo>/pull/<n> naming the target
// repository (host and repo case-insensitive, as forges are) with nothing else
// attached.
func (t *reads) prNumber(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("PR URL %q: %w", raw, err)
	}
	if u.Scheme != "https" || !strings.EqualFold(u.Host, t.webHost) || u.User != nil || u.Opaque != "" ||
		u.RawQuery != "" || u.ForceQuery || u.Fragment != "" {
		return "", fmt.Errorf("PR URL %q is not a plain https URL on %s", raw, t.webHost)
	}
	parts := strings.Split(u.Path, "/")
	if len(parts) != 5 || parts[0] != "" || parts[3] != "pull" || !isPRNumber(parts[4]) ||
		!strings.EqualFold(parts[1]+"/"+parts[2], t.target.Repo) {
		return "", fmt.Errorf("PR URL %q is not /%s/pull/<n>", raw, t.target.Repo)
	}
	return parts[4], nil
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

func (t *reads) PRState(prURL string) (forge.PRState, error) {
	n, err := t.prNumber(prURL)
	if err != nil {
		return "", err
	}
	var w wirePR
	if err := t.call(context.Background(), "forge.pr", numberedParams{t.target, n}, &w); err != nil {
		return "", err
	}
	state, err := t.check(w)
	if err != nil {
		return "", err
	}
	if w.Number != n {
		return "", fmt.Errorf("%w: asked for PR %s, got %s", ErrAnswer, n, w.Number)
	}
	return state, nil
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

func (t *reads) FailureDetail(prURL string) (string, error) {
	ro, err := t.rollup(prURL)
	if err != nil {
		return "", err
	}
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
			entries = append(entries, forge.FailureDetailEntry{Name: c.Name, State: c.Conclusion, Summary: c.Summary})
		}
	}
	return forge.RenderFailureDetail(entries), nil
}
