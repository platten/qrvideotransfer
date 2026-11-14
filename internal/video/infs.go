package video

import (
	"io"
	"io/fs"
	"os"
	"sync/atomic"
	"time"
)

// inputFS is the decoding instance's whole view of the host: one file, read-only, under one name.
// It is mounted with wazero's FSConfig.WithFSMount. Its files implement io.ReaderAt and io.Seeker
// (an MP4 index may sit at the end) but neither io.Writer nor io.WriterAt, so a guest write cannot
// reach the host file (wazero's fsFile writes through those interfaces when they exist). Every byte
// read is added to reads, the watchdog's progress signal while probing.
type inputFS struct {
	path  string // host path
	name  string // the only name the guest can open
	reads *atomic.Int64
}

// Open implements fs.FS.
func (v inputFS) Open(name string) (fs.File, error) {
	switch name {
	case ".":
		return &inputDir{v: v}, nil
	case v.name:
		f, err := os.Open(v.path)
		if err != nil {
			return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrNotExist}
		}
		return &inputFile{f: f, name: v.name, reads: v.reads}, nil
	}
	return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrNotExist}
}

// inputFile is the read-only input file. It deliberately has no Write or WriteAt method.
type inputFile struct {
	f     *os.File
	name  string
	reads *atomic.Int64
}

func (f *inputFile) Read(b []byte) (int, error) {
	n, err := f.f.Read(b)
	f.reads.Add(int64(n))
	return n, err
}

func (f *inputFile) ReadAt(b []byte, off int64) (int, error) {
	n, err := f.f.ReadAt(b, off)
	f.reads.Add(int64(n))
	return n, err
}

func (f *inputFile) Seek(off int64, whence int) (int64, error) { return f.f.Seek(off, whence) }
func (f *inputFile) Close() error                              { return f.f.Close() }

func (f *inputFile) Stat() (fs.FileInfo, error) {
	st, err := f.f.Stat()
	if err != nil {
		return nil, err
	}
	return info{name: f.name, size: st.Size(), mode: 0o444, t: st.ModTime()}, nil
}

// inputDir is the mount's root directory, listing the one file.
type inputDir struct {
	v    inputFS
	done bool
}

func (d *inputDir) Read([]byte) (int, error) { return 0, io.EOF }
func (d *inputDir) Close() error             { return nil }
func (d *inputDir) Stat() (fs.FileInfo, error) {
	return info{name: ".", mode: fs.ModeDir | 0o555}, nil
}

// ReadDir implements fs.ReadDirFile.
func (d *inputDir) ReadDir(n int) ([]fs.DirEntry, error) {
	if d.done {
		if n > 0 {
			return nil, io.EOF
		}
		return nil, nil
	}
	d.done = true
	return []fs.DirEntry{fs.FileInfoToDirEntry(info{name: d.v.name, mode: 0o444})}, nil
}

type info struct {
	name string
	size int64
	mode fs.FileMode
	t    time.Time
}

func (i info) Name() string       { return i.name }
func (i info) Size() int64        { return i.size }
func (i info) Mode() fs.FileMode  { return i.mode }
func (i info) ModTime() time.Time { return i.t }
func (i info) IsDir() bool        { return i.mode.IsDir() }
func (i info) Sys() any           { return nil }
