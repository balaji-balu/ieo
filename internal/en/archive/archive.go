// Package archive validates and extracts Margo Compose Archives on the host (SPEC §5.2, §9.2). It
// owns the archive rules and the safety of extraction; pulling the archive and running what it
// holds are other packages' work.
package archive

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// ComposeFile is the only name accepted for an archive's Compose file (SPEC §5.2).
const ComposeFile = "compose.yaml"

// StagingInfix marks the directories Extract works in: the component's directory name, then
// StagingInfix, then a random suffix, beside the component's directory. One is left behind only
// if the EN dies while extracting; the EN removes those when it starts (ADR 0018).
const StagingInfix = ".extracting-"

// maxLinkHops is how many symbolic links one link may lead through before it counts as never
// ending. Linux resolves at most 40.
const maxLinkHops = 40

// ErrInvalid is returned, wrapped with the rule broken, for an archive that breaks SPEC §5.2 or
// is over one of its limits. The EN reports it as IEO-ARCHIVE-INVALID (SPEC §8.9).
var ErrInvalid = errors.New("invalid compose archive")

// Limits bound what one archive may hold (SPEC §5.2, §6.3). A zero limit admits nothing.
type Limits struct {
	// MaxEntries is the most entries the archive may hold (en.archive.max_entries).
	MaxEntries int
	// MaxExtractedBytes is the most bytes its regular files may add up to once decompressed
	// (en.archive.max_extracted_bytes).
	MaxExtractedBytes int64
}

// Extract validates the gzip tar read from r against SPEC §5.2 and extracts it to dir, the
// component's directory (SPEC §9.1). It returns the name of the archive's top-level directory:
// the Compose file is then <dir>/<top>/compose.yaml.
//
// dir is replaced: what it held before is deleted first, and its parent is created if missing.
// When Extract fails, dir does not exist and nothing extracted is left behind. An archive that
// breaks a rule or a limit fails with an error wrapping ErrInvalid; no file is written past
// l.MaxExtractedBytes. Other errors are failures to read r or to write to disk, or ctx ending.
//
// Extraction never writes outside dir and never follows a link (SPEC §9.2). Files and directories
// keep their permission bits, without setuid, setgid and sticky; owners and times are not kept.
func Extract(ctx context.Context, r io.Reader, dir string, l Limits) (top string, err error) {
	if err := os.RemoveAll(dir); err != nil {
		return "", fmt.Errorf("extract compose archive: clear %s: %w", dir, err)
	}
	parent := filepath.Dir(dir)
	if err := os.MkdirAll(parent, 0o700); err != nil {
		return "", fmt.Errorf("extract compose archive: %w", err)
	}
	// The archive is extracted beside dir and renamed once it is known to be valid, so dir only
	// ever holds a whole, valid archive (ADR 0018).
	staging, err := os.MkdirTemp(parent, filepath.Base(dir)+StagingInfix+"*")
	if err != nil {
		return "", fmt.Errorf("extract compose archive: %w", err)
	}
	top, err = extractTo(ctx, r, staging, l)
	if err == nil {
		err = os.Rename(staging, dir)
	}
	if err != nil {
		// SPEC §5.2: delete everything extracted before reporting the failure.
		if rerr := os.RemoveAll(staging); rerr != nil {
			err = fmt.Errorf("%w (and removing %s: %w)", err, staging, rerr)
		}
		return "", fmt.Errorf("extract compose archive: %w", err)
	}
	return top, nil
}

func invalid(format string, a ...any) error {
	return fmt.Errorf("%w: "+format, append([]any{ErrInvalid}, a...)...)
}

// source reads the archive for one Extract. It stops when ctx ends, and remembers the error that
// ended the input, so a failure to read is not mistaken for a broken archive.
type source struct {
	ctx context.Context
	r   io.Reader
	err error
}

func (s *source) Read(p []byte) (int, error) {
	if err := s.ctx.Err(); err != nil {
		s.err = err
		return 0, err
	}
	n, err := s.r.Read(p)
	if err != nil && !errors.Is(err, io.EOF) {
		s.err = err
	}
	return n, err
}

// readFailure turns an error from the gzip or tar reader into the input's own error if the input
// failed, and into an invalid archive otherwise: the bytes arrived and are not a gzip tar.
func (s *source) readFailure(err error) error {
	if s.err != nil {
		return fmt.Errorf("read archive: %w", s.err)
	}
	return invalid("%v", err)
}

// extractor extracts one archive into root. Paths are the archive's own, cleaned: relative, with
// `/` between segments.
type extractor struct {
	root    *os.Root
	limits  Limits
	entries int
	written int64  // bytes of regular files so far
	top     string // the top-level directory, once an entry has named it
	// Symbolic links are checked and created after every other entry, so nothing is written
	// through one (SPEC §9.2). links maps a link's path to its target; order keeps archive order.
	links map[string]string
	order []string
}

// extractTo extracts the archive into the existing, empty directory staging and returns its
// top-level directory. Every write goes through an os.Root on staging, which refuses any path
// that would leave it (SPEC §9.2).
func extractTo(ctx context.Context, r io.Reader, staging string, l Limits) (top string, err error) {
	root, err := os.OpenRoot(staging)
	if err != nil {
		return "", err
	}
	defer func() {
		// Closed before the caller renames or removes staging: Windows refuses both while it is open.
		if cerr := root.Close(); err == nil {
			err = cerr
		}
	}()
	src := &source{ctx: ctx, r: r}
	gz, err := gzip.NewReader(src)
	if err != nil {
		return "", src.readFailure(err)
	}
	x := &extractor{root: root, limits: l, links: map[string]string{}}
	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return "", src.readFailure(err)
		}
		if err := x.entry(h, tr); err != nil {
			var berr *bodyError
			if errors.As(err, &berr) {
				return "", src.readFailure(berr.err)
			}
			return "", err
		}
	}
	if err := x.finish(); err != nil {
		return "", err
	}
	return x.top, nil
}

// entry checks one archive entry against SPEC §5.2 and writes it.
func (x *extractor) entry(h *tar.Header, body io.Reader) error {
	x.entries++
	if x.entries > x.limits.MaxEntries {
		return invalid("more than %d entries", x.limits.MaxEntries)
	}
	if h.Typeflag == tar.TypeXGlobalHeader {
		return nil // `git archive` writes one; it describes the archive and holds no file
	}
	name, err := entryPath(h.Name)
	if err != nil {
		return err
	}
	if name == "" { // the archive's root, written by `tar -C dir .`
		if h.Typeflag != tar.TypeDir {
			return invalid("entry %q is not a directory", h.Name)
		}
		return nil
	}
	first, _, nested := strings.Cut(name, "/")
	switch {
	case x.top == "":
		x.top = first
	case first != x.top:
		return invalid("more than one top-level entry: %q and %q", x.top, first)
	}
	if !nested && h.Typeflag != tar.TypeDir {
		return invalid("top-level entry %q is not a directory", name)
	}
	if err := x.parents(name, true); err != nil {
		return err
	}
	switch h.Typeflag {
	case tar.TypeDir:
		return x.dir(name, h)
	case tar.TypeReg:
		return x.file(name, h, body)
	case tar.TypeSymlink:
		return x.symlink(name, h)
	case tar.TypeLink:
		return x.hardlink(name, h)
	default:
		return invalid("entry %q is not a file, directory or link (tar type %q)", name, h.Typeflag)
	}
}

// entryPath returns an entry's path, cleaned, or "" for the archive's root. SPEC §5.2 and §9.2: no
// absolute path and no `..` segment, even one that would stay inside.
func entryPath(raw string) (string, error) {
	if raw == "" {
		return "", invalid("entry with no name")
	}
	if strings.HasPrefix(raw, "/") {
		return "", invalid("entry %q has an absolute path", raw)
	}
	for seg := range strings.SplitSeq(raw, "/") {
		if seg == ".." {
			return "", invalid("entry %q has a .. segment", raw)
		}
	}
	clean := path.Clean(raw)
	if clean == "." {
		return "", nil
	}
	// On Windows this also refuses drive letters, reserved names and `\`, which the file system
	// would read differently from the checks here.
	if !filepath.IsLocal(filepath.FromSlash(clean)) || filepath.ToSlash(clean) != clean {
		return "", invalid("entry %q is not a path inside the archive on this system", raw)
	}
	return clean, nil
}

// parents makes sure every directory above name is a directory. With create, it makes the ones no
// entry holds yet, since archives may leave out the entries for directories; without, a missing
// one is an error.
func (x *extractor) parents(name string, create bool) error {
	for i, c := range name {
		if c != '/' {
			continue
		}
		p := name[:i]
		fi, held, err := x.lstat(p)
		switch {
		case err != nil:
			return err
		case !held && !create:
			return invalid("no earlier entry holds %q", p)
		case !held:
			if err := x.mkdir(p, 0o755); err != nil {
				return err
			}
		case fi == nil:
			return invalid("entry %q passes through the symbolic link %q", name, p)
		case !fi.IsDir():
			return invalid("entry %q passes through %q, which is not a directory", name, p)
		}
	}
	return nil
}

// lstat reports whether an earlier entry holds name. fi is nil for a symbolic link, which is not
// on disk yet.
func (x *extractor) lstat(name string) (fi fs.FileInfo, held bool, err error) {
	if _, ok := x.links[name]; ok {
		return nil, true, nil
	}
	fi, err = x.root.Lstat(filepath.FromSlash(name))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, false, nil
	}
	return fi, err == nil, err
}

// free fails if an earlier entry holds name (SPEC §5.2).
func (x *extractor) free(name string) error {
	_, held, err := x.lstat(name)
	if err == nil && held {
		err = invalid("an earlier entry holds the path %q", name)
	}
	return err
}

// permissions returns an entry's permission bits. Leaving out every other bit is what clears
// setuid, setgid and sticky (SPEC §9.2).
func permissions(h *tar.Header) fs.FileMode {
	return fs.FileMode(h.Mode & int64(fs.ModePerm))
}

func (x *extractor) dir(name string, h *tar.Header) error {
	fi, held, err := x.lstat(name)
	if err != nil {
		return err
	}
	if held && (fi == nil || !fi.IsDir()) {
		return invalid("an earlier entry holds the path %q", name)
	}
	// The EN must be able to write what the directory holds, and to delete it on Remove.
	mode := permissions(h) | 0o700
	if held {
		return x.root.Chmod(filepath.FromSlash(name), mode)
	}
	return x.mkdir(name, mode)
}

// mkdir creates the directory name with exactly mode, whatever the process umask is.
func (x *extractor) mkdir(name string, mode fs.FileMode) error {
	osName := filepath.FromSlash(name)
	if err := x.root.Mkdir(osName, 0o700); err != nil {
		return err
	}
	return x.root.Chmod(osName, mode)
}

func (x *extractor) file(name string, h *tar.Header, body io.Reader) error {
	if err := x.free(name); err != nil {
		return err
	}
	// SPEC §5.2: counted from the header, before a byte of the file is read or written.
	if h.Size > x.limits.MaxExtractedBytes-x.written {
		return invalid("files add up to more than %d bytes at %q", x.limits.MaxExtractedBytes, name)
	}
	x.written += h.Size
	osName := filepath.FromSlash(name)
	f, err := x.root.OpenFile(osName, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	_, err = io.Copy(f, bodyReader{body})
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return fmt.Errorf("write %q: %w", name, err)
	}
	return x.root.Chmod(osName, permissions(h))
}

// bodyReader marks errors from reading a file's content as bodyError, to tell them from errors
// writing it.
type bodyReader struct{ r io.Reader }

func (b bodyReader) Read(p []byte) (int, error) {
	n, err := b.r.Read(p)
	if err != nil && !errors.Is(err, io.EOF) {
		err = &bodyError{err}
	}
	return n, err
}

type bodyError struct{ err error }

func (e *bodyError) Error() string { return e.err.Error() }
func (e *bodyError) Unwrap() error { return e.err }

func (x *extractor) symlink(name string, h *tar.Header) error {
	if err := x.free(name); err != nil {
		return err
	}
	target := filepath.ToSlash(h.Linkname)
	if target == "" || strings.HasPrefix(target, "/") || filepath.VolumeName(h.Linkname) != "" {
		return invalid("symbolic link %q has the absolute or empty target %q", name, h.Linkname)
	}
	x.links[name] = target
	x.order = append(x.order, name)
	return nil
}

func (x *extractor) hardlink(name string, h *tar.Header) error {
	if err := x.free(name); err != nil {
		return err
	}
	target, err := entryPath(h.Linkname)
	if err != nil {
		return fmt.Errorf("hard link %q: %w", name, err)
	}
	// A target outside the top-level directory is not on disk: nothing is extracted there.
	if err := x.parents(target, false); err != nil {
		return fmt.Errorf("hard link %q to %q: %w", name, h.Linkname, err)
	}
	if target == "" {
		return invalid("hard link %q names the archive's root", name)
	}
	fi, held, err := x.lstat(target)
	if err == nil && (!held || fi == nil || !fi.Mode().IsRegular()) {
		return invalid("hard link %q names %q, which is not a regular file of an earlier entry", name, h.Linkname)
	}
	if err != nil {
		return err
	}
	return x.root.Link(filepath.FromSlash(target), filepath.FromSlash(name))
}

// finish checks the rules that need the whole archive, then creates the symbolic links.
func (x *extractor) finish() error {
	if x.top == "" {
		return invalid("no top-level directory")
	}
	compose := x.top + "/" + ComposeFile
	fi, held, err := x.lstat(compose)
	switch {
	case err != nil:
		return err
	case !held:
		return invalid("no %s in the top-level directory %q", ComposeFile, x.top)
	case fi == nil || !fi.Mode().IsRegular():
		return invalid("%s is not a regular file", compose)
	}
	for _, name := range x.order {
		if !x.staysInside(name) {
			return invalid("symbolic link %q -> %q leaves the top-level directory or never ends", name, x.links[name])
		}
	}
	for _, name := range x.order {
		if err := x.root.Symlink(filepath.FromSlash(x.links[name]), filepath.FromSlash(name)); err != nil {
			return err
		}
	}
	return nil
}

// staysInside reports whether the symbolic link at name, followed to its end through the other
// links of the archive, never leaves the top-level directory (SPEC §5.2). The directories above a
// link are real ones (see parents), so the walk starts from a resolved path.
func (x *extractor) staysInside(name string) bool {
	at := strings.Split(path.Dir(name), "/") // resolved so far; at[0] is the top-level directory
	todo := strings.Split(x.links[name], "/")
	for hops := 0; len(todo) > 0; {
		seg := todo[0]
		todo = todo[1:]
		switch seg {
		case "", ".":
			continue
		case "..":
			if len(at) == 1 {
				return false
			}
			at = at[:len(at)-1]
			continue
		}
		target, isLink := x.links[strings.Join(at, "/")+"/"+seg]
		if !isLink {
			at = append(at, seg)
			continue
		}
		if hops++; hops > maxLinkHops {
			return false
		}
		todo = append(strings.Split(target, "/"), todo...)
	}
	return true
}
