package github

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"time"

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
	DetailsURL  string `json:"detailsUrl"`  // CheckRun
	Context     string `json:"context"`     // StatusContext
	State       string `json:"state"`       // StatusContext
	Description string `json:"description"` // StatusContext
}

// jobLogsBudget caps the total time one FailureDetail spends reading job logs
// (up to 50 sequential gh calls), so a slow log blob host cannot stall PR
// settle; jobs left when it is spent just get no log. A var so a test can
// shrink it.
var jobLogsBudget = 60 * time.Second

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

// FailureDetail renders the PR's failing checks into a bounded report, or ""
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
	const gql = `query($owner:String!,$repo:String!,$number:Int!){repository(owner:$owner,name:$repo){pullRequest(number:$number){commits(last:1){nodes{commit{statusCheckRollup{contexts(first:50){nodes{__typename ... on CheckRun{name conclusion summary detailsUrl} ... on StatusContext{context state description}}}}}}}}}}`
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
	prefix := "https://" + parts[2] + "/" + owner + "/" + repo
	ctx, cancel := context.WithTimeout(context.Background(), jobLogsBudget)
	defer cancel()
	logs := make(map[int]string)
	for i, c := range contexts {
		if c.TypeName != "CheckRun" || !failingCheckRunConclusions[c.Conclusion] {
			continue
		}
		// Only a run under the PR's own repo counts, as in CheckRun.
		if m := actionsRunRE.FindStringSubmatch(c.DetailsURL); m != nil && m[3] != "" && strings.EqualFold(m[1], prefix) {
			if log, err := jobLog(ctx, owner, repo, m[3]); err == nil {
				logs[i] = log
			}
		}
	}
	return renderFailureDetail(contexts, logs), nil
}

// jobLog is the tail of an Actions job's log. Callers drop an error (expired
// log, no access) because the log only enriches the detail. gh follows the
// endpoint's redirect to the log blob itself.
func jobLog(ctx context.Context, owner, repo, jobID string) (string, error) {
	var tail forge.TailBuffer
	cmd := exec.CommandContext(ctx, "gh", "api", "repos/"+owner+"/"+repo+"/actions/jobs/"+jobID+"/logs")
	cmd.Stdout = &tail
	if err := cmd.Run(); err != nil {
		return "", ghCommandErr("gh api actions job logs "+jobID, err)
	}
	return tail.String(), nil
}

// renderFailureDetail renders the failing contexts; logs maps a context index
// to its job log tail and may be nil.
func renderFailureDetail(contexts []failureDetailContext, logs map[int]string) string {
	var entries []forge.FailureDetailEntry
	for i, ctx := range contexts {
		switch ctx.TypeName {
		case "CheckRun":
			if !failingCheckRunConclusions[ctx.Conclusion] {
				continue
			}
			entries = append(entries, forge.FailureDetailEntry{
				Name:    ctx.Name,
				State:   ctx.Conclusion,
				Summary: ctx.Summary,
				Log:     logs[i],
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
