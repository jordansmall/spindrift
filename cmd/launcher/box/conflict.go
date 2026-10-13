package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"spindrift.dev/launcher/internal/bindregistry"
	"spindrift.dev/launcher/internal/conflictresolve"
	"spindrift.dev/launcher/internal/driver"
	"spindrift.dev/launcher/internal/outcome"
	"spindrift.dev/launcher/internal/promptassembly"
)

// conflictResolve runs the pre-work conflict-resolve pass before assembly, so
// its two early exits skip assembly entirely (issue #2354). It reports whether
// box carries on into assembly and, if not, the exit code.
func (r *boxRun) conflictResolve() (conflictresolve.Outcome, error) {
	cfg := conflictresolve.Config{
		Conflict:    r.recovery.Conflict,
		Publish:     r.recovery.Adopted,
		ResolveOnly: r.d.Getenv("CONFLICT_RESOLVE_PR_URL") != "",
		BaseBranch:  r.env.BaseBranch,
		Branch:      r.env.Branch,
	}
	out, err := conflictresolve.Resolve(cfg, conflictresolve.Actions{
		RunPass:          r.conflictPass,
		RebaseInProgress: r.rebaseInProgress,
		Abort:            func() { r.d.AbortRebase(r.in.WorkDir, r.d.Stdout) },
		Publish:          r.publishRebased,
	}, r.d.Stdout)
	if err != nil {
		return out, phaseErr("conflict-resolve", err)
	}
	return out, nil
}

// conflictPass runs the sessionless resolve agent through the orchestrator. Its
// exit status is not checked: success is read off the rebase state afterwards.
// It returns the Driver's result text (the agent's final message under claude),
// the pass summary.
func (r *boxRun) conflictPass() (string, error) {
	reg, err := promptassembly.LoadRegistryFile(r.in.Assembly.RegistryFile)
	if err != nil {
		return "", err
	}
	prompt, err := conflictresolve.RenderPrompt(r.in.Assembly.PromptsDir, r.in.Assembly.SkillsDir, conflictresolve.SubstNames(reg), r.d.Getenv)
	if err != nil {
		return "", err
	}
	// The pass inherits the decision made on the still-conflicted tree, so it
	// runs without the devShell when a conflicted flake file fails the probe;
	// the main run re-decides once the rebase is done.
	p := r.in.Assembly.Passthrough
	handoff, err := json.Marshal(promptassembly.Handoff{
		Model:        p.Model,
		Effort:       p.Effort,
		Driver:       p.Driver,
		DriverBin:    p.DriverBin,
		DriverFlags:  p.DriverFlags,
		Devshell:     p.Devshell,
		DevshellName: p.DevshellName,
		Issue:        r.env.IssueNumber,
		HeartbeatLog: p.HeartbeatLog,
		ArgvShape:    p.ArgvShape,
		Caps:         p.Caps,
	})
	if err != nil {
		return "", fmt.Errorf("marshal handoff: %w", err)
	}

	var files []string
	defer func() {
		for _, f := range files {
			os.Remove(f)
		}
	}()
	temp := func(pattern, content string) (string, error) {
		f, err := writeTemp(pattern, content)
		if f != "" {
			files = append(files, f)
		}
		return f, err
	}
	handoffFile, err := temp("box-cr-handoff-", string(handoff))
	if err != nil {
		return "", err
	}
	promptFile, err := temp("box-cr-prompt-", prompt)
	if err != nil {
		return "", err
	}
	// No session to pin or resume for this pass.
	sessionFile, err := temp("box-cr-session-", "")
	if err != nil {
		return "", err
	}
	logFile, err := temp("box-cr-log-", "")
	if err != nil {
		return "", err
	}
	_ = r.d.Orchestrate(r.orchestratorArgv(handoffFile, promptFile, sessionFile, logFile))
	return r.passSummary(p.Driver, logFile), nil
}

// passSummary reads the Driver's result text (the agent's final message under
// claude) off its stream log. The pass is advisory — success is the rebase
// state — so an unreadable log means no summary, never a failed Box.
func (r *boxRun) passSummary(driverName, streamLog string) string {
	text, err := resultText(driverName, streamLog)
	if err != nil {
		fmt.Fprintf(r.d.Stdout, "==> WARNING: conflict-resolve pass summary unreadable: %v\n", err)
		return ""
	}
	return strings.TrimSpace(outcome.StripResultText(text))
}

func resultText(driverName, streamLog string) (string, error) {
	d, err := driver.New(driverName)
	if err != nil {
		return "", err
	}
	return d.ResultText(streamLog)
}

func (r *boxRun) rebaseInProgress() bool {
	for _, dir := range []string{"rebase-merge", "rebase-apply"} {
		if fi, err := os.Stat(filepath.Join(r.in.WorkDir, ".git", dir)); err == nil && fi.IsDir() {
			return true
		}
	}
	return false
}

// publishRebased lands the rebased branch through the one publish decision
// branch recovery owns.
func (r *boxRun) publishRebased() error {
	return r.d.PublishBranch(r.recoveryConfig(), r.d.Stdout)
}

// abortRebase is best-effort. It reverts the in-tree bindings first (ADR 0044,
// issue #2851): when an in-tree config file is itself an unmerged conflicting
// path git refuses to check it out, so a failed revert is expected rather than
// a real problem; `git rebase --abort` cleans up regardless.
func abortRebase(workDir string, w io.Writer) {
	if bindregistry.RevertInTreeBindings(workDir, bindregistry.InTreeBindings(), "box", w) {
		fmt.Fprintln(w, "==> WARNING: in-tree binding revert failed")
	}
	cmd := exec.Command("git", "rebase", "--abort")
	cmd.Dir = workDir
	_ = cmd.Run()
}

// gitOutput runs git in dir and returns its stdout.
func gitOutput(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Stderr = os.Stderr
	out, err := cmd.Output()
	return string(out), err
}

// gitRun runs git in dir with the Box's stdio.
func gitRun(dir string, args ...string) error {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	return cmd.Run()
}
