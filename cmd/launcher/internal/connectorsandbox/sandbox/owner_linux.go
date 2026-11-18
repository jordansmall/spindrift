//go:build linux

package sandbox

import (
	"fmt"
	"os"
	"strings"
)

// namespaceIdentity names this process's pid namespace on this boot. The boot
// id separates hosts and reboots; the pid-namespace link target
// (pid:[<inode>]) separates containers sharing one runtime daemon.
func namespaceIdentity() (string, error) {
	boot, err := os.ReadFile("/proc/sys/kernel/random/boot_id")
	if err != nil {
		return "", err
	}
	ns, err := os.Readlink("/proc/self/ns/pid")
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%s/%s", strings.TrimSpace(string(boot)), ns), nil
}
