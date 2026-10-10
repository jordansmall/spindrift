package tuning

import (
	"strconv"
	"strings"
)

// tableHeader is the header and separator lines writeTable and Evidence share.
const tableHeader = "| Anchor | Metric | n | Window | Baseline | Δ | Flag |\n" +
	"|---|---|---|---|---|---|---|\n"

// Row is one aggregate row read back out of a rendered digest.
type Row struct {
	Anchor string
	N      int
	// Line is the row's table line exactly as the digest holds it, without
	// the trailing newline.
	Line string
}

// Rows returns every aggregate table row in a rendered digest keyed by anchor.
// The header and separator are not rows: their n cell does not parse as an int.
func Rows(digest string) map[string]Row {
	rows := map[string]Row{}
	for _, line := range strings.Split(digest, "\n") {
		if !strings.HasPrefix(line, "| ") {
			continue
		}
		cells := strings.Split(strings.TrimSuffix(strings.TrimPrefix(line, "| "), "|"), " | ")
		if len(cells) < 3 {
			continue
		}
		n, err := strconv.Atoi(cells[2])
		if err != nil {
			continue
		}
		rows[cells[0]] = Row{Anchor: cells[0], N: n, Line: line}
	}
	return rows
}

// Evidence renders the "## Evidence" section for a finding: the cited rows,
// in cite order and deduplicated, copied from the stored snapshot rather than
// the model's transcription. Cites absent from rows are skipped.
func Evidence(rows map[string]Row, cites []string) string {
	var b strings.Builder
	b.WriteString("## Evidence\n\n")
	b.WriteString("The host re-rendered these rows from the digest snapshot the sweep was served.\n\n")
	b.WriteString(tableHeader)
	seen := map[string]bool{}
	for _, c := range cites {
		r, ok := rows[c]
		if !ok || seen[c] {
			continue
		}
		seen[c] = true
		b.WriteString(r.Line + "\n")
	}
	return b.String()
}
