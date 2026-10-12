package ecosystem

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"

	"spindrift.dev/launcher/internal/registryvocab"
)

// npmPackumentMatches accepts the packument path (one segment under base) and
// rejects anything deeper, most importantly a tarball path
// (".../-/name-1.0.0.tgz"), which must reach the Forwarder unrewritten. A
// scoped name arrives already percent-decoded, so "%40scope%2fname" on the
// wire is "/@scope/name" here: two segments, not one literal "%40".
func npmPackumentMatches(routeRelativePath, base string) bool {
	// JoinBase rather than a bare prefix check on base: base == "/" is
	// JoinBase's "no base segment" sentinel, and that handling stays in one
	// place.
	rel, ok := strings.CutPrefix(routeRelativePath, registryvocab.JoinBase(base, ""))
	if !ok || !strings.HasPrefix(rel, "/") {
		return false
	}

	segments := strings.Split(rel[1:], "/")
	switch len(segments) {
	case 1:
		// npm has no one-segment "@..." name: a scoped name is always two.
		return isPackageNameSegment(segments[0]) && !strings.HasPrefix(segments[0], "@")
	case 2:
		// The first segment must carry a scope name, not the "@" alone; a
		// trailing slash leaves an empty final segment, which
		// isPackageNameSegment rejects.
		return len(segments[0]) > 1 && strings.HasPrefix(segments[0], "@") && isPackageNameSegment(segments[1])
	default:
		// Three or more segments is a tarball path, never the packument.
		return false
	}
}

// isPackageNameSegment rules out the shapes a packument request never carries:
// the empty segment, npm's own "-" tarball-directory marker, and the dot
// segments, which name a directory rather than a package.
func isPackageNameSegment(segment string) bool {
	switch segment {
	case "", "-", ".", "..":
		return false
	}
	return true
}

// rewriteNpmPackument repoints every same-host dist.tarball URL at the
// Forwarder: pacote fetches the packument's embedded absolute URL verbatim
// instead of deriving it from the registry env-var binding, so without this
// the download leaves the proxy (issue #3401). A foreign-host tarball is a
// declined edit (empty To), not a failure of the whole rewrite.
func rewriteNpmPackument(body []byte, rc registryvocab.RewriteContext) registryvocab.RewriteResult {
	obj, ok := decodeOneJSONObject(body)
	if !ok {
		return registryvocab.RewriteResult{Body: body, Outcome: registryvocab.RewriteNone}
	}

	versions, ok := obj["versions"].(map[string]any)
	if !ok {
		return registryvocab.RewriteResult{Body: body, Outcome: registryvocab.RewriteNone}
	}

	// Map iteration order is randomized; sort so the edit list, and the
	// caller's log lines drawn from it, stay deterministic.
	names := make([]string, 0, len(versions))
	for name := range versions {
		names = append(names, name)
	}
	sort.Strings(names)

	var edits []registryvocab.RewriteEdit

	for _, name := range names {
		version, ok := versions[name].(map[string]any)
		if !ok {
			continue
		}
		dist, ok := version["dist"].(map[string]any)
		if !ok {
			continue
		}
		tarballStr, ok := dist["tarball"].(string)
		if !ok {
			continue
		}

		edit, ok := repointRegistryURL(tarballStr, rc)
		if !ok {
			// Not an absolute URL: npm never emits this shape, so there is
			// nothing worth logging a skip for.
			continue
		}

		edits = append(edits, edit)
		if edit.To == "" {
			// Foreign host (a CDN, a mirror): reported so the caller can log
			// the skip, but nothing in the body changes for it.
			continue
		}

		dist["tarball"] = edit.To
	}

	applied := false
	for _, edit := range edits {
		if edit.To != "" {
			applied = true
			break
		}
	}

	if !applied {
		if len(edits) == 0 {
			return registryvocab.RewriteResult{Body: body, Outcome: registryvocab.RewriteNone}
		}
		// Every edit was declined as foreign-host: still a reportable skip,
		// not RewriteNone, because there was something recognizable here.
		return registryvocab.RewriteResult{Body: body, Edits: edits, Outcome: registryvocab.RewriteSkippedForeignHost}
	}

	newBody, err := json.Marshal(obj)
	if err != nil {
		// Unreachable: obj came from a successful decode, so every value in
		// it is already representable as JSON.
		return registryvocab.RewriteResult{Body: body, Outcome: registryvocab.RewriteNone}
	}
	return registryvocab.RewriteResult{Body: newBody, Edits: edits, Outcome: registryvocab.RewriteApplied}
}

// npmStreamWindowBytes caps the bytes streamNpmPackument holds unwritten while
// it tokenizes. npm's registry truncates readme, the largest single token in a
// packument, far below this, so the window bounds memory without ever binding
// on a real packument.
const npmStreamWindowBytes = 1 << 20

// errNpmStreamWindow is the recorder's "window full" signal to the decoder.
var errNpmStreamWindow = errors.New("token exceeds the stream window")

// npmRecorder tees every byte the json.Decoder reads into raw, so the stream
// rewriter can emit the exact source bytes of what the decoder has tokenized.
// raw[0] sits at stream offset base; bytes before written are already in dst.
type npmRecorder struct {
	src     io.Reader
	raw     []byte
	base    int64
	written int64
	srcErr  error // a genuine (non-EOF) src read error
}

func (r *npmRecorder) Read(p []byte) (int, error) {
	if r.srcErr != nil {
		return 0, r.srcErr
	}
	// The decoder only reads once every complete token it holds is consumed,
	// so what is left unwritten is the one token still in flight; dropping the
	// written prefix here is cheap and keeps the per-token path copy-free.
	if drop := r.written - r.base; drop > 0 {
		r.raw = r.raw[:copy(r.raw, r.raw[drop:])]
		r.base = r.written
	}
	room := npmStreamWindowBytes - len(r.raw)
	if room <= 0 {
		return 0, errNpmStreamWindow
	}
	if len(p) > room {
		p = p[:room]
	}
	n, err := r.src.Read(p)
	r.raw = append(r.raw, p[:n]...)
	if err != nil && err != io.EOF {
		r.srcErr = err
	}
	return n, err
}

// npmFrame is one open container on the path to a packument tarball. Only the
// four outermost containers can matter, so deeper ones are just counted.
type npmFrame struct {
	isObj     bool
	expectKey bool
	key       string
}

// npmTarballDepth is the container depth of a dist object: the packument,
// versions, one version, dist.
const npmTarballDepth = 4

// streamNpmPackument is rewriteNpmPackument for a document too large to
// buffer: it repoints every same-host versions.*.dist.tarball string at the
// Forwarder as it tokenizes, holding at most npmStreamWindowBytes, and passes
// every other byte through verbatim. Edits reach onEdit in document order, each
// before the bytes it edited reach dst. A body it cannot keep interpreting
// (malformed JSON, a token over the window) is relayed byte-identical from
// that point and reported as registryvocab.ErrRewriteAbandoned.
func streamNpmPackument(dst io.Writer, src io.Reader, rc registryvocab.RewriteContext, onEdit func(registryvocab.RewriteEdit)) error {
	rec := &npmRecorder{src: src}
	out := bufio.NewWriterSize(dst, 32<<10)
	dec := json.NewDecoder(rec)
	dec.UseNumber()

	var stack []npmFrame
	extra := 0 // containers open beneath the npmTarballDepth tracked frames

	for {
		tok, err := dec.Token()
		if err != nil {
			return finishNpmStream(out, rec, src, err)
		}
		off := dec.InputOffset()

		// done: the token completed a value of the innermost tracked
		// container, so an object there expects its next key.
		done := true
		target := ""
		isTarget := false

		switch v := tok.(type) {
		case json.Delim:
			switch v {
			case '{', '[':
				done = false
				if extra > 0 || len(stack) == npmTarballDepth {
					extra++
				} else {
					stack = append(stack, npmFrame{isObj: v == '{', expectKey: v == '{'})
				}
			default:
				if extra > 0 {
					extra--
				} else {
					stack = stack[:len(stack)-1]
				}
			}
		case string:
			if extra == 0 && len(stack) > 0 {
				top := &stack[len(stack)-1]
				if top.isObj && top.expectKey {
					top.key = v
					top.expectKey = false
					done = false
				} else if isNpmTarballPath(stack) {
					target, isTarget = v, true
				}
			}
		}

		if isTarget {
			err = writeNpmTarball(out, rec, off, target, rc, onEdit)
		} else {
			err = flushNpmTo(out, rec, off)
		}
		if err != nil {
			return err
		}

		if done && extra == 0 && len(stack) > 0 && stack[len(stack)-1].isObj {
			stack[len(stack)-1].expectKey = true
		}
	}
}

// isNpmTarballPath reports whether the string value just read sits at
// versions.<any>.dist.tarball, every ancestor an object.
func isNpmTarballPath(stack []npmFrame) bool {
	if len(stack) != npmTarballDepth {
		return false
	}
	for _, f := range stack {
		if !f.isObj {
			return false
		}
	}
	return stack[0].key == "versions" && stack[2].key == "dist" && stack[3].key == "tarball"
}

// flushNpmTo writes the not-yet-written source bytes up to stream offset off.
func flushNpmTo(out *bufio.Writer, rec *npmRecorder, off int64) error {
	_, err := out.Write(rec.raw[rec.written-rec.base : off-rec.base])
	rec.written = off
	return err
}

// writeNpmTarball handles a tarball string value ending at stream offset off.
// The pending segment is whatever separates it from the previous token (the
// swallowed ':' and whitespace) followed by the quoted string, so the value
// starts at the segment's first '"'.
func writeNpmTarball(out *bufio.Writer, rec *npmRecorder, off int64, value string, rc registryvocab.RewriteContext, onEdit func(registryvocab.RewriteEdit)) error {
	seg := rec.raw[rec.written-rec.base : off-rec.base]
	edit, ok := repointRegistryURL(value, rc)
	if !ok {
		return flushNpmTo(out, rec, off)
	}
	if onEdit != nil {
		onEdit(edit)
	}
	quote := bytes.IndexByte(seg, '"')
	if edit.To == "" || quote < 0 {
		return flushNpmTo(out, rec, off)
	}
	encoded, err := json.Marshal(edit.To)
	if err != nil {
		// Unreachable: a string always marshals.
		return flushNpmTo(out, rec, off)
	}
	if _, err := out.Write(seg[:quote]); err != nil {
		return err
	}
	rec.written = off
	_, err = out.Write(encoded)
	return err
}

// finishNpmStream ends the stream on a Token error. EOF is the normal end; a
// src read failure is returned as itself; anything else (a syntax error, a
// token over the window) abandons interpretation: the unwritten bytes and the
// rest of src go through verbatim before the abandonment is reported.
func finishNpmStream(out *bufio.Writer, rec *npmRecorder, src io.Reader, tokErr error) error {
	if rec.srcErr != nil {
		return rec.srcErr
	}
	_, werr := out.Write(rec.raw[rec.written-rec.base:])
	if tokErr == io.EOF {
		if werr != nil {
			return werr
		}
		return out.Flush()
	}
	if werr == nil {
		_, werr = io.Copy(out, src)
	}
	if werr == nil {
		werr = out.Flush()
	}
	if werr != nil {
		return werr
	}
	return fmt.Errorf("%w: %v", registryvocab.ErrRewriteAbandoned, tokErr)
}
