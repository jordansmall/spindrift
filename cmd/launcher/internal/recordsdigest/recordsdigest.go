// Package recordsdigest is the Digest Strategy a records-scoped Chore fills
// in and the registry of those fillings (ADR 0070 decision 2). It mirrors
// backendkit's role: the records adapter renders a Chore's Records digest
// through the Strategy registered under the Chore's name, so it names no
// Chore.
//
// recordsdigest imports no Chore; built-in Digests register from the
// host-side bundle (internal/hostbundle).
package recordsdigest

import (
	"fmt"
	"time"

	"spindrift.dev/launcher/internal/dispatchrecord"
	"spindrift.dev/launcher/internal/settle"
	"spindrift.dev/launcher/internal/signalwire"
)

// MergeLookup reports whether the PR at prURL merged, and the commit it landed
// as ("" when the forge cannot say). It is a forge round trip, so a Digest
// should call it at most once per prior finding.
type MergeLookup func(prURL string) (commit string, ok bool)

// NotRegistered is the error for a records-scoped Chore with no registered
// Digest: records Chores are built in, so it is a build wiring gap.
func NotRegistered(chore string) error {
	return fmt.Errorf("butler: chore %q is records-scoped (scopeSource = \"records\") but no Records digest is registered for it; records Chores are built in (ADR 0070)", chore)
}

// Limits are the row's sample floor and digest byte cap. The Digest reads
// them from its row-owned knobs through ReadKnobs (issue #5700).
type Limits struct {
	// MinSample marks a row thin when fewer window Records back it.
	MinSample int
	// MaxBytes caps the digest's size; zero or less means uncapped. The tuning
	// Digest trims Evidence first, Outliers second, and Prior findings third,
	// always from the tail; its aggregate tables are never trimmed, so a cap
	// below them is exceeded.
	MaxBytes int
}

// Lookup resolves a row-owned knob's env name, in os.LookupEnv's shape.
type Lookup func(env string) (value string, ok bool)

// Knobs are the row-owned settings a Digest reads itself.
type Knobs struct {
	Limits Limits
	// Cooldown is how long a finding closed as not planned keeps suppressing a
	// refile of its pattern; zero never suppresses on a not-planned close.
	Cooldown time.Duration
}

// Input is what a Digest renders from.
type Input struct {
	// Records is the store's Records; those past Cursor are the new window.
	Records []dispatchrecord.Record
	// Cursor is the prior cursor: the latest Record ID the last sweep covered.
	Cursor string
	// Now is the claim time.
	Now    time.Time
	Limits Limits
	// Prior holds each prior finding as ParsePrior read it.
	Prior []any
	// Merged is nil without a merge reader.
	Merged MergeLookup
}

// Output is a rendered digest.
type Output struct {
	Text string
	// Latest is the latest Record ID the digest covers.
	Latest string
}

// Strategy is a records-scoped Chore's Records digest, registered under the
// Chore's name. Render and Anchors are required; a nil optional hook means
// the Chore does without that feature.
type Strategy struct {
	// Name is the Chore name.
	Name string
	// ReadKnobs reads the Chore's row-owned settings through the lookup. Nil
	// means the Chore owns none; call Knobs rather than this field.
	ReadKnobs func(Lookup) (Knobs, error)
	// Render builds the digest.
	Render func(Input) Output
	// Anchors lists what a finding may cite in a digest Render produced. The
	// anchor syntax is the Chore's own, so the adapter cannot parse it; it drops
	// any finding with no cites or one citing an anchor outside this set.
	Anchors func(digest string) map[string]bool
	// ParsePrior reads a prior finding into the Chore's own form. Nil means
	// the Chore takes no prior findings, and the adapter does not query the
	// backlog.
	ParsePrior func(settle.PriorFinding) any
	// Validate applies the Chore's own settle rules, beyond the adapter's cite
	// check, to a filed intent against the stored digest the Box was served,
	// under the same Limits Render got.
	// tracked lists the scanned HEAD's tracked files, lazily. A non-empty
	// reason drops the finding with that reason; "" keeps it; an error fails
	// the settle and leaves the claim standing.
	Validate func(in signalwire.IssueIntent, digest string, limits Limits, tracked func() (map[string]bool, error)) (reason string, err error)
	// DedupTerms returns the Chore's dedup pattern terms for a finding.
	DedupTerms func(settle.Finding) []string
	// Evidence renders the host-written block appended to a filed finding's
	// body, from the stored digest and the finding's cites; "" appends nothing.
	// Nil means the Chore appends no block.
	Evidence func(digest string, cites []string) string
}

// Knobs reads the Chore's row-owned settings; a Chore with no ReadKnobs owns
// none and gets zero Knobs.
func (s Strategy) Knobs(l Lookup) (Knobs, error) {
	if s.ReadKnobs == nil {
		return Knobs{}, nil
	}
	return s.ReadKnobs(l)
}

// registry keeps registration order.
var registry []Strategy

// Register adds s to the registry, keyed on s.Name. It panics on a duplicate
// or empty name or a nil Render or Anchors, which are programming errors.
func Register(s Strategy) {
	if s.Name == "" {
		panic("recordsdigest: registration with empty name")
	}
	if s.Render == nil {
		panic("recordsdigest: nil Render for " + s.Name)
	}
	if s.Anchors == nil {
		panic("recordsdigest: nil Anchors for " + s.Name)
	}
	if _, exists := ByName(s.Name); exists {
		panic("recordsdigest: duplicate registration for " + s.Name)
	}
	registry = append(registry, s)
}

// All returns the registered Strategies in registration order.
func All() []Strategy {
	return append([]Strategy(nil), registry...)
}

// ByName returns the registered Strategy; ok is false for an unregistered
// name, with no fallback.
func ByName(name string) (Strategy, bool) {
	for _, s := range registry {
		if s.Name == name {
			return s, true
		}
	}
	return Strategy{}, false
}
