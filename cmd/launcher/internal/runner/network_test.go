package runner

import "testing"

func TestConfig_OCINetwork(t *testing.T) {
	modes := []struct {
		name string
		mode NetworkMode
	}{
		{"open", NetworkModeOpen},
		{"no-host-loopback", NetworkModeNoHostLoopback},
		{"none", NetworkModeNone},
		{"host", NetworkModeHost},
		{"empty", ""},
	}
	// "" is what runner.Lookup routes to OCI when RUNNER_KIND is unset.
	runtimes := []struct {
		runtime       string
		noHostLoopArg string
	}{
		{"podman", "pasta"},
		{"docker", "bridge"},
		{"rancher", "bridge"},
		{"", "bridge"},
	}
	for _, rt := range runtimes {
		for _, m := range modes {
			want := OCINetwork{Mode: m.mode}
			switch m.mode {
			case NetworkModeNoHostLoopback:
				want.Arg = rt.noHostLoopArg
			case NetworkModeNone:
				want.Arg = "none"
			}
			name := rt.runtime + "/" + m.name
			t.Run(name, func(t *testing.T) {
				got := Config{Runtime: rt.runtime, NetworkMode: m.mode}.OCINetwork()
				if got != want {
					t.Errorf("OCINetwork() = %+v, want %+v", got, want)
				}
			})
			// A raw PODMAN_NETWORK wins beside every mode on every runtime.
			t.Run(name+"/raw", func(t *testing.T) {
				got := Config{Runtime: rt.runtime, NetworkMode: m.mode, PodmanNetwork: "slirp4netns"}.OCINetwork()
				if want := (OCINetwork{Mode: m.mode, Arg: "slirp4netns"}); got != want {
					t.Errorf("OCINetwork() = %+v, want %+v", got, want)
				}
			})
		}
	}

	t.Run("ignores BwrapUnshareNet", func(t *testing.T) {
		got := Config{Runtime: "podman", NetworkMode: NetworkModeOpen, BwrapUnshareNet: true}.OCINetwork()
		if want := (OCINetwork{Mode: NetworkModeOpen}); got != want {
			t.Errorf("OCINetwork() = %+v, want %+v", got, want)
		}
	})
}

func TestConfig_BwrapNetwork(t *testing.T) {
	isolated := BwrapNetwork{Isolate: true, Pasta: true}
	offline := BwrapNetwork{Isolate: true}
	tests := []struct {
		name string
		cfg  Config
		want BwrapNetwork
	}{
		{"open", Config{NetworkMode: NetworkModeOpen}, isolated},
		{"no-host-loopback", Config{NetworkMode: NetworkModeNoHostLoopback}, isolated},
		{"none", Config{NetworkMode: NetworkModeNone}, offline},
		{"host", Config{NetworkMode: NetworkModeHost}, BwrapNetwork{}},
		{"empty isolates", Config{}, isolated},

		{"open unshare", Config{NetworkMode: NetworkModeOpen, BwrapUnshareNet: true}, isolated},
		{"no-host-loopback unshare", Config{NetworkMode: NetworkModeNoHostLoopback, BwrapUnshareNet: true}, isolated},
		{"none unshare", Config{NetworkMode: NetworkModeNone, BwrapUnshareNet: true}, offline},
		{"host unshare forces isolation", Config{NetworkMode: NetworkModeHost, BwrapUnshareNet: true}, isolated},
		{"empty unshare", Config{BwrapUnshareNet: true}, isolated},

		{"ignores PodmanNetwork", Config{NetworkMode: NetworkModeOpen, PodmanNetwork: "pasta"}, isolated},
		{"ignores PodmanNetwork host", Config{NetworkMode: NetworkModeHost, PodmanNetwork: "pasta"}, BwrapNetwork{}},

		// RUNNER_KIND picks the adapter, not RUNTIME (issue #2538): a bwrap
		// Box paired with any runtime name must still isolate.
		{"podman runtime open", Config{Runtime: "podman", NetworkMode: NetworkModeOpen}, isolated},
		{"docker runtime open", Config{Runtime: "docker", NetworkMode: NetworkModeOpen}, isolated},
		{"rancher runtime open", Config{Runtime: "rancher", NetworkMode: NetworkModeOpen}, isolated},
		{"empty runtime open", Config{Runtime: "", NetworkMode: NetworkModeOpen}, isolated},
		{"podman runtime open unshare", Config{Runtime: "podman", NetworkMode: NetworkModeOpen, BwrapUnshareNet: true}, isolated},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.cfg.BwrapNetwork(); got != tt.want {
				t.Errorf("BwrapNetwork() = %+v, want %+v", got, tt.want)
			}
		})
	}
}
