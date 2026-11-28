package connectorconformance

import (
	"context"
	"errors"
	"slices"
	"strconv"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/connectorforge/connectorforgetest"
	"spindrift.dev/launcher/internal/connectorhost"
	"spindrift.dev/launcher/internal/connectorprotocol"
	"spindrift.dev/launcher/internal/connectorprotocol/schematest"
	"spindrift.dev/launcher/internal/connectortest"
	"spindrift.dev/launcher/internal/connectortracker/connectortrackertest"
	"spindrift.dev/launcher/internal/forge"
)

// sampleParams is the params of one call to method against the scripted
// backend, which has the issue or PR number and its branch agent/issue-<number>.
// api is the backend's base URL; the forge target omits the tracker's dialect.
// A method the table lacks is a protocol addition the conformance suite has
// not learned, which TestSampleParamsCoverTheProtocol fails.
func sampleParams(method, api, number string) (any, bool) {
	forgeTarget := map[string]any{"api": api, "repo": connectortest.Repo}
	trackerTarget := map[string]any{"api": api, "repo": connectortest.Repo, "dialect": ""}
	with := func(target map[string]any, extra map[string]any) any {
		p := map[string]any{"target": target}
		for k, v := range extra {
			p[k] = v
		}
		return p
	}
	branch := "agent/issue-" + number
	switch method {
	case connectorprotocol.MethodForgePrsForBranch, connectorprotocol.MethodForgeBranchExists,
		connectorprotocol.MethodForgeDeleteBranch, connectorprotocol.MethodForgeBranchProtected:
		return with(forgeTarget, map[string]any{"branch": branch}), true
	case connectorprotocol.MethodForgePr, connectorprotocol.MethodForgeChecks, connectorprotocol.MethodForgeBehind,
		connectorprotocol.MethodForgePrFiles, connectorprotocol.MethodForgeMerge,
		connectorprotocol.MethodForgeEnqueueAutoMerge, connectorprotocol.MethodForgeCiRun, connectorprotocol.MethodForgeRerunFailedCI:
		return with(forgeTarget, map[string]any{"number": number}), true
	case connectorprotocol.MethodForgeJobLog:
		return with(forgeTarget, map[string]any{"jobId": "1", "tail": 1024, "errorLines": 256}), true
	case connectorprotocol.MethodForgeSetDraft:
		return with(forgeTarget, map[string]any{"number": number, "draft": true}), true
	case connectorprotocol.MethodForgeComment:
		return with(forgeTarget, map[string]any{"number": number, "body": "conformance"}), true
	case connectorprotocol.MethodForgeAutoMergeAllowed, connectorprotocol.MethodForgeProbe:
		return with(forgeTarget, nil), true
	case connectorprotocol.MethodForgeOpenPR:
		return with(forgeTarget, map[string]any{"pr": map[string]any{
			"title": "conformance", "body": "", "base": "main", "head": "agent/conformance-" + number, "draft": false,
		}}), true
	case connectorprotocol.MethodForgeTokenGrant:
		return with(forgeTarget, map[string]any{"tokenPrefix": "ghs_"}), true

	case connectorprotocol.MethodTrackerListIssues:
		return with(trackerTarget, map[string]any{"label": "ready-for-agent", "status": "", "limit": 10}), true
	case connectorprotocol.MethodTrackerIssue, connectorprotocol.MethodTrackerComments,
		connectorprotocol.MethodTrackerNativeDeps, connectorprotocol.MethodTrackerBlocks,
		connectorprotocol.MethodTrackerRemovedLabels, connectorprotocol.MethodTrackerCloseIssue:
		return with(trackerTarget, map[string]any{"number": number}), true
	case connectorprotocol.MethodTrackerProbe, connectorprotocol.MethodTrackerStatuses,
		connectorprotocol.MethodTrackerListLabels:
		return with(trackerTarget, nil), true
	case connectorprotocol.MethodTrackerCreateMeta:
		return with(trackerTarget, map[string]any{"issueType": "Task"}), true
	case connectorprotocol.MethodTrackerCount:
		return with(trackerTarget, map[string]any{"label": "ready-for-agent", "status": "", "limit": 10, "fresh": false}), true
	case connectorprotocol.MethodTrackerCountInterval:
		return map[string]any{}, true
	case connectorprotocol.MethodTrackerSearchIssues:
		return with(trackerTarget, map[string]any{"text": "conformance", "state": "open", "label": "", "limit": 10}), true
	case connectorprotocol.MethodTrackerLabeled:
		return with(trackerTarget, map[string]any{"label": "ready-for-agent", "state": "open", "limit": 10}), true
	case connectorprotocol.MethodTrackerTransition:
		return with(trackerTarget, map[string]any{
			"number": number, "state": "in-progress", "mapping": map[string]any{},
		}), true
	case connectorprotocol.MethodTrackerEditLabels:
		return with(trackerTarget, map[string]any{"number": number, "add": []string{"ready-for-agent"}, "remove": []string{}}), true
	case connectorprotocol.MethodTrackerComment:
		return with(trackerTarget, map[string]any{"number": number, "body": "conformance", "research": false}), true
	case connectorprotocol.MethodTrackerFileIssue:
		return with(trackerTarget, map[string]any{
			"title": "conformance", "body": "", "labels": []string{}, "dedupMarker": "", "issueType": "",
		}), true
	case connectorprotocol.MethodTrackerCreateLabel:
		return with(trackerTarget, map[string]any{"name": "conformance", "description": "", "color": "ededed"}), true
	case connectorprotocol.MethodTrackerMatchesRoute:
		return with(trackerTarget, map[string]any{
			"number": number, "route": []map[string]any{{"kind": "label", "value": "conformance"}},
		}), true
	}
	return nil, false
}

// declaredAxis is the axis of a tracker or forge capability, the ones the check
// calls; a Driver is checked by its own conformance.
func declaredAxis(capability string) (connectorprotocol.Axis, bool) {
	axis, ok := connectorprotocol.Capabilities[capability]
	return axis, ok && (axis == connectorprotocol.AxisTracker || axis == connectorprotocol.AxisForge)
}

// hasDeclared reports whether caps holds a capability declaredCapabilities
// calls.
func hasDeclared(caps []string) bool {
	return slices.ContainsFunc(caps, func(c string) bool { _, ok := declaredAxis(c); return ok })
}

// loaded is a Connector loaded against a scripted backend, the base URL it
// serves and how to seed an issue or PR there. Each call gets a freshly seeded
// number, so a write one method makes (a merge, a draft) cannot leave the next
// one's PR unmergeable.
type loaded struct {
	conn *connectorhost.Connector
	api  string
	seed func(number string)
	n    int
}

// declaredCapabilities calls, for every tracker or forge capability c declares,
// each required method that capability serves against the scripted backend and fails
// unless the Connector answers it for real. An answer is a result that matches
// the protocol schema, or a Connector-reported error of a class other than
// permanent. The scripted backend is not seeded for every sample, so a
// not-found or transient answer still proves the method is served, and core's
// contracts cover the success paths. Permanent is what the SDK answers for an
// unknown method and for bad params, so it, and a failed exchange, are not
// answers.
func declaredCapabilities(t *testing.T, c connectortest.Connector, caps []string) {
	t.Helper()
	schema, err := schematest.Load()
	if err != nil {
		t.Fatal(err)
	}
	var tracker, forgeConn *loaded
	for _, capability := range caps {
		axis, ok := declaredAxis(capability)
		if !ok {
			if _, known := connectorprotocol.Capabilities[capability]; known {
				continue
			}
			t.Errorf("connector declares capability %q, which the protocol does not define", capability)
			continue
		}
		var l **loaded
		if axis == connectorprotocol.AxisTracker {
			l = &tracker
		} else {
			l = &forgeConn
		}
		if *l == nil {
			*l = loadSeeded(t, c, axis, caps)
		}
		for _, m := range methodsOf(capability) {
			checkAnswers(t, schema, *l, capability, m)
		}
	}
}

// methodsOf is the non-raw, non-optional methods capability serves, in name
// order. A Connector may omit an optional method while declaring the
// capability, as the SDK's CheckDeclared also allows.
func methodsOf(capability string) []connectorprotocol.Method {
	var out []connectorprotocol.Method
	for _, m := range connectorprotocol.Methods {
		if m.Capability == capability && !m.Raw && !m.Optional {
			out = append(out, m)
		}
	}
	slices.SortFunc(out, func(a, b connectorprotocol.Method) int { return strings.Compare(a.Name, b.Name) })
	return out
}

// loadSeeded loads c against the scripted backend of axis, the PR forge one when
// caps declares forge.prs.
func loadSeeded(t *testing.T, c connectortest.Connector, axis connectorprotocol.Axis, caps []string) *loaded {
	t.Helper()
	if axis == connectorprotocol.AxisTracker {
		// New wraps its *Harness in capability structs, so assert the methods, not the type.
		h, ok := connectortrackertest.New(t, c).(interface {
			SeedIssue(forge.Issue)
			Loaded() (*connectorhost.Connector, string)
		})
		if !ok {
			t.Fatal("connectortrackertest.New's harness no longer exposes SeedIssue and Loaded")
		}
		conn, api := h.Loaded()
		return &loaded{conn: conn, api: api, seed: func(n string) {
			h.SeedIssue(forge.Issue{Number: n, Title: "conformance", Labels: []string{connectortrackertest.Labels.Dispatchable}})
		}}
	}
	if slices.Contains(caps, connectorprotocol.CapForgePrs) {
		h := connectorforgetest.NewPRFor(t, c)
		h.SeedAutoMergeAllowed(true)
		return &loaded{conn: h.Conn, api: h.Backend.URL + "/api/v1", seed: func(n string) { h.SeedOpenPR(n) }}
	}
	h := connectorforgetest.NewFor(t, c)
	return &loaded{conn: h.Conn, api: h.Backend.URL + "/api/v1", seed: func(string) {}}
}

// checkAnswers calls m and reports through t unless the Connector answers it,
// as declaredCapabilities defines an answer.
func checkAnswers(t *testing.T, schema *schematest.Schema, l *loaded, capability string, m connectorprotocol.Method) {
	t.Helper()
	l.n++
	number := strconv.Itoa(l.n)
	l.seed(number)
	params, ok := sampleParams(m.Name, l.api, number)
	if !ok {
		t.Fatalf("no sample for %q, which capability %q requires; add one to sampleParams", m.Name, capability)
	}
	var result map[string]any
	err := l.conn.Call(context.Background(), m.Name, params, &result)
	var ce *connectorhost.CallError
	switch {
	case err == nil:
		if verr := schema.ValidateResult(m.Name, result); verr != nil {
			t.Errorf("connector declares capability %q but %q answered a malformed result: %v", capability, m.Name, verr)
		}
	case errors.As(err, &ce) && ce.Class != connectorhost.Permanent:
		// The backend is not seeded for every sample; a not-found or transient
		// error still proves the method is served.
	case ce != nil:
		t.Errorf("connector declares capability %q but %q did not answer: %v", capability, m.Name, err)
	default:
		// A failed exchange leaves the Connector unusable, so no later call answers.
		t.Fatalf("connector declares capability %q but %q did not answer: %v", capability, m.Name, err)
	}
}
