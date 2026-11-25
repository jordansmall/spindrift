// Package credsource is a credential read at the moment of use. A backend
// row's secret is a Source the Connector runtime owns, so a rotation reaches
// the next request without a process environment variable carrying it.
package credsource

import (
	"sync"

	"spindrift.dev/launcher/internal/secretcmd"
	"spindrift.dev/launcher/internal/tokenrefresh"
)

// Source yields a secret on demand. "" means unset. Value is safe for
// concurrent use and never blocks on the network.
type Source interface{ Value() string }

// Row is one backend row's credential: Secret is always set, Identity only for
// a row whose scheme carries one (basic).
type Row struct{ Secret, Identity Source }

type static string

func (s static) Value() string { return string(s) }

// Static is a source that always yields v.
func Static(v string) Source { return static(v) }

type file struct {
	path     string
	fallback Source
	mu       sync.Mutex
	last     string
}

// File reads path at each Value, trimmed. A read error or empty contents keeps
// the last good token, so a call in flight never sees a cleared credential;
// before any good read it yields fallback (nil: ""). The minter replaces the
// file by rename, so a read sees a whole token.
func File(path string, fallback Source) Source {
	return &file{path: path, fallback: fallback}
}

func (f *file) Value() string {
	f.mu.Lock()
	f.last, _ = tokenrefresh.ReadIfChanged(f.path, f.last)
	last := f.last
	f.mu.Unlock()
	if last == "" && f.fallback != nil {
		return f.fallback.Value()
	}
	return last
}

// Command is a `_CMD` recipe's last good output.
type Command struct {
	env, cmd string
	run      func(string) (string, error)
	mu       sync.Mutex
	value    string
}

// NewCommand runs cmd through run now. The error is value-free
// (secretcmd.Resolve).
func NewCommand(env, cmd string, run func(string) (string, error)) (*Command, error) {
	c := &Command{env: env, cmd: cmd, run: run}
	if err := c.Refresh(); err != nil {
		return nil, err
	}
	return c, nil
}

// Env is the credential env the command sources.
func (c *Command) Env() string { return c.env }

// Refresh re-runs the command; a failure keeps the previous value.
func (c *Command) Refresh() error {
	v, err := secretcmd.Resolve(c.env, c.cmd, c.run)
	if err != nil {
		return err
	}
	c.mu.Lock()
	c.value = v
	c.mu.Unlock()
	return nil
}

// Value is the last good output.
func (c *Command) Value() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.value
}
