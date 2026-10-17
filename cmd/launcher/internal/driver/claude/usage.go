package claude

import (
	"sort"

	"spindrift.dev/launcher/internal/driver/driverkit"
	"spindrift.dev/launcher/internal/usage"
)

// sumInLog sums every "type":"result" event in the log at path (orchestrator
// mode runs the driver repeatedly, one result event per session), except a
// result whose totals are mistyped, which the decoder flags NoTotals and the
// sum skips. It also returns the span of top-level timestamps seen. A missing
// file or an absent result event reports found=false, not an error.
func sumInLog(path string) (usage.Usage, driverkit.EventSpan, bool, error) {
	return driverkit.SumTotals(path, NewDecoder, driverkit.SessionTotals)
}

// breakdownByModelFile returns per-model token breakdowns from the log at path,
// and (nil, nil) when the file is missing. Each assistant event carries that one
// call's usage, so the sum runs over DISTINCT message.id: claude-code re-emits a
// multi-block message once per block with identical usage. It ignores the result
// event's own usage header, which covers one call only (the ~9x gap in #2078).
func breakdownByModelFile(path string) ([]usage.ModelUsage, error) {
	models, err := driverkit.UsageByModel(path, NewDecoder)
	if err != nil {
		return nil, err
	}

	// Deterministic order: opus, haiku, sonnet, then the rest (including
	// "unknown"). Rows stay keyed by exact model id, not the family name, so
	// ModelFamily orders only; within a family, rows sort by raw id.
	familyRank := func(id string) int {
		switch ModelFamily(id) {
		case "opus":
			return 0
		case "haiku":
			return 1
		case "sonnet":
			return 2
		default:
			return 3
		}
	}
	sort.Slice(models, func(i, j int) bool {
		if ri, rj := familyRank(models[i].Model), familyRank(models[j].Model); ri != rj {
			return ri < rj
		}
		return models[i].Model < models[j].Model
	})
	return models, nil
}

// breakdownByModel indirects to breakdownByModelFile so tests can simulate an
// I/O error without racing the sumInLog scan on the real filesystem.
var breakdownByModel = breakdownByModelFile

// breakdownByAgent indirects to the shared per-agent reader so tests can
// simulate an I/O error. Every row's OutputTokens stays 0 for ExtractUsage to
// patch from the result events.
var breakdownByAgent = func(path string) ([]usage.AgentUsage, error) {
	return driverkit.UsageByAgent(path, NewDecoder, driverkit.SessionTotals)
}

// ExtractUsage returns the totals, per-model and per-agent breakdowns of
// logPath as one usage.Report.
func ExtractUsage(logPath string) (usage.Report, error) {
	return driverkit.ExtractUsage(logPath, NewDecoder, driverkit.SessionTotals, breakdownByModel, breakdownByAgent)
}
