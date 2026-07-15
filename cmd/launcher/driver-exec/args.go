package main

import (
	"os"
	"strings"
)

// driverInput is the file-path/flag data driver-exec assembles into the
// Driver's own argv (ADR 0009): the prompt, --agents JSON, and session
// pin/resume flags all cross into this process as files (issue #626),
// replacing the shell temp-file/eval marshalling that used to cross the
// devShell boundary.
type driverInput struct {
	promptFile  string
	model       string
	agentsFile  string
	sessionFile string
	driverFlags string
}

// buildDriverArgs reads promptFile (and, if set, agentsFile/sessionFile) and
// returns the Driver's argv: -p <prompt>, --model <model> (always present,
// even empty, to match the pipeline's prior unconditional
// `--model "${MODEL:-}"`), --agents <json> only when agentsFile holds
// non-empty content (matching the prior agents_args, which stayed empty when
// agents_json was ""), then the session file's content and driverFlags each
// word-split into separate argv elements (matching the shell's prior
// `read -ra`/unquoted-splice word-splitting of _driver_session_args and
// DRIVER_FLAGS_COMMON).
func buildDriverArgs(in driverInput) ([]string, error) {
	prompt, err := os.ReadFile(in.promptFile)
	if err != nil {
		return nil, err
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
	return args, nil
}
