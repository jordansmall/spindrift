// Command probe is a fixture Connector that tries to reach the outside world
// from init() and registers a capability for everything it reached, plus a
// "rand:<hex>" draw from crypto/rand. A sandboxed host therefore yields exactly
// ["probe", "rand:<hex>"], with the same hex on every load.
package main

import (
	"crypto/rand"
	"encoding/hex"
	"net"
	"os"
	"time"

	"spindrift.dev/sdk/connector"
)

func init() {
	// "probe" proves init ran, so an empty capability list cannot pass vacuously.
	caps := []string{"probe"}
	if reachedFS() {
		caps = append(caps, "fs")
	}
	if len(os.Environ()) > 0 {
		caps = append(caps, "env")
	}
	if len(os.Args) > 0 {
		caps = append(caps, "args")
	}
	// Stock Go wasip1 net.Dial always fails (WASI preview1 has no sock_open), so
	// "net" cannot appear today; kept as a tripwire for a toolchain that can.
	if c, err := net.Dial("tcp", "127.0.0.1:80"); err == nil {
		c.Close()
		caps = append(caps, "net")
	}
	// wazero's fake clock starts at 2022-01-01 and advances 1ms per reading.
	if time.Now().After(time.Date(2023, 1, 1, 0, 0, 0, 0, time.UTC)) {
		caps = append(caps, "clock")
	}
	var r [8]byte
	if _, err := rand.Read(r[:]); err != nil {
		panic(err)
	}
	caps = append(caps, "rand:"+hex.EncodeToString(r[:]))
	connector.Register(caps...)
}

func reachedFS() bool {
	if _, err := os.ReadFile("/etc/passwd"); err == nil {
		return true
	}
	if _, err := os.ReadDir("/"); err == nil {
		return true
	}
	_, err := os.ReadDir(".")
	return err == nil
}

func main() {}
