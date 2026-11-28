package connectortracker

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"path"
	"slices"
	"strings"

	"spindrift.dev/launcher/internal/dedupmarker"
	"spindrift.dev/launcher/internal/forge"
)

// transitionLayer performs TransitionState and CompleteVerdict through the
// Connector's transition and label-edit primitives. It exists only when the
// Connector advertises both; the Connector does each write it is asked for and
// every decision stays here.
type transitionLayer struct {
	forge.IssueTracker
	r *reads
}

func (l transitionLayer) Unwrap() forge.IssueTracker { return l.IssueTracker }

// alreadyClaimedNative reports whether the issue's status already equals the
// InProgress mapping. A from state mapped to the same status is exempt, so the
// dispatch-workflow re-entry case is not mistaken for a stolen claim (#3887).
func (l transitionLayer) alreadyClaimedNative(status string, from forge.DispatchState) bool {
	target := l.r.lifecycle.Statuses[forge.InProgress]
	if target == "" || l.r.lifecycle.Statuses[from] == target {
		return false
	}
	return strings.EqualFold(status, target)
}

// TransitionState moves num from state from to state to through the Connector,
// falling back to a label swap when the status is unmapped or unavailable
// (ADR 0013) unless the lifecycle is status-only, which refuses instead.
//
// A claim first reads num and returns forge.ErrAlreadyClaimed when the fallback
// label or the native status already reads InProgress (#3887); the read-then-
// write is not atomic. A native claim then strips settled labels and fails if
// that strip does: nothing has moved yet, whereas a surviving settled label
// makes shutdown.unsettled read the live Box as already settled (#4917). The
// strip after a successful transition stays best-effort.
func (l transitionLayer) TransitionState(num string, from, to forge.DispatchState) error {
	lc := l.r.lifecycle
	status := lc.Statuses[to]
	if to == forge.InProgress {
		iss, issueStatus, err := l.r.readIssue(num)
		if err != nil {
			return err
		}
		if (!lc.StatusOnly && lc.Labels.AlreadyClaimed(from, to, iss.Labels)) || l.alreadyClaimedNative(issueStatus, from) {
			// A label-only tracker reports no status; name the claim label instead.
			held := issueStatus
			if held == "" {
				held = lc.Labels.Label(to)
			}
			return fmt.Errorf("tracker: issue %s: %w (%q)", num, forge.ErrAlreadyClaimed, held)
		}
		if status != "" {
			var stale []string
			for _, label := range forge.SettledLabels(lc.Labels, lc.VerdictLabels) {
				if slices.Contains(iss.Labels, label) {
					stale = append(stale, label)
				}
			}
			if err := l.r.editLabels(num, nil, stale); err != nil {
				return fmt.Errorf("tracker: issue %s: strip settled labels before claim: %w", num, err)
			}
		}
	}

	var res transitionResult
	p := transitionParams{l.r.target, num, transitionRequest{State: to.String(), Mapping: l.mapping()}}
	if err := l.r.call(context.Background(), CapTransition, p, &res); err != nil {
		return err
	}
	removals := forge.TransitionRemoveLabels(lc.Labels, lc.VerdictLabels, from, to)
	switch res.Outcome {
	case transitionMoved:
		// ListIssues matches a state by status or its fallback label, so a stale
		// label must not survive; a cleanup failure must not undo the move.
		if err := l.r.editLabels(num, nil, removals); err != nil {
			fmt.Fprintf(os.Stderr, "    ?? #%s: moved to %s but could not strip stale labels: %v\n", num, to, err)
		}
		return nil
	case transitionUnavailable:
		if lc.StatusOnly {
			return l.refuse(num, &forge.TransitionRefusedError{Issue: num, To: res.Status, Offered: res.Offered})
		}
	case transitionUnmapped:
		if lc.StatusOnly {
			return l.refuse(num, &forge.TransitionRefusedError{Issue: num, State: to.String()})
		}
	}
	toLabel := lc.Labels.Label(to)
	if toLabel == "" {
		return fmt.Errorf("tracker: no status mapping or fallback label configured for state %v", to)
	}
	return l.r.editLabels(num, []string{toLabel}, removals)
}

// mapping is the state-to-status table the Connector resolves a transition
// against, keyed by canonical state name; states naming no status are omitted.
func (l transitionLayer) mapping() map[string]StatusTarget {
	lc := l.r.lifecycle
	m := make(map[string]StatusTarget, len(lc.Statuses))
	for state, status := range lc.Statuses {
		if status != "" {
			m[state.String()] = StatusTarget{Status: status, Resolution: lc.Resolutions[state]}
		}
	}
	return m
}

// refuse fills refusal's From with the status the issue could not leave. A
// failed read leaves From empty rather than replacing the refusal with a
// transport error callers could not tell from a transient fault.
func (l transitionLayer) refuse(num string, refusal *forge.TransitionRefusedError) error {
	if _, status, err := l.r.readIssue(num); err != nil {
		fmt.Fprintf(os.Stderr, "    ?? #%s: could not read the status a refused transition left it in: %v\n", num, err)
	} else {
		refusal.From = status
	}
	return refusal
}

// CompleteVerdict swaps num's InProgress label for verdict's label, first
// asserting the InProgress label is present as a double-dispatch guard (#701);
// the check-then-edit has a TOCTOU window.
func (l transitionLayer) CompleteVerdict(num string, verdict forge.Verdict) error {
	lc := l.r.lifecycle
	add := lc.VerdictLabels.Label(verdict)
	if add == "" {
		return fmt.Errorf("tracker: no label configured for verdict %v", verdict)
	}
	remove := lc.Labels.Label(forge.InProgress)
	if remove != "" {
		iss, _, err := l.r.readIssue(num)
		if err != nil {
			return err
		}
		if !slices.Contains(iss.Labels, remove) {
			return fmt.Errorf("tracker: issue %s: expected %q label, issue has %v", num, remove, iss.Labels)
		}
	}
	return l.r.editLabels(num, []string{add}, []string{remove})
}

// editLabels adds and removes labels in one call, dropping empty names and
// skipping the call when nothing is left to do.
func (r *reads) editLabels(num string, add, remove []string) error {
	p := editLabelsParams{Target: r.target, Number: num, Add: []string{}, Remove: []string{}}
	for _, a := range add {
		if a != "" {
			p.Add = append(p.Add, a)
		}
	}
	for _, rm := range remove {
		if rm != "" {
			p.Remove = append(p.Remove, rm)
		}
	}
	if len(p.Add) == 0 && len(p.Remove) == 0 {
		return nil
	}
	return r.call(context.Background(), CapEditLabels, p, &struct{}{})
}

// labelAddLayer adds labels to an existing issue through the Connector's
// label-edit primitive. It exists whenever the Connector advertises
// CapEditLabels, with or without CapTransition.
type labelAddLayer struct {
	forge.IssueTracker
	r *reads
}

func (l labelAddLayer) Unwrap() forge.IssueTracker { return l.IssueTracker }

func (l labelAddLayer) AddLabels(num string, labels []string) error {
	return l.r.editLabels(num, labels, nil)
}

// labelsLayer serves the label registry from the Connector. It exists only when
// the Connector advertises CapLabels.
type labelsLayer struct {
	forge.IssueTracker
	r *reads
}

func (l labelsLayer) Unwrap() forge.IssueTracker { return l.IssueTracker }

func (l labelsLayer) ListLabels() ([]string, error) {
	var res wireLabels
	if err := l.r.call(context.Background(), CapListLabels, targetParams{l.r.target}, &res); err != nil {
		return nil, err
	}
	return res.Labels, nil
}

func (l labelsLayer) CreateLabel(name, description, color string) error {
	p := createLabelParams{l.r.target, name, description, color}
	return l.r.call(context.Background(), CapCreateLabel, p, &struct{}{})
}

// commentPostLayer posts comments through the Connector. It exists only when
// the Connector advertises CapComment.
type commentPostLayer struct {
	forge.IssueTracker
	r *reads
}

func (l commentPostLayer) Unwrap() forge.IssueTracker { return l.IssueTracker }

// Comment sends body in canonical form: the first line that is exactly the
// research marker is dropped and flagged, the inverse of what commentsLayer
// puts back on read. Later copies (a verdict quoting the marker) stay verbatim.
func (l commentPostLayer) Comment(num, body string) error {
	lines := strings.Split(body, "\n")
	research := false
	for i, line := range lines {
		if strings.TrimSpace(line) == forge.ResearchMarker {
			lines = slices.Delete(lines, i, i+1)
			research = true
			break
		}
	}
	p := commentParams{l.r.target, num, commentRequest{Body: strings.Join(lines, "\n"), Research: research}}
	return l.r.call(context.Background(), CapComment, p, &struct{}{})
}

// filerLayer files host-posted issues through the Connector. It exists only
// when the Connector advertises CapFileIssue.
type filerLayer struct {
	forge.IssueTracker
	r *reads
}

func (l filerLayer) Unwrap() forge.IssueTracker { return l.IssueTracker }

// PostIssue splits the dedup marker off body so the Connector stores it apart
// from the description, and returns the new issue's URL.
func (l filerLayer) PostIssue(title, body string, labels []string) (string, error) {
	route, err := l.r.routeParam()
	if err != nil {
		return "", err
	}
	desc, marker := dedupmarker.Split(body)
	p := fileIssueParams{l.r.target, issueRequest{
		Title: title, Body: desc, Labels: append([]string{}, labels...),
		DedupMarker: marker, IssueType: l.r.lifecycle.IssueType, Route: route,
	}}
	var res wireFiled
	if err := l.r.call(context.Background(), CapFileIssue, p, &res); err != nil {
		return "", err
	}
	return res.URL, nil
}

// unusableFiledURL names why raw cannot be a filed issue's URL, "" when it can:
// an https URL with no userinfo, opaque part, query or fragment whose path is
// exactly the tracker's Lifecycle.IssueURL shape. Filing and the butler turn
// the trailing id into a branch name and PR target, so a same-host URL for
// another repo, a pull request or a deeper path must not pass.
func (t *reads) unusableFiledURL(raw string) string {
	u, err := url.Parse(raw)
	switch {
	case raw == "":
		return "no url"
	case err != nil:
		return "not a url"
	case u.Scheme != "https":
		return "scheme is not https"
	case u.User != nil:
		return "url carries userinfo"
	case u.Opaque != "" || u.RawQuery != "" || u.ForceQuery || u.Fragment != "":
		return "url carries an opaque part, query or fragment"
	}
	if t.issueHost == "" {
		return "tracker has no issue url shape"
	}
	if !strings.EqualFold(u.Host, t.issueHost) {
		return fmt.Sprintf("host is not %q", t.issueHost)
	}
	rest, ok := cutPrefixFold(u.Path, t.issuePrefix)
	if !ok || rest == "" || strings.Contains(rest, "/") || !t.ids.valid(path.Base(u.Path)) {
		return "path is not an issue of this tracker"
	}
	return ""
}

// parseIssueURL splits an IssueURL template, with {repo} filled in, into its
// host and the path before the trailing {n}. An empty template yields empty
// values: it is allowed, and refuses every filed URL.
func parseIssueURL(template, repo string) (host, prefix string, err error) {
	if template == "" {
		return "", "", nil
	}
	u, err := url.Parse(strings.ReplaceAll(template, "{repo}", repo))
	if err != nil {
		return "", "", fmt.Errorf("issue url template %q does not parse: %w", template, err)
	}
	if u.Host == "" {
		return "", "", fmt.Errorf("issue url template %q has no host", template)
	}
	prefix, ok := strings.CutSuffix(u.Path, "{n}")
	if !ok {
		return "", "", fmt.Errorf("issue url template %q does not end in {n}", template)
	}
	return u.Host, prefix, nil
}

// cutPrefixFold is strings.CutPrefix ignoring case, as trackers answer URLs in
// a different case than the template (a repo path, a host).
func cutPrefixFold(s, prefix string) (string, bool) {
	if len(s) < len(prefix) || !strings.EqualFold(s[:len(prefix)], prefix) {
		return "", false
	}
	return s[len(prefix):], true
}

// closerLayer closes a merged issue through the Connector. It exists only when
// the Connector advertises CapCloseIssue; the Connector's close is
// unconditional, so the already-closed no-op the interface promises is core's
// own read.
type closerLayer struct {
	forge.IssueTracker
	r *reads
}

func (l closerLayer) Unwrap() forge.IssueTracker { return l.IssueTracker }

func (l closerLayer) CloseMergedIssue(num string) error {
	iss, err := l.r.Issue(num)
	if err != nil {
		return err
	}
	if iss.State == forge.IssueClosed {
		return nil
	}
	return l.r.call(context.Background(), CapCloseIssue, closeIssueParams{l.r.target, num}, &struct{}{})
}

var (
	_ forge.Unwrapper            = transitionLayer{}
	_ forge.Unwrapper            = labelAddLayer{}
	_ forge.IssueLabeler         = labelAddLayer{}
	_ forge.Unwrapper            = labelsLayer{}
	_ forge.Unwrapper            = commentPostLayer{}
	_ forge.HostPostedCommenter  = commentPostLayer{}
	_ forge.Unwrapper            = filerLayer{}
	_ forge.HostPostedIssueFiler = filerLayer{}
	_ forge.Unwrapper            = closerLayer{}
	_ forge.MergeCloser          = closerLayer{}
)
