package tuning

import (
	"testing"

	"spindrift.dev/launcher/internal/digestkit"
	"spindrift.dev/launcher/internal/recordsdigest"
)

// TestDigestCorpus replays the Digest corpus under testdata/digest through
// the tuning Strategy's Render, pinning the digest text and the cursor it
// hands back (ADR 0070).
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
	in := recordsdigest.Input{
		Records: c.Records,
		Cursor:  c.Cursor,
		Now:     c.Now,
		Limits:  recordsdigest.Limits{MinSample: c.MinSample, MaxBytes: c.MaxBytes},
	}
	for _, p := range prior {
		in.Prior = append(in.Prior, p)
	}
	if c.Merged != nil {
		in.Merged = func(prURL string) (string, bool) {
			commit, ok := c.Merged[prURL]
			return commit, ok
		}
	}
	out := Strategy.Render(in)
	return out.Text, out.Latest
}
