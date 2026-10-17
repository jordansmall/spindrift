package claude

import (
	"sort"

	"spindrift.dev/launcher/internal/usage"
)

// SortModelUsage orders models deterministically: opus, haiku, sonnet, then the
// rest (including "unknown"). Rows stay keyed by exact model id, not the family
// name, so ModelFamily orders only; within a family, rows sort by raw id.
func SortModelUsage(models []usage.ModelUsage) {
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
}
