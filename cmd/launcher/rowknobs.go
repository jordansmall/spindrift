package main

import "strconv"

// rowKnobs maps each row-owned knob a row's ReadKnobs reads to the launcher
// config field that resolves it.
var rowKnobs = map[string]func(c config) string{
	"FORGEJO_BASE_URL":                 func(c config) string { return c.forgejoBaseURL },
	"FORGEJO_TOKEN":                    func(c config) string { return c.forgejoToken },
	"JIRA_BASE_URL":                    func(c config) string { return c.jiraBaseURL },
	"JIRA_PROJECT_KEY":                 func(c config) string { return c.jiraProjectKey },
	"JIRA_EMAIL":                       func(c config) string { return c.jiraEmail },
	"JIRA_TOKEN":                       func(c config) string { return c.jiraToken },
	"JIRA_STATUS_MAPPING":              func(c config) string { return c.jiraStatusMapping },
	"JIRA_INCLUDE_COMMENTS":            func(c config) string { return strconv.FormatBool(c.jiraIncludeComments) },
	"LOCAL_ISSUES_DIR":                 func(c config) string { return c.localIssuesDir },
	"CODE_FORGE_ACCUMULATION_REPO_DIR": func(c config) string { return c.codeForgeAccumulationRepoDir },
	"CODE_FORGE_REMOTE_URL":            func(c config) string { return c.codeForgeRemoteURL },
	"BUTLER_TUNING_MIN_RECORDS":        func(c config) string { return strconv.Itoa(c.butlerTuningMinRecords) },
	"BUTLER_TUNING_MIN_SAMPLE":         func(c config) string { return strconv.Itoa(c.butlerTuningMinSample) },
	"BUTLER_TUNING_DIGEST_BYTES":       func(c config) string { return strconv.Itoa(c.butlerTuningDigestBytes) },
	"BUTLER_TUNING_COOLDOWN":           func(c config) string { return c.butlerTuningCooldown },
}

// knobLookup serves the row knobs from c's already-resolved fields,
// so a Strategy's ReadKnobs sees what the launcher resolved, not the process
// environment.
func (c config) knobLookup(env string) (string, bool) {
	if field, ok := rowKnobs[env]; ok {
		return field(c), true
	}
	return "", false
}
