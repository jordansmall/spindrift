package main

import "strings"

// exportStartEnv puts the values the shell preambles hold unexported into
// box's own environment, so the orchestrator and the Driver it spawns inherit
// them as they did when entrypoint.sh exported them ahead of `exec box`.
// Empty flag values export nothing: the preambles render them only when set.
func (r *boxRun) exportStartEnv() error {
	set := func(k, v string) error {
		if err := r.d.Setenv(k, v); err != nil {
			return phaseErr("env-export", err)
		}
		return nil
	}
	if err := set("BRANCH", r.env.Branch); err != nil {
		return err
	}
	for k, v := range map[string]string{
		"DEV_SHELL_NAME":          r.in.DevShellName,
		"DEV_SHELL_PROBE_TIMEOUT": r.in.DevShellProbeTimeout,
	} {
		if v == "" {
			continue
		}
		if err := set(k, v); err != nil {
			return err
		}
	}
	return r.exportDriverBashTimeout(set)
}

// exportDriverBashTimeout exports the Consumer's Bash-timeout knob under each
// env var name the Driver's registry entry lists (issue #4409). A value that
// is not a positive integer is skipped with a warning rather than passed on:
// Claude Code may silently fall back to its 10-minute cap on a bad value.
func (r *boxRun) exportDriverBashTimeout(set func(k, v string) error) error {
	ms := r.in.DriverBashTimeoutMS
	if ms == "" {
		return nil
	}
	if strings.HasPrefix(ms, "0") || strings.ContainsFunc(ms, func(c rune) bool { return c < '0' || c > '9' }) {
		r.say("==> WARNING: DRIVER_BASH_TIMEOUT_MS='%s' is not a positive integer (milliseconds) — leaving the Driver's own Bash timeout", ms)
		return nil
	}
	for _, name := range strings.Fields(r.in.DriverBashTimeoutEnv) {
		if err := set(name, ms); err != nil {
			return err
		}
	}
	return nil
}
