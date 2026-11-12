// Package tracker is the Connector author's side of the provisional tracker
// read primitives: a Connector declares a Reader and Register exports it over
// the Connector protocol. The primitives are provisional at protocol 0.y;
// issue #6753 finalises the tracker axis.
//
// Every write stays on core's native adapter. The Connector returns data only
// (raw fields such as minimized, association and createdAt); core makes every
// policy decision.
package tracker

import (
	"encoding/json"
	"fmt"

	"spindrift.dev/sdk/connector"
)

// Target names the tracker repository a call reads from. API is the https
// base URL without a trailing slash, for example https://api.github.com.
type Target struct {
	API  string `json:"api"`
	Repo string `json:"repo"`
}

// Issue is one tracker issue. State is "open" or "closed".
type Issue struct {
	Number string   `json:"number"`
	Title  string   `json:"title"`
	Body   string   `json:"body"`
	State  string   `json:"state"`
	Labels []string `json:"labels"`
}

// Comment is one issue comment. CreatedAt is RFC3339 as the forge reports it.
type Comment struct {
	Author          string `json:"author"`
	CreatedAt       string `json:"createdAt"`
	Body            string `json:"body"`
	Minimized       bool   `json:"minimized"`
	MinimizedReason string `json:"minimizedReason"`
	Association     string `json:"association"`
}

// Reader is everything a tracker Connector reads.
type Reader struct {
	// ListIssues returns up to limit open issues carrying label; "" means
	// every open issue.
	ListIssues func(t Target, label string, limit int) ([]Issue, error)

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

// handlers builds the capability list and method table for r.
func handlers(r Reader) ([]string, map[string]method) {
	switch {
	case r.ListIssues == nil:
		panic("tracker: Reader.ListIssues is required")
	case r.Issue == nil:
		panic("tracker: Reader.Issue is required")
	}

	m := map[string]method{
		"tracker.listIssues": func(params json.RawMessage) (any, error) {
			var p struct {
				Target Target `json:"target"`
				Label  string `json:"label"`
				Limit  int    `json:"limit"`
			}
			if err := unmarshal(params, &p); err != nil {
				return nil, err
			}
			issues, err := r.ListIssues(p.Target, p.Label, p.Limit)
			if err != nil {
				return nil, err
			}
			out := make([]Issue, 0, len(issues)) // encode [] rather than null
			for _, i := range issues {
				out = append(out, normalize(i))
			}
			return struct {
				Issues []Issue `json:"issues"`
			}{out}, nil
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

	if r.NativeDeps != nil {
		caps = append(caps, "tracker.nativeDeps")
		m["tracker.nativeDeps"] = func(params json.RawMessage) (any, error) {
			t, n, err := numbered(params)
			if err != nil {
				return nil, err
			}
			ids, err := r.NativeDeps(t, n)
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
	return caps, m
}

// normalize makes Labels encode as [] rather than null.
func normalize(i Issue) Issue {
	if i.Labels == nil {
		i.Labels = []string{}
	}
	return i
}
