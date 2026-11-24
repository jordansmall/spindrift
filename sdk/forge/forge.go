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
	"regexp"
	"strings"
	"unicode/utf8"

	"spindrift.dev/sdk/connector"
)

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
		methodPrsForBranch: func(params json.RawMessage) (any, error) {
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
		methodPr: func(params json.RawMessage) (any, error) {
			t, n, err := numbered(params)
			if err != nil {
				return nil, err
			}
			return r.PR(t, n)
		},
		methodChecks: func(params json.RawMessage) (any, error) {
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
		m[methodJobLog] = func(params json.RawMessage) (any, error) {
			var p struct {
				Target Target `json:"target"`
				JobID  string `json:"jobId"`
				Tail   int    `json:"tail"`
				Errors int    `json:"errorLines"`
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
			}{tailBytes(log, p.Tail, p.Errors)}, nil
		}
	}
	return []string{capForge}, m
}

// errorLineRE mirrors the launcher's forge.errorLineRE: the SDK is its own
// module and cannot import it, and the two must pick the same lines; the
// shared PRForge contract (forgetest.testFailureDetailOnFailingCheck) pins
// that on both.
var errorLineRE = regexp.MustCompile(`\bFAIL\b|\bpanic:|(?i:\berror:)`)

// tailBytes returns s's last n bytes, starting on a rune boundary; n <= 0
// means no cut. Core asks for a tail so a long log cannot outgrow the call's
// output cap once JSON-escaped. When it cuts, up to errBudget bytes of the
// dropped head's whole error lines lead the tail, so a failure's cause near
// the start of the log survives; errBudget <= 0 keeps the plain tail.
func tailBytes(s string, n, errBudget int) string {
	if n <= 0 || len(s) <= n {
		return s
	}
	tail := s[len(s)-n:]
	for len(tail) > 0 && !utf8.RuneStart(tail[0]) {
		tail = tail[1:]
	}
	head := s[:len(s)-len(tail)]
	head = head[:strings.LastIndexByte(head, '\n')+1] // whole lines only
	var b strings.Builder
	for errBudget > 0 && head != "" {
		i := strings.IndexByte(head, '\n')
		line := head[:i+1]
		head = head[i+1:]
		if len(line) <= errBudget && errorLineRE.MatchString(line) {
			b.WriteString(line)
			errBudget -= len(line)
		}
	}
	return b.String() + tail
}
