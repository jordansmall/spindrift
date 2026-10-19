package runner

import "testing"

func TestConfig_OCINetwork(t *testing.T) {
	modes := []struct {
		name string
		mode NetworkMode
		want string
	}{
		{"open", NetworkModeOpen, ""},
		{"no-host-loopback", NetworkModeNoHostLoopback, "dialect-arg"},
		{"none", NetworkModeNone, "none"},
		{"host", NetworkModeHost, ""},
		{"empty", "", ""},
	}
	for _, m := range modes {
		t.Run(m.name, func(t *testing.T) {
			got := Config{NetworkMode: m.mode}.OCINetwork("", "dialect-arg")
			if want := (OCINetwork{Mode: m.mode, Arg: m.want}); got != want {
				t.Errorf("OCINetwork() = %+v, want %+v", got, want)
			}
		})
		// A raw PODMAN_NETWORK wins beside every mode.
		t.Run(m.name+"/raw", func(t *testing.T) {
			got := Config{NetworkMode: m.mode}.OCINetwork("slirp4netns", "dialect-arg")
			if want := (OCINetwork{Mode: m.mode, Arg: "slirp4netns"}); got != want {
				t.Errorf("OCINetwork() = %+v, want %+v", got, want)
			}
		})
	}
}

func TestConfig_BwrapNetwork(t *testing.T) {
	isolated := BwrapNetwork{Isolate: true, Pasta: true}
	offline := BwrapNetwork{Isolate: true}
	tests := []struct {
		name    string
		cfg     Config
		unshare bool
		want    BwrapNetwork
	}{
		{"open", Config{NetworkMode: NetworkModeOpen}, false, isolated},
		{"no-host-loopback", Config{NetworkMode: NetworkModeNoHostLoopback}, false, isolated},
		{"none", Config{NetworkMode: NetworkModeNone}, false, offline},
		{"host", Config{NetworkMode: NetworkModeHost}, false, BwrapNetwork{}},
		{"empty isolates", Config{}, false, isolated},

		{"open unshare", Config{NetworkMode: NetworkModeOpen}, true, isolated},
		{"no-host-loopback unshare", Config{NetworkMode: NetworkModeNoHostLoopback}, true, isolated},
		{"none unshare", Config{NetworkMode: NetworkModeNone}, true, offline},
		{"host unshare forces isolation", Config{NetworkMode: NetworkModeHost}, true, isolated},
		{"empty unshare", Config{}, true, isolated},

		// RUNNER_KIND picks the adapter, not RUNTIME (issue #2538): a bwrap
		// Box paired with any runtime name must still isolate.
		{"podman runtime open", Config{Runtime: "podman", NetworkMode: NetworkModeOpen}, false, isolated},
		{"docker runtime open", Config{Runtime: "docker", NetworkMode: NetworkModeOpen}, false, isolated},
		{"rancher runtime open", Config{Runtime: "rancher", NetworkMode: NetworkModeOpen}, false, isolated},
		{"empty runtime open", Config{Runtime: "", NetworkMode: NetworkModeOpen}, false, isolated},
		{"podman runtime open unshare", Config{Runtime: "podman", NetworkMode: NetworkModeOpen}, true, isolated},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.cfg.BwrapNetwork(tt.unshare); got != tt.want {
				t.Errorf("BwrapNetwork() = %+v, want %+v", got, tt.want)
			}
		})
	}
}
