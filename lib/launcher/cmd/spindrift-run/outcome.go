package main

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

var (
	prRe     = regexp.MustCompile(`\bpr=(\S+)`)
	statusRe = regexp.MustCompile(`\bstatus=(\S+)`)
	noteRe   = regexp.MustCompile(`\bnote=(.+)$`)
)

// printOutcomeReport reads each issue's log file for its SPINDRIFT_OUTCOME
// line and emits a roll-up.  For "merged" outcomes it independently verifies
// the PR state and issue label against GitHub before trusting the self-report.
func printOutcomeReport(cfg *Config, issues []Issue) {
	fmt.Println("==> outcome report")
	for _, iss := range issues {
		logPath := filepath.Join("logs", fmt.Sprintf("issue-%d.log", iss.Number))
		outLine := findOutcomeLine(logPath)
		if outLine == "" {
			fmt.Printf("    #%d  status=missing  note=no SPINDRIFT_OUTCOME in log\n", iss.Number)
			continue
		}

		pr := extractField(prRe, outLine)
		status := extractField(statusRe, outLine)
		note := extractNote(outLine)

		switch status {
		case "blocked":
			fmt.Printf("    #%d  pr=%s  status=%s  !! %s\n", iss.Number, pr, status, note)

		case "merged":
			prState := ghPRState(pr)
			issueLabels := ghIssueLabels(cfg, iss.Number)
			hasComplete := containsLabel(issueLabels, cfg.CompleteLabel)

			if prState == "MERGED" && hasComplete {
				fmt.Printf("    #%d  pr=%s  status=verified-merged\n", iss.Number, pr)
			} else {
				var reason string
				if prState != "MERGED" {
					reason = fmt.Sprintf("PR state is '%s', expected MERGED", prState)
				} else {
					reason = fmt.Sprintf("issue does not carry '%s'", cfg.CompleteLabel)
				}
				fmt.Printf("    #%d  pr=%s  status=failed  !! %s\n", iss.Number, pr, reason)
				swapLabel(cfg, iss.Number, cfg.FailedLabel, cfg.InProgressLabel)
			}

		default:
			fmt.Printf("    #%d  pr=%s  status=%s\n", iss.Number, pr, status)
		}
	}
}

func findOutcomeLine(logPath string) string {
	f, err := os.Open(logPath)
	if err != nil {
		return ""
	}
	defer f.Close()

	last := ""
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		if strings.HasPrefix(scanner.Text(), "SPINDRIFT_OUTCOME ") {
			last = scanner.Text()
		}
	}
	return last
}

func extractField(re *regexp.Regexp, s string) string {
	m := re.FindStringSubmatch(s)
	if m == nil {
		return ""
	}
	return m[1]
}

func extractNote(s string) string {
	m := noteRe.FindStringSubmatch(s)
	if m == nil {
		return ""
	}
	return m[1]
}

func ghPRState(prRef string) string {
	out, err := exec.Command("gh", "pr", "view", prRef,
		"--json", "state", "--jq", ".state").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func ghIssueLabels(cfg *Config, num int) []string {
	out, err := exec.Command("gh", "issue", "view",
		fmt.Sprintf("%d", num),
		"--repo", cfg.Repo,
		"--json", "labels",
		"--jq", ".labels[].name",
	).Output()
	if err != nil {
		return nil
	}
	var labels []string
	for _, l := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if l != "" {
			labels = append(labels, l)
		}
	}
	return labels
}

func containsLabel(labels []string, want string) bool {
	for _, l := range labels {
		if l == want {
			return true
		}
	}
	return false
}
