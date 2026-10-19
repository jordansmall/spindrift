package freshness

import (
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/testutil"
)

func TestArtifact_TipIdentity(t *testing.T) {
	storePath := "/nix/store/" + testutil.SameHash + "-agent-image"
	cases := []struct {
		name    string
		art     Artifact
		outPath string
		loaded  string
		want    string
		wantErr bool
	}{
		{"oci default repo", OCIImage, storePath, "spindrift:oldhash", "spindrift:" + testutil.SameHash, false},
		{"oci keeps the loaded repo", OCIImage, storePath, "registry:5000/spindrift-opencode:oldhash", "registry:5000/spindrift-opencode:" + testutil.SameHash, false},
		{"oci no-colon loaded falls back to spindrift", OCIImage, storePath, "oldhash", "spindrift:" + testutil.SameHash, false},
		{"oci rejects a non-store path", OCIImage, "/tmp/not-a-store-path", "spindrift:x", "", true},
		{"closure returns the store path verbatim", StoreClosure, storePath, "/nix/store/other-closure", storePath, false},
		{"closure does not validate the path", StoreClosure, "anything", "", "anything", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := c.art.TipIdentity(c.outPath, c.loaded)
			if (err != nil) != c.wantErr {
				t.Fatalf("TipIdentity err = %v, wantErr %v", err, c.wantErr)
			}
			if got != c.want {
				t.Errorf("TipIdentity = %q, want %q", got, c.want)
			}
		})
	}
}

// Probe's freshness verdict is equality between the loaded identity and the
// tip identity of the evaluated build, for every artifact.
func TestArtifact_ProbeComparison(t *testing.T) {
	same := "/nix/store/" + testutil.SameHash + "-agent"
	diff := "/nix/store/" + testutil.DiffHash + "-agent"
	cases := []struct {
		name      string
		art       Artifact
		loaded    string
		outPath   string
		wantFresh bool
	}{
		{"oci fresh", OCIImage, "spindrift:" + testutil.SameHash, same, true},
		{"oci stale", OCIImage, "spindrift:" + testutil.SameHash, diff, false},
		{"closure fresh", StoreClosure, same, same, true},
		{"closure stale", StoreClosure, same, diff, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			res := Probe(ProbeSpec{
				Artifact:       c.art,
				Pwd:            testutil.NewCloneWithOrigin(t, "main"),
				BaseBranch:     "main",
				FlakeImageAttr: ".#packages.x86_64-linux.agent",
				ImageTag:       c.loaded,
			}, &Fake{OutPath: c.outPath})
			if !res.Applicable {
				t.Fatalf("Applicable = false; message: %s", res.Message)
			}
			if res.Fresh != c.wantFresh {
				t.Errorf("Fresh = %v, want %v; message: %s", res.Fresh, c.wantFresh, res.Message)
			}
		})
	}
}

func TestArtifact_NounAndHotSwap(t *testing.T) {
	cases := []struct {
		art     Artifact
		name    string
		noun    string
		hotSwap bool
	}{
		{OCIImage, "oci-image", "image", false},
		{StoreClosure, "store-closure", "closure", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if c.art.Name != c.name {
				t.Errorf("Name = %q, want %q", c.art.Name, c.name)
			}
			if c.art.Noun != c.noun {
				t.Errorf("Noun = %q, want %q", c.art.Noun, c.noun)
			}
			if c.art.HotSwappable != c.hotSwap {
				t.Errorf("HotSwappable = %v, want %v", c.art.HotSwappable, c.hotSwap)
			}
		})
	}
}

func TestArtifact_HostTaintDiagnostic(t *testing.T) {
	const attr = ".#packages.x86_64-linux.agent-image"
	cases := []struct {
		name    string
		art     Artifact
		tip     string
		loaded  string
		want    string
		notWant []string
	}{
		{
			name:   "oci image",
			art:    OCIImage,
			tip:    "spindrift:aaaa",
			loaded: "spindrift:bbbb",
			want: "freshness divergence did not converge after rebuilding to main tip deadbeef: " +
				"the evaluated image tag spindrift:aaaa still does not match the loaded image tag spindrift:bbbb.\n" +
				"This persisted across a rebuild to the base tip, so the divergence is " +
				"structural (host), not stale content — a rebuild cannot fix it.\n" +
				"Likely cause: a consumer flake's packages, extraClosures, or skills " +
				"pulls in a host-system (e.g. darwin) derivation that reaches the " + attr + " " +
				"image graph. On a non-Linux host that derivation can never evaluate " +
				"to the same content-hash tag the baked image carries, so the tags will " +
				"never converge.\n" +
				"To locate the offending derivation, run:\n" +
				"  nix derivation show -r " + attr + " | grep -i darwin",
		},
		{
			// A closure's host is always Linux (lib/mkHarness.nix gates
			// agent-closure on isLinux), so darwin can never be the cause, and its
			// compared values are bare store paths, not repo:tag strings.
			name:    "store closure",
			art:     StoreClosure,
			tip:     "/nix/store/aaa-agent-closure",
			loaded:  "/nix/store/bbb-agent-closure",
			notWant: []string{"darwin", "image tag"},
			want: "freshness divergence did not converge after rebuilding to main tip deadbeef: " +
				"the evaluated closure identity /nix/store/aaa-agent-closure still does not match the loaded closure identity /nix/store/bbb-agent-closure.\n" +
				"This persisted across a rebuild to the base tip, so the divergence is " +
				"structural (host), not stale content — a rebuild cannot fix it.\n" +
				"Likely cause: a consumer flake's packages, extraClosures, or skills " +
				"pulls in a host-realized derivation (e.g. one that differs by host " +
				"architecture, or any other non-deterministic host-realized output) " +
				"that reaches the " + attr + " closure graph. Such a derivation can never " +
				"evaluate to the same store path the baked closure carries, so the " +
				"paths will never converge.\n" +
				"To locate the offending derivation, run:\n" +
				"  nix derivation show -r " + attr,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := c.art.HostTaintDiagnostic("main", "deadbeef", attr, c.tip, c.loaded)
			if got != c.want {
				t.Errorf("HostTaintDiagnostic mismatch\ngot:\n%s\nwant:\n%s", got, c.want)
			}
			for _, nw := range c.notWant {
				if strings.Contains(strings.ToLower(got), nw) {
					t.Errorf("diagnostic contains %q; got:\n%s", nw, got)
				}
			}
		})
	}
}

func TestLookupArtifact(t *testing.T) {
	cases := []struct {
		name    string
		want    string
		wantErr bool
	}{
		{"", "oci-image", false},
		{"oci-image", "oci-image", false},
		{"store-closure", "store-closure", false},
		{"bwrap", "", true},
		{"OCI-IMAGE", "", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := LookupArtifact(c.name)
			if (err != nil) != c.wantErr {
				t.Fatalf("LookupArtifact(%q) err = %v, wantErr %v", c.name, err, c.wantErr)
			}
			if err == nil && got.Name != c.want {
				t.Errorf("LookupArtifact(%q).Name = %q, want %q", c.name, got.Name, c.want)
			}
			if err != nil && !strings.Contains(err.Error(), c.name) {
				t.Errorf("error %q does not name the unknown artifact %q", err, c.name)
			}
		})
	}
}
