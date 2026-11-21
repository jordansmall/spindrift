package main

import (
	"fmt"
	"slices"

	"spindrift.dev/sdk/tracker"
)

// routeTerm is one validated route selector: its kind, the Jira field it
// narrows (labels or component) and the value it must hold.
type routeTerm struct{ kind, field, value string }

// routeTerms validates route and drops a repeated selector, keeping selector
// order. A kind Jira cannot honour is an error: dropping it would widen a read
// to the whole scope and file an issue outside the route.
func routeTerms(route []tracker.Selector) ([]routeTerm, error) {
	var terms []routeTerm
	for _, sel := range route {
		var field string
		switch sel.Kind {
		case "label":
			field = "labels"
		case "component":
			field = "component"
		default:
			return nil, fmt.Errorf("jira: unsupported route selector kind %q (want \"label\" or \"component\")", sel.Kind)
		}
		if term := (routeTerm{sel.Kind, field, sel.Value}); !slices.Contains(terms, term) {
			terms = append(terms, term)
		}
	}
	return terms, nil
}
