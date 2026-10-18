package main

import (
	"testing"

	"spindrift.dev/launcher/internal/driver/drivertest"
)

func TestBinaryLinksDriverBundle(t *testing.T) { drivertest.RequireBundled(t) }
