//go:build !escapeprobe

package connectorhost

import (
	"context"

	"github.com/tetratelabs/wazero"
)

// instantiateEscape is a no-op: no production build exposes escape primitives.
func instantiateEscape(context.Context, wazero.Runtime) error { return nil }
