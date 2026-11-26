package connectorhost

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"time"
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
	// AddressCheckCustomTransport: the caller's transport is not an
	// *http.Transport, so the host does not dial for it.
	AddressCheckCustomTransport = "not applied: custom transport"
)

// errNotPublic marks a dial the address check refused.
var errNotPublic = errors.New("not a public address")

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

func (e *notPublicError) Is(target error) bool { return target == errNotPublic }

var (
	// thisNetwork is 0.0.0.0/8, which Go counts as global unicast.
	thisNetwork = netip.MustParsePrefix("0.0.0.0/8")
	// sharedSpace is the carrier-grade NAT range, home of Alibaba Cloud's
	// metadata service at 100.100.100.200.
	sharedSpace = netip.MustParsePrefix("100.64.0.0/10")
	nat64       = netip.MustParsePrefix("64:ff9b::/96")
)

// publicAddress reports whether a is one a literal Route host may be dialled
// at: global unicast, and neither private, shared, nor the "this network" block.
// An IPv4-mapped or NAT64 form is judged by the IPv4 address it carries.
func publicAddress(a netip.Addr) bool {
	a = a.Unmap()
	if a.Is6() && nat64.Contains(a) {
		b := a.As16()
		a = netip.AddrFrom4([4]byte(b[12:]))
	}
	return a.IsGlobalUnicast() && !a.IsPrivate() && !thisNetwork.Contains(a) && !sharedSpace.Contains(a)
}

// checkSeams refuses a Network that sets a test seam in a binary that is not a
// test, so production cannot weaken the address check.
func checkSeams(n Network, testBinary bool) error {
	if !testBinary && (n.PublicForTest != nil || n.LookupForTest != nil) {
		return errors.New("connector network grant sets a test-only address-check seam outside a test binary")
	}
	return nil
}

// checkedDial resolves the host itself and dials the exact address that
// passed, so a DNS answer that changes between check and dial cannot pass.
func (n *Network) checkedDial(ctx context.Context, _, addr string) (net.Conn, error) {
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
	d := net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second}
	var errs []error
	for _, a := range ok {
		c, derr := d.DialContext(ctx, "tcp", net.JoinHostPort(a.String(), port))
		if derr == nil {
			return c, nil
		}
		errs = append(errs, derr)
	}
	return nil, errors.Join(errs...)
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
	ct.DialContext = n.checkedDial
	ct.Dial, ct.DialTLS, ct.DialTLSContext = nil, nil, nil
	c.Transport = ct
	return &c
}

// literalClient picks the client for a request to a literal Route host and the
// address_check value to audit: the checked client, or nil with why the check
// cannot apply (the caller's client then sends it unchanged).
func (n *Network) literalClient(req *http.Request) (*http.Client, string) {
	t := n.callerTransport()
	if t == nil {
		return nil, AddressCheckCustomTransport
	}
	if t.Proxy != nil {
		// An error here fails the caller's send the same way, so check anyway.
		if u, err := t.Proxy(req); err == nil && u != nil {
			return nil, AddressCheckProxy
		}
	}
	if n.checked == nil {
		// A Network that did not come through a Load, as a unit test builds.
		return n.newCheckedClient(), AddressCheckPublic
	}
	return n.checked, AddressCheckPublic
}

// LoopbackForTest is the narrowest PublicForTest: it admits loopback, where a
// test backend listens, and nothing else. A resolver may answer 127.0.0.1 in
// its IPv4-mapped form.
func LoopbackForTest(a netip.Addr) bool { return a.Unmap().IsLoopback() }
