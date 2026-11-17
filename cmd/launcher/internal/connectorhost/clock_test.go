package connectorhost_test

import (
	"testing"
	"time"
)

// A decoder sees the host's time through newDecoder; the fixture echoes it
// for "now".
func TestGuestDecoderGetsTheHostClock(t *testing.T) {
	c, err := load(t, "driver.wasm")
	if err != nil {
		t.Fatal(err)
	}
	pinned := time.Date(2026, 11, 17, 2, 20, 0, 0, time.UTC)
	c.SetClock(func() time.Time { return pinned })
	raw, js := rawAndJSON(t, c)
	want := pinned.Format(time.RFC3339)

	for name, decode := range map[string]func() string{
		"decoder": func() string { return raw.NewDecoder()("now")[0].Text },
		"raw batch": func() string {
			return raw.NewBatchDecoder()([]string{"now"})[0][0].Text
		},
		"JSON batch": func() string {
			return js.NewBatchDecoder()([]string{"now"})[0][0].Text
		},
	} {
		if got := decode(); got != want {
			t.Errorf("%s: guest now = %q; want %q", name, got, want)
		}
	}
}
