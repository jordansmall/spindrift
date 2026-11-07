package promptassembly

import (
	"fmt"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/forge"
)

// researchVerdictTemplate carries the three raw markers a prompt-dir override
// ships and the baked prompt has rendered at nix eval time (issues #2630,
// #4159).
const researchVerdictTemplate = "status=<${RESEARCH_STATUS_ENUM}>\n" +
	"enum: `<RESEARCH_VERDICT_ENUM>`\n" +
	"<!-- RESEARCH_VERDICT_BULLETS -->\n"

const customResearchVerdicts = `[{"verdict":"accept","label":"l-accept","description":"take it up"},{"verdict":"decline","label":"l-decline","description":"leave it"}]`

func researchVerdictRegistry(t *testing.T, files map[string]string) (Registry, Env) {
	t.Helper()
	reg, env := syntheticRegistry(t, nil, files)
	env.DispatchKind = "research"
	return reg, env
}

// The three verdict renderings (status alternation, verdict enum, verdict
// bullets) are Go-derived from the raw RESEARCH_VERDICTS JSON, so a raw
// template renders the configured set in order; empty keeps the default three.
func TestAssembleResearchVerdictRenderingsDerivedFromVerdicts(t *testing.T) {
	for _, selfContained := range []bool{false, true} {
		for _, tc := range []struct {
			name, verdicts, want string
		}{
			{"default", "",
				"status=<recommend|reject|unclear>\nenum: `recommend` / `reject` / `unclear`\n- `recommend` — "},
			{"custom", customResearchVerdicts,
				"status=<accept|decline>\nenum: `accept` / `decline`\n- `accept` — take it up\n- `decline` — leave it"},
		} {
			t.Run(fmt.Sprintf("%s/selfContained=%v", tc.name, selfContained), func(t *testing.T) {
				reg, env := researchVerdictRegistry(t, map[string]string{
					"research-prompt.md":                researchVerdictTemplate,
					"research-self-contained-prompt.md": researchVerdictTemplate,
				})
				env.SelfContained = selfContained
				env.ResearchVerdicts = tc.verdicts

				result, err := Assemble(env, reg)
				if err != nil {
					t.Fatalf("Assemble: %v", err)
				}
				if !strings.HasPrefix(result.Prompt, tc.want) {
					t.Errorf("Prompt = %q, want prefix %q", result.Prompt, tc.want)
				}
				if strings.Contains(result.Prompt, "${RESEARCH_STATUS_ENUM}") || strings.Contains(result.Prompt, "RESEARCH_VERDICT_") {
					t.Errorf("Prompt contains an unrendered verdict marker: %q", result.Prompt)
				}
			})
		}
	}
}

// A prompt nix already rendered at eval time (the baked path) carries none of
// the markers, so assembly with the matching RESEARCH_VERDICTS leaves it as is.
func TestAssembleResearchBakedPromptUnchanged(t *testing.T) {
	verdicts, err := forge.ParseResearchVerdicts(customResearchVerdicts)
	if err != nil {
		t.Fatalf("ParseResearchVerdicts: %v", err)
	}
	baked := verdicts.RenderPrompt(researchVerdictTemplate)
	if forge.HasVerdictTargets(baked) {
		t.Fatalf("baked prompt still carries a target")
	}

	rawReg, rawEnv := researchVerdictRegistry(t, map[string]string{"research-prompt.md": researchVerdictTemplate})
	bakedReg, bakedEnv := researchVerdictRegistry(t, map[string]string{"research-prompt.md": baked})
	rawEnv.ResearchVerdicts, bakedEnv.ResearchVerdicts = customResearchVerdicts, customResearchVerdicts

	fromRaw, err := Assemble(rawEnv, rawReg)
	if err != nil {
		t.Fatalf("Assemble raw: %v", err)
	}
	fromBaked, err := Assemble(bakedEnv, bakedReg)
	if err != nil {
		t.Fatalf("Assemble baked: %v", err)
	}
	if fromBaked.Prompt != fromRaw.Prompt {
		t.Errorf("baked prompt assembled differently from the raw template:\n%s\nvs\n%s", fromBaked.Prompt, fromRaw.Prompt)
	}
	if !strings.Contains(fromBaked.Prompt, "status=<accept|decline>") {
		t.Errorf("baked Prompt missing status=<accept|decline>:\n%s", fromBaked.Prompt)
	}
}

// RESEARCH_VERDICTS is parsed only when the base prompt carries a verdict
// marker, so a malformed value cannot abort a work assembly that
// never reads it, yet fails a research one that does.
func TestAssembleMalformedResearchVerdictsFailOnlyWhenPromptCarriesMarker(t *testing.T) {
	for _, tc := range []struct {
		name, kind, template string
		wantErr              bool
	}{
		{"work prompt without markers", "work", "issue\n", false},
		{"research prompt without markers", "research", "research\n", false},
		{"research prompt with markers", "research", researchVerdictTemplate, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reg, env := syntheticRegistry(t, nil, map[string]string{"issue-prompt.md": tc.template, "research-prompt.md": tc.template})
			env.DispatchKind = tc.kind
			env.ResearchVerdicts = "not json"

			_, err := Assemble(env, reg)
			if !tc.wantErr {
				if err != nil {
					t.Fatalf("Assemble: %v, want nil error", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), "RESEARCH_VERDICTS") {
				t.Fatalf("Assemble error = %v, want a RESEARCH_VERDICTS parse error", err)
			}
		})
	}
}
