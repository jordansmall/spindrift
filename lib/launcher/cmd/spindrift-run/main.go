package main

import (
	"fmt"
	"os"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := loadConfig()
	if err != nil {
		return err
	}

	if cfg.Runtime != "bwrap" {
		if err := ensureImage(cfg); err != nil {
			return err
		}
	}

	issues, err := queryIssues(cfg)
	if err != nil {
		return err
	}

	if len(issues) == 0 {
		fmt.Printf("no open '%s' issues — nothing to do.\n", cfg.Label)
		return nil
	}

	if cfg.MaxJobs > 0 && len(issues) > cfg.MaxJobs {
		issues = issues[:cfg.MaxJobs]
	}

	fmt.Printf("==> %d issue(s); launching up to %d container(s) at a time\n",
		len(issues), cfg.MaxParallel)

	if err := os.MkdirAll("logs", 0o755); err != nil {
		return fmt.Errorf("creating logs dir: %w", err)
	}

	deps, err := buildDepGraph(cfg, issues)
	if err != nil {
		return err
	}

	if len(deps) == 0 {
		if err := dispatchSingleWave(cfg, issues); err != nil {
			return err
		}
	} else {
		fmt.Println("==> dependency edges found; dispatching in waves")
		if err := dispatchWaves(cfg, issues, deps); err != nil {
			return err
		}
	}

	printOutcomeReport(cfg, issues)
	fmt.Printf("==> all agents finished — branches pushed and PRs opened on %s.\n", cfg.Repo)
	return nil
}
