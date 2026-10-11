package tuning

import (
	"strconv"
	"strings"
)

// tableHeader is the header and separator lines writeTable and Evidence share.
const tableHeader = "| Anchor | Metric | n | Window | Baseline | Δ | Flag |\n" +
	"|---|---|---|---|---|---|---|\n"

// Section headings the renderer writes and the parsers match on;
// evidenceHeading also titles the finding's issue-body section, so the two stay
// tied.
const (
	summaryHeading  = "## Summary"
	rolesHeading    = "## Roles"
	splitsHeading   = "## Splits"
	priorHeading    = "## Prior findings"
	outliersHeading = "## Outliers"
	evidenceHeading = "## Evidence"
)

// Line prefixes of the anchored Outliers rows and Evidence items, shared by the
// renderer and Anchors so a format change cannot leave anchors unparsed.
const (
	priorRowPrefix     = "| finding:"
	outlierRowPrefix   = "| record:"
	evidenceItemPrefix = "**evidence:"
)

// Row is one aggregate row read back out of a rendered digest.
type Row struct {
	Anchor string
	N      int
	// Line is the row's table line exactly as the digest holds it, without
	// the trailing newline.
	Line string
}

// Rows returns every aggregate table row in a rendered digest keyed by anchor.
// It reads only the host-rendered Summary, Roles and Splits tables, each opened
// by the exact tableHeader, and stops at the first other "## " heading: the
// Outliers rows carry Record IDs, not aggregates, and the Evidence text is
// Box-written and can echo a forged heading, header or row.
func Rows(digest string) map[string]Row {
	rows := map[string]Row{}
	header, sep, _ := strings.Cut(strings.TrimSuffix(tableHeader, "\n"), "\n")
	inTable := false
	for _, line := range strings.Split(digest, "\n") {
		if strings.HasPrefix(line, "## ") {
			if line != summaryHeading && line != rolesHeading && line != splitsHeading {
				break
			}
			inTable = false
			continue
		}
		if line == header {
			inTable = true
			continue
		}
		if line == sep {
			continue
		}
		if !strings.HasPrefix(line, "| ") {
			inTable = false
			continue
		}
		if !inTable {
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
// the model's transcription. Cites absent from rows, such as record: and
// evidence: anchors, are skipped; it returns "" when no cite is a row.
func Evidence(rows map[string]Row, cites []string) string {
	var lines strings.Builder
	seen := map[string]bool{}
	for _, c := range cites {
		r, ok := rows[c]
		if !ok || seen[c] {
			continue
		}
		seen[c] = true
		lines.WriteString(r.Line + "\n")
	}
	if lines.Len() == 0 {
		return ""
	}
	return evidenceHeading + "\n\n" +
		"The host re-rendered these rows from the digest snapshot the sweep was served.\n\n" +
		tableHeader + lines.String()
}

// Anchors returns every anchor a rendered digest offers a finding to cite: the
// aggregate rows, the Prior findings "finding:<n>" rows, the Outliers
// "record:<id>" rows, and the Evidence "evidence:<id>:<n>:<kind>" items. Only
// host-written lines count. Evidence text is Box-written inside a
// promptfence.Block fence, so lines from a fence opener to its identical
// closer are skipped, and a forged heading, row or evidence line inside one
// neither adds an anchor nor changes section.
func Anchors(digest string) map[string]bool {
	anchors := map[string]bool{}
	for a := range Rows(digest) {
		anchors[a] = true
	}
	section, fence := "", ""
	for _, line := range strings.Split(digest, "\n") {
		if fence != "" {
			if line == fence {
				fence = ""
			}
			continue
		}
		if strings.HasPrefix(line, "```") {
			fence = line
			continue
		}
		if strings.HasPrefix(line, "## ") {
			section = line
			continue
		}
		switch {
		case section == priorHeading && strings.HasPrefix(line, priorRowPrefix),
			section == outliersHeading && strings.HasPrefix(line, outlierRowPrefix):
			if a, _, ok := strings.Cut(strings.TrimPrefix(line, "| "), " |"); ok {
				anchors[a] = true
			}
		case section == evidenceHeading && strings.HasPrefix(line, evidenceItemPrefix):
			if a, _, ok := strings.Cut(strings.TrimPrefix(line, "**"), "**"); ok {
				anchors[a] = true
			}
		}
	}
	return anchors
}
