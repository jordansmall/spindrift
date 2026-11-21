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
// directory read as a checkout. Committed content serves both the bare
// Accumulation repo (ADR 0033) and a non-bare one, whose uncommitted state must
// never leak in. cleanup is always non-nil and a no-op on the error return,
// so defer it at once.
//
// An in-tree symlink resolves to its target, as in a checkout. These skip like
// a missing file: a directory at the config path (Extract hard-errors on it in
// a checkout, but a snapshot has no failing read, only nothing to declare), an
// absolute or out-of-tree symlink (a snapshot must never read the host file it
// names), and a dangling link.
func MaterializeRef(repoDir, ref string, rows []ecosystem.Strategy) (dir string, cleanup func(), err error) {
	noop := func() {}

	if err := ResolveRef(repoDir, ref); err != nil {
		return "", noop, err
	}

	// Walking the same Strategies Extract walks keeps the file sets in step.
	type candidate struct {
		path string
		row  int
	}
	var cands []candidate
	var stdin bytes.Buffer
	for i, row := range rows {
		for _, path := range row.ConfigPaths() {
			cands = append(cands, candidate{path, i})
			fmt.Fprintf(&stdin, "%s:%s\n", ref, path)
		}
	}

	// --follow-symlinks is what resolves in-tree links, and what reports an
	// out-of-tree one as a "symlink" reply rather than following it.
	cmd := exec.Command("git", "-C", repoDir, "cat-file", "--batch", "--follow-symlinks")
	cmd.Stdin = &stdin
	out, cerr := cmd.Output()
	if cerr != nil {
		return "", noop, fmt.Errorf("read config files at ref %q in repo %q: %w", ref, repoDir, cerr)
	}
	blobs, perr := parseCatFileBatch(out, len(cands))
	if perr != nil {
		return "", noop, fmt.Errorf("read config files at ref %q in repo %q: %w", ref, repoDir, perr)
	}

	tmp, err := os.MkdirTemp("", "registrydiscover-gitref-")
	if err != nil {
		return "", noop, fmt.Errorf("create snapshot dir for ref %q in repo %q: %w", ref, repoDir, err)
	}
	cleanup = func() { os.RemoveAll(tmp) }

	// Only a row's first present spelling is written: on a case-insensitive
	// filesystem a second spelling would clobber the one Extract must read.
	done := make(map[int]bool)
	for i, c := range cands {
		if blobs[i] == nil || done[c.row] {
			continue
		}
		done[c.row] = true
		dest := filepath.Join(tmp, c.path)
		if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
			cleanup()
			return "", noop, fmt.Errorf("materialize snapshot dir for %q: %w", c.path, err)
		}
		if err := os.WriteFile(dest, blobs[i], 0o644); err != nil {
			cleanup()
			return "", noop, fmt.Errorf("materialize snapshot file %q: %w", c.path, err)
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
		// a dangling/loop/notdir/out-of-tree symlink's path; "<object>
		// missing|ambiguous" carries nothing. <object> is the stdin ref:path,
		// which can hold whitespace ("HEAD@{1 day ago}"), so match the suffix.
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
// anything. It reports a broken repoDir or missing ref as itself, not as
// every config file being absent.
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

// UncoveredHostsFromGitRef is UncoveredHosts for a ref inside a git repo
// rather than a checkout on disk, so the result reflects what ref has committed
// and nothing an uncommitted or divergent working tree might contribute.
func UncoveredHostsFromGitRef(repoDir, ref string, covered []string, rows []ecosystem.Strategy) ([]string, error) {
	dir, cleanup, err := MaterializeRef(repoDir, ref, rows)
	defer cleanup()
	if err != nil {
		return nil, err
	}
	return UncoveredHosts(dir, covered, rows)
}
