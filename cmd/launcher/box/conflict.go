package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"

	"spindrift.dev/launcher/internal/bindregistry"
	"spindrift.dev/launcher/internal/bundleout"
	"spindrift.dev/launcher/internal/conflictresolve"
	"spindrift.dev/launcher/internal/passmanifest"
	"spindrift.dev/launcher/internal/promptassembly"
)

// conflictResolve runs the pre-work conflict-resolve pass before assembly, so
// its two early exits skip assembly entirely (issue #2354). It reports whether
// box carries on into assembly and, if not, the exit code.
func (r *boxRun) conflictResolve() (conflictresolve.Outcome, error) {
	cfg := conflictresolve.Config{
		Conflict:    r.in.PreworkRebaseConflict,
		Publish:     r.in.PublishRebase,
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
func (r *boxRun) conflictPass() error {
	reg, err := promptassembly.LoadRegistryFile(r.in.Assembly.RegistryFile)
	if err != nil {
		return err
	}
	prompt, err := conflictresolve.RenderPrompt(r.in.Assembly.PromptsDir, r.in.Assembly.SkillsDir, conflictresolve.SubstNames(reg), r.d.Getenv)
	if err != nil {
		return err
	}
	// Only the main run enters the devShell, so this pass's handoff leaves
	// Devshell off.
	p := r.in.Assembly.Passthrough
	handoff, err := json.Marshal(promptassembly.Handoff{
		Model:        p.Model,
		Effort:       p.Effort,
		Driver:       p.Driver,
		DriverBin:    p.DriverBin,
		DriverFlags:  p.DriverFlags,
		Issue:        r.env.IssueNumber,
		HeartbeatLog: p.HeartbeatLog,
		ArgvShape:    p.ArgvShape,
		Caps:         p.Caps,
	})
	if err != nil {
		return fmt.Errorf("marshal handoff: %w", err)
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
		return err
	}
	promptFile, err := temp("box-cr-prompt-", prompt)
	if err != nil {
		return err
	}
	// No session to pin or resume for this pass.
	sessionFile, err := temp("box-cr-session-", "")
	if err != nil {
		return err
	}
	logFile, err := temp("box-cr-log-", "")
	if err != nil {
		return err
	}
	argv := []string{
		"--handoff-file", handoffFile,
		"--prompt-file", promptFile,
		"--session-file", sessionFile,
		"--log-path", logFile,
	}
	if r.needsBox {
		argv = append(argv, "--manifest-path", r.in.OutboxDir+"/"+passmanifest.FileName)
	}
	_ = r.d.Orchestrate(argv)
	return nil
}

func (r *boxRun) rebaseInProgress() bool {
	for _, dir := range []string{"rebase-merge", "rebase-apply"} {
		if fi, err := os.Stat(filepath.Join(r.in.WorkDir, ".git", dir)); err == nil && fi.IsDir() {
			return true
		}
	}
	return false
}

// publishRebased lands the rebased branch. A read-only Box holds no push-capable
// token (issue #1979: a force-push would 403), so it relays through the outbox
// bundle instead, behind the same BOX_WRITE_ENABLED gate the PR contract uses
// (issues #1918, #1808).
func (r *boxRun) publishRebased() error {
	if r.env.BoxWriteEnabled {
		return r.d.Git(r.in.WorkDir, "push", "--force-with-lease", "origin", r.env.Branch)
	}
	return r.d.BundleOut(bundleout.Config{
		Repo:      r.in.WorkDir,
		Base:      "origin/" + r.env.BaseBranch,
		Branch:    r.env.Branch,
		OutboxDir: r.in.OutboxDir,
	}, r.d.Stdout)
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

// gitRun runs git in dir with the Box's stdio.
func gitRun(dir string, args ...string) error {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	return cmd.Run()
}
