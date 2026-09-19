package github

import (
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"

	"spindrift.dev/launcher/internal/forge"
)

// failureDetailContext is one node of a statusCheckRollup contexts union: a
// CheckRun (GitHub Actions and most third-party checks) or a StatusContext
// (the legacy commit-status API).
type failureDetailContext struct {
	TypeName    string `json:"__typename"`
	Name        string `json:"name"`        // CheckRun
	Conclusion  string `json:"conclusion"`  // CheckRun
	Summary     string `json:"summary"`     // CheckRun
	Context     string `json:"context"`     // StatusContext
	State       string `json:"state"`       // StatusContext
	Description string `json:"description"` // StatusContext
}

// failingCheckRunConclusions excludes SUCCESS, NEUTRAL, and SKIPPED.
var failingCheckRunConclusions = map[string]bool{
	"FAILURE":         true,
	"TIMED_OUT":       true,
	"CANCELLED":       true,
	"ACTION_REQUIRED": true,
	"STARTUP_FAILURE": true,
}

// failingStatusContextStates are the legacy states that count as a failure.
var failingStatusContextStates = map[string]bool{
	"FAILURE": true,
	"ERROR":   true,
}

// FailureDetail renders the PR's failing checks into a bounded excerpt, or ""
// when none are failing. It reads the head commit's statusCheckRollup over
// GraphQL because `gh pr checks` uses the REST check-runs endpoint, which 403s
// under a fine-grained PAT. The fetch is best-effort: a non-nil error means
// the detail is unavailable, and callers should proceed without it.
func (e *execClient) FailureDetail(url string) (string, error) {
	parts := strings.Split(url, "/")
	if len(parts) < 7 {
		return "", fmt.Errorf("invalid PR URL: %s", url)
	}
	owner, repo, number := parts[3], parts[4], parts[6]
	const gql = `query($owner:String!,$repo:String!,$number:Int!){repository(owner:$owner,name:$repo){pullRequest(number:$number){commits(last:1){nodes{commit{statusCheckRollup{contexts(first:50){nodes{__typename ... on CheckRun{name conclusion summary} ... on StatusContext{context state description}}}}}}}}}}`
	cmd := exec.Command("gh", "api", "graphql",
		"-f", "query="+gql,
		"-f", "owner="+owner,
		"-f", "repo="+repo,
		"-F", "number="+number,
		"--jq", `.data.repository.pullRequest.commits.nodes[0].commit.statusCheckRollup.contexts.nodes // []`,
	)
	out, err := cmd.Output()
	if err != nil {
		return "", ghCommandErr("gh api graphql (statusCheckRollup contexts)", err)
	}
	var contexts []failureDetailContext
	if err := json.Unmarshal(out, &contexts); err != nil {
		return "", fmt.Errorf("parse statusCheckRollup contexts: %w", err)
	}
	return renderFailureDetail(contexts), nil
}

func renderFailureDetail(contexts []failureDetailContext) string {
	var entries []forge.FailureDetailEntry
	for _, ctx := range contexts {
		switch ctx.TypeName {
		case "CheckRun":
			if !failingCheckRunConclusions[ctx.Conclusion] {
				continue
			}
			entries = append(entries, forge.FailureDetailEntry{
				Name:    ctx.Name,
				State:   ctx.Conclusion,
				Summary: ctx.Summary,
			})
		case "StatusContext":
			if !failingStatusContextStates[ctx.State] {
				continue
			}
			entries = append(entries, forge.FailureDetailEntry{
				Name:    ctx.Context,
				State:   ctx.State,
				Summary: ctx.Description,
			})
		}
	}
	return forge.RenderFailureDetail(entries)
}
