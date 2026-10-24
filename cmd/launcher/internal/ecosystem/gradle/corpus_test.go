package gradle_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/ecosystem"
	"spindrift.dev/launcher/internal/ecosystem/ecosystemkit"
	"spindrift.dev/launcher/internal/ecosystem/gradle"
	"spindrift.dev/launcher/internal/ecosystemtest"
)

// TestCorpus replays the gradle Ecosystem corpus through the ecosystemtest kit
// (ADR 0066 decision 4). Cases that name the "neighbour" row get a synthetic
// stand-in, because an ecosystem package never imports another.
func TestCorpus(t *testing.T) {
	ecosystemtest.Run(t, gradle.Strategy, filepath.Join("testdata", "corpus"), lookup)
}

// neighbour is a synthetic, format-neutral row standing in for any other
// ecosystem: its invented neighbour.url holds one registry URL. It deliberately
// borrows no real ecosystem's name or file format.
var neighbour = ecosystem.Strategy{
	Name:             "neighbour",
	InTreeConfigPath: "neighbour.url",
	ConfigParser: func(content string) ([]ecosystem.Declaration, bool, error) {
		host, base, ok := ecosystemkit.HTTPAbsoluteURL(strings.TrimSpace(content))
		if !ok {
			return nil, true, nil
		}
		return []ecosystem.Declaration{{Host: host, UpstreamBaseURL: base}}, true, nil
	},
}

func lookup(name string) (ecosystem.Strategy, bool) {
	switch name {
	case gradle.Strategy.Name:
		return gradle.Strategy, true
	case neighbour.Name:
		return neighbour, true
	}
	return ecosystem.Strategy{}, false
}

// The replay pipeline reads the process-wide registry (route parsing validates
// ecosystem names against it), and the Bundle that owns the real Register call
// is not linked here. Register the neighbour before gradle, as the Bundle lists
// its rows.
func TestMain(m *testing.M) {
	ecosystem.Register(neighbour, gradle.Strategy)
	os.Exit(m.Run())
}
