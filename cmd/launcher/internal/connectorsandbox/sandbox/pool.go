package sandbox

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"

	"spindrift.dev/launcher/internal/connectorhost"
	"spindrift.dev/launcher/internal/connectorsandbox"
)

// ErrNetworkInShared is why Pool.LoadNetwork refuses.
var ErrNetworkInShared = errors.New("a network Connector holds a credential, so it never loads into the shared Connector sandbox")

// Pool is a run's Connector sandboxes: the one every Driver Connector shares,
// and one of its own for each Connector that holds a credential.
type Pool struct {
	r      Starter
	label  string
	shared *Sandbox

	mu     sync.Mutex
	closed bool
	owns   []*ownSandbox
	// A closed own sandbox leaves owns for these, so a daemon's Pool, which
	// lives as long as its process, does not grow with every Own. They are what
	// the retired ones cost, and the latest start failure among them.
	retired       connectorsandbox.Stats
	retiredServed bool
	retiredErr    error
}

var _ connectorsandbox.OwningLoader = (*Pool)(nil)

// ownSandbox is a Sandbox that retires itself from its Pool on Close.
type ownSandbox struct {
	*Sandbox
	p    *Pool
	once sync.Once
}

// Close stops the sandbox and, once the stop succeeds, retires it, once
// however often it is closed. A failed stop stays in the pool for Pool.Close
// to retry.
func (o *ownSandbox) Close(ctx context.Context) error {
	err := o.Sandbox.Close(ctx)
	if err == nil {
		o.once.Do(func() { o.p.retire(o) })
	}
	return err
}

// retire moves o's stats and start failure into the pool's retired totals.
func (p *Pool) retire(o *ownSandbox) {
	st, served := o.Stats()
	err := o.Err()
	p.mu.Lock()
	defer p.mu.Unlock()
	p.owns = slices.DeleteFunc(p.owns, func(x *ownSandbox) bool { return x == o })
	if served {
		p.retiredServed = true
		p.retired.Start += st.Start
		p.retired.HopStats = p.retired.HopStats.Add(st.HopStats)
	}
	if err != nil {
		p.retiredErr = err
	}
}

// NewPool returns a Pool that starts its sandboxes through r. label names r in
// a start error.
func NewPool(r Starter, label string) *Pool {
	return &Pool{r: r, label: label, shared: New(r, label)}
}

// Load loads a Driver Connector, which makes no Host call, into the shared
// sandbox.
func (p *Pool) Load(ctx context.Context, wasm []byte) (*connectorhost.Connector, error) {
	return p.shared.Load(ctx, wasm)
}

// LoadNetwork refuses: a network Connector holds a credential, and the shared
// sandbox hosts Connectors that must not see it. It loads into Own's sandbox.
func (p *Pool) LoadNetwork(context.Context, []byte, connectorhost.Network) (*connectorhost.Connector, error) {
	return nil, ErrNetworkInShared
}

// Own is a new sandbox for the Connector name alone. After Close it is already
// closed: Pool.Close is past the sandboxes it knew of, so nothing else would
// stop a container a later load started.
func (p *Pool) Own(name string) connectorsandbox.Sandbox {
	o := &ownSandbox{Sandbox: New(p.r, fmt.Sprintf("%s for Connector %s", p.label, name)), p: p}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		_ = o.Sandbox.Close(context.Background()) // idle: stops nothing
		return o
	}
	p.owns = append(p.owns, o)
	return o
}

// snapshot is the own sandboxes still live and the retired totals, read
// together so a sandbox retiring between reads is neither counted twice nor
// missed.
func (p *Pool) snapshot() (owns []*ownSandbox, retired connectorsandbox.Stats, served bool, err error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return slices.Clone(p.owns), p.retired, p.retiredServed, p.retiredErr
}

// Err joins each live sandbox's latest start failure and the last retired one's.
func (p *Pool) Err() error {
	owns, _, _, retiredErr := p.snapshot()
	errs := []error{p.shared.Err(), retiredErr}
	for _, o := range owns {
		errs = append(errs, o.Err())
	}
	return errors.Join(errs...)
}

// Stats sums every sandbox's, retired ones included; false when none served.
func (p *Pool) Stats() (connectorsandbox.Stats, bool) {
	owns, sum, served, _ := p.snapshot()
	live := []*Sandbox{p.shared}
	for _, o := range owns {
		live = append(live, o.Sandbox)
	}
	for _, s := range live {
		st, ok := s.Stats()
		if !ok {
			continue
		}
		served = true
		sum.Start += st.Start
		sum.HopStats = sum.HopStats.Add(st.HopStats)
	}
	return sum, served
}

// Close stops every sandbox still live. A Runtime has normally closed the own
// ones it took already; those whose stop succeeded are retired, so not stopped
// twice, and those whose stop failed are retried here.
func (p *Pool) Close(ctx context.Context) error {
	p.mu.Lock()
	p.closed = true
	p.mu.Unlock()
	owns, _, _, _ := p.snapshot()
	errs := []error{p.shared.Close(ctx)}
	for _, o := range owns {
		errs = append(errs, o.Close(ctx))
	}
	return errors.Join(errs...)
}
