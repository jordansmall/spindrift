package ecosystemtest

import (
	"bytes"
	"fmt"
	"os"
	"strings"

	"spindrift.dev/launcher/internal/bindregistry"
	"spindrift.dev/launcher/internal/registrymanifest"
)

// forwarderEndpoint stands in for the Forwarder's endpoint: neither stage
// dials it, but a manifest and a ready gate each carry one.
var forwarderEndpoint = registrymanifest.NewUnixEndpoint("/run/ecosystemtest/forwarder.sock")

// lockfileScan is the output WarnStaleLockfilesRows prints over a git copy of
// repo/: a lockfile still naming the Forwarder URL. The copy's root is the
// scan's repoDir and hits are repo-relative, so no temp path reaches the golden.
func (c *replay) lockfileScan() ([]byte, error) {
	work, cleanup, err := c.gitCopy()
	if err != nil {
		return nil, err
	}
	defer cleanup()

	encoded, err := registrymanifest.Encode(registrymanifest.Manifest{Endpoint: forwarderEndpoint})
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	bindregistry.WarnStaleLockfilesRows(&buf, work, c.allRows, func(k string) string {
		if k == registrymanifest.EnvVar {
			return encoded
		}
		return ""
	})
	return buf.Bytes(), nil
}

// bindingsSummary is what BindHomes prints for the case's routes. A HomeConfig
// Strategy's home resolves to a fresh temp dir, since BindHomes writes the config
// there, and the output names it as $<HomeEnvVar> so the golden is stable.
func (c *replay) bindingsSummary() ([]byte, error) {
	routes, err := c.manifestRoutes()
	if err != nil {
		return nil, err
	}
	env, _, err := c.env()
	if err != nil {
		return nil, err
	}
	home := ""
	if c.row.HomeConfig != nil {
		if home, err = os.MkdirTemp("", "ecosystemtest-home-"); err != nil {
			return nil, err
		}
		defer os.RemoveAll(home)
	}
	getenv := func(k string) string {
		if c.row.HomeConfig != nil && k == c.row.HomeConfig.HomeEnvVar {
			return home
		}
		return env[k]
	}

	var buf bytes.Buffer
	gate := bindregistry.ReadyGate(registrymanifest.Manifest{Endpoint: forwarderEndpoint, Routes: routes})
	ok := bindregistry.BindHomes(&buf, c.primaryRows(), gate, nil, getenv)
	out := buf.String()
	if home != "" {
		out = strings.ReplaceAll(out, home, "$"+c.row.HomeConfig.HomeEnvVar)
	}
	if !ok {
		return nil, fmt.Errorf("BindHomes failed:\n%s", out)
	}
	return []byte(out), nil
}
