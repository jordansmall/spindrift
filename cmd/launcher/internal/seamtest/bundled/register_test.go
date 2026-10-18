// Package bundled holds the test that RegisterDriver defers to a linked
// bundle; seamtest's own tests cannot, since they link no Driver.
package bundled

import (
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/driver"
	_ "spindrift.dev/launcher/internal/driver/bundle"
	"spindrift.dev/launcher/internal/seamtest"
)

func TestRegisterDriverIsNoOpWithBundle(t *testing.T) {
	before := driver.Names()
	seamtest.RegisterDriver(t)
	if got := driver.Names(); strings.Join(got, ",") != strings.Join(before, ",") {
		t.Errorf("Names() = %v, want %v", got, before)
	}
	d, err := driver.New("claude")
	if err != nil {
		t.Fatal(err)
	}
	ev, err := d.ResultEvent("real")
	if err != nil {
		t.Fatal(err)
	}
	if strings.HasPrefix(string(ev), `{"Kind":"result"`) {
		t.Errorf("claude Driver encoded %q, want the real claude grammar", ev)
	}
}
