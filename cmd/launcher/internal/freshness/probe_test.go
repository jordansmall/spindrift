package freshness

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/testutil"
)

var errEvalBoom = errors.New("nix eval boom")

// Probe does not special-case a "bwrap" runnerKind into always reporting
// not-applicable. It runs the same fetch-base-tip and eval logic as any
// other runnerKind and reports not-applicable only for the same underlying
// reasons, naming that reason rather than stale bwrap-specific wording.
// Mirrors TestProbe_NotAGitRepo.
func TestProbe_Bwrap_NotApplicable_WhenNotAGitRepo(t *testing.T) {
	pwd := t.TempDir()
	eval := &Fake{OutPath: "/nix/store/" + testutil.SameHash + "-agent-closure"}

	res := Probe(ProbeSpec{
		RunnerKind:     "bwrap",
		Pwd:            pwd,
		BaseBranch:     "main",
		FlakeImageAttr: ".#packages.x86_64-linux.agent-closure",
		ImageTag:       "/nix/store/" + testutil.SameHash + "-agent-closure",
	}, eval)

	if res.Applicable {
		t.Errorf("Applicable = true, want false when pwd is not a git repository")
	}
	if !strings.Contains(res.Message, "not a git repository") {
		t.Errorf("Message %q does not name the not-a-git-repository condition", res.Message)
	}
	if len(eval.Calls) != 0 {
		t.Errorf("Eval called %d times, want 0 when pwd is not a git repository", len(eval.Calls))
	}
}

// For bwrap, imageTag is a bare nix store path rather than a "repo:tag"
// string, so Probe compares the freshly evaluated outPath against it
// directly.
func TestProbe_Bwrap_FreshWhenClosureOutPathMatches(t *testing.T) {
	pwd := testutil.NewCloneWithOrigin(t, "main")
	closurePath := "/nix/store/" + testutil.SameHash + "-agent-closure"
	eval := &Fake{OutPath: closurePath}

	res := Probe(ProbeSpec{
		RunnerKind:     "bwrap",
		Pwd:            pwd,
		BaseBranch:     "main",
		FlakeImageAttr: ".#packages.x86_64-linux.agent-closure",
		ImageTag:       closurePath,
	}, eval)

	if !res.Applicable {
		t.Fatalf("Applicable = false, want true for a bwrap closure that can be fetched and evaluated")
	}
	if !res.Fresh {
		t.Errorf("Fresh = false, want true when the loaded outPath matches the freshly evaluated one; message: %s", res.Message)
	}
	if res.TipTag != "" {
		t.Errorf("TipTag = %q, want empty when fresh (no genuine divergence to name)", res.TipTag)
	}
}

// For bwrap, TipTag carries the raw fresh outPath verbatim, never the OCI
// "<repo>:<hash>" tag format, since a raw nix store path never contains a
// colon.
func TestProbe_Bwrap_RebuildNeededWhenClosureOutPathDiffers(t *testing.T) {
	pwd := testutil.NewCloneWithOrigin(t, "main")
	freshPath := "/nix/store/" + testutil.DiffHash + "-agent-closure"
	loadedPath := "/nix/store/" + testutil.SameHash + "-agent-closure"
	eval := &Fake{OutPath: freshPath}

	res := Probe(ProbeSpec{
		RunnerKind:     "bwrap",
		Pwd:            pwd,
		BaseBranch:     "main",
		FlakeImageAttr: ".#packages.x86_64-linux.agent-closure",
		ImageTag:       loadedPath,
	}, eval)

	if !res.Applicable {
		t.Fatalf("Applicable = false, want true for a bwrap closure that can be fetched and evaluated")
	}
	if res.Fresh {
		t.Errorf("Fresh = true, want false when the outPath differs; message: %s", res.Message)
	}
	if res.TipTag != freshPath {
		t.Errorf("TipTag = %q, want the raw fresh outPath %q verbatim", res.TipTag, freshPath)
	}
	if strings.Contains(res.TipTag, ":") {
		t.Errorf("TipTag = %q contains a colon; the OCI repo:tag formatting path must be skipped for bwrap", res.TipTag)
	}
}

// A bwrap runner loads a bundled nix store path, not an OCI image, so the
// rebuild-needed message must call it a "closure". Calling it "the loaded
// image" misleads an operator reading the message.
func TestProbe_Bwrap_RebuildNeededMessage_SaysClosureNotImage(t *testing.T) {
	pwd := testutil.NewCloneWithOrigin(t, "main")
	freshPath := "/nix/store/" + testutil.DiffHash + "-agent-closure"
	loadedPath := "/nix/store/" + testutil.SameHash + "-agent-closure"
	eval := &Fake{OutPath: freshPath}

	res := Probe(ProbeSpec{
		RunnerKind:     "bwrap",
		Pwd:            pwd,
		BaseBranch:     "main",
		FlakeImageAttr: ".#packages.x86_64-linux.agent-closure",
		ImageTag:       loadedPath,
	}, eval)

	if !strings.Contains(res.Message, "loaded closure") {
		t.Errorf("Message %q does not say \"loaded closure\"", res.Message)
	}
	if strings.Contains(res.Message, "loaded image") {
		t.Errorf("Message %q says \"loaded image\", want \"loaded closure\" for a bwrap runnerKind", res.Message)
	}
}

// The launcher dimension still composes for a bwrap runnerKind: a matching
// closure outPath with a stale launcher hash leaves ImageFresh true, and
// Message names the launcher rather than "image:" as the cause.
func TestProbe_Bwrap_LauncherStale_ImageFresh_RebuildNeeded(t *testing.T) {
	pwd := testutil.NewCloneWithOrigin(t, "main")
	closurePath := "/nix/store/" + testutil.SameHash + "-agent-closure"
	eval := &Fake{
		OutPathForAttr: map[string]string{
			"packages.x86_64-linux.agent-closure": closurePath,
			"packages.x86_64-linux.launcher":      "/nix/store/" + testutil.DiffHash + "-launcher",
		},
	}

	res := Probe(ProbeSpec{
		RunnerKind:         "bwrap",
		Pwd:                pwd,
		BaseBranch:         "main",
		FlakeImageAttr:     ".#packages.x86_64-linux.agent-closure",
		ImageTag:           closurePath,
		FlakeLauncherAttr:  ".#packages.x86_64-linux.launcher",
		LoadedLauncherHash: testutil.SameHash,
	}, eval)

	if res.Fresh {
		t.Errorf("Fresh = true, want false when the launcher hash differs; message: %s", res.Message)
	}
	if res.LauncherFresh {
		t.Errorf("LauncherFresh = true, want false when the launcher hash differs")
	}
	if !res.ImageFresh {
		t.Errorf("ImageFresh = false, want true when the closure outPath matched")
	}
	if !strings.Contains(res.Message, "launcher") {
		t.Errorf("Message %q does not name the launcher as the stale dimension", res.Message)
	}
	if strings.Contains(res.Message, "image:") {
		t.Errorf("Message %q names the image as a cause, but only the launcher is stale", res.Message)
	}
}

// Issue #2538's regression test for a surviving runtime-name comparison at
// probe.go:93. The runnerKind "oci" is not itself any real runtime CLI name
// (unlike "podman" and "docker" used elsewhere here), so Probe proceeding
// past the early return proves the comparison keys off the literal string
// "bwrap", not a coincidental match against a known runtime executable.
func TestProbe_RunnerKindNotApplicable_KeysOffValueNotRuntimeName(t *testing.T) {
	pwd := testutil.NewCloneWithOrigin(t, "main")
	eval := &Fake{OutPath: "/nix/store/" + testutil.SameHash + "-agent-image"}

	res := Probe(ProbeSpec{
		RunnerKind:     "oci",
		Pwd:            pwd,
		BaseBranch:     "main",
		FlakeImageAttr: ".#packages.x86_64-linux.agent-image",
		ImageTag:       "spindrift:" + testutil.SameHash,
	}, eval)

	if !res.Applicable {
		t.Fatalf("Applicable = false, want true for runnerKind %q (not bwrap)", "oci")
	}
	if !res.Fresh {
		t.Errorf("Fresh = false, want true when the image tag matches; message: %s", res.Message)
	}
}

func gitWriteFile(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestProbe_FreshWhenImageHashMatches(t *testing.T) {
	pwd := testutil.NewCloneWithOrigin(t, "main")
	eval := &Fake{OutPath: "/nix/store/" + testutil.SameHash + "-agent-image"}

	res := Probe(ProbeSpec{
		RunnerKind:     "podman",
		Pwd:            pwd,
		BaseBranch:     "main",
		FlakeImageAttr: ".#packages.x86_64-linux.agent-image",
		ImageTag:       "spindrift:" + testutil.SameHash,
	}, eval)

	if !res.Applicable {
		t.Fatalf("Applicable = false, want true for a non-bwrap runnerKind (podman)")
	}
	if !res.Fresh {
		t.Errorf("Fresh = false, want true when the image tag matches; message: %s", res.Message)
	}
	if len(eval.Calls) != 1 {
		t.Fatalf("Eval called %d times, want 1", len(eval.Calls))
	}
	if eval.Calls[0].Pwd != pwd {
		t.Errorf("Eval called with pwd %q, want %q", eval.Calls[0].Pwd, pwd)
	}
}

// Probe passes Eval the fetched base-tip sha, not the clone's own
// checked-out HEAD, so the eval is hermetic against the tip rather than
// against whatever pwd happens to have checked out.
func TestProbe_EvalReceivesFetchedRev(t *testing.T) {
	pwd := testutil.NewCloneWithOrigin(t, "main")
	localHead := gitOutput(t, pwd, "rev-parse", "HEAD")
	advancedSha, err := gitAdvanceOrigin(t, pwd, "main")
	if err != nil {
		t.Fatalf("gitAdvanceOrigin: %v", err)
	}
	eval := &Fake{OutPath: "/nix/store/" + testutil.SameHash + "-agent-image"}

	Probe(ProbeSpec{
		RunnerKind:     "podman",
		Pwd:            pwd,
		BaseBranch:     "main",
		FlakeImageAttr: ".#packages.x86_64-linux.agent-image",
		ImageTag:       "spindrift:" + testutil.SameHash,
	}, eval)

	if len(eval.Calls) != 1 {
		t.Fatalf("Eval called %d times, want 1", len(eval.Calls))
	}
	if eval.Calls[0].Rev != advancedSha {
		t.Errorf("Eval called with rev %q, want the fetched base tip %q", eval.Calls[0].Rev, advancedSha)
	}
	if eval.Calls[0].Rev == localHead {
		t.Errorf("Eval called with rev %q, the clone's own stale checked-out HEAD, not the fetched tip", eval.Calls[0].Rev)
	}
}

func TestProbe_RebuildNeededWhenImageHashDiffers(t *testing.T) {
	pwd := testutil.NewCloneWithOrigin(t, "main")
	eval := &Fake{OutPath: "/nix/store/" + testutil.DiffHash + "-agent-image"}

	res := Probe(ProbeSpec{
		RunnerKind:     "podman",
		Pwd:            pwd,
		BaseBranch:     "main",
		FlakeImageAttr: ".#packages.x86_64-linux.agent-image",
		ImageTag:       "spindrift:" + testutil.SameHash,
	}, eval)

	if !res.Applicable {
		t.Fatalf("Applicable = false, want true for a non-bwrap runnerKind (podman)")
	}
	if res.Fresh {
		t.Errorf("Fresh = true, want false when the image tag differs; message: %s", res.Message)
	}
}

// TipTag carries the tag a rebuild would load, so the non-convergence
// diagnostic (issue #2113) can name it alongside the loaded tag.
func TestProbe_RebuildNeededSetsTipTag(t *testing.T) {
	pwd := testutil.NewCloneWithOrigin(t, "main")
	eval := &Fake{OutPath: "/nix/store/" + testutil.DiffHash + "-agent-image"}

	res := Probe(ProbeSpec{
		RunnerKind:     "podman",
		Pwd:            pwd,
		BaseBranch:     "main",
		FlakeImageAttr: ".#packages.x86_64-linux.agent-image",
		ImageTag:       "spindrift:" + testutil.SameHash,
	}, eval)

	want := "spindrift:" + testutil.DiffHash
	if res.TipTag != want {
		t.Errorf("TipTag = %q, want %q", res.TipTag, want)
	}
}

// Reproduces the #587 livelock: a loaded image whose content-hash tag
// matches the base tip is fresh even when the full store path text differs,
// for example a differing derivation name suffix. The tag is what
// `build` and EnsureReady gate on, not the raw drvPath a stale baked
// IMAGE_DRV could desync from with no way to re-sync.
func TestProbe_LivelockRegression_FreshWhenTagMatchesDespiteOutPathNameDrift(t *testing.T) {
	pwd := testutil.NewCloneWithOrigin(t, "main")
	hash := "abcdefghijklmnopqrstuvwxyz012345"
	eval := &Fake{OutPath: "/nix/store/" + hash + "-agent-image-generation-7"}
	loadedTag := "spindrift:" + hash

	res := Probe(ProbeSpec{
		RunnerKind:     "podman",
		Pwd:            pwd,
		BaseBranch:     "main",
		FlakeImageAttr: ".#packages.x86_64-linux.agent-image",
		ImageTag:       loadedTag,
	}, eval)

	if !res.Applicable {
		t.Fatalf("Applicable = false, want true for a non-bwrap runnerKind (podman)")
	}
	if !res.Fresh {
		t.Errorf("Fresh = false, want true when the tip's image tag matches the loaded tag; message: %s", res.Message)
	}
}

// An image tagged under a driver-scoped repo such as "spindrift-opencode"
// makes Probe derive its tip tag under that same repo. An opencode image
// must never compare against a hardcoded "spindrift:" tip tag (#262).
func TestProbe_DriverScopedRepo_FreshWhenImageHashMatches(t *testing.T) {
	pwd := testutil.NewCloneWithOrigin(t, "main")
	eval := &Fake{OutPath: "/nix/store/" + testutil.SameHash + "-agent-image"}

	res := Probe(ProbeSpec{
		RunnerKind:     "podman",
		Pwd:            pwd,
		BaseBranch:     "main",
		FlakeImageAttr: ".#packages.x86_64-linux.agent-image",
		ImageTag:       "spindrift-opencode:" + testutil.SameHash,
	}, eval)

	if !res.Applicable {
		t.Fatalf("Applicable = false, want true for a non-bwrap runnerKind (podman)")
	}
	if !res.Fresh {
		t.Errorf("Fresh = false, want true when the driver-scoped image tag matches; message: %s", res.Message)
	}
}

// The message must name the repo-matching tip tag rather than a hardcoded
// "spindrift:" tag, so the diagnostic is accurate for the driver in play.
func TestProbe_DriverScopedRepo_RebuildNeededWhenImageHashDiffers(t *testing.T) {
	pwd := testutil.NewCloneWithOrigin(t, "main")
	eval := &Fake{OutPath: "/nix/store/" + testutil.DiffHash + "-agent-image"}

	res := Probe(ProbeSpec{
		RunnerKind:     "podman",
		Pwd:            pwd,
		BaseBranch:     "main",
		FlakeImageAttr: ".#packages.x86_64-linux.agent-image",
		ImageTag:       "spindrift-opencode:" + testutil.SameHash,
	}, eval)

	if !res.Applicable {
		t.Fatalf("Applicable = false, want true for a non-bwrap runnerKind (podman)")
	}
	if res.Fresh {
		t.Errorf("Fresh = true, want false when the driver-scoped image tag's hash differs; message: %s", res.Message)
	}
	if !strings.Contains(res.Message, "spindrift-opencode:"+testutil.DiffHash) {
		t.Errorf("Message %q does not name the repo-matching tip tag spindrift-opencode:%s", res.Message, testutil.DiffHash)
	}
}

func TestProbe_LauncherStale_ImageFresh_RebuildNeeded(t *testing.T) {
	pwd := testutil.NewCloneWithOrigin(t, "main")
	eval := &Fake{
		OutPathForAttr: map[string]string{
			"packages.x86_64-linux.agent-image": "/nix/store/" + testutil.SameHash + "-agent-image",
			"packages.x86_64-linux.launcher":    "/nix/store/" + testutil.DiffHash + "-launcher",
		},
	}

	res := Probe(ProbeSpec{
		RunnerKind:         "podman",
		Pwd:                pwd,
		BaseBranch:         "main",
		FlakeImageAttr:     ".#packages.x86_64-linux.agent-image",
		ImageTag:           "spindrift:" + testutil.SameHash,
		FlakeLauncherAttr:  ".#packages.x86_64-linux.launcher",
		LoadedLauncherHash: testutil.SameHash,
	}, eval)

	if res.Fresh {
		t.Errorf("Fresh = true, want false when the launcher hash differs; message: %s", res.Message)
	}
	if res.LauncherFresh {
		t.Errorf("LauncherFresh = true, want false when the launcher hash differs")
	}
	if !strings.Contains(res.Message, "launcher") {
		t.Errorf("Message %q does not name the launcher as the stale dimension", res.Message)
	}
	if strings.Contains(res.Message, "image:") {
		t.Errorf("Message %q names the image as a cause, but only the launcher is stale", res.Message)
	}
	if res.TipTag != "" {
		t.Errorf("TipTag = %q, want empty when the image itself is fresh and only the launcher is stale (no genuine image-tag divergence to name)", res.TipTag)
	}
	if !res.ImageFresh {
		t.Errorf("ImageFresh = false, want true when the image dimension matched, even though overall Fresh is false due to the launcher")
	}
}

func TestProbe_ImageStale_LauncherFresh_RebuildNeeded(t *testing.T) {
	pwd := testutil.NewCloneWithOrigin(t, "main")
	eval := &Fake{
		OutPathForAttr: map[string]string{
			"packages.x86_64-linux.agent-image": "/nix/store/" + testutil.DiffHash + "-agent-image",
			"packages.x86_64-linux.launcher":    "/nix/store/" + testutil.SameHash + "-launcher",
		},
	}

	res := Probe(ProbeSpec{
		RunnerKind:         "podman",
		Pwd:                pwd,
		BaseBranch:         "main",
		FlakeImageAttr:     ".#packages.x86_64-linux.agent-image",
		ImageTag:           "spindrift:" + testutil.SameHash,
		FlakeLauncherAttr:  ".#packages.x86_64-linux.launcher",
		LoadedLauncherHash: testutil.SameHash,
	}, eval)

	if res.Fresh {
		t.Errorf("Fresh = true, want false when the image hash differs; message: %s", res.Message)
	}
	if !res.LauncherFresh {
		t.Errorf("LauncherFresh = false, want true when the launcher hash matches")
	}
	if !strings.Contains(res.Message, "image:") {
		t.Errorf("Message %q does not name the image as the stale dimension", res.Message)
	}
	if strings.Contains(res.Message, "launcher:") {
		t.Errorf("Message %q names the launcher as a cause, but only the image is stale", res.Message)
	}
}

func TestProbe_ImageAndLauncherBothStale_RebuildNeeded(t *testing.T) {
	pwd := testutil.NewCloneWithOrigin(t, "main")
	eval := &Fake{
		OutPathForAttr: map[string]string{
			"packages.x86_64-linux.agent-image": "/nix/store/" + testutil.DiffHash + "-agent-image",
			"packages.x86_64-linux.launcher":    "/nix/store/" + testutil.DiffHash + "-launcher",
		},
	}

	res := Probe(ProbeSpec{
		RunnerKind:         "podman",
		Pwd:                pwd,
		BaseBranch:         "main",
		FlakeImageAttr:     ".#packages.x86_64-linux.agent-image",
		ImageTag:           "spindrift:" + testutil.SameHash,
		FlakeLauncherAttr:  ".#packages.x86_64-linux.launcher",
		LoadedLauncherHash: testutil.SameHash,
	}, eval)

	if res.Fresh {
		t.Errorf("Fresh = true, want false when both the image and launcher hashes differ; message: %s", res.Message)
	}
	if res.LauncherFresh {
		t.Errorf("LauncherFresh = true, want false when the launcher hash differs")
	}
	if !strings.Contains(res.Message, "image:") {
		t.Errorf("Message %q does not name the image as a stale dimension", res.Message)
	}
	if !strings.Contains(res.Message, "launcher:") {
		t.Errorf("Message %q does not name the launcher as a stale dimension", res.Message)
	}
}

func TestProbe_ImageAndLauncherBothFresh(t *testing.T) {
	pwd := testutil.NewCloneWithOrigin(t, "main")
	eval := &Fake{
		OutPathForAttr: map[string]string{
			"packages.x86_64-linux.agent-image": "/nix/store/" + testutil.SameHash + "-agent-image",
			"packages.x86_64-linux.launcher":    "/nix/store/" + testutil.SameHash + "-launcher",
		},
	}

	res := Probe(ProbeSpec{
		RunnerKind:         "podman",
		Pwd:                pwd,
		BaseBranch:         "main",
		FlakeImageAttr:     ".#packages.x86_64-linux.agent-image",
		ImageTag:           "spindrift:" + testutil.SameHash,
		FlakeLauncherAttr:  ".#packages.x86_64-linux.launcher",
		LoadedLauncherHash: testutil.SameHash,
	}, eval)

	if !res.Fresh {
		t.Errorf("Fresh = false, want true when both the image and launcher hashes match; message: %s", res.Message)
	}
	if !res.LauncherFresh {
		t.Errorf("LauncherFresh = false, want true when the launcher hash matches")
	}
	if !strings.Contains(res.Message, "fresh") {
		t.Errorf("Message %q does not confirm fresh", res.Message)
	}
	if res.TipLauncherHash != testutil.SameHash {
		t.Errorf("TipLauncherHash = %q, want %q", res.TipLauncherHash, testutil.SameHash)
	}
}

// An image-only caller leaves flakeLauncherAttr empty. An unconfigured
// launcher dimension must never veto an otherwise-fresh image verdict, and
// Probe must not call Eval a second time for an attr never supplied.
func TestProbe_LauncherNotConfigured_ImageFresh_Fresh(t *testing.T) {
	pwd := testutil.NewCloneWithOrigin(t, "main")
	eval := &Fake{OutPath: "/nix/store/" + testutil.SameHash + "-agent-image"}

	res := Probe(ProbeSpec{
		RunnerKind:     "podman",
		Pwd:            pwd,
		BaseBranch:     "main",
		FlakeImageAttr: ".#packages.x86_64-linux.agent-image",
		ImageTag:       "spindrift:" + testutil.SameHash,
	}, eval)

	if !res.Fresh {
		t.Errorf("Fresh = false, want true when the image tag matches and the launcher dimension isn't configured; message: %s", res.Message)
	}
	if !res.LauncherFresh {
		t.Errorf("LauncherFresh = false, want true when the launcher dimension isn't configured (\"not configured\" is not \"stale\")")
	}
	if res.TipLauncherHash != "" {
		t.Errorf("TipLauncherHash = %q, want empty when the launcher dimension isn't configured", res.TipLauncherHash)
	}
	if len(eval.Calls) != 1 {
		t.Errorf("Eval called %d times, want 1 (image only) when flakeLauncherAttr is empty", len(eval.Calls))
	}
}

// A launcher eval failure leaves TipTag empty while setting Rev. A stuck
// failure repeating at the same rev must stay Rebuild under Guard.Classify,
// not look like a genuine image-tag divergence (HostTainted).
func TestProbe_LauncherEvalFailure_TipTagEmptyImageFresh(t *testing.T) {
	pwd := testutil.NewCloneWithOrigin(t, "main")
	eval := &Fake{
		OutPathForAttr: map[string]string{
			"packages.x86_64-linux.agent-image": "/nix/store/" + testutil.SameHash + "-agent-image",
		},
		ErrForAttr: map[string]error{
			"packages.x86_64-linux.launcher": errEvalBoom,
		},
	}

	res := Probe(ProbeSpec{
		RunnerKind:         "podman",
		Pwd:                pwd,
		BaseBranch:         "main",
		FlakeImageAttr:     ".#packages.x86_64-linux.agent-image",
		ImageTag:           "spindrift:" + testutil.SameHash,
		FlakeLauncherAttr:  ".#packages.x86_64-linux.launcher",
		LoadedLauncherHash: testutil.SameHash,
	}, eval)

	if res.Fresh {
		t.Errorf("Fresh = true, want false when the launcher eval fails")
	}
	if res.TipTag != "" {
		t.Errorf("TipTag = %q, want empty on a launcher eval failure (a stuck failure, not a genuine image-tag divergence)", res.TipTag)
	}
	if !res.ImageFresh {
		t.Errorf("ImageFresh = false, want true when the image dimension itself succeeded and matched")
	}
	if res.Rev == "" {
		t.Errorf("Rev = %q, want the fetched base tip set even on a launcher eval failure", res.Rev)
	}
	if !strings.Contains(res.Message, errEvalBoom.Error()) {
		t.Errorf("Message %q does not name the launcher eval failure", res.Message)
	}
}

// A launcher outPath that is not a valid nix store path makes storeHash
// error. That is the same stuck-failure shape as the eval-failure case, so
// TipTag stays empty and a repeat at the same rev stays Rebuild under
// Guard.Classify.
func TestProbe_LauncherHashDeriveFailure_TipTagEmptyImageFresh(t *testing.T) {
	pwd := testutil.NewCloneWithOrigin(t, "main")
	eval := &Fake{
		OutPathForAttr: map[string]string{
			"packages.x86_64-linux.agent-image": "/nix/store/" + testutil.SameHash + "-agent-image",
			"packages.x86_64-linux.launcher":    "not-a-store-path",
		},
	}

	res := Probe(ProbeSpec{
		RunnerKind:         "podman",
		Pwd:                pwd,
		BaseBranch:         "main",
		FlakeImageAttr:     ".#packages.x86_64-linux.agent-image",
		ImageTag:           "spindrift:" + testutil.SameHash,
		FlakeLauncherAttr:  ".#packages.x86_64-linux.launcher",
		LoadedLauncherHash: testutil.SameHash,
	}, eval)

	if res.Fresh {
		t.Errorf("Fresh = true, want false when the launcher hash cannot be derived")
	}
	if res.TipTag != "" {
		t.Errorf("TipTag = %q, want empty on a launcher hash-derive failure (a stuck failure, not a genuine image-tag divergence)", res.TipTag)
	}
	if !res.ImageFresh {
		t.Errorf("ImageFresh = false, want true when the image dimension itself succeeded and matched")
	}
	if res.Rev == "" {
		t.Errorf("Rev = %q, want the fetched base tip set even on a launcher hash-derive failure", res.Rev)
	}
	if !strings.Contains(res.Message, "not a nix store path") {
		t.Errorf("Message %q does not name the launcher hash-derive failure", res.Message)
	}
}

// imageRepo splits on the last colon because a repo can itself embed one,
// for example a "host:port" registry prefix. A tag with no colon falls back
// to the default "spindrift" repo rather than an empty one.
func TestImageRepo_DerivesRepoFromLastColon(t *testing.T) {
	cases := []struct {
		name     string
		imageTag string
		want     string
	}{
		{"default claude repo", "spindrift:" + testutil.SameHash, "spindrift"},
		{"driver-scoped repo", "spindrift-opencode:" + testutil.SameHash, "spindrift-opencode"},
		{"no colon falls back to default", "spindrift", "spindrift"},
		{"empty tag falls back to default", "", "spindrift"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := imageRepo(c.imageTag); got != c.want {
				t.Errorf("imageRepo(%q) = %q, want %q", c.imageTag, got, c.want)
			}
		})
	}
}

// The Console's in-session rebuild (issue #652) needs the rev itself, not
// just the tag comparison, to recognize that it already rebuilt this exact
// tip without re-parsing Message.
func TestProbe_Rev_MatchesFetchedTip(t *testing.T) {
	pwd := testutil.NewCloneWithOrigin(t, "main")
	advancedSha, err := gitAdvanceOrigin(t, pwd, "main")
	if err != nil {
		t.Fatalf("gitAdvanceOrigin: %v", err)
	}
	eval := &Fake{OutPath: "/nix/store/" + testutil.DiffHash + "-agent-image"}

	res := Probe(ProbeSpec{
		RunnerKind:     "podman",
		Pwd:            pwd,
		BaseBranch:     "main",
		FlakeImageAttr: ".#packages.x86_64-linux.agent-image",
		ImageTag:       "spindrift:" + testutil.SameHash,
	}, eval)

	if res.Rev != advancedSha {
		t.Errorf("Rev = %q, want the fetched base tip %q", res.Rev, advancedSha)
	}
}

func TestProbe_EvalFailureFailsClosed(t *testing.T) {
	pwd := testutil.NewCloneWithOrigin(t, "main")
	eval := &Fake{Err: errEvalBoom}

	res := Probe(ProbeSpec{
		RunnerKind:     "podman",
		Pwd:            pwd,
		BaseBranch:     "main",
		FlakeImageAttr: ".#packages.x86_64-linux.agent-image",
		ImageTag:       "spindrift:" + testutil.SameHash,
	}, eval)

	if !res.Applicable {
		t.Fatalf("Applicable = false, want true for a non-bwrap runnerKind (podman)")
	}
	if res.Fresh {
		t.Errorf("Fresh = true, want false (fail closed) on eval error")
	}
	if !strings.Contains(res.Message, errEvalBoom.Error()) {
		t.Errorf("Message %q does not surface the eval error", res.Message)
	}
}

// A fetch error against a configured origin, such as a transient network
// failure, fails closed without calling the evaluator. That is distinct from
// the definitive not-applicable cases (TestProbe_NotAGitRepo,
// TestProbe_MissingRemoteRefNotApplicable, and
// TestProbe_NoOriginRemoteNotApplicable) where proceeding is safe.
func TestProbe_FetchFailureFailsClosed(t *testing.T) {
	pwd := t.TempDir()
	testutil.GitRun(t, pwd, "init")
	testutil.GitRun(t, pwd, "remote", "add", "origin", "https://example.invalid/nope.git")
	eval := &Fake{OutPath: "/nix/store/" + testutil.SameHash + "-agent-image"}

	res := Probe(ProbeSpec{
		RunnerKind:     "podman",
		Pwd:            pwd,
		BaseBranch:     "main",
		FlakeImageAttr: ".#packages.x86_64-linux.agent-image",
		ImageTag:       "spindrift:" + testutil.SameHash,
	}, eval)

	if !res.Applicable {
		t.Fatalf("Applicable = false, want true for a non-bwrap runnerKind (podman)")
	}
	if res.Fresh {
		t.Errorf("Fresh = true, want false (fail closed) on fetch error")
	}
	if len(eval.Calls) != 0 {
		t.Errorf("Eval called %d times, want 0 when fetch fails", len(eval.Calls))
	}
}

// A pwd outside any git repository is not-applicable, distinct from a
// transient fetch failure inside a real repo
// (TestProbe_FetchFailureFailsClosed), so the console does not hold launches
// or offer a [b] rebuild that would fail the same way.
func TestProbe_NotAGitRepo(t *testing.T) {
	pwd := t.TempDir()
	eval := &Fake{OutPath: "/nix/store/" + testutil.SameHash + "-agent-image"}

	res := Probe(ProbeSpec{
		RunnerKind:     "podman",
		Pwd:            pwd,
		BaseBranch:     "main",
		FlakeImageAttr: ".#packages.x86_64-linux.agent-image",
		ImageTag:       "spindrift:" + testutil.SameHash,
	}, eval)

	if res.Applicable {
		t.Errorf("Applicable = true, want false when pwd is not a git repository")
	}
	if !strings.Contains(res.Message, "not a git repository") {
		t.Errorf("Message %q does not name the not-a-git-repository condition", res.Message)
	}
	if res.Rev != "" {
		t.Errorf("Rev = %q, want empty when Applicable is false", res.Rev)
	}
	if len(eval.Calls) != 0 {
		t.Errorf("Eval called %d times, want 0 when pwd is not a git repository", len(eval.Calls))
	}
}

// A base branch missing from origin (git's own "couldn't find remote ref")
// is not-applicable rather than fail-closed: freshness cannot be checked
// here, and continuous dispatch must not treat it as rebuild-needed (#1753).
func TestProbe_MissingRemoteRefNotApplicable(t *testing.T) {
	pwd := testutil.NewCloneWithOrigin(t, "main")
	eval := &Fake{OutPath: "/nix/store/" + testutil.SameHash + "-agent-image"}

	res := Probe(ProbeSpec{
		RunnerKind:     "podman",
		Pwd:            pwd,
		BaseBranch:     "release",
		FlakeImageAttr: ".#packages.x86_64-linux.agent-image",
		ImageTag:       "spindrift:" + testutil.SameHash,
	}, eval)

	if res.Applicable {
		t.Errorf("Applicable = true, want false when the base branch isn't on origin")
	}
	if !strings.Contains(res.Message, "release") {
		t.Errorf("Message %q does not name the missing base branch", res.Message)
	}
	if res.Rev != "" {
		t.Errorf("Rev = %q, want empty when Applicable is false", res.Rev)
	}
	if len(eval.Calls) != 0 {
		t.Errorf("Eval called %d times, want 0 when the base branch is missing", len(eval.Calls))
	}
}

// A fully local repo with no origin remote (CODE_FORGE=local, say) has
// nothing to fetch, so freshness cannot be checked here and continuous
// dispatch must not treat it as rebuild-needed (#2034).
func TestProbe_NoOriginRemoteNotApplicable(t *testing.T) {
	pwd := t.TempDir()
	testutil.GitRun(t, pwd, "init")
	eval := &Fake{OutPath: "/nix/store/" + testutil.SameHash + "-agent-image"}

	res := Probe(ProbeSpec{
		RunnerKind:     "podman",
		Pwd:            pwd,
		BaseBranch:     "main",
		FlakeImageAttr: ".#packages.x86_64-linux.agent-image",
		ImageTag:       "spindrift:" + testutil.SameHash,
	}, eval)

	if res.Applicable {
		t.Errorf("Applicable = true, want false when the repo has no origin remote")
	}
	if !strings.Contains(res.Message, "origin") {
		t.Errorf("Message %q does not name the missing origin remote", res.Message)
	}
	if res.Rev != "" {
		t.Errorf("Rev = %q, want empty when Applicable is false", res.Rev)
	}
	if len(eval.Calls) != 0 {
		t.Errorf("Eval called %d times, want 0 when there is no origin remote", len(eval.Calls))
	}
}

// A flake that does not define flakeImageAttr (nix's own "does not provide
// attribute") means pwd is not the spindrift image-source flake, so
// freshness cannot be checked here and continuous dispatch must not treat
// it as rebuild-needed (#1754).
func TestProbe_ImageAttrMissingNotApplicable(t *testing.T) {
	pwd := testutil.NewCloneWithOrigin(t, "main")
	attrErr := errors.New(`nix eval git+file:///tmp/target#packages.x86_64-linux.agent-image.outPath: exit status 1: error: flake 'git+file:///tmp/target' does not provide attribute 'packages.x86_64-linux.agent-image', 'legacyPackages.x86_64-linux.agent-image' or 'packages.x86_64-linux.default'`)
	eval := &Fake{Err: attrErr}

	res := Probe(ProbeSpec{
		RunnerKind:     "podman",
		Pwd:            pwd,
		BaseBranch:     "main",
		FlakeImageAttr: ".#packages.x86_64-linux.agent-image",
		ImageTag:       "spindrift:" + testutil.SameHash,
	}, eval)

	if res.Applicable {
		t.Errorf("Applicable = true, want false when the flake does not provide the image attr")
	}
	if !strings.Contains(res.Message, "packages.x86_64-linux.agent-image") {
		t.Errorf("Message %q does not name the missing image attr", res.Message)
	}
	if res.Rev != "" {
		t.Errorf("Rev = %q, want empty when Applicable is false", res.Rev)
	}
}

// The fetch-failure message carries git's own stderr, not just the bare
// exit status, so an operator reading `preview` output can see why.
func TestProbe_FetchFailure_MessageIncludesGitStderr(t *testing.T) {
	pwd := t.TempDir()
	testutil.GitRun(t, pwd, "init")
	testutil.GitRun(t, pwd, "remote", "add", "origin", "https://example.invalid/nope.git")
	eval := &Fake{OutPath: "/nix/store/" + testutil.SameHash + "-agent-image"}

	res := Probe(ProbeSpec{
		RunnerKind:     "podman",
		Pwd:            pwd,
		BaseBranch:     "main",
		FlakeImageAttr: ".#packages.x86_64-linux.agent-image",
		ImageTag:       "spindrift:" + testutil.SameHash,
	}, eval)

	if !strings.Contains(res.Message, "example.invalid") {
		t.Errorf("Message %q does not surface git's stderr detail", res.Message)
	}
}

// Probe fetches the base tip without checking it out.
func TestProbe_NeverMutatesWorkingCopy(t *testing.T) {
	pwd := testutil.NewCloneWithOrigin(t, "main")
	before := gitOutput(t, pwd, "rev-parse", "HEAD")
	eval := &Fake{OutPath: "/nix/store/" + testutil.SameHash + "-agent-image"}

	if _, err := gitAdvanceOrigin(t, pwd, "main"); err != nil {
		t.Fatalf("gitAdvanceOrigin: %v", err)
	}

	res := Probe(ProbeSpec{
		RunnerKind:     "podman",
		Pwd:            pwd,
		BaseBranch:     "main",
		FlakeImageAttr: ".#packages.x86_64-linux.agent-image",
		ImageTag:       "spindrift:" + testutil.SameHash,
	}, eval)
	if !res.Applicable {
		t.Fatalf("Applicable = false, want true")
	}

	after := gitOutput(t, pwd, "rev-parse", "HEAD")
	if before != after {
		t.Errorf("checked-out HEAD changed: %q -> %q; Probe must never check out", before, after)
	}
	status := gitOutput(t, pwd, "status", "--porcelain")
	if status != "" {
		t.Errorf("working copy dirtied by Probe: %q", status)
	}
}

func gitOutput(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).Output()
	if err != nil {
		t.Fatalf("git %v: %v", args, err)
	}
	return strings.TrimSpace(string(out))
}

// gitAdvanceOrigin pushes a commit on baseBranch from a second clone of the
// same origin, simulating a merge landing after pwd's own clone was made,
// without touching pwd itself.
func gitAdvanceOrigin(t *testing.T, pwd, baseBranch string) (string, error) {
	t.Helper()
	origin := gitOutput(t, pwd, "remote", "get-url", "origin")
	second := t.TempDir()
	testutil.GitRun(t, "", "clone", origin, second)
	testutil.GitRun(t, second, "checkout", baseBranch)
	testutil.GitRun(t, second, "config", "user.email", "test@example.com")
	testutil.GitRun(t, second, "config", "user.name", "Test")
	gitWriteFile(t, filepath.Join(second, "new.txt"), "new\n")
	testutil.GitRun(t, second, "add", "new.txt")
	testutil.GitRun(t, second, "commit", "-m", "advance")
	testutil.GitRun(t, second, "push", "origin", baseBranch)
	return gitOutput(t, second, "rev-parse", "HEAD"), nil
}
