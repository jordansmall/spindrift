package connectorforge

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"spindrift.dev/launcher/internal/connectorhost"
	"spindrift.dev/launcher/internal/retry"
)

// ErrNoProbe marks a Connector that does not declare "forge.probe": it cannot
// resolve the repository, which is not a failed probe.
var ErrNoProbe = errors.New("connector does not declare forge.probe")

// Probe asks c for the repository t names, as the Code Forge's Probe does, and
// returns its canonical owner/name. An auth failure matches forge.ErrAuthFailure
// and a rate limit forge.ErrRateLimit; any other error is the Connector's own,
// its class readable through connectorhost.ErrorClass. It returns ErrNoProbe
// when c does not declare "forge.probe".
func Probe(c *connectorhost.Connector, t Target) (string, error) {
	if !slices.Contains(c.Capabilities, capProbe) {
		return "", ErrNoProbe
	}
	return probeRepo(classedCall(c, retry.Clock{}), t)
}

func probeRepo(call guestCall, t Target) (string, error) {
	var res struct {
		Repo string `json:"repo"`
	}
	p := struct {
		Target Target `json:"target"`
	}{t}
	if err := call(context.Background(), "forge.probe", p, &res); err != nil {
		return "", err
	}
	if res.Repo == "" {
		return "", fmt.Errorf("%w: probe named no repository", ErrAnswer)
	}
	return res.Repo, nil
}
