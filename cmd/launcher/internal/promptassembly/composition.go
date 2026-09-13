package promptassembly

import (
	"fmt"
	"strings"

	"spindrift.dev/launcher/internal/passmachine"
)

// SourceBytes is one Source's byte contribution to a pass's prompt.
type SourceBytes struct {
	Source
	Bytes int `json:"bytes"`
}

// CarriedText is a pass-specific block a later stage appends to the
// assembled prompt at pass time -- the orchestrator's own run-state handoff
// (reviewer findings, the decisions record) is the concrete case. Assemble
// never sees it, so a caller that wants it counted hands it to Compose.
// It is appended, never prepended (issue #3445): prompt caching is
// a prefix match, so keeping the assembled prompt as a byte-identical
// leading block across a run's passes -- rather than shifting it under a
// prepended block -- is what lets a seeded pass hit cache instead of paying
// a full re-write.
type CarriedText struct {
	Pass string // pass kind it is carried into; empty means every pass
	Name string
	Text string
}

// PassComposition breaks one pass kind's prompt down by source.
type PassComposition struct {
	Pass      string        `json:"pass"`
	Template  string        `json:"template"`
	Bytes     int           `json:"bytes"`
	Sources   []SourceBytes `json:"sources"`
	Remainder int           `json:"remainder"`
}

// PassDiff is two pass kinds' compositions set side by side.
type PassDiff struct {
	A           string        `json:"a"`
	B           string        `json:"b"`
	SharedBytes int           `json:"sharedBytes"`
	Shared      []SourceBytes `json:"shared"`
	OnlyA       []SourceBytes `json:"onlyA"`
	OnlyB       []SourceBytes `json:"onlyB"`
}

// Composition is Compose's report: every pass kind the cell renders, broken
// down by source, plus every pairwise diff between them.
type Composition struct {
	Passes []PassComposition `json:"passes"`
	Diffs  []PassDiff        `json:"diffs"`
}

// sourceAggregator sums bytes per Source in first-appearance order. Both
// inputs that feed one pass's Sources -- the rendered body's segments, then
// zero or more carried blocks -- run through the same aggregator, so a
// Source (e.g. two --composition-carried flags sharing one name) can never
// appear twice in the result even though it is built incrementally.
type sourceAggregator struct {
	order  []Source
	totals map[Source]int
}

func newSourceAggregator() *sourceAggregator {
	return &sourceAggregator{totals: map[Source]int{}}
}

func (a *sourceAggregator) add(src Source, n int) {
	if _, ok := a.totals[src]; !ok {
		a.order = append(a.order, src)
	}
	a.totals[src] += n
}

// addBody folds every segment of b into the aggregator.
func (a *sourceAggregator) addBody(b body) {
	for _, seg := range b {
		a.add(seg.src, len(seg.text))
	}
}

func (a *sourceAggregator) sources() []SourceBytes {
	out := make([]SourceBytes, len(a.order))
	for i, src := range a.order {
		out[i] = SourceBytes{Source: src, Bytes: a.totals[src]}
	}
	return out
}

// Compose reports the assembled prompt's composition for every pass kind
// e/reg's cell renders, without dispatching a Box: it calls
// checkCoveredCell then assemblePromptBodies -- the exact helper Assemble
// itself renders through -- so the report can never drift from what
// Assemble would actually produce.
func Compose(e Env, reg Registry, carried []CarriedText) (Composition, error) {
	if err := checkCoveredCell(e); err != nil {
		return Composition{}, err
	}

	bodies, err := assemblePromptBodies(e, reg)
	if err != nil {
		return Composition{}, err
	}

	type passDef struct {
		name     string
		body     body
		template string
	}

	// orchestratorOnFreshWork is true when the orchestrator is on for
	// fresh, non-research work, so the cell renders all five orchestrator
	// pass kinds rather than the single legacy or research pass.
	orchestratorOnFreshWork := bodies.review != nil

	var defs []passDef
	switch {
	case bodies.kind == "research":
		defs = []passDef{{name: "research", body: bodies.base, template: bodies.baseName}}
	case orchestratorOnFreshWork:
		defs = []passDef{
			{name: passmachine.KindImplement.ManifestKind(), body: bodies.base, template: bodies.baseName},
			{name: passmachine.KindFix.ManifestKind(), body: bodies.base, template: bodies.baseName},
			{name: passmachine.KindLand.ManifestKind(), body: bodies.base, template: bodies.baseName},
			{name: passmachine.KindReview.ManifestKind(), body: bodies.review, template: bodies.reviewName},
			{name: passmachine.KindDeltaReview.ManifestKind(), body: bodies.review, template: bodies.reviewName},
		}
	default:
		defs = []passDef{{name: passmachine.KindLegacy.ManifestKind(), body: bodies.base, template: bodies.baseName}}
	}

	// A carried block naming a pass kind this cell doesn't render would
	// otherwise vanish silently from every pass's Sources -- validate against
	// the actual pass set up front rather than inside the per-pass loop
	// below, so the check runs exactly once regardless of len(defs).
	knownPasses := make([]string, len(defs))
	knownPass := make(map[string]bool, len(defs))
	for i, d := range defs {
		knownPasses[i] = d.name
		knownPass[d.name] = true
	}
	for _, c := range carried {
		if c.Pass != "" && !knownPass[c.Pass] {
			return Composition{}, fmt.Errorf("composition: carried block %q names unknown pass %q; this cell renders: %s", c.Name, c.Pass, strings.Join(knownPasses, ", "))
		}
	}

	passes := make([]PassComposition, 0, len(defs))
	for _, d := range defs {
		agg := newSourceAggregator()
		agg.addBody(d.body)
		total := len(d.body.text())

		for _, c := range carried {
			if c.Pass != "" && c.Pass != d.name {
				continue
			}
			agg.add(Source{Kind: SourceCarried, Name: c.Name}, len(c.Text))
			total += len(c.Text)
		}

		sources := agg.sources()
		sum := 0
		for _, s := range sources {
			sum += s.Bytes
		}

		passes = append(passes, PassComposition{
			Pass:      d.name,
			Template:  d.template,
			Bytes:     total,
			Sources:   sources,
			Remainder: total - sum,
		})
	}

	var diffs []PassDiff
	for i := 0; i < len(passes); i++ {
		for j := i + 1; j < len(passes); j++ {
			diffs = append(diffs, DiffPasses(passes[i], passes[j]))
		}
	}

	return Composition{Passes: passes, Diffs: diffs}, nil
}

// DiffPasses partitions a and b's sources: a Source both carry contributes
// min(aBytes, bBytes) to Shared, with the positive remainder (if any) on
// each side going to that side's Only list; a Source only one side carries
// goes wholly to that side's Only list. Shared/OnlyA follow a's own source
// order, OnlyB follows b's.
func DiffPasses(a, b PassComposition) PassDiff {
	bIndex := make(map[Source]int, len(b.Sources))
	for i, sb := range b.Sources {
		bIndex[sb.Source] = i
	}
	consumedB := make([]int, len(b.Sources))

	diff := PassDiff{A: a.Pass, B: b.Pass}
	for _, asb := range a.Sources {
		bi, ok := bIndex[asb.Source]
		if !ok {
			diff.OnlyA = append(diff.OnlyA, asb)
			continue
		}
		bsb := b.Sources[bi]
		shared := min(asb.Bytes, bsb.Bytes)
		if shared > 0 {
			diff.Shared = append(diff.Shared, SourceBytes{Source: asb.Source, Bytes: shared})
			diff.SharedBytes += shared
		}
		if asb.Bytes > shared {
			diff.OnlyA = append(diff.OnlyA, SourceBytes{Source: asb.Source, Bytes: asb.Bytes - shared})
		}
		consumedB[bi] = shared
	}

	for i, bsb := range b.Sources {
		if rem := bsb.Bytes - consumedB[i]; rem > 0 {
			diff.OnlyB = append(diff.OnlyB, SourceBytes{Source: bsb.Source, Bytes: rem})
		}
	}

	return diff
}
