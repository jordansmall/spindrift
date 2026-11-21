package forge

import (
	"fmt"
	"slices"
	"strings"
)

// RouteKindLabel is the one route selector kind a label-family tracker
// understands: the issue carries the label.
const RouteKindLabel = "label"

// RouteSelector is one {kind, value} narrowing of a tracker scope. It mirrors
// sdk/tracker.Selector, which cmd/launcher cannot import.
type RouteSelector struct {
	Kind  string
	Value string
}

// Route is an Issue route (ADR 0075): selectors that must ALL match for an
// issue to belong to the target repo. Nil means the whole tracker scope.
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

// Labels returns the values of the label-kind selectors: what a label-family
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

// MatchesLabels reports whether labels carry every label-kind selector; a
// route with none matches everything.
func (r Route) MatchesLabels(labels []string) bool {
	for _, l := range r.Labels() {
		if !slices.Contains(labels, l) {
			return false
		}
	}
	return true
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
