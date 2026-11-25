//go:build !escapeprobe

package connectorhost

import (
	"context"

	"github.com/tetratelabs/wazero"
)

// instantiateEscape is a no-op: no production build exposes escape primitives.
func instantiateEscape(context.Context, wazero.Runtime) error { return nil }

// withEscapeOut leaves ctx alone: no production build lets a probe write to the
// host's stream.
func withEscapeOut(ctx context.Context, _ *chunkWriter) context.Context { return ctx }
