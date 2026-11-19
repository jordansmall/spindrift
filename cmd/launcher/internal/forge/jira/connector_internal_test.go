package jira

import (
	"errors"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/connectortracker"
)

// The Connector has no native writes behind it, so one that omits a method
// core drives is refused at load rather than panicking on first use.
func TestMissingConnectorCapabilities(t *testing.T) {
	full := []string{"tracker", connectortracker.CapTransition, connectortracker.CapEditLabels, connectortracker.CapLabels,
		connectortracker.CapComment, connectortracker.CapProbe, connectortracker.CapFileIssue, connectortracker.CapLabeled, connectortracker.CapComments}
	if got := missingConnectorCapabilities(full); len(got) != 0 {
		t.Errorf("missing = %v from a full handshake, want none", got)
	}
	for _, want := range connectorCapabilities {
		var caps []string
		for _, c := range full {
			if c != want {
				caps = append(caps, c)
			}
		}
		got := missingConnectorCapabilities(caps)
		if len(got) != 1 || got[0] != want {
			t.Errorf("missing = %v without %q, want [%s]", got, want, want)
		}
		if err := checkConnectorCapabilities(caps); !errors.Is(err, errConnectorCapabilities) || !strings.Contains(err.Error(), want) {
			t.Errorf("err = %v without %q, want errConnectorCapabilities naming it", err, want)
		}
	}
}
