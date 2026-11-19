//go:build !integration

package main

import (
	"os"
	"testing"

	"spindrift.dev/launcher/internal/driver/drivertest"
)

// The integration build's TestMain (box_seam_test.go) registers the same Driver.
func TestMain(m *testing.M) { os.Exit(drivertest.RunWithClaudeConnector(m)) }
