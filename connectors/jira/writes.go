package main

import (
	"errors"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"

	"spindrift.dev/sdk/tracker"
)

type transitionsPayload struct {
	Transitions []struct {
		ID string `json:"id"`
		To struct {
			Name string `json:"name"`
		} `json:"to"`
		Fields map[string]struct {
			Required        bool   `json:"required"`
			HasDefaultValue bool   `json:"hasDefaultValue"`
			Name            string `json:"name"`
		} `json:"fields"`
	} `json:"transitions"`
}

// checkMapping rejects a mapping in which two states name the same status
// (case-insensitively) other than failed and ambiguous, the one pair a
// workflow may legitimately fold into a single parking status. Keys are
// sorted so the error names the same pair on every run.
func checkMapping(m map[string]tracker.StatusTarget) error {
	keys := slices.Sorted(maps.Keys(m))
	for i, k := range keys {
		status := m[k].Status
		if status == "" {
			continue
		}
		for _, p := range keys[:i] {
			if !strings.EqualFold(m[p].Status, status) || (p == "ambiguous" && k == "failed") {
				continue
			}
			return fmt.Errorf("jira: states %q and %q both map to status %q (only failed and ambiguous may share a status)", p, k, m[p].Status)
		}
	}
	return nil
}

// transition performs the workflow transition on issue number to the status
// r.Mapping names for r.State. A mapping that breaks the shared-status rule is
// an error, as is a transition that needs a field the request cannot supply or
// a resolution its screen cannot take, which fail before any POST (Jira would
// answer 400): none is an Unavailable outcome, because falling back would hide
// a mapping the operator must fix.
func transition(t tracker.Target, number string, r tracker.TransitionRequest) (tracker.TransitionResult, error) {
	if err := checkMapping(r.Mapping); err != nil {
		return tracker.TransitionResult{}, err
	}
	target := r.Mapping[r.State]
	if target.Status == "" {
		return tracker.TransitionResult{Outcome: tracker.TransitionUnmapped}, nil
	}
	path := issuePath(number) + "/transitions"
	var payload transitionsPayload
	if err := get(t, path+"?expand=transitions.fields", &payload); err != nil {
		return tracker.TransitionResult{}, err
	}
	for _, tr := range payload.Transitions {
		if !strings.EqualFold(tr.To.Name, target.Status) {
			continue
		}
		if _, ok := tr.Fields["resolution"]; target.Resolution != "" && !ok {
			return tracker.TransitionResult{}, fmt.Errorf("jira: transition to %q on issue %s has no resolution field on its screen, so the resolution %q that JIRA_STATUS_MAPPING sets cannot be applied",
				target.Status, number, target.Resolution)
		}
		var missing []string
		for _, key := range slices.Sorted(maps.Keys(tr.Fields)) {
			f := tr.Fields[key]
			if !f.Required || f.HasDefaultValue || (key == "resolution" && target.Resolution != "") {
				continue
			}
			name := key
			if f.Name != "" {
				name = fmt.Sprintf("%s (%s)", f.Name, key)
			}
			missing = append(missing, name)
		}
		if len(missing) > 0 {
			return tracker.TransitionResult{}, fmt.Errorf("jira: transition to %q on issue %s requires field(s) %s that JIRA_STATUS_MAPPING does not supply (only resolution can be set, via the {\"status\",\"resolution\"} object form)",
				target.Status, number, strings.Join(missing, ", "))
		}
		body := map[string]any{"transition": map[string]string{"id": tr.ID}}
		if target.Resolution != "" {
			body["fields"] = map[string]any{"resolution": map[string]string{"name": target.Resolution}}
		}
		if err := do(t, "POST", path, body, nil); err != nil {
			return tracker.TransitionResult{}, err
		}
		return tracker.TransitionResult{Outcome: tracker.TransitionMoved, Status: target.Status}, nil
	}
	offered := make([]string, len(payload.Transitions))
	for i, tr := range payload.Transitions {
		offered[i] = tr.To.Name
	}
	return tracker.TransitionResult{Outcome: tracker.TransitionUnavailable, Status: target.Status, Offered: offered}, nil
}

// editLabels removes then adds labels in one label-field update, skipping empty
// names; with nothing to do it sends nothing.
func editLabels(t tracker.Target, number string, add, remove []string) error {
	var ops []map[string]string
	for _, name := range remove {
		if name != "" {
			ops = append(ops, map[string]string{"remove": name})
		}
	}
	for _, name := range add {
		if name != "" {
			ops = append(ops, map[string]string{"add": name})
		}
	}
	if len(ops) == 0 {
		return nil
	}
	return do(t, "PUT", issuePath(number),
		map[string]any{"update": map[string]any{"labels": ops}}, nil)
}

// listLabels returns Jira's site-wide label list, which is free text with no
// registry to create into. /label pages by startAt; an unpaged body carries no
// total, which ends the walk after its first page.
func listLabels(t tracker.Target) ([]string, error) {
	all := []string{}
	for range maxPages {
		var p struct {
			Values []string `json:"values"`
			Total  int      `json:"total"`
			IsLast bool     `json:"isLast"`
		}
		if err := get(t, "/rest/api/2/label?startAt="+strconv.Itoa(len(all)), &p); err != nil {
			return nil, err
		}
		all = append(all, p.Values...)
		if p.IsLast || len(p.Values) == 0 || len(all) >= p.Total {
			return all, nil
		}
	}
	return nil, fmt.Errorf("GET /rest/api/2/label: more than %d pages", maxPages)
}

// comment posts r.Body as a wiki-markup comment on issue number. A research
// verdict also carries the research entity property, so core finds it by
// property rather than by marker text the wiki renderer could mangle.
func comment(t tracker.Target, number string, r tracker.CommentRequest) error {
	req := map[string]any{"body": toWikiMarkup(r.Body)}
	if r.Research {
		req["properties"] = []map[string]any{{"key": researchPropertyKey, "value": true}}
	}
	return do(t, "POST", issuePath(number)+"/comment", req, nil)
}

// fileIssue creates an issue of type r.IssueType in the target's project and
// returns its browse URL. The dedup marker is stored as an entity property in
// the same request, so the visible description never carries it.
func fileIssue(t tracker.Target, r tracker.IssueRequest) (string, error) {
	if r.IssueType == "" {
		return "", errors.New("jira: no issue type to file under")
	}
	req := map[string]any{
		"fields": map[string]any{
			"project":     map[string]string{"key": t.Repo},
			"summary":     r.Title,
			"issuetype":   map[string]string{"name": r.IssueType},
			"description": toWikiMarkup(r.Body),
			"labels":      append([]string{}, r.Labels...),
		},
	}
	if r.DedupMarker != "" {
		req["properties"] = []map[string]any{{"key": dedupPropertyKey, "value": map[string]string{"marker": r.DedupMarker}}}
	}
	var created struct {
		Key string `json:"key"`
	}
	if err := do(t, "POST", "/rest/api/2/issue", req, &created); err != nil {
		return "", err
	}
	if created.Key == "" {
		return "", errors.New("jira: create issue response carried no key")
	}
	return strings.TrimRight(t.API, "/") + "/browse/" + created.Key, nil
}
