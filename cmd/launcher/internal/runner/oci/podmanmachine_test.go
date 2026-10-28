package oci

import (
	"testing"
	"time"
)

// TestParsePodmanMachineMemoryMiB verifies the pure decode helper against
// realistic `podman machine inspect` fixtures, including the shapes that
// must map to found=false: empty output, malformed JSON, an empty array, a
// missing Resources.Memory key, and a zero Memory value.
func TestParsePodmanMachineMemoryMiB(t *testing.T) {
	tests := []struct {
		name    string
		out     string
		wantMiB int
		wantOK  bool
	}{
		{
			name: "well-formed single machine",
			out: `[
				{
					"Name": "podman-machine-default",
					"Running": true,
					"Resources": {
						"CPUs": 5,
						"DiskSize": 100,
						"Memory": 8192,
						"USBs": []
					}
				}
			]`,
			wantMiB: 8192,
			wantOK:  true,
		},
		{
			name:    "empty output",
			out:     "",
			wantMiB: 0,
			wantOK:  false,
		},
		{
			name:    "malformed JSON",
			out:     `{not json`,
			wantMiB: 0,
			wantOK:  false,
		},
		{
			name:    "empty array",
			out:     `[]`,
			wantMiB: 0,
			wantOK:  false,
		},
		{
			name: "Resources present but no Memory key",
			out: `[
				{
					"Name": "podman-machine-default",
					"Resources": {
						"CPUs": 5,
						"DiskSize": 100,
						"USBs": []
					}
				}
			]`,
			wantMiB: 0,
			wantOK:  false,
		},
		{
			name: "Memory is zero",
			out: `[
				{
					"Name": "podman-machine-default",
					"Resources": {
						"CPUs": 5,
						"DiskSize": 100,
						"Memory": 0,
						"USBs": []
					}
				}
			]`,
			wantMiB: 0,
			wantOK:  false,
		},
		{
			name: "multi-machine payload reads the first element",
			out: `[
				{
					"Name": "podman-machine-default",
					"Resources": {
						"CPUs": 5,
						"DiskSize": 100,
						"Memory": 4096,
						"USBs": []
					}
				},
				{
					"Name": "podman-machine-secondary",
					"Resources": {
						"CPUs": 2,
						"DiskSize": 50,
						"Memory": 16384,
						"USBs": []
					}
				}
			]`,
			wantMiB: 4096,
			wantOK:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotMiB, gotOK := parsePodmanMachineMemoryMiB([]byte(tt.out))
			if gotMiB != tt.wantMiB {
				t.Errorf("parsePodmanMachineMemoryMiB() mib = %d, want %d", gotMiB, tt.wantMiB)
			}
			if gotOK != tt.wantOK {
				t.Errorf("parsePodmanMachineMemoryMiB() found = %v, want %v", gotOK, tt.wantOK)
			}
		})
	}
}

// A grandchild inheriting stdout must not hold PodmanMachineMemoryMiB open
// past WaitDelay; the bytes the CLI wrote before exiting 0 still parse.
func TestPodmanMachineMemoryMiBGrandchildHoldsStdout(t *testing.T) {
	fakePodman(t, "echo '[{\"Resources\":{\"Memory\":4096}}]'\n"+holdStdoutBackground+"\nexit 0")

	start := time.Now()
	mib, found := PodmanMachineMemoryMiB()
	if elapsed := time.Since(start); elapsed > cliWaitDelay+time.Second {
		t.Errorf("returned after %v, want within %v", elapsed, cliWaitDelay+time.Second)
	}
	if !found || mib != 4096 {
		t.Errorf("got (%d, %v), want (4096, true)", mib, found)
	}
}

// A wedged podman whose grandchild holds stdout must still return at the
// timeout plus WaitDelay, not when the grandchild exits.
func TestPodmanMachineMemoryMiBTimeoutWithGrandchild(t *testing.T) {
	old := podmanMachineInspectTimeout
	podmanMachineInspectTimeout = 200 * time.Millisecond
	t.Cleanup(func() { podmanMachineInspectTimeout = old })
	fakePodman(t, holdStdoutBackground+"\nexec sleep 10")

	budget := podmanMachineInspectTimeout + cliWaitDelay + time.Second
	start := time.Now()
	_, found := PodmanMachineMemoryMiB()
	if elapsed := time.Since(start); elapsed > budget {
		t.Errorf("returned after %v, want within %v", elapsed, budget)
	}
	if found {
		t.Error("found = true, want false on timeout")
	}
}
