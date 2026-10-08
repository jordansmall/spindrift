package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"

	"spindrift.dev/launcher/internal/driver/claude"
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
	emitPromptHashes(env, reg, w)
	return out.Handoff, nil
}

// emitPromptHashes writes the prompt_hashes op (issue #4786) onto the Pass log.
// An older host forwards no Record ID, so there is nothing to key the op to.
// Hashing is telemetry: a failure warns and never fails the Box.
func emitPromptHashes(env promptassembly.Env, reg promptassembly.Registry, w io.Writer) {
	if env.RecordID == "" {
		return
	}
	hashes, err := promptassembly.TemplateHashes(env, reg)
	if err != nil {
		fmt.Fprintf(w, "box: prompt template hashes skipped: %v\n", err)
		return
	}
	fmt.Fprint(w, claude.EncodeSpindriftOp(claude.SpindriftOp{
		Op:           "prompt_hashes",
		PromptHashes: &claude.PromptHashes{RecordID: env.RecordID, Roles: hashes},
	}))
}
