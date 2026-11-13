package payload

import (
	"cmp"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"runtime"
	"slices"
	"strconv"
	"strings"
)

// extractor writes verified files into a staging folder inside the output folder, through os.Root,
// and places them on commit.
//
// Entries are visited in tree order (paths compared with '/' lowest), so that each folder on the way
// to an entry is visited once, whatever the depth: the pre-check and the commit cost one Lstat or
// Mkdir per distinct folder, plus one placement per file, and no step repeats the walk of an ancestor.
type extractor struct {
	root      *os.Root
	stage     string
	entries   []Entry
	order     []int // entry indices in tree order
	overwrite bool
	undo      []func() error  // how to reverse each change commit made, in order
	placed    map[fileID]bool // files this transfer placed, kept with overwrite
	created   []Entry         // folder entries commit created, in tree order
	committed bool
}

// stagePrefix starts the name of the staging folder; a random suffix follows. A manifest cannot
// predict the suffix, so an entry of the same name is left to chance (2^-64), not checked.
const stagePrefix = ".qrvt-staging-"

// Test hooks: testHookPlace runs before each file is placed; linkFile places a staged file.
var (
	testHookPlace   func(root *os.Root, path string) error
	defaultLinkFile = func(r *os.Root, oldname, newname string) error { return r.Link(oldname, newname) }
	linkFile        = defaultLinkFile
)

// newExtractor opens the output folder, checks that no entry would replace something it may not, and
// creates the staging folder.
func newExtractor(dir string, entries []Entry, overwrite bool) (*extractor, error) {
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, err
	}
	x := &extractor{root: root, entries: entries, overwrite: overwrite, placed: map[fileID]bool{}}
	x.order = make([]int, len(entries))
	for i := range x.order {
		x.order[i] = i
	}
	slices.SortFunc(x.order, func(a, b int) int { return cmpTree(entries[a].Path, entries[b].Path) })
	var r [8]byte
	_, _ = rand.Read(r[:]) // never fails
	x.stage = stagePrefix + hex.EncodeToString(r[:])
	err = x.walk(x.check)
	if err == nil {
		err = root.Mkdir(x.stage, 0o700)
	}
	if err != nil {
		_ = root.Close()
		return nil, err
	}
	return x, nil
}

// cmpTree compares paths byte by byte with '/' below every other byte, so that a folder's
// descendants directly follow it.
func cmpTree(a, b string) int {
	n := commonPrefix(a, b)
	switch {
	case n == len(a) || n == len(b):
		return cmp.Compare(len(a), len(b))
	case a[n] == '/':
		return -1
	case b[n] == '/':
		return 1
	}
	return cmp.Compare(a[n], b[n])
}

func commonPrefix(a, b string) int {
	n := min(len(a), len(b))
	for i := range n {
		if a[i] != b[i] {
			return i
		}
	}
	return n
}

// walk visits, in tree order, each folder on the way to an entry once and then the entry itself.
// visit receives the path, the entry's index (-1 for a folder that is not an entry), and whether the
// parent is known to be an existing folder; it returns whether the path now is one.
func (x *extractor) walk(visit func(p string, i int, parent bool) (bool, error)) error {
	type level struct {
		end int // the component ends at this byte of the path
		ok  bool
	}
	var stack []level
	prev := ""
	for _, i := range x.order {
		p := x.entries[i].Path
		c, k := commonPrefix(p, prev), 0
		for k < len(stack) && stack[k].end <= c && stack[k].end < len(p) && p[stack[k].end] == '/' {
			k++
		}
		stack = stack[:k]
		start := 0
		if k > 0 {
			start = stack[k-1].end + 1
		}
		for start <= len(p) {
			end, j := len(p), -1
			if s := strings.IndexByte(p[start:], '/'); s >= 0 {
				end = start + s
			} else {
				j = i
			}
			parent := len(stack) == 0 || stack[len(stack)-1].ok
			ok, err := visit(p[:end], j, parent)
			if err != nil {
				return err
			}
			stack = append(stack, level{end, ok})
			start = end + 1
		}
		prev = p
	}
	return nil
}

// check refuses a path that exists in a form it may not replace. Below a missing folder nothing is
// looked up.
func (x *extractor) check(p string, i int, parent bool) (bool, error) {
	if !parent {
		return false, nil
	}
	st, err := x.root.Lstat(p)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return false, nil
	case err != nil:
		return false, &ExistsError{p, fmt.Sprintf("cannot be created: %v", err)}
	case i < 0 && !st.IsDir():
		return false, &ExistsError{p, "exists and is not a folder"}
	case i < 0:
		return true, nil
	}
	e := x.entries[i]
	switch {
	case e.Dir && st.IsDir():
		return true, nil
	case e.Dir:
		return false, &ExistsError{e.Path, "exists and is not a folder"}
	case !st.Mode().IsRegular():
		return false, &ExistsError{e.Path, "exists and is not a regular file"}
	case !x.overwrite:
		return false, &ExistsError{e.Path, "already exists (allow overwriting to replace it)"}
	}
	return false, nil
}

// staged names the staging file of entry i; the staging folder is flat.
func (x *extractor) staged(i int) string { return x.stage + "/" + strconv.Itoa(i) }

// write stores entry i in the staging folder, leaving holes for runs of zeros, and checks its hash.
func (x *extractor) write(i int, e Entry, r io.Reader, pw *progressWriter) error {
	f, err := x.root.OpenFile(x.staged(i), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	h := sha256.New()
	// The tar reader yields exactly e.Size bytes, or fails.
	if _, err := io.CopyBuffer(io.MultiWriter(sparseWriter{f}, h, pw), r, make([]byte, 64<<10)); err != nil {
		return archiveErr(err)
	}
	if [32]byte(h.Sum(nil)) != e.SHA256 {
		return &UnsafeArchiveError{e.Path, "SHA-256 differs from the manifest"}
	}
	return errors.Join(f.Truncate(e.Size), f.Close())
}

// sparseWriter seeks over blocks of zeros instead of writing them; the caller truncates the file to
// its length at the end.
type sparseWriter struct{ f *os.File }

func (s sparseWriter) Write(b []byte) (int, error) {
	for _, c := range b {
		if c != 0 {
			return s.f.Write(b)
		}
	}
	if _, err := s.f.Seek(int64(len(b)), io.SeekCurrent); err != nil {
		return 0, err
	}
	return len(b), nil
}

// commit creates the folders and places every staged file, then sets folder modes and times, deepest
// first. If any step fails, every change made so far is reversed.
func (x *extractor) commit() error {
	if err := x.walk(x.commitOne); err != nil {
		for _, undo := range slices.Backward(x.undo) {
			_ = undo() // best effort: a folder something else was put into stays
		}
		return err
	}
	x.committed = true
	err := x.root.RemoveAll(x.stage) // with it go the files that overwriting replaced
	for _, e := range slices.Backward(x.created) {
		err = errors.Join(err, x.root.Chmod(e.Path, os.FileMode(e.perm())), x.root.Chtimes(e.Path, e.MTime, e.MTime))
	}
	return err
}

// commitOne creates a folder or places a file.
func (x *extractor) commitOne(p string, i int, _ bool) (bool, error) {
	if i >= 0 && !x.entries[i].Dir {
		return false, x.place(i, x.entries[i])
	}
	mode := os.FileMode(0o750)
	if i >= 0 {
		mode = 0o700 // the entry's mode is set at the end, when nothing more goes into it
	}
	err := x.root.Mkdir(p, mode)
	switch {
	case err == nil:
		x.undo = append(x.undo, func() error { return x.root.Remove(p) })
		if i >= 0 {
			x.created = append(x.created, x.entries[i])
		}
		return true, nil
	case errors.Is(err, fs.ErrExist):
		if st, err := x.root.Lstat(p); err != nil || !st.IsDir() {
			return false, &ExistsError{p, "exists and is not a folder"}
		}
		return true, nil // an existing folder is merged into, and left as it was
	}
	return false, err
}

// place moves staged file i to its path. Without overwrite it hard-links the staged file there, which
// fails if anything has appeared at the path since the pre-check, and then removes the staged name.
// Where hard links are not supported it checks that the path is free and renames, leaving a short
// window between the two. With overwrite, an existing regular file is first moved into the staging
// folder (and back on rollback); anything else, or another name for a file this transfer has already
// placed, is refused.
func (x *extractor) place(i int, e Entry) error {
	if testHookPlace != nil {
		if err := testHookPlace(x.root, e.Path); err != nil {
			return err
		}
	}
	// Times, mode and identity are taken on the staged name, which is short to resolve: a hard link
	// shares them, and a rename keeps them. Only on Windows is the mode set after placing, because a
	// read-only staged name could not be removed there.
	s := x.staged(i)
	err := x.root.Chtimes(s, e.MTime, e.MTime)
	if err == nil && runtime.GOOS != "windows" {
		err = x.root.Chmod(s, os.FileMode(e.perm()))
	}
	if err == nil && x.overwrite {
		var id fileID
		if id, err = statID(x.root, s); err == nil {
			err = x.setAside(i, e.Path)
			x.placed[id] = true
		}
	}
	if err != nil {
		return err
	}
	err = linkFile(x.root, s, e.Path)
	switch {
	case err == nil:
		x.undo = append(x.undo, func() error { return x.root.Remove(e.Path) })
		err = x.root.Remove(s)
	case errors.Is(err, fs.ErrExist):
		return &ExistsError{e.Path, "appeared while the transfer was being extracted"}
	default:
		if _, err := x.root.Lstat(e.Path); !errors.Is(err, fs.ErrNotExist) {
			return &ExistsError{e.Path, "appeared while the transfer was being extracted"}
		}
		if err = x.root.Rename(s, e.Path); err == nil {
			x.undo = append(x.undo, func() error { return x.root.Remove(e.Path) })
		}
	}
	if err == nil && runtime.GOOS == "windows" {
		err = x.root.Chmod(e.Path, os.FileMode(e.perm()))
	}
	return err
}

// setAside moves an existing regular file at p into the staging folder, so that it can be restored.
func (x *extractor) setAside(i int, p string) error {
	st, err := x.root.Lstat(p)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return nil
	case err != nil:
		return err
	case !st.Mode().IsRegular():
		return &ExistsError{p, "exists and is not a regular file"}
	}
	id, err := statID(x.root, p)
	if err != nil {
		return err
	}
	if x.placed[id] {
		return &ExistsError{p, "is another name for a file this transfer has already written"}
	}
	backup := x.stage + "/old-" + strconv.Itoa(i)
	if err := x.root.Rename(p, backup); err != nil {
		return err
	}
	x.undo = append(x.undo, func() error { return x.root.Rename(backup, p) })
	return nil
}

// close removes the staging folder unless commit got past placing the files.
func (x *extractor) close() {
	if !x.committed {
		_ = x.root.RemoveAll(x.stage)
	}
	_ = x.root.Close()
}
