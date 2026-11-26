package connectorhost

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
)

// The address_check values an AuditEntry carries for a request to a literal
// Route host. A host an Operator knob names carries none: it may resolve
// anywhere.
const (
	// AddressCheckPublic: the request was dialled through the address check,
	// which dials only an address that passed it.
	AddressCheckPublic = "public"
	// AddressCheckRefused: the check ran and no address the host resolves to
	// passed it.
	AddressCheckRefused = "refused: not public"
	// AddressCheckProxy: the request goes through an environment proxy, so the
	// Launcher never sees the address dialled and cannot check it.
	AddressCheckProxy = "not applied: proxy"
	// AddressCheckProxyErr: the environment's proxy lookup failed. Nothing is
	// dialled directly; the caller's client surfaces the same error.
	AddressCheckProxyErr = "not applied: proxy lookup failed"
	// AddressCheckCustomTransport: the caller's transport is not an
	// *http.Transport, so the host does not dial for it. Only a test binary
	// reaches this (checkSeams).
	AddressCheckCustomTransport = "not applied: custom transport"
)

// notPublicError is the refusal the checked dial returns; it names every
// address the host resolved to.
type notPublicError struct {
	host  string
	addrs []netip.Addr
}

func (e *notPublicError) Error() string {
	if len(e.addrs) == 0 {
		return fmt.Sprintf("%s resolves to no address", e.host)
	}
	s := make([]string, len(e.addrs))
	for i, a := range e.addrs {
		s[i] = a.String()
	}
	return fmt.Sprintf("%s resolves to %s, not a public address", e.host, strings.Join(s, ", "))
}

// nonPublic are the ranges Go counts as global unicast but no public host
// answers on: "this network", carrier-grade NAT (home of Alibaba Cloud's
// metadata service at 100.100.100.200), IETF protocol assignments,
// benchmarking, reserved class E, the IPv4-compatible ::a.b.c.d form, NAT64's
// local-use prefix, deprecated site-local, and the 6to4 and Teredo prefixes,
// which embed an IPv4 address the check would not see.
var nonPublic = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),
	netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("192.0.0.0/24"),
	netip.MustParsePrefix("198.18.0.0/15"),
	netip.MustParsePrefix("240.0.0.0/4"),
	netip.MustParsePrefix("::/96"),
	netip.MustParsePrefix("64:ff9b:1::/48"),
	netip.MustParsePrefix("fec0::/10"),
	netip.MustParsePrefix("2002::/16"),
	netip.MustParsePrefix("2001::/32"),
}

var nat64 = netip.MustParsePrefix("64:ff9b::/96")

// publicAddress reports whether a is one a literal Route host may be dialled
// at: global unicast, and neither private nor in nonPublic. An IPv4-mapped or
// NAT64 form is judged by the IPv4 address it carries.
func publicAddress(a netip.Addr) bool {
	a = a.Unmap()
	if a.Is6() && nat64.Contains(a) {
		b := a.As16()
		a = netip.AddrFrom4([4]byte(b[12:]))
	}
	return a.IsGlobalUnicast() && !a.IsPrivate() &&
		!slices.ContainsFunc(nonPublic, func(p netip.Prefix) bool { return p.Contains(a) })
}

// checkSeams refuses a Network that sets a test seam in a binary that is not a
// test, so production cannot weaken the address check. A Client whose transport
// is not an *http.Transport is a seam too: the host cannot dial for it, so it
// would send a literal host unchecked.
func checkSeams(n Network, testBinary bool) error {
	if testBinary {
		return nil
	}
	if n.PublicForTest != nil || n.LookupForTest != nil {
		return errors.New("connector network grant sets a test-only address-check seam outside a test binary")
	}
	if n.callerTransport() == nil {
		return errors.New("connector network grant's client transport is not an *http.Transport, so the address check cannot apply")
	}
	return nil
}

// checkedDial resolves the host itself and dials the exact address that
// passed, so a DNS answer that changes between check and dial cannot pass. The
// dial goes through base, the caller transport's own DialContext, so its
// timeouts and keep-alive survive; a nil base dials as a zero net.Dialer, as
// the transport would.
func (n *Network) checkedDial(base func(context.Context, string, string) (net.Conn, error)) func(context.Context, string, string) (net.Conn, error) {
	if base == nil {
		base = (&net.Dialer{}).DialContext
	}
	return func(ctx context.Context, _, addr string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(addr)
		if err != nil {
			return nil, err
		}
		lookup := n.LookupForTest
		if lookup == nil {
			lookup = func(ctx context.Context, host string) ([]netip.Addr, error) {
				return net.DefaultResolver.LookupNetIP(ctx, "ip", host)
			}
		}
		public := n.PublicForTest
		if public == nil {
			public = publicAddress
		}
		addrs, err := lookup(ctx, host)
		if err != nil {
			return nil, err
		}
		var ok []netip.Addr
		for _, a := range addrs {
			if public(a) {
				ok = append(ok, a)
			}
		}
		if len(ok) == 0 {
			return nil, &notPublicError{host: host, addrs: addrs}
		}
		var errs []error
		for _, a := range ok {
			c, derr := base(ctx, "tcp", net.JoinHostPort(a.String(), port))
			if derr == nil {
				return c, nil
			}
			errs = append(errs, derr)
		}
		return nil, errors.Join(errs...)
	}
}

// callerTransport is the *http.Transport the Network's Client sends through,
// or nil when it sends through some other RoundTripper.
func (n *Network) callerTransport() *http.Transport {
	var rt http.RoundTripper = http.DefaultTransport
	if n.Client != nil && n.Client.Transport != nil {
		rt = n.Client.Transport
	}
	t, _ := rt.(*http.Transport)
	return t
}

// newCheckedClient is the Client with its transport swapped for a clone that
// dials through checkedDial and never proxies; nil when the Client's transport
// is not an *http.Transport. The clone drops DialTLS, which would otherwise
// bypass the dial.
func (n *Network) newCheckedClient() *http.Client {
	t := n.callerTransport()
	if t == nil {
		return nil
	}
	c := http.Client{}
	if n.Client != nil {
		c = *n.Client
	}
	ct := t.Clone()
	ct.Proxy = nil
	ct.DialContext = n.checkedDial(t.DialContext)
	ct.Dial, ct.DialTLS, ct.DialTLSContext = nil, nil, nil
	c.Transport = ct
	return &c
}

// literalClient picks the client for a request and the address_check value to
// audit: the checked client, or nil when the check does not apply and the
// caller's client sends it unchanged. A knob-named host is never checked, so it
// only notes a proxy.
func (n *Network) literalClient(req *http.Request, knobNamed bool) (*http.Client, string) {
	t := n.callerTransport()
	// A Proxy error is never handed to the checked client: it has no proxy, so
	// it would dial directly past the one the Operator set. The caller's client
	// sends it instead and the same error surfaces there.
	if t != nil && t.Proxy != nil {
		u, err := t.Proxy(req)
		if err != nil {
			return nil, AddressCheckProxyErr
		}
		if u != nil {
			return nil, AddressCheckProxy
		}
	}
	if knobNamed {
		return nil, ""
	}
	if t == nil {
		return nil, AddressCheckCustomTransport
	}
	return n.checked(), AddressCheckPublic
}

// useChecked arms the checked client, built once on first use so the
// transport keeps its connections across requests.
func (n *Network) useChecked() {
	if n.checked == nil {
		built := new(atomic.Bool)
		n.checkedBuilt = built
		n.checked = sync.OnceValue(func() *http.Client {
			built.Store(true)
			return n.newCheckedClient()
		})
	}
}

// closeIdle drops the checked transport's idle connections, if a request ever
// built it.
func (n *Network) closeIdle() {
	if n.checked == nil || !n.checkedBuilt.Load() {
		return
	}
	if c := n.checked(); c != nil {
		c.CloseIdleConnections()
	}
}
