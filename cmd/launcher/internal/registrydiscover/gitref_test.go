package registrydiscover

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"

	"spindrift.dev/launcher/internal/ecosystem"
	"spindrift.dev/launcher/internal/ecosystemtest/synthetic"
	"spindrift.dev/launcher/internal/testutil"
)

var (
	syntheticRows = []ecosystem.Strategy{synthetic.Row}
	cfgPath       = synthetic.Row.InTreeConfigPath
)

const hostDecl = "main = https://host.example.com/registry\n"

// Pins ResolveRef's fail-closed contract directly, not only through its
// MaterializeRef and UncoveredHostsFromGitRefRows callers.
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
	bare, _ := testutil.BareRepoWithConfigs(t, "main", map[string]string{
		cfgPath: hostDecl,
	}, nil)
	t.Chdir(bare)

	if err := ResolveRef("", "main"); err == nil {
		t.Fatal("ResolveRef: want error for an empty repo dir, got nil")
	}
}

// The error must name the ref: registrypathset asserts on that same wording
// through MaterializeRef.
func TestResolveRef_MissingRefFails(t *testing.T) {
	bare, _ := testutil.BareRepoWithConfigs(t, "main", map[string]string{
		cfgPath: hostDecl,
	}, nil)

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
	bare, _ := testutil.BareRepoWithConfigs(t, "main", map[string]string{
		cfgPath: hostDecl,
	}, nil)

	if err := ResolveRef(bare, "main"); err != nil {
		t.Errorf("ResolveRef: unexpected error for a resolvable ref: %v", err)
	}
}

func TestUncoveredHostsFromGitRefRows_UncoveredHostReported(t *testing.T) {
	bare, _ := testutil.BareRepoWithConfigs(t, "main", map[string]string{
		cfgPath: hostDecl,
	}, nil)

	got, err := UncoveredHostsFromGitRefRows(bare, "main", nil, syntheticRows)
	if err != nil {
		t.Fatalf("UncoveredHostsFromGitRefRows: unexpected error: %v", err)
	}
	want := []string{"host.example.com"}
	if len(got) != 1 || got[0] != want[0] {
		t.Errorf("UncoveredHostsFromGitRefRows = %v, want %v", got, want)
	}
}

func TestUncoveredHostsFromGitRefRows_FullyCoveredReturnsNone(t *testing.T) {
	bare, _ := testutil.BareRepoWithConfigs(t, "main", map[string]string{
		cfgPath: hostDecl,
	}, nil)

	got, err := UncoveredHostsFromGitRefRows(bare, "main", []string{"host.example.com"}, syntheticRows)
	if err != nil {
		t.Fatalf("UncoveredHostsFromGitRefRows: unexpected error: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("UncoveredHostsFromGitRefRows = %v, want none", got)
	}
}

func TestUncoveredHostsFromGitRefRows_MissingRepoDirFails(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "does-not-exist")
	_, err := UncoveredHostsFromGitRefRows(missing, "main", nil, syntheticRows)
	if err == nil {
		t.Fatal("UncoveredHostsFromGitRefRows: want error for a missing repo dir, got nil")
	}
}

func TestUncoveredHostsFromGitRefRows_MissingRefFails(t *testing.T) {
	bare, _ := testutil.BareRepoWithConfigs(t, "main", map[string]string{
		cfgPath: hostDecl,
	}, nil)

	_, err := UncoveredHostsFromGitRefRows(bare, "does-not-exist", nil, syntheticRows)
	if err == nil {
		t.Fatal("UncoveredHostsFromGitRefRows: want error for a ref that does not exist, got nil")
	}
	if !strings.Contains(err.Error(), "does-not-exist") {
		t.Errorf("UncoveredHostsFromGitRefRows error = %q, want it to name the missing ref", err.Error())
	}
}

// A ref declaring no config files is not an error, unlike a broken repo or
// ref. Both yield no hosts, so only the error distinguishes them.
func TestUncoveredHostsFromGitRefRows_NoConfigFilesReturnsNone(t *testing.T) {
	bare, _ := testutil.BareRepoWithConfigs(t, "main", nil, nil)

	got, err := UncoveredHostsFromGitRefRows(bare, "main", nil, syntheticRows)
	if err != nil {
		t.Fatalf("UncoveredHostsFromGitRefRows: unexpected error: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("UncoveredHostsFromGitRefRows = %v, want none", got)
	}
}

func mustMaterializeRef(t *testing.T, bare, ref string) string {
	t.Helper()
	dir, cleanup, err := MaterializeRefRows(bare, ref, syntheticRows)
	t.Cleanup(cleanup)
	if err != nil {
		t.Fatalf("MaterializeRefRows: unexpected error: %v", err)
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
	refHosts, err := UncoveredHostsFromGitRefRows(bare, ref, nil, syntheticRows)
	if err != nil {
		t.Fatalf("UncoveredHostsFromGitRefRows: unexpected error: %v", err)
	}
	checkoutHosts, err := UncoveredHostsRows(src, nil, syntheticRows)
	if err != nil {
		t.Fatalf("UncoveredHostsRows: unexpected error: %v", err)
	}
	if len(refHosts) == 0 {
		t.Fatalf("UncoveredHostsFromGitRefRows = %v, want at least one host", refHosts)
	}
	if !slices.Equal(refHosts, checkoutHosts) {
		t.Errorf("UncoveredHostsFromGitRefRows = %v, UncoveredHostsRows = %v, want them equal", refHosts, checkoutHosts)
	}
}

// A leaf in-tree symlink (the config path itself is a symlink) must resolve
// to its target's content, matching what os.ReadFile gives a real checkout.
func TestMaterializeRef_LeafSymlinkResolvesToTargetContent(t *testing.T) {
	const content = "leaf = https://leaf.example.com/registry\n"
	bare, src := testutil.BareRepoWithConfigs(t, "main",
		map[string]string{filepath.Join(filepath.Dir(cfgPath), "real"): content},
		map[string]string{cfgPath: "real"},
	)

	dir := mustMaterializeRef(t, bare, "main")
	got, err := os.ReadFile(filepath.Join(dir, cfgPath))
	if err != nil {
		t.Fatalf("read materialized config: %v", err)
	}
	if string(got) != content {
		t.Errorf("materialized config = %q, want %q", got, content)
	}

	// The ref snapshot and a real checkout of the same tree must agree:
	// both follow the leaf symlink to the same host.
	assertRefCheckoutHostsAgree(t, bare, "main", src)
}

// A symlinked parent directory (the Target repo keeping the config path's
// directory itself as a symlink) must resolve the same way: the leaf path
// under the symlinked directory materializes with its real content.
func TestMaterializeRef_ParentDirSymlinkResolvesToTargetContent(t *testing.T) {
	const content = "parent = https://parent.example.com/registry\n"
	bare, src := testutil.BareRepoWithConfigs(t, "main",
		map[string]string{"real/registries": content},
		map[string]string{filepath.Dir(cfgPath): "real"},
	)

	dir := mustMaterializeRef(t, bare, "main")
	got, err := os.ReadFile(filepath.Join(dir, cfgPath))
	if err != nil {
		t.Fatalf("read materialized config: %v", err)
	}
	if string(got) != content {
		t.Errorf("materialized config = %q, want %q", got, content)
	}

	assertRefCheckoutHostsAgree(t, bare, "main", src)
}

// A directory at a config path skips like a missing file, although
// ExtractRows hard-errors reading one in a checkout.
func TestMaterializeRef_DirectoryAtConfigPathSkips(t *testing.T) {
	bare, _ := testutil.BareRepoWithConfigs(t, "main", map[string]string{
		cfgPath + "/inner": "anything",
	}, nil)

	dir := mustMaterializeRef(t, bare, "main")
	mustNotExist(t, filepath.Join(dir, cfgPath))
}

// The checkout side of the parity above: ExtractRows does hard-error reading
// a directory at the config path, unlike the ref snapshot it is compared
// against.
func TestUncoveredHostsRows_DirectoryAtConfigPathErrors(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, cfgPath), 0o755); err != nil {
		t.Fatal(err)
	}

	if _, err := UncoveredHostsRows(dir, nil, syntheticRows); !errors.Is(err, syscall.EISDIR) {
		t.Fatalf("UncoveredHostsRows: err = %v, want EISDIR for a directory at the config path", err)
	}
}

// Unlike a checkout, a ref snapshot never reads a host file that an
// out-of-tree symlink names (ADR 0033).
func TestMaterializeRef_OutOfTreeSymlinkSkips(t *testing.T) {
	outside := t.TempDir()
	target := filepath.Join(outside, "external")
	if err := os.WriteFile(target, []byte("out = https://outside.example.com/registry\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	bare, _ := testutil.BareRepoWithConfigs(t, "main", nil, map[string]string{cfgPath: target})

	dir := mustMaterializeRef(t, bare, "main")
	mustNotExist(t, filepath.Join(dir, cfgPath))
}

func TestMaterializeRef_DanglingSymlinkSkips(t *testing.T) {
	bare, _ := testutil.BareRepoWithConfigs(t, "main", nil, map[string]string{cfgPath: "nope"})

	dir := mustMaterializeRef(t, bare, "main")
	mustNotExist(t, filepath.Join(dir, cfgPath))
}

// A self-referential symlink (the config path pointing at itself) is the
// "loop" reply shape, confirmed against real git with `printf
// 'HEAD:<path>\n' | git cat-file --batch --follow-symlinks`.
func TestMaterializeRef_SelfLoopSymlinkSkips(t *testing.T) {
	bare, _ := testutil.BareRepoWithConfigs(t, "main", nil, map[string]string{cfgPath: filepath.Base(cfgPath)})

	dir := mustMaterializeRef(t, bare, "main")
	mustNotExist(t, filepath.Join(dir, cfgPath))
}

// The config path's directory committed as a symlink to a regular file makes
// the config path the "notdir" reply shape: a path component that resolves to
// a non-directory.
func TestMaterializeRef_NotDirComponentSkips(t *testing.T) {
	bare, _ := testutil.BareRepoWithConfigs(t, "main",
		map[string]string{"realfile": "anything"},
		map[string]string{filepath.Dir(cfgPath): "realfile"},
	)

	dir := mustMaterializeRef(t, bare, "main")
	mustNotExist(t, filepath.Join(dir, cfgPath))
}

// One batch reply per row: mis-consuming the first row's tree reply payload
// would desync the blob reply after it. The third row stays absent so the
// "missing" reply is parsed in the same batch.
func TestMaterializeRef_MultiRowBatchParsing(t *testing.T) {
	const twinContent = "twin = https://twin.example.com/registry\n"
	twin := synthetic.Twin("synthetic-twin")
	missing := synthetic.Twin("synthetic-missing")
	rows := []ecosystem.Strategy{synthetic.Row, twin, missing}
	bare, _ := testutil.BareRepoWithConfigs(t, "main", map[string]string{
		cfgPath + "/inner":    "anything",
		twin.InTreeConfigPath: twinContent,
	}, nil)

	dir, cleanup, err := MaterializeRefRows(bare, "main", rows)
	t.Cleanup(cleanup)
	if err != nil {
		t.Fatalf("MaterializeRefRows: unexpected error: %v", err)
	}

	mustNotExist(t, filepath.Join(dir, cfgPath))
	mustNotExist(t, filepath.Join(dir, missing.InTreeConfigPath))

	got, err := os.ReadFile(filepath.Join(dir, twin.InTreeConfigPath))
	if err != nil {
		t.Fatalf("read materialized twin config: %v", err)
	}
	if string(got) != twinContent {
		t.Errorf("materialized twin config = %q, want %q", got, twinContent)
	}
}

// A ref containing whitespace (ResolveRef accepts e.g. "HEAD@{1 day ago}")
// puts a space inside the header line too, so splitting the whole header on
// whitespace misreads a two-field "missing" reply as an unrecognized header.
func TestParseCatFileBatch_RefWithSpaceMissingHeader(t *testing.T) {
	out := []byte("HEAD@{1 day ago}:" + cfgPath + " missing\n")

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
	loopPath := "HEAD:" + cfgPath
	out := []byte("loop " + strconv.Itoa(len(loopPath)) + "\n" + loopPath + "\n" +
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
