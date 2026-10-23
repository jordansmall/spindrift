package bwrap

import (
	"net"
	"os"
	"strconv"
	"strings"
	"syscall"
)

const (
	netnsProbeEnv = "SPINDRIFT_BWRAP_NETNS_PROBE"
	// netnsProbeArg is the sole argument of the re-exec, so a stray
	// netnsProbeEnv in an operator's environment cannot make a normal launcher
	// invocation exit 0 from init.
	netnsProbeArg = "--spindrift-bwrap-netns-probe"
	// netnsProbeMarker prefixes the one line the in-namespace child prints:
	// netnsProbeOK when the namespace is usable, else marker plus a reason.
	netnsProbeMarker = "spindrift-netns-probe:"
	netnsProbeOK     = netnsProbeMarker + " ok"
	// zeroHex is /proc/net/route's rendering of 0.0.0.0, the destination and
	// mask of a default route.
	zeroHex = "00000000"
)

// init makes every binary linking this package double as the in-namespace
// half of ValidatePasta's probe, so it needs no host tool like `ip` and no
// hook in main. It answers only a re-exec carrying both netnsProbeEnv and
// netnsProbeArg.
func init() {
	if !netnsProbeRequested(os.Getenv(netnsProbeEnv), os.Args) {
		return
	}
	if reason := netnsProbeReason(); reason != "" {
		os.Stdout.WriteString(netnsProbeMarker + " " + reason + "\n")
	} else {
		os.Stdout.WriteString(netnsProbeOK + "\n")
	}
	os.Exit(0)
}

func netnsProbeRequested(env string, args []string) bool {
	return env != "" && len(args) == 2 && args[1] == netnsProbeArg
}

// netnsIface is the slice of a net.Interface the usability decision needs.
type netnsIface struct {
	Name     string
	Up       bool
	Loopback bool
	HasV4    bool
}

func netnsProbeReason() string {
	nics, err := net.Interfaces()
	if err != nil {
		return "cannot list network interfaces: " + err.Error()
	}
	ifaces := make([]netnsIface, 0, len(nics))
	for _, n := range nics {
		i := netnsIface{Name: n.Name, Up: n.Flags&net.FlagUp != 0, Loopback: n.Flags&net.FlagLoopback != 0}
		addrs, err := n.Addrs()
		if err != nil {
			return "cannot list addresses of " + n.Name + ": " + err.Error()
		}
		for _, a := range addrs {
			if ipn, ok := a.(*net.IPNet); ok && ipn.IP.To4() != nil {
				i.HasV4 = true
			}
		}
		ifaces = append(ifaces, i)
	}
	// /proc/self/net/route reflects the reading task's netns, not the host's.
	routes, err := os.ReadFile("/proc/self/net/route")
	if err != nil {
		return "cannot read the IPv4 route table: " + err.Error()
	}
	return netnsUnusable(ifaces, string(routes))
}

// netnsUnusable returns "" when the namespace has an IPv4 default route via an
// UP non-loopback interface that itself has an IPv4 address, else the short
// reason it does not. routes is /proc/net/route's text. IPv6-only fails on
// purpose: the launcher's --dns-forward is IPv4.
func netnsUnusable(ifaces []netnsIface, routes string) string {
	up := map[string]bool{} // UP non-loopback interface name -> has an IPv4 address
	anyV4 := false
	for _, i := range ifaces {
		if i.Loopback || !i.Up {
			continue
		}
		up[i.Name] = i.HasV4
		anyV4 = anyV4 || i.HasV4
	}
	if len(up) == 0 {
		return "no non-loopback interface is UP"
	}
	if !anyV4 {
		return "no UP non-loopback interface has an IPv4 address"
	}
	for _, line := range strings.Split(routes, "\n") {
		// Iface Destination Gateway Flags RefCnt Use Metric Mask ...
		f := strings.Fields(line)
		if len(f) < 8 || !up[f[0]] || f[1] != zeroHex || f[7] != zeroHex {
			continue
		}
		if flags, err := strconv.ParseUint(f[3], 16, 32); err == nil && flags&syscall.RTF_UP != 0 {
			return ""
		}
	}
	return "no IPv4 default route via an UP interface with an IPv4 address"
}
