//go:build !escapeprobe

package connectorhost_test

import (
	"context"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/connectorhost"
)

// A production Load instantiates no spindrift_escape module, so the escape
// fixture cannot link: the primitives exist only in an escapeprobe build.
func TestEscapeFixtureFailsToLinkInProductionBuild(t *testing.T) {
	c, err := connectorhost.Load(context.Background(), fixture(t, "escape.wasm"))
	if err == nil {
		_ = c.Close(context.Background())
		t.Fatal("Load of escape.wasm succeeded; production build exposes spindrift_escape")
	}
	if !strings.Contains(err.Error(), "spindrift_escape") {
		t.Fatalf("Load error = %v; want the unlinked spindrift_escape import named", err)
	}
}
