package github

import (
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
)

// maxFailureDetailBytes bounds the string FailureDetail returns, so a large
// CI log excerpt cannot blow the fix Box's env/prompt budget.
const maxFailureDetailBytes = 4000

// failureDetailContext is one node of a statusCheckRollup's contexts union —
// either a CheckRun (GitHub Actions and most third-party checks) or a
// StatusContext (the legacy commit-status API).
type failureDetailContext struct {
	TypeName    string `json:"__typename"`
	Name        string `json:"name"`        // CheckRun
	Conclusion  string `json:"conclusion"`  // CheckRun
	Summary     string `json:"summary"`     // CheckRun
	Context     string `json:"context"`     // StatusContext
	State       string `json:"state"`       // StatusContext
	Description string `json:"description"` // StatusContext
}

// failingCheckRunConclusions are the CheckRun.conclusion values that
// represent a genuine failure, as opposed to SUCCESS, NEUTRAL, or SKIPPED.
var failingCheckRunConclusions = map[string]bool{
	"FAILURE":         true,
	"TIMED_OUT":       true,
	"CANCELLED":       true,
	"ACTION_REQUIRED": true,
	"STARTUP_FAILURE": true,
}

// failingStatusContextStates are the legacy StatusContext.state values that
// represent a genuine failure.
var failingStatusContextStates = map[string]bool{
	"FAILURE": true,
	"ERROR":   true,
}

// FailureDetail queries the PR's head-commit statusCheckRollup via GraphQL —
// the same fine-grained-PAT-safe query CheckState uses, unlike `gh pr checks`
// (REST check-runs, 403s under a fine-grained PAT) — and renders the failing
// checks' names plus their reported summary into a bounded excerpt. Returns
// "" when no checks are currently failing. The fetch is best-effort: callers
// should treat a non-nil error as "detail unavailable" and proceed without it.
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
		return "", fmt.Errorf("gh api graphql: %w", err)
	}
	var contexts []failureDetailContext
	if err := json.Unmarshal(out, &contexts); err != nil {
		return "", fmt.Errorf("parse statusCheckRollup contexts: %w", err)
	}
	return renderFailureDetail(contexts), nil
}

// renderFailureDetail formats the failing contexts into a bounded, human-
// readable excerpt: one "name: conclusion" header per failing check plus its
// summary, truncated to maxFailureDetailBytes.
func renderFailureDetail(contexts []failureDetailContext) string {
	var b strings.Builder
	for _, ctx := range contexts {
		switch ctx.TypeName {
		case "CheckRun":
			if !failingCheckRunConclusions[ctx.Conclusion] {
				continue
			}
			fmt.Fprintf(&b, "%s: %s\n", ctx.Name, ctx.Conclusion)
			if ctx.Summary != "" {
				fmt.Fprintf(&b, "%s\n", ctx.Summary)
			}
			b.WriteString("---\n")
		case "StatusContext":
			if !failingStatusContextStates[ctx.State] {
				continue
			}
			fmt.Fprintf(&b, "%s: %s\n", ctx.Context, ctx.State)
			if ctx.Description != "" {
				fmt.Fprintf(&b, "%s\n", ctx.Description)
			}
			b.WriteString("---\n")
		}
	}
	s := strings.TrimSpace(b.String())
	if len(s) > maxFailureDetailBytes {
		s = s[:maxFailureDetailBytes]
	}
	return s
}
