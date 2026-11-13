// Package forge is the Connector author's side of the forge primitives. A
// Connector declares a Reader and Register exports it over the Connector
// protocol; it declares Branches (remote identity, branch exists, branch
// delete) and RegisterBranches exports those; and it declares PRs (open,
// behind, files, draft flips, comments, merge) and RegisterPRs exports those.
//
// A method is in the contract only when it needs the forge's API: core does
// every git operation itself, and derives whatever it can. The Connector
// returns raw fields only; core makes every policy decision (which
// conclusions fail, rendering and capping failure detail, PR URL validation).
// Auto-merge, the CI run link, branch protection, the forge probe and the token
// grant are optional primitives, each a handshake capability a Connector
// declares by setting its funcs.
package forge

import (
	"encoding/json"
	"fmt"
	"unicode/utf8"

	"spindrift.dev/sdk/connector"
)

// Target names the forge repository a call reads from. API is the https base
// URL without a trailing slash, for example https://api.github.com.
type Target struct {
	API  string `json:"api"`
	Repo string `json:"repo"`
}

// PR is one pull request. State is "open" or "closed" as the forge reports it;
// Merged is true for a merged (hence closed) PR. URL is the PR's web URL.
//
// The fields after Merged are read by a PR-capable forge (CapPRs); a
// Reader-only Connector may leave them zero. Head is the head branch name.
// Mergeable is the raw "MERGEABLE", "CONFLICTING" or "UNKNOWN"; core fails
// closed on anything else. MergeCommit is the commit a merged PR landed as,
// "" when none.
type PR struct {
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

// Check is one context of a head commit's status-check rollup, in GitHub
// GraphQL's vocabulary (provisional): Kind is "CheckRun" or "StatusContext";
// Conclusion is a CheckRun's conclusion or a StatusContext's state; Summary is
// a CheckRun's summary or a StatusContext's description. Name and Summary are
// untrusted text.
type Check struct {
	Kind       string `json:"kind"`
	Name       string `json:"name"`
	Conclusion string `json:"conclusion"`
	Summary    string `json:"summary"`
	// JobID names the job whose log JobLog reads; "" when the context has none
	// (a StatusContext, or a CheckRun that is not a workflow job).
	JobID string `json:"jobId,omitempty"`
}

// Rollup is a PR head commit's status-check rollup. State is the aggregate
// rollup state (for example "SUCCESS", "PENDING" or "FAILURE"), "" when the
// commit has no checks.
type Rollup struct {
	State  string  `json:"state"`
	Checks []Check `json:"checks"`
}

// Reader is everything a forge Connector reads.
type Reader struct {
	// PRsForBranch returns every PR whose head is branch, any state, newest
	// first.
	PRsForBranch func(t Target, branch string) ([]PR, error)

	// PR returns one PR by number.
	PR func(t Target, number string) (PR, error)

	// Checks returns the status-check rollup of the PR's head commit.
	Checks func(t Target, number string) (Rollup, error)

	// JobLog returns the raw log of the job a Check's JobID names. It is
	// optional: a Connector that leaves it nil does not register
	// forge.jobLog, and core renders failure detail without logs. The method
	// cuts the result to the tail core asks for, so JobLog may return the
	// whole log.
	JobLog func(t Target, jobID string) (string, error)
}

type method = func(json.RawMessage) (any, error)

// Register exports r as the Connector's forge reads. Call it from init(). It
// panics if any Reader field is unset.
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

// numbered decodes the {target, number} params shared by the per-PR reads.
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
	case r.PRsForBranch == nil:
		panic("forge: Reader.PRsForBranch is required")
	case r.PR == nil:
		panic("forge: Reader.PR is required")
	case r.Checks == nil:
		panic("forge: Reader.Checks is required")
	}

	m := map[string]method{
		"forge.prsForBranch": func(params json.RawMessage) (any, error) {
			var p struct {
				Target Target `json:"target"`
				Branch string `json:"branch"`
			}
			if err := unmarshal(params, &p); err != nil {
				return nil, err
			}
			prs, err := r.PRsForBranch(p.Target, p.Branch)
			if err != nil {
				return nil, err
			}
			if prs == nil {
				prs = []PR{} // encode [] rather than null
			}
			return struct {
				PRs []PR `json:"prs"`
			}{prs}, nil
		},
		"forge.pr": func(params json.RawMessage) (any, error) {
			t, n, err := numbered(params)
			if err != nil {
				return nil, err
			}
			return r.PR(t, n)
		},
		"forge.checks": func(params json.RawMessage) (any, error) {
			t, n, err := numbered(params)
			if err != nil {
				return nil, err
			}
			ro, err := r.Checks(t, n)
			if err != nil {
				return nil, err
			}
			if ro.Checks == nil {
				ro.Checks = []Check{} // encode [] rather than null
			}
			return ro, nil
		},
	}
	if r.JobLog != nil {
		m["forge.jobLog"] = func(params json.RawMessage) (any, error) {
			var p struct {
				Target Target `json:"target"`
				JobID  string `json:"jobId"`
				Tail   int    `json:"tail"`
			}
			if err := unmarshal(params, &p); err != nil {
				return nil, err
			}
			log, err := r.JobLog(p.Target, p.JobID)
			if err != nil {
				return nil, err
			}
			return struct {
				Log string `json:"log"`
			}{tailBytes(log, p.Tail)}, nil
		}
	}
	return []string{"forge"}, m
}

// tailBytes returns s's last n bytes, starting on a rune boundary; n <= 0
// means no cut. Core asks for a tail so a long log cannot outgrow the call's
// output cap once JSON-escaped.
func tailBytes(s string, n int) string {
	if n <= 0 || len(s) <= n {
		return s
	}
	s = s[len(s)-n:]
	for len(s) > 0 && !utf8.RuneStart(s[0]) {
		s = s[1:]
	}
	return s
}
