package main

import (
	"os"
	"strings"
)

// driverInput is the file-path/flag data driver-exec assembles into the
// Driver's own argv (ADR 0009): the prompt, --agents JSON, and session
// pin/resume flags all cross into this process as files (issue #626),
// replacing the shell temp-file/eval marshalling that used to cross the
// devShell boundary. driver names which of buildDriverArgs' two argv shapes
// applies (issue #262 slice 4) -- empty (and any non-"opencode" value)
// resolves to the claude shape, matching driver.New's own empty-defaults-to-
// claude convention.
type driverInput struct {
	driver      string
	promptFile  string
	model       string
	effort      string
	agentsFile  string
	sessionFile string
	driverFlags string
}

// buildDriverArgs reads promptFile (and, if set, agentsFile/sessionFile) and
// returns the Driver's argv, in one of two shapes selected by in.driver
// (issue #262 slice 4):
//
// claude (the default, and every driver name other than "opencode"):
// -p <prompt>, --model <model> (always present, even empty, to match the
// pipeline's prior unconditional `--model "${MODEL:-}"`), --agents <json>
// only when agentsFile holds non-empty content (matching the prior
// agents_args, which stayed empty when agents_json was ""), then the
// session file's content and driverFlags each word-split into separate argv
// elements (matching the shell's prior `read -ra`/unquoted-splice
// word-splitting of _driver_session_args and DRIVER_FLAGS_COMMON), and
// finally --effort <effort> appended last, but only when effort is
// non-empty -- unlike --model, --effort is omitted entirely rather than
// emitted with an empty value (issue #2241).
//
// opencode: driverFlags word-split first (its own `run --format json --auto`
// -- the `run` subcommand must lead argv), then -m <model> only when model
// is non-empty (never --model, and omitted entirely rather than -m ""),
// then --variant <effort> (opencode's cross-provider reasoning-effort
// selector) only when effort is non-empty, then the session file's content
// word-split (a no-op today, kept for symmetry with the claude shape), and
// finally the prompt spliced in as ONE trailing positional argument -- never
// -p, and never --agents (opencode has no equivalent flag).
func buildDriverArgs(in driverInput) ([]string, error) {
	prompt, err := os.ReadFile(in.promptFile)
	if err != nil {
		return nil, err
	}

	if in.driver == "opencode" {
		args := strings.Fields(in.driverFlags)
		if in.model != "" {
			args = append(args, "-m", in.model)
		}
		if in.effort != "" {
			args = append(args, "--variant", in.effort)
		}
		if in.sessionFile != "" {
			session, err := os.ReadFile(in.sessionFile)
			if err != nil {
				return nil, err
			}
			args = append(args, strings.Fields(string(session))...)
		}
		args = append(args, string(prompt))
		return args, nil
	}

	args := []string{"-p", string(prompt), "--model", in.model}
	if in.agentsFile != "" {
		agents, err := os.ReadFile(in.agentsFile)
		if err != nil {
			return nil, err
		}
		if len(agents) > 0 {
			args = append(args, "--agents", string(agents))
		}
	}
	if in.sessionFile != "" {
		session, err := os.ReadFile(in.sessionFile)
		if err != nil {
			return nil, err
		}
		args = append(args, strings.Fields(string(session))...)
	}
	args = append(args, strings.Fields(in.driverFlags)...)
	if in.effort != "" {
		args = append(args, "--effort", in.effort)
	}
	return args, nil
}
