package forge

import (
	"fmt"
	"slices"
	"strings"
)

// RouteKindLabel is the one route selector kind a label-based tracker
// understands: the issue carries the label.
const RouteKindLabel = "label"

// RouteSelector is one {kind, value} narrowing of a Tracker scope. It mirrors
// sdk/tracker.Selector, which cmd/launcher cannot import.
type RouteSelector struct {
	Kind  string
	Value string
}

// Route is an Issue route (ADR 0075): selectors that must ALL match for an
// issue to belong to the Target repo. Nil means the whole Tracker scope.
type Route []RouteSelector

// ParseRoute parses ISSUE_ROUTE's form, comma-joined kind=value pairs such as
// "label=orders-api". The empty string is the nil route. A repeated kind
// ("label=a,label=b") is accepted and ANDed like any other selectors, which the
// flake attrset form cannot express.
func ParseRoute(s string) (Route, error) {
	if strings.TrimSpace(s) == "" {
		return nil, nil
	}
	var r Route
	for _, pair := range strings.Split(s, ",") {
		kind, value, ok := strings.Cut(pair, "=")
		kind, value = strings.TrimSpace(kind), strings.TrimSpace(value)
		if !ok || kind == "" || value == "" {
			return nil, fmt.Errorf("issue route %q: %q is not kind=value", s, strings.TrimSpace(pair))
		}
		r = append(r, RouteSelector{Kind: kind, Value: value})
	}
	return r, nil
}

// Labels returns the values of the label-kind selectors: what a label-based
// tracker filters on and stamps onto an issue it files.
func (r Route) Labels() []string {
	var out []string
	for _, s := range r {
		if s.Kind == RouteKindLabel {
			out = append(out, s.Value)
		}
	}
	return out
}

// MatchesLabels reports whether labels carry every selector; an empty route
// matches everything. A non-label selector fails closed: a label-based
// tracker cannot evaluate it, and matching would list the whole Tracker scope.
func (r Route) MatchesLabels(labels []string) bool {
	for _, s := range r {
		if s.Kind != RouteKindLabel || !slices.Contains(labels, s.Value) {
			return false
		}
	}
	return true
}

// ResolveRoute parses raw (ISSUE_ROUTE) and checks every selector kind against
// the kinds tracker declares, wrapping any error with the knob name.
func ResolveRoute(raw, tracker string, kinds []string) (Route, error) {
	r, err := ParseIssueRoute(raw)
	if err != nil {
		return nil, err
	}
	if err := r.CheckKinds(tracker, kinds); err != nil {
		return nil, fmt.Errorf("ISSUE_ROUTE: %w", err)
	}
	return r, nil
}

// ParseIssueRoute is ParseRoute with the knob name on its error, for a caller
// that has no tracker row to check kinds against.
func ParseIssueRoute(raw string) (Route, error) {
	r, err := ParseRoute(raw)
	if err != nil {
		return nil, fmt.Errorf("ISSUE_ROUTE: %w", err)
	}
	return r, nil
}

// StampLabels returns labels plus any route label not already present. It
// returns labels itself, not a copy, when there is nothing to add.
func (r Route) StampLabels(labels []string) []string {
	out := labels
	for _, l := range r.Labels() {
		if !slices.Contains(out, l) {
			out = append(slices.Clip(out), l)
		}
	}
	return out
}

// CheckKinds rejects a selector whose kind tracker has not declared
// (backend.Descriptor.RouteKinds): the tracker would ignore it and quietly
// list its whole scope.
func (r Route) CheckKinds(tracker string, declared []string) error {
	for _, s := range r {
		if !slices.Contains(declared, s.Kind) {
			have := "none"
			if len(declared) > 0 {
				have = strings.Join(declared, ", ")
			}
			return fmt.Errorf("route kind %q is not declared by ISSUE_TRACKER=%s (declares: %s)", s.Kind, tracker, have)
		}
	}
	return nil
}
