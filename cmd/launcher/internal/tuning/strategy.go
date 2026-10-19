package tuning

import (
	"fmt"
	"slices"
	"strings"
	"time"

	"spindrift.dev/launcher/internal/recordsdigest"
	"spindrift.dev/launcher/internal/settle"
	"spindrift.dev/launcher/internal/signalwire"
)

// Strategy is the tuning Chore's Digest (ADR 0070 decision 2). The host-side
// bundle registers it; this package does not.
var Strategy = recordsdigest.Strategy{
	Name:      "tuning",
	ReadKnobs: readKnobs,
	Render: func(in recordsdigest.Input) recordsdigest.Output {
		prior := make([]Prior, 0, len(in.Prior))
		for _, p := range in.Prior {
			prior = append(prior, p.(Prior))
		}
		d := Render(in.Records, in.Cursor, in.Now, in.Limits, prior, in.Merged)
		return recordsdigest.Output{Text: d.Text, Latest: d.Latest}
	},
	Anchors: Anchors,
	ParsePrior: func(f settle.PriorFinding) any {
		p := Prior{
			Number:     f.Issue.Number,
			Open:       f.Open,
			NotPlanned: f.Issue.ClosedNotPlanned,
			Reopened:   f.Issue.Reopened,
			Body:       f.Issue.Body,
		}
		if len(f.Terms) > 0 {
			t := f.Terms[0]
			p.Metric = t[strings.LastIndex(t, ":")+1:]
		}
		return p
	},
	Validate: func(in signalwire.IssueIntent, digest string, lim recordsdigest.Limits, tracked func() (map[string]bool, error)) (string, error) {
		files := settle.DedupFiles(in.DedupTerms)
		if len(files) == 0 {
			return dropMissingTarget, nil
		}
		set, err := tracked()
		if err != nil {
			return "", err
		}
		for _, f := range files {
			if !set[f] {
				return dropMissingTarget, nil
			}
		}
		rows := Rows(digest)
		if in.Class != evidenceGapClass && !slices.ContainsFunc(in.Cites, func(c string) bool { return rows[c].N >= lim.MinSample }) {
			return dropThinEvidence, nil
		}
		return "", nil
	},
	// DedupTerms keys a finding on the pattern it names, one class:file:metric
	// per file, not on the code site (ADR 0062 §6): two patterns on one prompt
	// file stay distinct. It feeds only settle's dedup; f.DedupTerms stays the
	// files, which the butler's backlink still reads.
	DedupTerms: func(f settle.Finding) []string {
		var terms []string
		for _, file := range settle.DedupFiles(f.DedupTerms) {
			terms = append(terms, f.Class+":"+file+":"+f.Metric)
		}
		return terms
	},
	Evidence: func(digest string, cites []string) string {
		return Evidence(Rows(digest), cites)
	},
}

// readKnobs maps the row's generated knob reader onto the Digest's Knobs. A
// zero cooldown is legal (a not-planned close then never suppresses a
// refile); only a negative one is rejected.
func readKnobs(l recordsdigest.Lookup) (recordsdigest.Knobs, error) {
	k, err := ReadKnobs(Lookup(l))
	if err != nil {
		return recordsdigest.Knobs{}, err
	}
	cooldown, err := time.ParseDuration(k.ButlerTuningCooldown)
	if err != nil {
		return recordsdigest.Knobs{}, fmt.Errorf("BUTLER_TUNING_COOLDOWN: invalid duration %q: %w", k.ButlerTuningCooldown, err)
	}
	if cooldown < 0 {
		return recordsdigest.Knobs{}, fmt.Errorf("BUTLER_TUNING_COOLDOWN: must be >= 0, got %q", k.ButlerTuningCooldown)
	}
	return recordsdigest.Knobs{
		Limits:   recordsdigest.Limits{MinSample: k.ButlerTuningMinSample, MaxBytes: k.ButlerTuningDigestBytes},
		Cooldown: cooldown,
	}, nil
}

// Drop reasons recorded in the Ledger for a tuning finding settle refused.
const (
	dropMissingTarget = "missing-target"
	dropThinEvidence  = "thin-evidence"
)

// evidenceGapClass is exempt from the sample floor: it reports a metric the
// digest cannot yet support, so thin rows are its whole point.
const evidenceGapClass = "evidence-gap"
