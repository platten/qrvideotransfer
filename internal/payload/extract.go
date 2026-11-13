package payload

import (
	"cmp"
	"context"
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
	ctx       context.Context // checked before each entry; a cancelled commit is rolled back
	root      *os.Root
	stage     string
	entries   []Entry
	order     []int // entry indices in tree order
	overwrite bool
	undo      []func() error  // how to reverse each change commit made, in order
	placed    map[fileID]bool // files this transfer placed, kept with overwrite
	created   []bool          // by entry index: folders commit created
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
func newExtractor(ctx context.Context, dir string, entries []Entry, overwrite bool) (*extractor, error) {
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, err
	}
	x := &extractor{ctx: ctx, root: root, entries: entries, overwrite: overwrite, placed: map[fileID]bool{}}
	x.order = make([]int, len(entries))
	for i := range x.order {
		x.order[i] = i
	}
	slices.SortFunc(x.order, func(a, b int) int { return cmpTree(entries[a].Path, entries[b].Path) })
	var r [8]byte
	_, _ = rand.Read(r[:]) // never fails
	x.stage = stagePrefix + hex.EncodeToString(r[:])
	err = x.walk(x.check, nil)
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

// visitFunc handles one path component during a walk: parent is the handle of the folder that holds
// it (nil when that folder does not exist), name the component, p the whole path, and i the entry's
// index (-1 for a folder that is not an entry). It returns a handle on p when p is now an existing
// folder whose contents are to be visited, and nil otherwise.
type visitFunc func(parent *os.Root, name, p string, i int) (*os.Root, error)

// walk visits, in tree order, each folder on the way to an entry once and then the entry itself.
// Every operation goes through the handle of the parent folder, so it costs the same at any depth;
// at most one handle per level of depth is open. leave, if not nil, is called for each component
// after everything beneath it, again through its parent's handle.
func (x *extractor) walk(visit visitFunc, leave func(parent *os.Root, name string, i int) error) (err error) {
	type level struct {
		end  int // the component ends at this byte of the path
		name string
		i    int
		dir  *os.Root
	}
	var stack []level
	parent := func() *os.Root {
		if len(stack) == 0 {
			return x.root
		}
		return stack[len(stack)-1].dir
	}
	// unwind pops levels down to k, calling leave while nothing has failed, and closes their handles.
	unwind := func(k int, err error) error {
		for len(stack) > k {
			l := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			if err == nil && leave != nil && parent() != nil {
				err = leave(parent(), l.name, l.i)
			}
			if l.dir != nil {
				_ = l.dir.Close()
			}
		}
		return err
	}
	prev := ""
	for _, i := range x.order {
		if err := x.ctx.Err(); err != nil {
			return unwind(0, err)
		}
		p := x.entries[i].Path
		c, k := commonPrefix(p, prev), 0
		for k < len(stack) && stack[k].end <= c && stack[k].end < len(p) && p[stack[k].end] == '/' {
			k++
		}
		if err := unwind(k, nil); err != nil {
			return unwind(0, err)
		}
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
			dir, err := visit(parent(), p[start:end], p[:end], j)
			if err != nil {
				return unwind(0, err)
			}
			stack = append(stack, level{end, p[start:end], j, dir})
			start = end + 1
		}
		prev = p
	}
	return unwind(0, nil)
}

// check refuses a path that exists in a form it may not replace. Below a missing folder nothing is
// looked up.
func (x *extractor) check(parent *os.Root, name, p string, i int) (*os.Root, error) {
	if parent == nil {
		return nil, nil
	}
	st, err := parent.Lstat(name)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return nil, nil
	case err != nil:
		return nil, &ExistsError{p, fmt.Sprintf("cannot be created: %v", err)}
	case i < 0 && !st.IsDir():
		return nil, &ExistsError{p, "exists and is not a folder"}
	case i < 0:
		return parent.OpenRoot(name)
	}
	e := x.entries[i]
	switch {
	case e.Dir && st.IsDir():
		return parent.OpenRoot(name)
	case e.Dir:
		return nil, &ExistsError{e.Path, "exists and is not a folder"}
	case !st.Mode().IsRegular():
		return nil, &ExistsError{e.Path, "exists and is not a regular file"}
	case !x.overwrite:
		return nil, &ExistsError{e.Path, "already exists (allow overwriting to replace it)"}
	}
	return nil, nil
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
	x.created = make([]bool, len(x.entries))
	if err := x.walk(x.commitOne, nil); err != nil {
		for _, undo := range slices.Backward(x.undo) {
			_ = undo() // best effort: a folder something else was put into stays
		}
		return err
	}
	x.committed = true
	err := x.root.RemoveAll(x.stage) // with it go the files that overwriting replaced
	// A second walk sets the modes and times of the folders it created, after their contents.
	open := func(parent *os.Root, name, _ string, i int) (*os.Root, error) {
		if i >= 0 && !x.entries[i].Dir {
			return nil, nil
		}
		return parent.OpenRoot(name)
	}
	return errors.Join(err, x.walk(open, func(parent *os.Root, name string, i int) error {
		if i < 0 || !x.created[i] {
			return nil
		}
		e := x.entries[i]
		return errors.Join(parent.Chmod(name, os.FileMode(e.perm())), parent.Chtimes(name, e.MTime, e.MTime))
	}))
}

// commitOne creates a folder or places a file.
func (x *extractor) commitOne(parent *os.Root, name, p string, i int) (*os.Root, error) {
	if i >= 0 && !x.entries[i].Dir {
		return nil, x.place(i, x.entries[i])
	}
	mode := os.FileMode(0o750)
	if i >= 0 {
		mode = 0o700 // the entry's mode is set at the end, when nothing more goes into it
	}
	err := parent.Mkdir(name, mode)
	switch {
	case err == nil:
		x.undo = append(x.undo, func() error { return x.root.Remove(p) })
		if i >= 0 {
			x.created[i] = true
		}
	case errors.Is(err, fs.ErrExist):
		if st, err := parent.Lstat(name); err != nil || !st.IsDir() {
			return nil, &ExistsError{p, "exists and is not a folder"}
		}
		// an existing folder is merged into, and left as it was
	default:
		return nil, err
	}
	return parent.OpenRoot(name)
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
	// shares them, and a rename keeps them. A read-only mode is set after placing on Windows, because a
	// read-only staged name could not be removed there.
	s := x.staged(i)
	err := x.root.Chtimes(s, e.MTime, e.MTime)
	if err == nil && !x.modeLater(e) {
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
	if err == nil && x.modeLater(e) {
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

// modeLater reports whether a file's mode must be set after it is placed: on Windows, a read-only
// staged name could not be removed.
func (x *extractor) modeLater(e Entry) bool {
	return runtime.GOOS == "windows" && e.perm()&0o200 == 0
}

// close removes the staging folder unless commit got past placing the files.
func (x *extractor) close() {
	if !x.committed {
		_ = x.root.RemoveAll(x.stage)
	}
	_ = x.root.Close()
}
