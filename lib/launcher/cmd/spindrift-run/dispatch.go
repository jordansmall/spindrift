package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// swapLabel moves issue num from the remove label to the add label.
// Non-fatal: a labelling hiccup warns but does not abort the run.
func swapLabel(cfg *Config, num int, add, remove string) {
	cmd := exec.Command("gh", "issue", "edit",
		strconv.Itoa(num),
		"--repo", cfg.Repo,
		"--add-label", add,
		"--remove-label", remove,
	)
	if err := cmd.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "    ?? #%d: could not set label '%s' (remove '%s')\n", num, add, remove)
	}
}

// dispatchSingleWave fans out all issues in one wave, respecting MAX_PARALLEL.
// Labels are claimed synchronously before backgrounding so a re-run mid-flight
// skips in-progress issues immediately.
func dispatchSingleWave(cfg *Config, issues []Issue) error {
	for _, iss := range issues {
		swapLabel(cfg, iss.Number, cfg.InProgressLabel, cfg.Label)
	}

	wg := &sync.WaitGroup{}
	sem := make(chan struct{}, cfg.MaxParallel)
	for _, iss := range issues {
		wg.Add(1)
		iss := iss
		sem <- struct{}{}
		go func() {
			defer func() { <-sem; wg.Done() }()
			runOne(cfg, iss)
		}()
	}
	wg.Wait()
	return nil
}

// dispatchWaves dispatches issues in dependency order, holding blocked issues
// until their blockers carry cfg.CompleteLabel.  Times out with a deadlock
// error after cfg.DepsWaitSecs seconds without progress.
func dispatchWaves(cfg *Config, allIssues []Issue, deps map[int][]int) error {
	remaining := make([]Issue, len(allIssues))
	copy(remaining, allIssues)

	elapsed := 0
	waitSecs := cfg.DepsWaitSecs

	for len(remaining) > 0 {
		var ready []Issue
		for _, iss := range remaining {
			if !isBlocked(cfg, iss.Number, deps) {
				ready = append(ready, iss)
			}
		}

		if len(ready) == 0 {
			if elapsed >= waitSecs {
				var lines []string
				for _, iss := range remaining {
					lines = append(lines, fmt.Sprintf("  #%d %s", iss.Number, iss.Title))
				}
				return fmt.Errorf(
					"ERROR: dependency deadlock — blockers did not reach '%s' after %ds\n%s",
					cfg.CompleteLabel, waitSecs, strings.Join(lines, "\n"),
				)
			}
			fmt.Printf("    .. all remaining issues blocked; retrying in %ds (%ds elapsed)\n",
				cfg.DepsPollSecs, elapsed)
			time.Sleep(time.Duration(cfg.DepsPollSecs) * time.Second)
			elapsed += cfg.DepsPollSecs
			continue
		}

		elapsed = 0

		// Claim labels synchronously before backgrounding.
		for _, iss := range ready {
			swapLabel(cfg, iss.Number, cfg.InProgressLabel, cfg.Label)
		}

		// Run the ready wave with the parallelism cap.
		wg := &sync.WaitGroup{}
		sem := make(chan struct{}, cfg.MaxParallel)
		for _, iss := range ready {
			wg.Add(1)
			iss := iss
			sem <- struct{}{}
			go func() {
				defer func() { <-sem; wg.Done() }()
				runOne(cfg, iss)
			}()
		}
		wg.Wait()

		// Remove this wave's issues from remaining.
		dispatched := make(map[int]bool)
		for _, iss := range ready {
			dispatched[iss.Number] = true
		}
		var next []Issue
		for _, iss := range remaining {
			if !dispatched[iss.Number] {
				next = append(next, iss)
			}
		}
		remaining = next
	}

	return nil
}

// runOne dispatches one issue via the configured runtime (OCI or bwrap).
func runOne(cfg *Config, iss Issue) {
	fmt.Printf("    -> #%d: %s\n", iss.Number, iss.Title)
	logPath := filepath.Join("logs", fmt.Sprintf("issue-%d.log", iss.Number))

	var err error
	if cfg.Runtime == "bwrap" {
		err = runOneBwrap(cfg, iss, logPath)
	} else {
		err = runOneOCI(cfg, iss, logPath)
	}

	if err != nil {
		fmt.Fprintf(os.Stderr, "    !! #%d FAILED (logs/issue-%d.log)\n", iss.Number, iss.Number)
		swapLabel(cfg, iss.Number, cfg.FailedLabel, cfg.InProgressLabel)
	} else {
		fmt.Printf("    <- #%d done  (logs/issue-%d.log)\n", iss.Number, iss.Number)
	}
}

// runOneOCI launches one issue in a disposable OCI container.
func runOneOCI(cfg *Config, iss Issue, logPath string) error {
	// Reap any stale container left by an interrupted prior run.
	_ = exec.Command(cfg.Runtime, "rm", "-f", "agent-issue-"+strconv.Itoa(iss.Number)).Run()

	args := []string{
		"run", "--rm",
		"--name", "agent-issue-" + strconv.Itoa(iss.Number),
		"-e", "GH_TOKEN",
	}
	if cfg.OAuthToken != "" {
		args = append(args, "-e", "CLAUDE_CODE_OAUTH_TOKEN")
	}
	if cfg.APIKey != "" {
		args = append(args, "-e", "ANTHROPIC_API_KEY")
	}
	args = append(args,
		"-e", "GIT_USER_NAME="+cfg.GitUserName,
		"-e", "GIT_USER_EMAIL="+cfg.GitUserEmail,
		"-e", "REPO_SLUG="+cfg.Repo,
		"-e", "ISSUE_NUMBER="+strconv.Itoa(iss.Number),
		"-e", "ISSUE_TITLE="+iss.Title,
		"-e", "BASE_BRANCH="+cfg.BaseBranch,
		"-e", "BRANCH_PREFIX="+cfg.BranchPrefix,
		"-e", "MODEL="+cfg.Model,
		"-e", "SCOUT_MODEL="+cfg.ScoutModel,
		"-e", "REVIEW_MODEL="+cfg.ReviewModel,
		"-e", "IN_PROGRESS_LABEL="+cfg.InProgressLabel,
		"-e", "COMPLETE_LABEL="+cfg.CompleteLabel,
	)
	// Prompt override: bind-mount only when the directory exists.
	if cfg.PromptDir != "" {
		if fi, err := os.Stat(cfg.PromptDir); err == nil && fi.IsDir() {
			fmt.Printf("==> SPINDRIFT_PROMPT_DIR set; mounting %s over the baked prompt\n", cfg.PromptDir)
			args = append(args, "-v", cfg.PromptDir+":/agent/prompts:ro")
		}
	}
	args = append(args, cfg.ImageTag, "/agent/entrypoint.sh")

	return runCaptured(cfg.Runtime, args, logPath)
}

// runOneBwrap launches one issue inside a bubblewrap sandbox against the nix store.
func runOneBwrap(cfg *Config, iss Issue, logPath string) error {
	etcDir, err := os.MkdirTemp("", "spindrift-etc-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(etcDir)

	if err := os.WriteFile(filepath.Join(etcDir, "passwd"),
		[]byte("root:x:0:0:root:/root:/bin/bash\nagent:x:1000:1000:agent:/home/agent:/bin/bash\n"), 0o644); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(etcDir, "group"),
		[]byte("root:x:0:\nagent:x:1000:\n"), 0o644); err != nil {
		return err
	}

	args := []string{
		"--ro-bind", "/nix/store", "/nix/store",
		"--tmpfs", "/tmp",
		"--tmpfs", "/work",
		"--tmpfs", "/home/agent",
		"--proc", "/proc",
		"--dev", "/dev",
		"--dir", "/etc",
		"--ro-bind", filepath.Join(etcDir, "passwd"), "/etc/passwd",
		"--ro-bind", filepath.Join(etcDir, "group"), "/etc/group",
	}

	if _, err := os.Stat("/etc/resolv.conf"); err == nil {
		args = append(args, "--ro-bind", "/etc/resolv.conf", "/etc/resolv.conf")
	}

	args = append(args, "--ro-bind", filepath.Join(cfg.AgentFiles, "agent"), "/agent")

	if cfg.PromptDir != "" {
		if fi, err := os.Stat(cfg.PromptDir); err == nil && fi.IsDir() {
			fmt.Printf("==> SPINDRIFT_PROMPT_DIR set; mounting %s over the baked prompt\n", cfg.PromptDir)
			args = append(args, "--ro-bind", cfg.PromptDir, "/agent/prompts")
		}
	}

	args = append(args,
		"--clearenv",
		"--setenv", "HOME", "/home/agent",
		"--setenv", "PATH", cfg.AgentEnv+"/bin",
		"--setenv", "SSL_CERT_FILE", cfg.AgentEnv+"/etc/ssl/certs/ca-bundle.crt",
		"--setenv", "GIT_SSL_CAINFO", cfg.AgentEnv+"/etc/ssl/certs/ca-bundle.crt",
		"--setenv", "GH_TOKEN", cfg.GHToken,
	)
	if cfg.OAuthToken != "" {
		args = append(args, "--setenv", "CLAUDE_CODE_OAUTH_TOKEN", cfg.OAuthToken)
	}
	if cfg.APIKey != "" {
		args = append(args, "--setenv", "ANTHROPIC_API_KEY", cfg.APIKey)
	}
	args = append(args,
		"--setenv", "GIT_USER_NAME", cfg.GitUserName,
		"--setenv", "GIT_USER_EMAIL", cfg.GitUserEmail,
		"--setenv", "REPO_SLUG", cfg.Repo,
		"--setenv", "ISSUE_NUMBER", strconv.Itoa(iss.Number),
		"--setenv", "ISSUE_TITLE", iss.Title,
		"--setenv", "BASE_BRANCH", cfg.BaseBranch,
		"--setenv", "BRANCH_PREFIX", cfg.BranchPrefix,
		"--setenv", "MODEL", cfg.Model,
		"--setenv", "SCOUT_MODEL", cfg.ScoutModel,
		"--setenv", "REVIEW_MODEL", cfg.ReviewModel,
		"--setenv", "IN_PROGRESS_LABEL", cfg.InProgressLabel,
		"--setenv", "COMPLETE_LABEL", cfg.CompleteLabel,
		"--setenv", "PREFETCH", cfg.BakedPrefetch,
		"--unshare-user", "--uid", "1000", "--gid", "1000",
		"--unshare-pid", "--unshare-ipc", "--unshare-uts",
		"--", "/agent/entrypoint.sh",
	)

	return runCaptured("bwrap", args, logPath)
}

// runCaptured runs cmd with args, redirecting stdout+stderr to logPath.
func runCaptured(cmd string, args []string, logPath string) error {
	logFile, err := os.Create(logPath)
	if err != nil {
		return err
	}
	defer logFile.Close()

	c := exec.Command(cmd, args...)
	c.Stdout = logFile
	c.Stderr = logFile
	return c.Run()
}
