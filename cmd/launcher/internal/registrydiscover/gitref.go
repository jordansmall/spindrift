package registrydiscover

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"spindrift.dev/launcher/internal/ecosystem"
)

// MaterializeRef writes ref's committed ecosystem config files into a throwaway
// directory the caller reads as a checkout. Reading committed content rather
// than a working tree lets one helper serve both the bare Accumulation repo
// (ADR 0033) and a non-bare one, whose uncommitted state must never leak in.
// cleanup is always non-nil and a no-op on the error return, so defer it at once.
//
// A symlink inside ref's tree, at the config path or at a parent directory,
// resolves to its target's content, as a checkout's os.ReadFile would. Two
// shapes deliberately part from a checkout and skip like a missing file: a
// directory at the config path, which extractRows hard-errors on, since a
// snapshot has no failing read to report, only nothing to declare; and an
// absolute or out-of-tree symlink, since a snapshot must never read the host
// file it names. A dangling link skips too.
func MaterializeRef(repoDir, ref string) (dir string, cleanup func(), err error) {
	noop := func() {}

	if err := ResolveRef(repoDir, ref); err != nil {
		return "", noop, err
	}

	// Walking ecosystem.Table, the same table Extract walks, keeps the
	// materialized file set from drifting from the scanned one: a config path
	// added to Table is picked up here with no second list to maintain.
	var paths []string
	var stdin bytes.Buffer
	for _, row := range ecosystem.Table {
		if row.InTreeConfigPath == "" {
			continue
		}
		paths = append(paths, row.InTreeConfigPath)
		fmt.Fprintf(&stdin, "%s:%s\n", ref, row.InTreeConfigPath)
	}

	// --follow-symlinks is what resolves in-tree links, and what reports an
	// out-of-tree one as a "symlink" reply rather than following it.
	cmd := exec.Command("git", "-C", repoDir, "cat-file", "--batch", "--follow-symlinks")
	cmd.Stdin = &stdin
	out, cerr := cmd.Output()
	if cerr != nil {
		return "", noop, fmt.Errorf("read config files at ref %q in repo %q: %w", ref, repoDir, cerr)
	}
	blobs, perr := parseCatFileBatch(out, len(paths))
	if perr != nil {
		return "", noop, fmt.Errorf("read config files at ref %q in repo %q: %w", ref, repoDir, perr)
	}

	tmp, err := os.MkdirTemp("", "registrydiscover-gitref-")
	if err != nil {
		return "", noop, fmt.Errorf("create snapshot dir for ref %q in repo %q: %w", ref, repoDir, err)
	}
	cleanup = func() { os.RemoveAll(tmp) }

	for i, path := range paths {
		if blobs[i] == nil {
			continue
		}
		dest := filepath.Join(tmp, path)
		if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
			cleanup()
			return "", noop, fmt.Errorf("materialize snapshot dir for %q: %w", path, err)
		}
		if err := os.WriteFile(dest, blobs[i], 0o644); err != nil {
			cleanup()
			return "", noop, fmt.Errorf("materialize snapshot file %q: %w", path, err)
		}
	}

	return tmp, cleanup, nil
}

// parseCatFileBatch splits n `git cat-file --batch --follow-symlinks` replies,
// returning each blob's content and nil for every other reply.
func parseCatFileBatch(out []byte, n int) ([][]byte, error) {
	r := bufio.NewReader(bytes.NewReader(out))
	blobs := make([][]byte, n)
	for i := range blobs {
		header, err := r.ReadString('\n')
		if err != nil {
			return nil, fmt.Errorf("cat-file reply %d: %w", i, err)
		}
		// "<oid> <type> <size>" precedes an object; "<kind> <size>" precedes
		// the path a dangling, loop, notdir, or out-of-tree symlink reply
		// names; "<object> missing" and "<object> ambiguous" carry nothing.
		// <object> is the ref:path string given on stdin, which can itself
		// contain whitespace (e.g. a ref like "HEAD@{1 day ago}"), so the
		// missing/ambiguous check matches the header's fixed suffix rather
		// than splitting the whole line into fields.
		if strings.HasSuffix(header, " missing\n") || strings.HasSuffix(header, " ambiguous\n") {
			continue
		}
		fields := strings.Fields(header)
		if len(fields) != 2 && len(fields) != 3 {
			return nil, fmt.Errorf("cat-file reply %d: unrecognized header %q", i, header)
		}
		size, err := strconv.Atoi(fields[len(fields)-1])
		if err != nil {
			return nil, fmt.Errorf("cat-file reply %d: unrecognized header %q", i, header)
		}
		// +1 for the LF git appends after every payload.
		payload := make([]byte, size+1)
		if _, err := io.ReadFull(r, payload); err != nil {
			return nil, fmt.Errorf("cat-file reply %d: %w", i, err)
		}
		if len(fields) == 3 && fields[1] == "blob" {
			blobs[i] = payload[:size]
		}
	}
	return blobs, nil
}

// ResolveRef confirms ref resolves to a tree in repoDir without materializing
// anything, so a caller can skip the temp-dir cost of MaterializeRef. Checking
// here rather than leaning on that function's batched read reports a broken
// repoDir or a missing ref as itself, not as every config file being absent;
// fixing the repo or ref and committing a config file are different responses.
func ResolveRef(repoDir, ref string) error {
	// `git -C ""` is a documented no-op, so an empty repoDir would silently
	// resolve ref against whatever repo the process cwd sits in.
	if repoDir == "" {
		return fmt.Errorf("resolve ref %q: no repo dir given", ref)
	}
	if out, verr := exec.Command("git", "-C", repoDir, "rev-parse", "--verify", ref+"^{tree}").CombinedOutput(); verr != nil {
		return fmt.Errorf("resolve ref %q in repo %q: %w: %s", ref, repoDir, verr, out)
	}
	return nil
}

// UncoveredHostsFromGitRef is UncoveredHosts for a ref inside a git repo rather
// than a checkout on disk, so the result reflects what ref has committed and
// nothing an uncommitted or divergent working tree might contribute.
func UncoveredHostsFromGitRef(repoDir, ref string, covered []string) ([]string, error) {
	dir, cleanup, err := MaterializeRef(repoDir, ref)
	defer cleanup()
	if err != nil {
		return nil, err
	}
	return UncoveredHosts(dir, covered)
}
