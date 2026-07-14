// Package console is the Elm-architecture core of the `console` subcommand
// (issue #645): a pure Model/Update/View, fed by a thin adapter that turns
// IssueTracker results into Msg values. The dependency arrow is one-way —
// engine packages (forge, waves, dispatch, settle, runner) never import
// console.
package console

import (
	"strings"

	"spindrift.dev/launcher/internal/forge"
)

// Model is the console's whole state: the unfiltered backlog, the active
// label filter, and whether the operator has asked to quit. Update is the
// only function that produces a new Model; View is the only function that
// renders one.
type Model struct {
	All      []forge.Issue
	Filter   string
	Quitting bool
	// Err is the last refresh error, if any. A failed refresh leaves All
	// untouched — Err surfaces alongside the stale list rather than
	// replacing it with an empty one.
	Err error
	// DogfoodLive is whether a live dogfood pid-file was found at startup —
	// informational only, set once and never gated on.
	DogfoodLive bool
}

// NewModel returns the zero-value console state: no issues loaded yet, no
// filter, not quitting.
func NewModel() Model {
	return Model{}
}

// Visible returns All narrowed by Filter — the list View renders. An empty
// Filter returns All unchanged.
func (m Model) Visible() []forge.Issue {
	if m.Filter == "" {
		return m.All
	}
	var out []forge.Issue
	for _, iss := range m.All {
		if issueHasLabelContaining(iss, m.Filter) {
			out = append(out, iss)
		}
	}
	return out
}

// Update applies msg to m and returns the resulting Model. It is pure: no
// I/O, no network — the adapter and run loop are the only callers that touch
// either, translating their results into a Msg before calling Update.
func Update(m Model, msg Msg) Model {
	switch msg := msg.(type) {
	case IssuesLoadedMsg:
		m.Err = msg.Err
		if msg.Err == nil {
			m.All = msg.Issues
		}
	case FilterChangedMsg:
		m.Filter = msg.Filter
	case QuitMsg:
		m.Quitting = true
	case DogfoodNoticeMsg:
		m.DogfoodLive = msg.Live
	}
	return m
}

// issueHasLabelContaining reports whether any of iss's labels contains
// substr, case-insensitively — the match rule behind Filter, chosen so the
// filter narrows as the operator types rather than requiring an exact label.
func issueHasLabelContaining(iss forge.Issue, substr string) bool {
	substr = strings.ToLower(substr)
	for _, l := range iss.Labels {
		if strings.Contains(strings.ToLower(l), substr) {
			return true
		}
	}
	return false
}
