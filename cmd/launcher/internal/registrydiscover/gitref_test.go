package registrydiscover

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"

	"spindrift.dev/launcher/internal/testutil"
)

// mustBareRepoWithConfigs builds the fixture the way an Accumulation repo
// actually looks (ADR 0033): a source checkout commits the files, then pushes
// them to a second, bare repo, so the fixture has no working tree at all.
func mustBareRepoWithConfigs(t *testing.T, branch string, files map[string]string) string {
	t.Helper()
	bare, _ := mustBareRepoWithSymlinks(t, branch, files, nil)
	return bare
}

// mustBareRepoWithSymlinks also commits each symlinks entry as a link to its
// target, and returns the source checkout so a test can compare the ref
// snapshot against a real checkout of the same tree.
func mustBareRepoWithSymlinks(t *testing.T, branch string, files, symlinks map[string]string) (bare, src string) {
	t.Helper()
	src = t.TempDir()
	testutil.GitRun(t, src, "init", "-b", branch)
	testutil.GitRun(t, src, "config", "user.email", "test@example.com")
	testutil.GitRun(t, src, "config", "user.name", "Test")

	for rel, body := range files {
		full := filepath.Join(src, rel)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		testutil.GitRun(t, src, "add", rel)
	}
	for link, target := range symlinks {
		if err := os.Symlink(target, filepath.Join(src, link)); err != nil {
			t.Fatal(err)
		}
		testutil.GitRun(t, src, "add", link)
	}
	if len(files)+len(symlinks) == 0 {
		// A branch with no commit has no resolvable ^{tree}, so give it one
		// harmless commit to keep "carries no config files" distinct from
		// "carries no commits at all".
		if err := os.WriteFile(filepath.Join(src, "README"), []byte("empty\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		testutil.GitRun(t, src, "add", "README")
	}
	testutil.GitRun(t, src, "commit", "-m", "seed")

	bare = filepath.Join(t.TempDir(), "accum.git")
	testutil.GitRun(t, "", "init", "--bare", bare)
	testutil.GitRun(t, src, "push", bare, "+refs/heads/"+branch+":refs/heads/"+branch)

	return bare, src
}

// Pins ResolveRef's fail-closed contract directly, not only through its
// MaterializeRef and UncoveredHostsFromGitRef callers.
func TestResolveRef_MissingRepoDirFails(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "does-not-exist")
	if err := ResolveRef(missing, "main"); err == nil {
		t.Fatal("ResolveRef: want error for a missing repo dir, got nil")
	}
}

// An empty repoDir must error rather than fall through to `git -C ""`, which
// resolves the ref against the process cwd's own repo. Run from inside any
// checkout, a ref like "main" would otherwise resolve and report drift for the
// wrong repo. The t.Chdir into the fixture is what makes that failure visible.
func TestResolveRef_EmptyRepoDirFails(t *testing.T) {
	bare := mustBareRepoWithConfigs(t, "main", map[string]string{
		".npmrc": "registry=https://host.example.com/npm\n",
	})
	t.Chdir(bare)

	if err := ResolveRef("", "main"); err == nil {
		t.Fatal("ResolveRef: want error for an empty repo dir, got nil")
	}
}

// The error must name the ref: registrypathset asserts on that same wording
// through MaterializeRef.
func TestResolveRef_MissingRefFails(t *testing.T) {
	bare := mustBareRepoWithConfigs(t, "main", map[string]string{
		".npmrc": "registry=https://host.example.com/npm\n",
	})

	err := ResolveRef(bare, "does-not-exist")
	if err == nil {
		t.Fatal("ResolveRef: want error for a ref that does not exist, got nil")
	}
	if !strings.Contains(err.Error(), "does-not-exist") {
		t.Errorf("ResolveRef error = %q, want it to name the missing ref", err.Error())
	}
}

// registryRouteDriftCheckForRef gates the drift row's existence on this
// success path.
func TestResolveRef_ResolvableRefSucceeds(t *testing.T) {
	bare := mustBareRepoWithConfigs(t, "main", map[string]string{
		".npmrc": "registry=https://host.example.com/npm\n",
	})

	if err := ResolveRef(bare, "main"); err != nil {
		t.Errorf("ResolveRef: unexpected error for a resolvable ref: %v", err)
	}
}

func TestUncoveredHostsFromGitRef_UncoveredHostReported(t *testing.T) {
	bare := mustBareRepoWithConfigs(t, "main", map[string]string{
		".npmrc": "registry=https://host.example.com/npm\n",
	})

	got, err := UncoveredHostsFromGitRef(bare, "main", nil)
	if err != nil {
		t.Fatalf("UncoveredHostsFromGitRef: unexpected error: %v", err)
	}
	want := []string{"host.example.com"}
	if len(got) != 1 || got[0] != want[0] {
		t.Errorf("UncoveredHostsFromGitRef = %v, want %v", got, want)
	}
}

func TestUncoveredHostsFromGitRef_FullyCoveredReturnsNone(t *testing.T) {
	bare := mustBareRepoWithConfigs(t, "main", map[string]string{
		".npmrc": "registry=https://host.example.com/npm\n",
	})

	got, err := UncoveredHostsFromGitRef(bare, "main", []string{"host.example.com"})
	if err != nil {
		t.Fatalf("UncoveredHostsFromGitRef: unexpected error: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("UncoveredHostsFromGitRef = %v, want none", got)
	}
}

func TestUncoveredHostsFromGitRef_MissingRepoDirFails(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "does-not-exist")
	_, err := UncoveredHostsFromGitRef(missing, "main", nil)
	if err == nil {
		t.Fatal("UncoveredHostsFromGitRef: want error for a missing repo dir, got nil")
	}
}

func TestUncoveredHostsFromGitRef_MissingRefFails(t *testing.T) {
	bare := mustBareRepoWithConfigs(t, "main", map[string]string{
		".npmrc": "registry=https://host.example.com/npm\n",
	})

	_, err := UncoveredHostsFromGitRef(bare, "does-not-exist", nil)
	if err == nil {
		t.Fatal("UncoveredHostsFromGitRef: want error for a ref that does not exist, got nil")
	}
	if !strings.Contains(err.Error(), "does-not-exist") {
		t.Errorf("UncoveredHostsFromGitRef error = %q, want it to name the missing ref", err.Error())
	}
}

// A ref declaring no config files is not an error, unlike a broken repo or
// ref. Both yield no hosts, so only the error distinguishes them.
func TestUncoveredHostsFromGitRef_NoConfigFilesReturnsNone(t *testing.T) {
	bare := mustBareRepoWithConfigs(t, "main", nil)

	got, err := UncoveredHostsFromGitRef(bare, "main", nil)
	if err != nil {
		t.Fatalf("UncoveredHostsFromGitRef: unexpected error: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("UncoveredHostsFromGitRef = %v, want none", got)
	}
}

func mustMaterializeRef(t *testing.T, bare, ref string) string {
	t.Helper()
	dir, cleanup, err := MaterializeRef(bare, ref)
	t.Cleanup(cleanup)
	if err != nil {
		t.Fatalf("MaterializeRef: unexpected error: %v", err)
	}
	return dir
}

func mustNotExist(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		t.Fatalf("Lstat(%q) = %v, want IsNotExist", path, err)
	}
}

// assertRefCheckoutHostsAgree pins that a ref snapshot and a real checkout of
// the same tree declare the same hosts, used by the symlink parity tests
// below. The non-empty check keeps the parity meaningful: two equal empty
// slices would pass slices.Equal without either side having followed a link.
func assertRefCheckoutHostsAgree(t *testing.T, bare, ref, src string) {
	t.Helper()
	refHosts, err := UncoveredHostsFromGitRef(bare, ref, nil)
	if err != nil {
		t.Fatalf("UncoveredHostsFromGitRef: unexpected error: %v", err)
	}
	checkoutHosts, err := UncoveredHosts(src, nil)
	if err != nil {
		t.Fatalf("UncoveredHosts: unexpected error: %v", err)
	}
	if len(refHosts) == 0 {
		t.Fatalf("UncoveredHostsFromGitRef = %v, want at least one host", refHosts)
	}
	if !slices.Equal(refHosts, checkoutHosts) {
		t.Errorf("UncoveredHostsFromGitRef = %v, UncoveredHosts = %v, want them equal", refHosts, checkoutHosts)
	}
}

// A leaf in-tree symlink (the config path itself is a symlink) must resolve
// to its target's content, matching what os.ReadFile gives a real checkout.
func TestMaterializeRef_LeafSymlinkResolvesToTargetContent(t *testing.T) {
	const content = "registry=https://leaf.example.com/npm\n"
	bare, src := mustBareRepoWithSymlinks(t, "main",
		map[string]string{"sub/npmrc": content},
		map[string]string{".npmrc": "sub/npmrc"},
	)

	dir := mustMaterializeRef(t, bare, "main")
	got, err := os.ReadFile(filepath.Join(dir, ".npmrc"))
	if err != nil {
		t.Fatalf("read materialized .npmrc: %v", err)
	}
	if string(got) != content {
		t.Errorf("materialized .npmrc = %q, want %q", got, content)
	}

	// The ref snapshot and a real checkout of the same tree must agree:
	// both follow the leaf symlink to the same host.
	assertRefCheckoutHostsAgree(t, bare, "main", src)
}

// A symlinked parent directory (cargo's ".cargo" row path, when the Target
// repo keeps ".cargo" itself as a symlink) must resolve the same way: the
// leaf path under the symlinked directory materializes with its real content.
func TestMaterializeRef_ParentDirSymlinkResolvesToTargetContent(t *testing.T) {
	const content = "[registries.othercorp]\nindex = \"sparse+https://cargo.example.test/index/\"\n"
	bare, src := mustBareRepoWithSymlinks(t, "main",
		map[string]string{"real/config.toml": content},
		map[string]string{".cargo": "real"},
	)

	dir := mustMaterializeRef(t, bare, "main")
	got, err := os.ReadFile(filepath.Join(dir, ".cargo", "config.toml"))
	if err != nil {
		t.Fatalf("read materialized .cargo/config.toml: %v", err)
	}
	if string(got) != content {
		t.Errorf("materialized .cargo/config.toml = %q, want %q", got, content)
	}

	assertRefCheckoutHostsAgree(t, bare, "main", src)
}

// A directory at a config path skips like a missing file, although
// extractRows hard-errors reading one in a checkout.
func TestMaterializeRef_DirectoryAtConfigPathSkips(t *testing.T) {
	bare := mustBareRepoWithConfigs(t, "main", map[string]string{
		".npmrc/inner": "anything",
	})

	dir := mustMaterializeRef(t, bare, "main")
	mustNotExist(t, filepath.Join(dir, ".npmrc"))
}

// The checkout side of the parity above: extractRows does hard-error reading
// a directory at the config path, unlike the ref snapshot it is compared
// against.
func TestUncoveredHosts_DirectoryAtConfigPathErrors(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".npmrc"), 0o755); err != nil {
		t.Fatal(err)
	}

	if _, err := UncoveredHosts(dir, nil); !errors.Is(err, syscall.EISDIR) {
		t.Fatalf("UncoveredHosts: err = %v, want EISDIR for a directory at the config path", err)
	}
}

// Unlike a checkout, a ref snapshot never reads a host file that an
// out-of-tree symlink names (ADR 0033).
func TestMaterializeRef_OutOfTreeSymlinkSkips(t *testing.T) {
	outside := t.TempDir()
	target := filepath.Join(outside, "external.npmrc")
	if err := os.WriteFile(target, []byte("registry=https://outside.example.com/npm\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	bare, _ := mustBareRepoWithSymlinks(t, "main", nil, map[string]string{".npmrc": target})

	dir := mustMaterializeRef(t, bare, "main")
	mustNotExist(t, filepath.Join(dir, ".npmrc"))
}

func TestMaterializeRef_DanglingSymlinkSkips(t *testing.T) {
	bare, _ := mustBareRepoWithSymlinks(t, "main", nil, map[string]string{".npmrc": "nope"})

	dir := mustMaterializeRef(t, bare, "main")
	mustNotExist(t, filepath.Join(dir, ".npmrc"))
}

// A self-referential symlink (.npmrc -> .npmrc) is the "loop" reply shape,
// confirmed against real git with `printf 'HEAD:.npmrc\n' | git cat-file
// --batch --follow-symlinks`.
func TestMaterializeRef_SelfLoopSymlinkSkips(t *testing.T) {
	bare, _ := mustBareRepoWithSymlinks(t, "main", nil, map[string]string{".npmrc": ".npmrc"})

	dir := mustMaterializeRef(t, bare, "main")
	mustNotExist(t, filepath.Join(dir, ".npmrc"))
}

// ".cargo" committed as a symlink to a regular file makes ".cargo/config.toml"
// the "notdir" reply shape: a path component that resolves to a non-directory.
func TestMaterializeRef_NotDirComponentSkips(t *testing.T) {
	bare, _ := mustBareRepoWithSymlinks(t, "main",
		map[string]string{"realfile": "anything"},
		map[string]string{".cargo": "realfile"},
	)

	dir := mustMaterializeRef(t, bare, "main")
	mustNotExist(t, filepath.Join(dir, ".cargo", "config.toml"))
}

// One batch reply per row: mis-consuming the .npmrc tree reply's payload
// would desync the .yarnrc.yml blob reply after it.
func TestMaterializeRef_MultiRowBatchParsing(t *testing.T) {
	const yarnContent = "some yarn yml content\n"
	bare := mustBareRepoWithConfigs(t, "main", map[string]string{
		".npmrc/inner": "anything",
		".yarnrc.yml":  yarnContent,
	})

	dir := mustMaterializeRef(t, bare, "main")

	mustNotExist(t, filepath.Join(dir, ".cargo", "config.toml"))
	mustNotExist(t, filepath.Join(dir, ".npmrc"))
	mustNotExist(t, filepath.Join(dir, "pnpm-workspace.yaml"))

	got, err := os.ReadFile(filepath.Join(dir, ".yarnrc.yml"))
	if err != nil {
		t.Fatalf("read materialized .yarnrc.yml: %v", err)
	}
	if string(got) != yarnContent {
		t.Errorf("materialized .yarnrc.yml = %q, want %q", got, yarnContent)
	}
}

// A ref containing whitespace (ResolveRef accepts e.g. "HEAD@{1 day ago}")
// puts a space inside the header line too, so splitting the whole header on
// whitespace misreads a two-field "missing" reply as an unrecognized header.
func TestParseCatFileBatch_RefWithSpaceMissingHeader(t *testing.T) {
	out := []byte("HEAD@{1 day ago}:.npmrc missing\n")

	blobs, err := parseCatFileBatch(out, 1)
	if err != nil {
		t.Fatalf("parseCatFileBatch: unexpected error: %v", err)
	}
	if blobs[0] != nil {
		t.Errorf("parseCatFileBatch blobs[0] = %q, want nil", blobs[0])
	}
}

// A loop reply carries a payload, the path it names, which the parser must
// consume to stay in step with the blob reply after it. The self-loop fixture
// test only shows the file is absent, which a "missing" reply would show too.
func TestParseCatFileBatch_LoopPayloadThenBlob(t *testing.T) {
	out := []byte("loop 11\nHEAD:.npmrc\n" +
		"0123456789012345678901234567890123456789 blob 5\nhello\n")

	blobs, err := parseCatFileBatch(out, 2)
	if err != nil {
		t.Fatalf("parseCatFileBatch: unexpected error: %v", err)
	}
	if blobs[0] != nil {
		t.Errorf("parseCatFileBatch blobs[0] = %q, want nil", blobs[0])
	}
	if string(blobs[1]) != "hello" {
		t.Errorf("parseCatFileBatch blobs[1] = %q, want %q", blobs[1], "hello")
	}
}
