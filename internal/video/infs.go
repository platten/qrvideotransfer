package video

import (
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
	path  string
	name  string
	reads *atomic.Int64
}

// Open implements fs.FS.
func (v inputFS) Open(name string) (fs.File, error) { panic("not implemented") }

// inputFile is the read-only input file. It deliberately has no Write or WriteAt method.
type inputFile struct {
	f     *os.File
	name  string
	reads *atomic.Int64
}

func (f *inputFile) Read(b []byte) (int, error) { panic("not implemented") }

func (f *inputFile) ReadAt(b []byte, off int64) (int, error) { panic("not implemented") }

func (f *inputFile) Seek(off int64, whence int) (int64, error) { panic("not implemented") }
func (f *inputFile) Close() error                              { panic("not implemented") }

func (f *inputFile) Stat() (fs.FileInfo, error) { panic("not implemented") }

// inputDir is the mount's root directory, listing the one file.
type inputDir struct {
	v    inputFS
	done bool
}

func (d *inputDir) Read([]byte) (int, error)   { panic("not implemented") }
func (d *inputDir) Close() error               { panic("not implemented") }
func (d *inputDir) Stat() (fs.FileInfo, error) { panic("not implemented") }

// ReadDir implements fs.ReadDirFile.
func (d *inputDir) ReadDir(n int) ([]fs.DirEntry, error) { panic("not implemented") }

type info struct {
	name string
	size int64
	mode fs.FileMode
	t    time.Time
}

func (i info) Name() string       { panic("not implemented") }
func (i info) Size() int64        { panic("not implemented") }
func (i info) Mode() fs.FileMode  { panic("not implemented") }
func (i info) ModTime() time.Time { panic("not implemented") }
func (i info) IsDir() bool        { panic("not implemented") }
func (i info) Sys() any           { panic("not implemented") }
