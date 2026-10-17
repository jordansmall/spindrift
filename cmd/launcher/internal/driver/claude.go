package driver

import "spindrift.dev/launcher/internal/driver/claude"

func init() { register(claude.Strategy) }
