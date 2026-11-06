package main

import (
	"fmt"
	"os"
	"strings"

	"spindrift.dev/launcher/internal/promptassembly"
)

// driverInput holds the pieces of a Driver's argv (ADR 0009). The prompt,
// --agents JSON, and session pin/resume flags reach this process as files
// (issue #626); shape carries the argv layout as data (issue #2534).
type driverInput struct {
	shape       promptassembly.ArgvShape
	promptFile  string
	model       string
	effort      string
	agentsFile  string
	sessionFile string
	driverFlags string
}

// buildDriverArgs reads promptFile (and, when set, agentsFile/sessionFile) and
// assembles the Driver's argv by walking in.shape.Order. No driver-name
// conditional appears here: a new Driver's argv shape is a new
// promptassembly.ArgvShape data row, not a change to this function (issue
// #2534).
func buildDriverArgs(in driverInput) ([]string, error) {
	if in.shape.PromptStyle != "flag" && in.shape.PromptStyle != "positional" {
		return nil, fmt.Errorf("buildDriverArgs: invalid promptStyle %q, want \"flag\" or \"positional\"", in.shape.PromptStyle)
	}

	prompt, err := os.ReadFile(in.promptFile)
	if err != nil {
		return nil, err
	}

	var args []string
	for _, slot := range in.shape.Order {
		switch slot {
		case "prompt":
			if in.shape.PromptStyle == "flag" {
				args = append(args, in.shape.PromptFlag, string(prompt))
			} else {
				args = append(args, string(prompt))
			}
		case "model":
			if in.model != "" || !in.shape.ModelOmitEmpty {
				args = append(args, in.shape.ModelFlag, in.model)
			}
		case "agents":
			if in.shape.AgentsFlag == "" || in.agentsFile == "" {
				continue
			}
			agents, err := os.ReadFile(in.agentsFile)
			if err != nil {
				return nil, err
			}
			if len(agents) > 0 {
				args = append(args, in.shape.AgentsFlag, string(agents))
			}
		case "session":
			if in.sessionFile == "" {
				continue
			}
			session, err := os.ReadFile(in.sessionFile)
			if err != nil {
				return nil, err
			}
			args = append(args, strings.Fields(string(session))...)
		case "driverFlags":
			args = append(args, strings.Fields(in.driverFlags)...)
		case "effort":
			if in.effort == "" {
				continue
			}
			args = append(args, in.shape.EffortFlag, in.effort)
		default:
			return nil, fmt.Errorf("buildDriverArgs: unrecognised argv slot %q", slot)
		}
	}
	return args, nil
}
