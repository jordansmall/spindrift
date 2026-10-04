// Package homelayout lays out what the Driver finds in HOME (ADR 0058, issue
// #4296): its skills directory and the baked home-agent files.
//
// Both copies mirror `cp -r src/. dst/` without -p, and both must cope with
// read-only sources (bwrap ro-binds /agent from the Nix store), so every copy
// is followed by a u+w pass; without it the next copy over the same tree would
// fail (issue #3941).
package homelayout

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
)

// PopulateSkills copies each src into dst in order, so a later src wins a name
// collision (the operator's skill overrides a harness skill of the same name)
// while an earlier skill the later one didn't override survives. A src that is
// absent or not a directory is skipped: an absent operator mount is the common
// case; any other stat failure (say, a permission error) is returned.
// Everything under dst, dst included, ends owner-writable; that blanket chmod
// is safe because nothing is bind-mounted under dst.
func PopulateSkills(dst string, srcs ...string) error {
	if err := os.MkdirAll(dst, 0o755); err != nil {
		return fmt.Errorf("create %s: %w", dst, err)
	}
	for _, src := range srcs {
		if ok, err := isDir(src); err != nil {
			return err
		} else if !ok {
			continue
		}
		if err := copyTree(src, dst); err != nil {
			return err
		}
		err := filepath.WalkDir(dst, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			return makeUserWritable(p, d)
		})
		if err != nil {
			return fmt.Errorf("make %s writable: %w", dst, err)
		}
	}
	return nil
}

// PopulateHome copies the staged home-agent tree into home and makes every
// copied entry owner-writable. It is a no-op when staged is absent or not a
// directory (under OCI the image bakes /home/agent directly); any other stat
// failure is returned. A symlinked entry in the staged tree is never chmod'd,
// unlike the old shell's `chmod u+w`, which followed it into the read-only
// store.
//
// The directory sessionCacheDir ("" when the Driver has none) is never
// chmod'd: bwrap binds the live host directory at that path, so a chmod would
// mutate a directory outside the sandbox (issue #2845). A trailing slash on
// sessionCacheDir is ignored.
func PopulateHome(home, staged, sessionCacheDir string) error {
	if ok, err := isDir(staged); err != nil {
		return err
	} else if !ok {
		return nil
	}
	if err := copyTree(staged, home); err != nil {
		return err
	}
	skip := ""
	if sessionCacheDir != "" {
		skip = filepath.Clean(sessionCacheDir)
	}
	err := walkInto(staged, home, func(_, target string, d fs.DirEntry) error {
		if d.IsDir() && target == skip {
			return nil
		}
		return makeUserWritable(target, d)
	})
	if err != nil {
		return fmt.Errorf("make %s writable: %w", home, err)
	}
	return nil
}

// isDir reports whether path is an existing directory. Only a missing path is
// "no"; any other stat error is surfaced so a permission problem can't pass for
// an absent mount.
func isDir(path string) (bool, error) {
	info, err := os.Stat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("stat %s: %w", path, err)
	}
	return info.IsDir(), nil
}

// walkInto walks the symlink-resolved tree src, skipping its root, and calls fn
// with each entry's source path and its counterpart under dst.
func walkInto(src, dst string, fn func(p, target string, d fs.DirEntry) error) error {
	root, err := filepath.EvalSymlinks(src)
	if err != nil {
		return fmt.Errorf("resolve %s: %w", src, err)
	}
	return filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if p == root {
			return nil
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		return fn(p, filepath.Join(dst, rel), d)
	})
}

// makeUserWritable adds u+w to path unless the walked entry d is a symlink,
// which `chmod -R` leaves alone and which would otherwise change its target.
func makeUserWritable(path string, d fs.DirEntry) error {
	if d.Type()&fs.ModeSymlink != 0 {
		return nil
	}
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if info.Mode().Perm()&0o200 != 0 {
		return nil
	}
	if err := os.Chmod(path, info.Mode().Perm()|0o200); err != nil {
		return fmt.Errorf("chmod %s: %w", path, err)
	}
	return nil
}

// copyTree copies the contents of src into the existing directory dst the way
// `cp -r src/. dst/` does: an existing destination directory keeps its mode,
// a new one takes the source's, files are truncated in place and keep an
// existing destination's mode (a read-only one fails, as with cp), and
// symlinks are copied as symlinks.
func copyTree(src, dst string) error {
	type newDir struct {
		path string
		mode fs.FileMode
	}
	var created []newDir
	err := walkInto(src, dst, func(p, target string, d fs.DirEntry) error {
		info, err := d.Info()
		if err != nil {
			return err
		}
		switch {
		case d.IsDir():
			if ti, err := os.Lstat(target); err == nil {
				if !ti.IsDir() {
					return fmt.Errorf("copy %s: %s exists and is not a directory", p, target)
				}
				return nil
			}
			// Writable while children land; the source mode is restored below.
			if err := os.Mkdir(target, info.Mode().Perm()|0o700); err != nil {
				return fmt.Errorf("mkdir %s: %w", target, err)
			}
			created = append(created, newDir{target, info.Mode().Perm()})
		case d.Type()&fs.ModeSymlink != 0:
			link, err := os.Readlink(p)
			if err != nil {
				return err
			}
			if ti, err := os.Lstat(target); err == nil {
				if ti.IsDir() {
					return fmt.Errorf("copy %s: %s is a directory", p, target)
				}
				if err := os.Remove(target); err != nil {
					return fmt.Errorf("replace %s: %w", target, err)
				}
			}
			if err := os.Symlink(link, target); err != nil {
				return fmt.Errorf("symlink %s: %w", target, err)
			}
		case info.Mode().IsRegular():
			return copyFile(p, target, info.Mode().Perm())
		}
		return nil
	})
	for i := len(created) - 1; i >= 0; i-- {
		if cerr := os.Chmod(created[i].path, created[i].mode); cerr != nil && err == nil {
			err = fmt.Errorf("chmod %s: %w", created[i].path, cerr)
		}
	}
	return err
}

func copyFile(src, dst string, perm fs.FileMode) (err error) {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, perm)
	if err != nil {
		return fmt.Errorf("copy %s: %w", src, err)
	}
	defer func() {
		if cerr := out.Close(); cerr != nil && err == nil {
			err = fmt.Errorf("copy %s: %w", src, cerr)
		}
	}()
	if _, err := io.Copy(out, in); err != nil {
		return fmt.Errorf("copy %s: %w", src, err)
	}
	return nil
}
