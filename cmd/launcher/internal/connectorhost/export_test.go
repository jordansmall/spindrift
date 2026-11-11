package connectorhost

import "context"

// Call exposes the unexported call to the external tests.
func (c *Connector) Call(ctx context.Context, method string, params, result any) error {
	return c.call(ctx, method, params, result)
}
