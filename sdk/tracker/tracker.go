// Package tracker is the Connector author's side of the tracker read
// primitives of Connector protocol 0.2: a Connector declares a Reader and
// Register exports it over the Connector protocol.
//
// Every write stays on core's native adapter. The Connector returns data only
// (raw fields such as minimized, association and createdAt); core makes every
// policy decision. That includes the markers: a Connector whose tracker keeps
// the dedup or research marker in a side store returns it as a field
// (Issue.DedupMarker, Comment.ResearchMarker) and never splices it into a
// body; core restores it. The optional reads are Comments, NativeDeps (the
// issues an issue is blocked on) and Blocks (the issues it blocks). The
// optional doctor and daemon reads are Probe, Statuses, CreateMeta and Count
// with its CountInterval; each returns raw API data and core decides what it
// means.
package tracker

import (
	"encoding/json"
	"fmt"
	"time"

	"spindrift.dev/sdk/connector"
)

// Target names the tracker repository a call reads from. API is the https
// base URL without a trailing slash, for example https://api.github.com.
// Dialect names the tracker deployment's API dialect (a Jira Connector takes
// "cloud" or "server"); it is "" for a tracker with one.
type Target struct {
	API     string `json:"api"`
	Repo    string `json:"repo"`
	Dialect string `json:"dialect"`
}

// Query selects the open issues a ListIssues call returns. They are the
// selectors core resolved from a Dispatch state: a Connector matches an open
// issue carrying Label OR in Status, ignoring an empty one. Both empty means
// every open issue.
type Query struct {
	Label  string `json:"label"`
	Status string `json:"status"`
}

// Issue is one tracker issue. State is "open" or "closed".
type Issue struct {
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

// Comment is one issue comment. CreatedAt is RFC3339 as the forge reports it.
type Comment struct {
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

// IDGrammar names the shape of the issue ids a Connector returns. Core checks
// every id against it and refuses any outside.
type IDGrammar int

const (
	// DecimalIDs is the default: positive canonical decimal integers.
	DecimalIDs IDGrammar = iota
	// ProjectKeyIDs are "ENG-12": an upper-case project key, a hyphen and a
	// positive canonical integer. It advertises "tracker.ids.projectKey".
	ProjectKeyIDs
)

// CreateMeta is a project's create metadata for one issue type.
type CreateMeta struct {
	// IssueTypes names every issue type the project offers.
	IssueTypes []string `json:"issueTypes"`
	// Fields are the create fields of the issue type asked for, empty when the
	// project does not offer it.
	Fields []CreateField `json:"fields"`
}

// CreateField is one field of an issue type's create form.
type CreateField struct {
	ID              string `json:"fieldId"`
	Name            string `json:"name"`
	Required        bool   `json:"required"`
	HasDefaultValue bool   `json:"hasDefaultValue"`
}

// Reader is everything a tracker Connector reads.
type Reader struct {
	// IDs declares the grammar of every issue number and dependency id the
	// Connector returns. The zero value is DecimalIDs.
	IDs IDGrammar

	// ListIssues returns up to limit open issues matching q.
	ListIssues func(t Target, q Query, limit int) ([]Issue, error)

	// Issue returns one issue.
	Issue func(t Target, number string) (Issue, error)

	// Comments is optional: it returns an issue's comments, oldest first.
	// Setting it advertises "tracker.comments", which attests that each
	// comment's Minimized and MinimizedReason are reported faithfully; core
	// drops minimized comments on that flag. A Connector whose API cannot
	// report minimized state leaves it nil, and core reads comments from its
	// native adapter instead.
	Comments func(t Target, number string) ([]Comment, error)

	// NativeDeps is optional: it returns the ids of the issues number blocks
	// on, in the API's order. Leaving it nil advertises no
	// "tracker.nativeDeps" capability.
	NativeDeps func(t Target, number string) ([]string, error)

	// Blocks is optional: it returns the ids of the issues number blocks, in
	// the API's order, the reverse of NativeDeps. Leaving it nil advertises no
	// "tracker.blocks" capability.
	Blocks func(t Target, number string) ([]string, error)

	// Probe is optional: one authenticated read that checks the credential and
	// that the tracker is reachable. It returns the HTTP status rather than
	// failing on a non-2xx, so core decides what an auth failure is; err is for
	// a transport failure only. Setting it advertises "tracker.probe".
	Probe func(t Target) (status int, err error)

	// Statuses is optional: it returns the names of every status in the
	// project's workflows, any issue type's. Core checks its status mapping
	// against them. Setting it advertises "tracker.statuses".
	Statuses func(t Target) ([]string, error)

	// CreateMeta is optional: it returns the project's create metadata for
	// issueType. Setting it advertises "tracker.createMeta".
	CreateMeta func(t Target, issueType string) (CreateMeta, error)

	// Count is optional: it returns how many open issues match q, the same set
	// ListIssues(q) would return, read without walking them. It may be
	// approximate where the API only offers an approximate count; a demand
	// probe tolerates lag. Setting it advertises "tracker.count" and requires
	// CountInterval.
	Count func(t Target, q Query) (int, error)

	// CountInterval is how often core re-asks Count, chosen for the API's rate
	// limits. It is required, at least one second, when Count is set.
	CountInterval time.Duration

	// Search is optional: it returns up to limit issues matching the free text
	// text, narrowed to state ("open" or "closed") and, when label is not "",
	// to issues carrying it. Leaving it nil advertises no
	// "tracker.searchIssues" capability, and core searches through its native
	// adapter instead.
	Search func(t Target, text, state, label string, limit int) ([]Issue, error)
}

type method = func(json.RawMessage) (any, error)

// Register exports r as the Connector's tracker reads. Call it from init().
// It panics if ListIssues or Issue is unset.
func Register(r Reader) {
	caps, methods := handlers(r)
	connector.Register(caps...)
	for name, fn := range methods {
		connector.Handle(name, fn)
	}
}

func unmarshal(params json.RawMessage, v any) error {
	if err := json.Unmarshal(params, v); err != nil {
		return fmt.Errorf("bad params: %w", err)
	}
	return nil
}

// numbered decodes the {target, number} params shared by the per-issue reads.
func numbered(params json.RawMessage) (Target, string, error) {
	var p struct {
		Target Target `json:"target"`
		Number string `json:"number"`
	}
	err := unmarshal(params, &p)
	return p.Target, p.Number, err
}

// targetOnly decodes the {target} params of the project-level reads.
func targetOnly(params json.RawMessage) (Target, error) {
	var p struct {
		Target Target `json:"target"`
	}
	err := unmarshal(params, &p)
	return p.Target, err
}

// handlers builds the capability list and method table for r.
func handlers(r Reader) ([]string, map[string]method) {
	switch {
	case r.ListIssues == nil:
		panic("tracker: Reader.ListIssues is required")
	case r.Issue == nil:
		panic("tracker: Reader.Issue is required")
	case r.Count != nil && r.CountInterval < time.Second:
		panic("tracker: Reader.CountInterval of at least one second is required with Count")
	}

	m := map[string]method{
		"tracker.listIssues": func(params json.RawMessage) (any, error) {
			var p struct {
				Target Target `json:"target"`
				Query
				Limit int `json:"limit"`
			}
			if err := unmarshal(params, &p); err != nil {
				return nil, err
			}
			issues, err := r.ListIssues(p.Target, p.Query, p.Limit)
			if err != nil {
				return nil, err
			}
			return issueList(issues), nil
		},
		"tracker.issue": func(params json.RawMessage) (any, error) {
			t, n, err := numbered(params)
			if err != nil {
				return nil, err
			}
			i, err := r.Issue(t, n)
			if err != nil {
				return nil, err
			}
			return normalize(i), nil
		},
	}
	caps := []string{"tracker"}

	if r.Comments != nil {
		caps = append(caps, "tracker.comments")
		m["tracker.comments"] = func(params json.RawMessage) (any, error) {
			t, n, err := numbered(params)
			if err != nil {
				return nil, err
			}
			cs, err := r.Comments(t, n)
			if err != nil {
				return nil, err
			}
			if cs == nil {
				cs = []Comment{} // encode [] rather than null
			}
			return struct {
				Comments []Comment `json:"comments"`
			}{cs}, nil
		}
	}

	for _, c := range []struct {
		name string
		fn   func(Target, string) ([]string, error)
	}{{"tracker.nativeDeps", r.NativeDeps}, {"tracker.blocks", r.Blocks}} {
		if c.fn == nil {
			continue
		}
		caps = append(caps, c.name)
		m[c.name] = idList(c.fn)
	}
	if r.Probe != nil {
		caps = append(caps, "tracker.probe")
		m["tracker.probe"] = func(params json.RawMessage) (any, error) {
			t, err := targetOnly(params)
			if err != nil {
				return nil, err
			}
			status, err := r.Probe(t)
			if err != nil {
				return nil, err
			}
			return struct {
				Status int `json:"status"`
			}{status}, nil
		}
	}
	if r.Statuses != nil {
		caps = append(caps, "tracker.statuses")
		m["tracker.statuses"] = func(params json.RawMessage) (any, error) {
			t, err := targetOnly(params)
			if err != nil {
				return nil, err
			}
			names, err := r.Statuses(t)
			if err != nil {
				return nil, err
			}
			if names == nil {
				names = []string{} // encode [] rather than null
			}
			return struct {
				Statuses []string `json:"statuses"`
			}{names}, nil
		}
	}
	if r.CreateMeta != nil {
		caps = append(caps, "tracker.createMeta")
		m["tracker.createMeta"] = func(params json.RawMessage) (any, error) {
			var p struct {
				Target    Target `json:"target"`
				IssueType string `json:"issueType"`
			}
			if err := unmarshal(params, &p); err != nil {
				return nil, err
			}
			cm, err := r.CreateMeta(p.Target, p.IssueType)
			if err != nil {
				return nil, err
			}
			if cm.IssueTypes == nil {
				cm.IssueTypes = []string{} // encode [] rather than null
			}
			if cm.Fields == nil {
				cm.Fields = []CreateField{}
			}
			return cm, nil
		}
	}
	if r.Count != nil {
		caps = append(caps, "tracker.count")
		m["tracker.count"] = func(params json.RawMessage) (any, error) {
			var p struct {
				Target Target `json:"target"`
				Query
			}
			if err := unmarshal(params, &p); err != nil {
				return nil, err
			}
			n, err := r.Count(p.Target, p.Query)
			if err != nil {
				return nil, err
			}
			return struct {
				Count int `json:"count"`
			}{n}, nil
		}
		// The cadence is the Connector's, not a repo's: no target.
		m["tracker.countInterval"] = func(json.RawMessage) (any, error) {
			return struct {
				Seconds int `json:"seconds"`
			}{int(r.CountInterval / time.Second)}, nil
		}
	}
	if r.Search != nil {
		caps = append(caps, "tracker.searchIssues")
		m["tracker.searchIssues"] = func(params json.RawMessage) (any, error) {
			var p struct {
				Target Target `json:"target"`
				Text   string `json:"text"`
				State  string `json:"state"`
				Label  string `json:"label"`
				Limit  int    `json:"limit"`
			}
			if err := unmarshal(params, &p); err != nil {
				return nil, err
			}
			issues, err := r.Search(p.Target, p.Text, p.State, p.Label, p.Limit)
			if err != nil {
				return nil, err
			}
			return issueList(issues), nil
		}
	}
	if r.IDs == ProjectKeyIDs {
		caps = append(caps, "tracker.ids.projectKey")
	}
	return caps, m
}

// idList serves an {ids} method over fn.
func idList(fn func(Target, string) ([]string, error)) method {
	return func(params json.RawMessage) (any, error) {
		t, n, err := numbered(params)
		if err != nil {
			return nil, err
		}
		ids, err := fn(t, n)
		if err != nil {
			return nil, err
		}
		if ids == nil {
			ids = []string{} // encode [] rather than null
		}
		return struct {
			IDs []string `json:"ids"`
		}{ids}, nil
	}
}

// issueList is the {"issues": [...]} reply of the issue-list reads.
func issueList(issues []Issue) any {
	out := make([]Issue, 0, len(issues)) // encode [] rather than null
	for _, i := range issues {
		out = append(out, normalize(i))
	}
	return struct {
		Issues []Issue `json:"issues"`
	}{out}
}

// normalize makes Labels encode as [] rather than null.
func normalize(i Issue) Issue {
	if i.Labels == nil {
		i.Labels = []string{}
	}
	return i
}
