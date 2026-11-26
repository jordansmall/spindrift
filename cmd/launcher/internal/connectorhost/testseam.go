package connectorhost

import "net/netip"

// LoopbackForTest is the narrowest PublicForTest: it admits loopback, where a
// test backend listens, and nothing else. A resolver may answer 127.0.0.1 in
// its IPv4-mapped form.
func LoopbackForTest(a netip.Addr) bool { return a.Unmap().IsLoopback() }
