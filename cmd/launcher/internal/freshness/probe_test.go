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

// TestProbe_Bwrap_NotApplicable_WhenNotAGitRepo verifies that a "bwrap"
// runnerKind is not special-cased to always report not-applicable — it now
// flows through the same fetch-base-tip + eval logic as any other
// runnerKind, and only reports not-applicable for the same underlying
// reasons (here: pwd isn't inside any git repository at all), naming that
// reason rather than stale bwrap-specific wording. Mirrors
// TestProbe_NotAGitRepo but with runnerKind "bwrap".
func TestProbe_Bwrap_NotApplicable_WhenNotAGitRepo(t *testing.T) {
	pwd := t.TempDir()
	eval := &Fake{OutPath: "/nix/store/" + testutil.SameHash + "-agent-closure"}

	res := Probe("bwrap", pwd, "main", ".#packages.x86_64-linux.agent-closure", "/nix/store/"+testutil.SameHash+"-agent-closure", "", "", eval)

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

// TestProbe_Bwrap_FreshWhenClosureOutPathMatches verifies that a "bwrap"
// runnerKind compares the freshly evaluated outPath directly against
// imageTag (a bare nix store path for bwrap, not a "repo:tag" string) — an
// exact match reports fresh with no genuine divergence to name.
func TestProbe_Bwrap_FreshWhenClosureOutPathMatches(t *testing.T) {
	pwd := testutil.NewCloneWithOrigin(t, "main")
	closurePath := "/nix/store/" + testutil.SameHash + "-agent-closure"
	eval := &Fake{OutPath: closurePath}

	res := Probe("bwrap", pwd, "main", ".#packages.x86_64-linux.agent-closure", closurePath, "", "", eval)

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

// TestProbe_Bwrap_RebuildNeededWhenClosureOutPathDiffers verifies that a
// "bwrap" runnerKind reports rebuild-needed when the freshly evaluated
// outPath differs from the loaded one, and that TipTag carries the raw fresh
// outPath verbatim — never the OCI "<repo>:<hash>" tag format, since a raw
// nix store path never contains a colon.
func TestProbe_Bwrap_RebuildNeededWhenClosureOutPathDiffers(t *testing.T) {
	pwd := testutil.NewCloneWithOrigin(t, "main")
	freshPath := "/nix/store/" + testutil.DiffHash + "-agent-closure"
	loadedPath := "/nix/store/" + testutil.SameHash + "-agent-closure"
	eval := &Fake{OutPath: freshPath}

	res := Probe("bwrap", pwd, "main", ".#packages.x86_64-linux.agent-closure", loadedPath, "", "", eval)

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

// TestProbe_Bwrap_RebuildNeededMessage_SaysClosureNotImage verifies that a
// "bwrap" runnerKind's rebuild-needed message calls the loaded value a
// "closure", never an "image" — the loaded value is a bundled nix store
// path, not an OCI image, and calling it "the loaded image" is misleading
// for an operator reading the message.
func TestProbe_Bwrap_RebuildNeededMessage_SaysClosureNotImage(t *testing.T) {
	pwd := testutil.NewCloneWithOrigin(t, "main")
	freshPath := "/nix/store/" + testutil.DiffHash + "-agent-closure"
	loadedPath := "/nix/store/" + testutil.SameHash + "-agent-closure"
	eval := &Fake{OutPath: freshPath}

	res := Probe("bwrap", pwd, "main", ".#packages.x86_64-linux.agent-closure", loadedPath, "", "", eval)

	if !strings.Contains(res.Message, "loaded closure") {
		t.Errorf("Message %q does not say \"loaded closure\"", res.Message)
	}
	if strings.Contains(res.Message, "loaded image") {
		t.Errorf("Message %q says \"loaded image\", want \"loaded closure\" for a bwrap runnerKind", res.Message)
	}
}

// TestProbe_Bwrap_LauncherStale_ImageFresh_RebuildNeeded verifies that the
// launcher dimension still composes correctly for a "bwrap" runnerKind: a
// matching closure outPath but a stale launcher hash drives Fresh to false
// and LauncherFresh to false while ImageFresh stays true, and Message names
// the launcher (not "image:") as the cause.
func TestProbe_Bwrap_LauncherStale_ImageFresh_RebuildNeeded(t *testing.T) {
	pwd := testutil.NewCloneWithOrigin(t, "main")
	closurePath := "/nix/store/" + testutil.SameHash + "-agent-closure"
	eval := &Fake{
		OutPathForAttr: map[string]string{
			"packages.x86_64-linux.agent-closure": closurePath,
			"packages.x86_64-linux.launcher":      "/nix/store/" + testutil.DiffHash + "-launcher",
		},
	}

	res := Probe("bwrap", pwd, "main", ".#packages.x86_64-linux.agent-closure", closurePath, ".#packages.x86_64-linux.launcher", testutil.SameHash, eval)

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

// TestProbe_RunnerKindNotApplicable_KeysOffValueNotRuntimeName is issue
// #2538's regression test for probe.go:93 (a surviving runtime-name
// comparison the original AC1 implementation missed): it feeds Probe a
// runnerKind of "oci" — a value that is not itself any real runtime CLI
// name (unlike "podman"/"docker" used elsewhere in this file) — and
// confirms Probe proceeds past the early return exactly as it would for any
// other non-"bwrap" runnerKind. This proves the comparison keys off the
// parameter's runnerKind semantics (only the literal string "bwrap" is
// special), not a coincidental match against a known runtime executable
// name.
func TestProbe_RunnerKindNotApplicable_KeysOffValueNotRuntimeName(t *testing.T) {
	pwd := testutil.NewCloneWithOrigin(t, "main")
	eval := &Fake{OutPath: "/nix/store/" + testutil.SameHash + "-agent-image"}

	res := Probe("oci", pwd, "main", ".#packages.x86_64-linux.agent-image", "spindrift:"+testutil.SameHash, "", "", eval)

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

// TestProbe_FreshWhenImageHashMatches verifies that an outPath evaluated at
// the fetched base tip whose content-hash tag equals the loaded image's tag
// reports fresh.
func TestProbe_FreshWhenImageHashMatches(t *testing.T) {
	pwd := testutil.NewCloneWithOrigin(t, "main")
	eval := &Fake{OutPath: "/nix/store/" + testutil.SameHash + "-agent-image"}

	res := Probe("podman", pwd, "main", ".#packages.x86_64-linux.agent-image", "spindrift:"+testutil.SameHash, "", "", eval)

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

// TestProbe_EvalReceivesFetchedRev verifies that Probe passes the fetched
// base-tip sha (not the local clone's own checked-out HEAD) to Eval — the
// wiring that makes the eval hermetic against the fetched tip rather than
// whatever pwd happens to have checked out.
func TestProbe_EvalReceivesFetchedRev(t *testing.T) {
	pwd := testutil.NewCloneWithOrigin(t, "main")
	localHead := gitOutput(t, pwd, "rev-parse", "HEAD")
	advancedSha, err := gitAdvanceOrigin(t, pwd, "main")
	if err != nil {
		t.Fatalf("gitAdvanceOrigin: %v", err)
	}
	eval := &Fake{OutPath: "/nix/store/" + testutil.SameHash + "-agent-image"}

	Probe("podman", pwd, "main", ".#packages.x86_64-linux.agent-image", "spindrift:"+testutil.SameHash, "", "", eval)

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

// TestProbe_RebuildNeededWhenImageHashDiffers verifies that a base-tip
// commit which changed image inputs — a different evaluated content-hash
// tag — reports rebuild-needed, not fresh.
func TestProbe_RebuildNeededWhenImageHashDiffers(t *testing.T) {
	pwd := testutil.NewCloneWithOrigin(t, "main")
	eval := &Fake{OutPath: "/nix/store/" + testutil.DiffHash + "-agent-image"}

	res := Probe("podman", pwd, "main", ".#packages.x86_64-linux.agent-image", "spindrift:"+testutil.SameHash, "", "", eval)

	if !res.Applicable {
		t.Fatalf("Applicable = false, want true for a non-bwrap runnerKind (podman)")
	}
	if res.Fresh {
		t.Errorf("Fresh = true, want false when the image tag differs; message: %s", res.Message)
	}
}

// TestProbe_RebuildNeededSetsTipTag verifies that a rebuild-needed verdict
// populates Result.TipTag with the freshly evaluated "<repo>:<hash>" tag — the
// tag a rebuild would load — so the non-convergence diagnostic (issue #2113)
// can name it alongside the loaded tag.
func TestProbe_RebuildNeededSetsTipTag(t *testing.T) {
	pwd := testutil.NewCloneWithOrigin(t, "main")
	eval := &Fake{OutPath: "/nix/store/" + testutil.DiffHash + "-agent-image"}

	res := Probe("podman", pwd, "main", ".#packages.x86_64-linux.agent-image", "spindrift:"+testutil.SameHash, "", "", eval)

	want := "spindrift:" + testutil.DiffHash
	if res.TipTag != want {
		t.Errorf("TipTag = %q, want %q", res.TipTag, want)
	}
}

// TestProbe_LivelockRegression_FreshWhenTagMatchesDespiteOutPathNameDrift
// reproduces the #587 livelock: a loaded image whose output identity
// (content-hash tag) matches the base tip must report fresh even when the
// full store path text differs (e.g. a differing derivation name suffix) —
// the same currency `build`/EnsureReady gates on (the tag), not the raw
// drvPath a stale baked IMAGE_DRV could desync from with no way to re-sync.
func TestProbe_LivelockRegression_FreshWhenTagMatchesDespiteOutPathNameDrift(t *testing.T) {
	pwd := testutil.NewCloneWithOrigin(t, "main")
	hash := "abcdefghijklmnopqrstuvwxyz012345"
	eval := &Fake{OutPath: "/nix/store/" + hash + "-agent-image-generation-7"}
	loadedTag := "spindrift:" + hash

	res := Probe("podman", pwd, "main", ".#packages.x86_64-linux.agent-image", loadedTag, "", "", eval)

	if !res.Applicable {
		t.Fatalf("Applicable = false, want true for a non-bwrap runnerKind (podman)")
	}
	if !res.Fresh {
		t.Errorf("Fresh = false, want true when the tip's image tag matches the loaded tag; message: %s", res.Message)
	}
}

// TestProbe_DriverScopedRepo_FreshWhenImageHashMatches verifies that a
// loaded image tagged under a driver-scoped repo (e.g. "spindrift-opencode",
// not the default "spindrift") makes Probe derive its tip tag under that
// SAME repo, so a matching content hash reports fresh — an opencode image
// must never compare against a hardcoded "spindrift:" tip tag (#262).
func TestProbe_DriverScopedRepo_FreshWhenImageHashMatches(t *testing.T) {
	pwd := testutil.NewCloneWithOrigin(t, "main")
	eval := &Fake{OutPath: "/nix/store/" + testutil.SameHash + "-agent-image"}

	res := Probe("podman", pwd, "main", ".#packages.x86_64-linux.agent-image", "spindrift-opencode:"+testutil.SameHash, "", "", eval)

	if !res.Applicable {
		t.Fatalf("Applicable = false, want true for a non-bwrap runnerKind (podman)")
	}
	if !res.Fresh {
		t.Errorf("Fresh = false, want true when the driver-scoped image tag matches; message: %s", res.Message)
	}
}

// TestProbe_DriverScopedRepo_RebuildNeededWhenImageHashDiffers verifies that
// a driver-scoped loaded tag (e.g. "spindrift-opencode:<hash>") whose hash
// differs from the tip's evaluated hash reports rebuild-needed, and the
// message names the repo-matching tip tag (not a hardcoded "spindrift:"
// tag) so the diagnostic is accurate for the driver in play.
func TestProbe_DriverScopedRepo_RebuildNeededWhenImageHashDiffers(t *testing.T) {
	pwd := testutil.NewCloneWithOrigin(t, "main")
	eval := &Fake{OutPath: "/nix/store/" + testutil.DiffHash + "-agent-image"}

	res := Probe("podman", pwd, "main", ".#packages.x86_64-linux.agent-image", "spindrift-opencode:"+testutil.SameHash, "", "", eval)

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

// TestProbe_LauncherStale_ImageFresh_RebuildNeeded verifies that a stale
// launcher hash (the tip's evaluated launcher store hash differs from the
// loaded one) drives Fresh to false and LauncherFresh to false even when the
// image dimension matches — and that Message names the launcher, not the
// image, as the cause, since the image itself is fresh.
func TestProbe_LauncherStale_ImageFresh_RebuildNeeded(t *testing.T) {
	pwd := testutil.NewCloneWithOrigin(t, "main")
	eval := &Fake{
		OutPathForAttr: map[string]string{
			"packages.x86_64-linux.agent-image": "/nix/store/" + testutil.SameHash + "-agent-image",
			"packages.x86_64-linux.launcher":    "/nix/store/" + testutil.DiffHash + "-launcher",
		},
	}

	res := Probe("podman", pwd, "main", ".#packages.x86_64-linux.agent-image", "spindrift:"+testutil.SameHash, ".#packages.x86_64-linux.launcher", testutil.SameHash, eval)

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

// TestProbe_ImageStale_LauncherFresh_RebuildNeeded verifies that a stale
// image hash drives Fresh to false even when the launcher dimension matches
// (LauncherFresh true) — and that Message names the image, not the
// launcher, as the cause.
func TestProbe_ImageStale_LauncherFresh_RebuildNeeded(t *testing.T) {
	pwd := testutil.NewCloneWithOrigin(t, "main")
	eval := &Fake{
		OutPathForAttr: map[string]string{
			"packages.x86_64-linux.agent-image": "/nix/store/" + testutil.DiffHash + "-agent-image",
			"packages.x86_64-linux.launcher":    "/nix/store/" + testutil.SameHash + "-launcher",
		},
	}

	res := Probe("podman", pwd, "main", ".#packages.x86_64-linux.agent-image", "spindrift:"+testutil.SameHash, ".#packages.x86_64-linux.launcher", testutil.SameHash, eval)

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

// TestProbe_ImageAndLauncherBothStale_RebuildNeeded verifies that a stale
// hash on BOTH dimensions drives Fresh and LauncherFresh both to false, and
// that Message names both the image and the launcher as causes.
func TestProbe_ImageAndLauncherBothStale_RebuildNeeded(t *testing.T) {
	pwd := testutil.NewCloneWithOrigin(t, "main")
	eval := &Fake{
		OutPathForAttr: map[string]string{
			"packages.x86_64-linux.agent-image": "/nix/store/" + testutil.DiffHash + "-agent-image",
			"packages.x86_64-linux.launcher":    "/nix/store/" + testutil.DiffHash + "-launcher",
		},
	}

	res := Probe("podman", pwd, "main", ".#packages.x86_64-linux.agent-image", "spindrift:"+testutil.SameHash, ".#packages.x86_64-linux.launcher", testutil.SameHash, eval)

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

// TestProbe_ImageAndLauncherBothFresh verifies that a matching hash on BOTH
// dimensions reports Fresh and LauncherFresh both true, Message confirms
// both match, and TipLauncherHash carries the freshly evaluated launcher
// hash.
func TestProbe_ImageAndLauncherBothFresh(t *testing.T) {
	pwd := testutil.NewCloneWithOrigin(t, "main")
	eval := &Fake{
		OutPathForAttr: map[string]string{
			"packages.x86_64-linux.agent-image": "/nix/store/" + testutil.SameHash + "-agent-image",
			"packages.x86_64-linux.launcher":    "/nix/store/" + testutil.SameHash + "-launcher",
		},
	}

	res := Probe("podman", pwd, "main", ".#packages.x86_64-linux.agent-image", "spindrift:"+testutil.SameHash, ".#packages.x86_64-linux.launcher", testutil.SameHash, eval)

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

// TestProbe_LauncherNotConfigured_ImageFresh_Fresh is a regression check for
// an existing image-only caller (flakeLauncherAttr == ""): the launcher
// dimension is not configured/not checked at all, so it must never veto an
// otherwise-fresh image verdict, and Probe must not call Eval a second time
// for a launcher attr that was never supplied.
func TestProbe_LauncherNotConfigured_ImageFresh_Fresh(t *testing.T) {
	pwd := testutil.NewCloneWithOrigin(t, "main")
	eval := &Fake{OutPath: "/nix/store/" + testutil.SameHash + "-agent-image"}

	res := Probe("podman", pwd, "main", ".#packages.x86_64-linux.agent-image", "spindrift:"+testutil.SameHash, "", "", eval)

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

// TestProbe_LauncherEvalFailure_TipTagEmptyImageFresh verifies that a
// launcher eval failure (the image attr evaluates fine, but the launcher
// attr's Eval call errors) reports rebuild-needed with Rev set but TipTag
// left empty — a stuck launcher eval failure repeating at the same rev must
// stay Rebuild under Guard.Classify, not spuriously look like a genuine
// image-tag divergence (HostTainted). ImageFresh is true since the image
// dimension itself succeeded and matched.
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

	res := Probe("podman", pwd, "main", ".#packages.x86_64-linux.agent-image", "spindrift:"+testutil.SameHash, ".#packages.x86_64-linux.launcher", testutil.SameHash, eval)

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

// TestProbe_LauncherHashDeriveFailure_TipTagEmptyImageFresh verifies that a
// launcher hash-derive failure (the launcher attr evaluates to an outPath
// that isn't a valid nix store path, so storeHash errors) reports
// rebuild-needed with Rev set but TipTag left empty — same "stuck failure,
// not a genuine divergence" shape as the eval-failure case, so it also stays
// Rebuild under Guard.Classify on a repeat at the same rev.
func TestProbe_LauncherHashDeriveFailure_TipTagEmptyImageFresh(t *testing.T) {
	pwd := testutil.NewCloneWithOrigin(t, "main")
	eval := &Fake{
		OutPathForAttr: map[string]string{
			"packages.x86_64-linux.agent-image": "/nix/store/" + testutil.SameHash + "-agent-image",
			"packages.x86_64-linux.launcher":    "not-a-store-path",
		},
	}

	res := Probe("podman", pwd, "main", ".#packages.x86_64-linux.agent-image", "spindrift:"+testutil.SameHash, ".#packages.x86_64-linux.launcher", testutil.SameHash, eval)

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

// TestImageRepo_DerivesRepoFromLastColon verifies imageRepo splits an
// "<repo>:<tag>" reference on the LAST colon (a repo can itself embed a
// colon, e.g. a "host:port" registry prefix), and falls back to the default
// "spindrift" repo for a degenerate tag with no colon at all rather than
// deriving an empty or nonsensical repo.
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

// TestProbe_Rev_MatchesFetchedTip verifies Result.Rev carries the same
// fetched base-tip sha Eval was hermetically evaluated at — a caller (the
// Console's in-session rebuild, issue #652) needs the rev itself, not just
// the tag comparison, to recognize "I already rebuilt this exact tip"
// without re-parsing Message.
func TestProbe_Rev_MatchesFetchedTip(t *testing.T) {
	pwd := testutil.NewCloneWithOrigin(t, "main")
	advancedSha, err := gitAdvanceOrigin(t, pwd, "main")
	if err != nil {
		t.Fatalf("gitAdvanceOrigin: %v", err)
	}
	eval := &Fake{OutPath: "/nix/store/" + testutil.DiffHash + "-agent-image"}

	res := Probe("podman", pwd, "main", ".#packages.x86_64-linux.agent-image", "spindrift:"+testutil.SameHash, "", "", eval)

	if res.Rev != advancedSha {
		t.Errorf("Rev = %q, want the fetched base tip %q", res.Rev, advancedSha)
	}
}

// TestProbe_EvalFailureFailsClosed verifies that an eval error reports
// rebuild-needed with a loud message rather than guessing fresh.
func TestProbe_EvalFailureFailsClosed(t *testing.T) {
	pwd := testutil.NewCloneWithOrigin(t, "main")
	eval := &Fake{Err: errEvalBoom}

	res := Probe("podman", pwd, "main", ".#packages.x86_64-linux.agent-image", "spindrift:"+testutil.SameHash, "", "", eval)

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

// TestProbe_FetchFailureFailsClosed verifies that a git fetch error against a
// configured, name-resolvable-looking origin (e.g. a transient network
// failure) reports rebuild-needed with a loud message, without ever calling
// the evaluator — distinct from the definitive, not-applicable cases
// (TestProbe_NotAGitRepo, TestProbe_MissingRemoteRefNotApplicable,
// TestProbe_NoOriginRemoteNotApplicable) where proceeding is safe.
func TestProbe_FetchFailureFailsClosed(t *testing.T) {
	pwd := t.TempDir()
	testutil.GitRun(t, pwd, "init")
	testutil.GitRun(t, pwd, "remote", "add", "origin", "https://example.invalid/nope.git")
	eval := &Fake{OutPath: "/nix/store/" + testutil.SameHash + "-agent-image"}

	res := Probe("podman", pwd, "main", ".#packages.x86_64-linux.agent-image", "spindrift:"+testutil.SameHash, "", "", eval)

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

// TestProbe_NotAGitRepo verifies that a pwd which is not inside any git
// repository at all reports not-applicable — distinct from a transient fetch
// failure inside a real repo (TestProbe_FetchFailureFailsClosed) — so the
// console does not hold launches or offer a [b] rebuild that would fail the
// same way.
func TestProbe_NotAGitRepo(t *testing.T) {
	pwd := t.TempDir()
	eval := &Fake{OutPath: "/nix/store/" + testutil.SameHash + "-agent-image"}

	res := Probe("podman", pwd, "main", ".#packages.x86_64-linux.agent-image", "spindrift:"+testutil.SameHash, "", "", eval)

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

// TestProbe_MissingRemoteRefNotApplicable verifies that a base branch which
// simply doesn't exist on origin — git's own "couldn't find remote ref"
// diagnostic — reports not-applicable rather than fail-closed: this repo's
// origin has no such branch, so freshness cannot be checked here, and
// continuous dispatch must not treat it as rebuild-needed (#1753).
func TestProbe_MissingRemoteRefNotApplicable(t *testing.T) {
	pwd := testutil.NewCloneWithOrigin(t, "main")
	eval := &Fake{OutPath: "/nix/store/" + testutil.SameHash + "-agent-image"}

	res := Probe("podman", pwd, "release", ".#packages.x86_64-linux.agent-image", "spindrift:"+testutil.SameHash, "", "", eval)

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

// TestProbe_NoOriginRemoteNotApplicable verifies that a repo with no
// "origin" remote configured at all — git's own "does not appear to be a
// git repository" diagnostic — reports not-applicable rather than
// fail-closed: a fully local repo (e.g. CODE_FORGE=local, no live remote)
// has nothing to fetch, so freshness cannot be checked here, and continuous
// dispatch must not treat it as rebuild-needed (#2034).
func TestProbe_NoOriginRemoteNotApplicable(t *testing.T) {
	pwd := t.TempDir()
	testutil.GitRun(t, pwd, "init")
	eval := &Fake{OutPath: "/nix/store/" + testutil.SameHash + "-agent-image"}

	res := Probe("podman", pwd, "main", ".#packages.x86_64-linux.agent-image", "spindrift:"+testutil.SameHash, "", "", eval)

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

// TestProbe_ImageAttrMissingNotApplicable verifies that an Eval failure
// because the flake simply does not define flakeImageAttr — nix's own "does
// not provide attribute" diagnostic — reports not-applicable rather than
// fail-closed: pwd isn't the spindrift image-source flake, so freshness
// cannot be checked here, and continuous dispatch must not treat it as
// rebuild-needed (#1754).
func TestProbe_ImageAttrMissingNotApplicable(t *testing.T) {
	pwd := testutil.NewCloneWithOrigin(t, "main")
	attrErr := errors.New(`nix eval git+file:///tmp/target#packages.x86_64-linux.agent-image.outPath: exit status 1: error: flake 'git+file:///tmp/target' does not provide attribute 'packages.x86_64-linux.agent-image', 'legacyPackages.x86_64-linux.agent-image' or 'packages.x86_64-linux.default'`)
	eval := &Fake{Err: attrErr}

	res := Probe("podman", pwd, "main", ".#packages.x86_64-linux.agent-image", "spindrift:"+testutil.SameHash, "", "", eval)

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

// TestProbe_FetchFailure_MessageIncludesGitStderr verifies that the loud
// fetch-failure message surfaces git's own diagnostic (its stderr), not just
// the bare exit status, so an operator reading `preview` output can see why.
func TestProbe_FetchFailure_MessageIncludesGitStderr(t *testing.T) {
	pwd := t.TempDir()
	testutil.GitRun(t, pwd, "init")
	testutil.GitRun(t, pwd, "remote", "add", "origin", "https://example.invalid/nope.git")
	eval := &Fake{OutPath: "/nix/store/" + testutil.SameHash + "-agent-image"}

	res := Probe("podman", pwd, "main", ".#packages.x86_64-linux.agent-image", "spindrift:"+testutil.SameHash, "", "", eval)

	if !strings.Contains(res.Message, "example.invalid") {
		t.Errorf("Message %q does not surface git's stderr detail", res.Message)
	}
}

// TestProbe_NeverMutatesWorkingCopy verifies that Probe fetches the base tip
// without checking it out — the local clone's checked-out commit and dirty
// files are unchanged after the call.
func TestProbe_NeverMutatesWorkingCopy(t *testing.T) {
	pwd := testutil.NewCloneWithOrigin(t, "main")
	before := gitOutput(t, pwd, "rev-parse", "HEAD")
	eval := &Fake{OutPath: "/nix/store/" + testutil.SameHash + "-agent-image"}

	if _, err := gitAdvanceOrigin(t, pwd, "main"); err != nil {
		t.Fatalf("gitAdvanceOrigin: %v", err)
	}

	res := Probe("podman", pwd, "main", ".#packages.x86_64-linux.agent-image", "spindrift:"+testutil.SameHash, "", "", eval)
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

// gitOutput runs git in dir and returns trimmed stdout, failing the test on
// error.
func gitOutput(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).Output()
	if err != nil {
		t.Fatalf("git %v: %v", args, err)
	}
	return strings.TrimSpace(string(out))
}

// gitAdvanceOrigin commits a new file on baseBranch in a second clone of the
// same origin as pwd and pushes it, simulating a merge landing on the base
// branch after pwd's own clone was made — without touching pwd itself.
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
