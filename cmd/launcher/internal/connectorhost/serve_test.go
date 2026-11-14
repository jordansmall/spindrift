package connectorhost

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

func TestServeNamesWhatIsWrongWithTheFirstFrame(t *testing.T) {
	for name, tc := range map[string]struct {
		kind byte
		want string
	}{
		"wrong kind":            {kindCall, "want a load"},
		"load without its byte": {kindLoad, "grant byte"},
	} {
		t.Run(name, func(t *testing.T) {
			var in bytes.Buffer
			if err := writeFrame(&in, tc.kind); err != nil {
				t.Fatal(err)
			}
			err := Serve(context.Background(), &in, &bytes.Buffer{})
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Serve = %v, want an error containing %q", err, tc.want)
			}
		})
	}
}
