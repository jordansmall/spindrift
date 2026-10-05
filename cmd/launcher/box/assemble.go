package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"

	"spindrift.dev/launcher/internal/promptassembly"
)

// assemblyInputs are the prompt-assembly facts entrypoint.sh holds as
// shell-local variables, which box cannot read from its environment without
// entrypoint.sh exporting them into the Driver's env too. Everything else
// assembly needs box reads off the environment it inherits.
type assemblyInputs struct {
	promptassembly.AssemblyFlags
	SkillsDir string
}

// assemblePrompt produces the prompt, agents JSON, review prompt and handoff
// document into a fresh directory that outlives the call, since the
// orchestrator and driver-exec read the files the handoff names after it
// returns. It returns the handoff file's path; validate warnings go to w.
func assemblePrompt(in assemblyInputs, env promptassembly.Env, w io.Writer) (string, error) {
	reg, err := promptassembly.LoadRegistryFile(in.RegistryFile)
	if err != nil {
		return "", err
	}
	markers, err := promptassembly.LoadValidateMarkersFile(in.ValidateMarkersFile)
	if err != nil {
		return "", err
	}

	env.ProbeBakedSkills(in.SkillsDir)
	env.SkillsFound = promptassembly.ScanSkillsFound(in.SkillsDir)
	in.ApplyTo(&env)

	dir, err := os.MkdirTemp("", "box-assembly-")
	if err != nil {
		return "", fmt.Errorf("create assembly dir: %w", err)
	}
	out := promptassembly.OutputPaths{
		Prompt:       filepath.Join(dir, "prompt.txt"),
		AgentsJSON:   filepath.Join(dir, "agents.json"),
		Handoff:      filepath.Join(dir, "handoff.json"),
		ReviewPrompt: filepath.Join(dir, "review-prompt.txt"),
	}
	if _, err := promptassembly.WriteAssembly(env, reg, markers, in.Passthrough, out, w); err != nil {
		return "", err
	}
	return out.Handoff, nil
}
