package registryproxy

import "spindrift.dev/launcher/internal/registryvocab"

// findResponseRewriteRow returns the row matching method and path, with the
// base it matched under, or (nil, "") when none matches. A row matches only
// against the bases the host-side derivation enumerated for that row's own
// ecosystem (ADR 0047), never by stripping a suffix off path or guessing from
// media type. Rows are tried in order, so the first wins.
func findResponseRewriteRow(method, path string, rs routeState, rows []registryvocab.RewriteRow) (*registryvocab.RewriteRow, string) {
	for i := range rows {
		row := &rows[i]
		if row.Method != method {
			continue
		}
		for _, base := range rs.basesByEcosystem[row.Ecosystem] {
			if row.Matches(path, base) {
				return row, base
			}
		}
	}
	return nil, ""
}
