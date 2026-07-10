package main

import (
	"errors"
	"testing"

	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/settle"
)

// TestRun_EmptyQueue_ReturnsErrQueueEmpty asserts run's orchestration logic
// (as opposed to the bootstrap prologue) runs correctly against a
// fake-populated launchContext, with no ISSUE_NUMBER and no dispatchable
// issues in the fake forge.
func TestRun_EmptyQueue_ReturnsErrQueueEmpty(t *testing.T) {
	c := baseConfig()
	c.label = "ready-for-agent"
	dir := tempLogDir(t)
	lc := &launchContext{
		config:  c,
		pwd:     dir,
		forge:   forge.NewFake(),
		factory: testFactory(t, dir, nil),
		settle:  settle.NewFake(),
	}

	err := run(lc)

	if !errors.Is(err, errQueueEmpty) {
		t.Fatalf("run(lc) = %v, want errQueueEmpty", err)
	}
}
