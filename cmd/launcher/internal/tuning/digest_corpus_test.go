package tuning

import (
	"testing"

	"spindrift.dev/launcher/internal/digestkit"
)

// TestDigestCorpus replays the Digest corpus under testdata/digest through
// Render, pinning the digest text and the cursor it hands back (ADR 0070).
func TestDigestCorpus(t *testing.T) {
	digestkit.Run(t, renderDigest, "testdata/digest")
}

func renderDigest(t testing.TB, c digestkit.Case) (text, latest string) {
	var prior []Prior
	if len(c.Prior) > 0 {
		if err := digestkit.DecodeStrict(c.Prior, &prior); err != nil {
			t.Fatalf("tuning corpus: prior: %v", err)
		}
	}
	var merged MergeLookup
	if c.Merged != nil {
		merged = func(prURL string) (string, bool) {
			commit, ok := c.Merged[prURL]
			return commit, ok
		}
	}
	d := Render(c.Records, c.Cursor, c.Now, Limits{MinSample: c.MinSample, MaxBytes: c.MaxBytes}, prior, merged)
	return d.Text, d.Latest
}
