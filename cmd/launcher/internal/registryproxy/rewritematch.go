package registryproxy

import "spindrift.dev/launcher/internal/registryvocab"

// findResponseRewriteRow returns the rows entry matching method and path,
// plus the base the match was found under, or (nil, "") when no row
// matches.
//
// A row matches iff its Method equals method and its Matches func reports
// true against path for some base in rs.basesByEcosystem[row.Ecosystem] --
// checked by membership in that ecosystem's derived bases, not by stripping
// a suffix off path or guessing from media type: ADR 0047 keys a row on the
// exact subtrees the host-side derivation already enumerated for its own
// ecosystem, tagged so one ecosystem's row can never match against another
// ecosystem's bases. Rows are tried in the order given, so a caller
// declaring two rows for the same method+ecosystem shape gets the first.
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
