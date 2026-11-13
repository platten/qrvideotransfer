package rs

import (
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"os"
	"path/filepath"

	"github.com/platten/qrvideotransfer/internal/fec"
)

// MaxShardSize is the largest shard size a Store accepts, far above the 192 bytes of FORMAT.md
// section 8; it bounds the store's buffers.
const MaxShardSize = 1 << 16

// slotTrailer is the bytes stored after each shard: a marker and a CRC-32C.
const slotTrailer = 8

// slotMarker is never all zero, so a slot that was never written reads as absent.
var slotMarker = [4]byte{'q', 'r', 'v', 's'}

var castagnoli = crc32.MakeTable(crc32.Castagnoli)

// scanBytes is the most the store reads at once when it opens a file.
const scanBytes = 1 << 20

// Store is a disk-backed fec.ShardStore holding every shard of a plan in one file.
//
// The file is a sequence of slots, one per shard, block by block: slot (block * (K+M) + shard) at
// offset slot * (ShardSize + 8). A slot is the shard followed by the marker "qrvs" and the CRC-32C
// (big-endian) of the block and shard numbers (4 bytes each, big-endian) and the shard bytes. A slot
// counts as stored only when its marker and CRC are right, so a write torn by a crash, in any order,
// reads back as absent (unless a torn write happens to match the 32-bit CRC), and a shard written
// to the wrong slot is refused. Writes are not synced: a killed process loses nothing the kernel
// has accepted, but a power failure may lose recent shards, which then read as absent.
//
// A Store is not safe for concurrent use; one goroutine owns it.
type Store struct {
	f    *os.File
	p    fec.Plan
	n    int   // shards per block, K + M
	slot int64 // bytes per slot
	have []int32
	buf  []byte
}

// StoreSize returns the size in bytes of the store file for plan p, (K + M) x Blocks x
// (ShardSize + 8), or an error when the plan is not valid. A caller checks the free space with it
// before Create. With the default parity of 20% it is about 1.2 x (1 + 8/ShardSize) x the stream.
func StoreSize(p fec.Plan) (int64, error) {
	switch {
	case p.Blocks < 1 || p.Blocks > fec.MaxBlocks:
		return 0, fmt.Errorf("rs: %d blocks out of range", p.Blocks)
	case p.K < 1 || p.K > fec.MaxShards || p.M < 1 || p.M > fec.MaxShards:
		return 0, fmt.Errorf("rs: %d data and %d parity shards out of range", p.K, p.M)
	case p.ShardSize < 64 || p.ShardSize > MaxShardSize || p.ShardSize%64 != 0:
		return 0, fmt.Errorf("rs: shard size %d is not a multiple of 64 from 64 to %d", p.ShardSize, MaxShardSize)
	}
	// At most 2^24 x 2^16 x (2^16 + 8) bytes: no overflow.
	return int64(p.Blocks) * int64(p.K+p.M) * int64(p.ShardSize+slotTrailer), nil
}

func openedStore(f *os.File, p fec.Plan) *Store {
	return &Store{f: f, p: p, n: p.K + p.M, slot: int64(p.ShardSize + slotTrailer), have: make([]int32, p.Blocks), buf: make([]byte, p.ShardSize+slotTrailer)}
}

// Create creates the store file for plan p at path, which must not exist. The file is created
// sparse at its full size (StoreSize).
func Create(path string, p fec.Plan) (*Store, error) {
	size, err := StoreSize(p)
	if err != nil {
		return nil, err
	}
	f, err := os.OpenFile(filepath.Clean(path), os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return nil, fmt.Errorf("rs: %w", err)
	}
	if err := f.Truncate(size); err != nil {
		_ = f.Close()
		_ = os.Remove(path)
		return nil, fmt.Errorf("rs: %w", err)
	}
	return openedStore(f, p), nil
}

// Open opens the existing store file of plan p at path, for example after a crash, and reads it
// whole to count the shards it holds.
func Open(path string, p fec.Plan) (*Store, error) {
	size, err := StoreSize(p)
	if err != nil {
		return nil, err
	}
	f, err := os.OpenFile(filepath.Clean(path), os.O_RDWR, 0) //nolint:gosec // the caller names the file (a temporary file), never decoded input
	if err != nil {
		return nil, fmt.Errorf("rs: %w", err)
	}
	s := openedStore(f, p)
	if err := s.scan(size); err != nil {
		_ = f.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) scan(size int64) error {
	fi, err := s.f.Stat()
	if err != nil {
		return fmt.Errorf("rs: %w", err)
	}
	if fi.Size() != size {
		return fmt.Errorf("rs: store file is %d bytes, want %d for the plan", fi.Size(), size)
	}
	per := max(1, scanBytes/int(s.slot))
	buf := make([]byte, int64(per)*s.slot)
	total := int64(s.p.Blocks) * int64(s.n)
	for first := int64(0); first < total; first += int64(per) {
		cnt := min(int64(per), total-first)
		chunk := buf[:cnt*s.slot]
		if _, err := s.f.ReadAt(chunk, first*s.slot); err != nil {
			return fmt.Errorf("rs: %w", err)
		}
		for i := range cnt {
			idx := first + i
			b, sh := int(idx/int64(s.n)), int(idx%int64(s.n))
			if valid(b, sh, chunk[i*s.slot:(i+1)*s.slot]) {
				s.have[b]++
			}
		}
	}
	return nil
}

// offset returns the file offset of a slot.
func (s *Store) offset(block, shard int) int64 {
	return (int64(block)*int64(s.n) + int64(shard)) * s.slot
}

// slotSum returns the CRC-32C of a slot's block and shard numbers and its shard bytes.
func slotSum(block, shard int, b []byte) uint32 {
	var idx [8]byte
	binary.BigEndian.PutUint32(idx[:], uint32(block))  //nolint:gosec // block < fec.MaxBlocks, checked by the caller
	binary.BigEndian.PutUint32(idx[4:], uint32(shard)) //nolint:gosec // shard < 2 fec.MaxShards, checked by the caller
	return crc32.Update(crc32.Checksum(idx[:], castagnoli), castagnoli, b)
}

// slotBytes returns the slot holding shard b.
func slotBytes(block, shard int, b []byte) []byte {
	out := append(append(append([]byte(nil), b...), slotMarker[:]...), 0, 0, 0, 0)
	binary.BigEndian.PutUint32(out[len(b)+4:], slotSum(block, shard, b))
	return out
}

// valid reports whether slot holds a complete shard written for this block and shard.
func valid(block, shard int, slot []byte) bool {
	n := len(slot) - slotTrailer
	return [4]byte(slot[n:n+4]) == slotMarker && binary.BigEndian.Uint32(slot[n+4:]) == slotSum(block, shard, slot[:n])
}

func (s *Store) check(block, shard int, b []byte) error {
	if block < 0 || block >= s.p.Blocks || shard < 0 || shard >= s.n {
		return fmt.Errorf("rs: shard %d of block %d out of range", shard, block)
	}
	if len(b) != s.p.ShardSize {
		return fmt.Errorf("rs: %d-byte buffer for a %d-byte shard", len(b), s.p.ShardSize)
	}
	return nil
}

// read reads a slot into s.buf and reports whether it holds a valid shard.
func (s *Store) read(block, shard int) (bool, error) {
	if _, err := s.f.ReadAt(s.buf, s.offset(block, shard)); err != nil && !errors.Is(err, io.EOF) {
		return false, fmt.Errorf("rs: %w", err)
	}
	return valid(block, shard, s.buf), nil
}

// Put stores shard number shard of block block. Storing a shard that is already present does
// nothing.
func (s *Store) Put(block, shard int, b []byte) error {
	if err := s.check(block, shard, b); err != nil {
		return err
	}
	if ok, err := s.read(block, shard); err != nil || ok {
		return err
	}
	if _, err := s.f.WriteAt(slotBytes(block, shard, b), s.offset(block, shard)); err != nil {
		return fmt.Errorf("rs: %w", err)
	}
	s.have[block]++
	return nil
}

// Get copies the shard into b, which must be ShardSize bytes, and reports whether it was present.
func (s *Store) Get(block, shard int, b []byte) (bool, error) {
	if err := s.check(block, shard, b); err != nil {
		return false, err
	}
	ok, err := s.read(block, shard)
	if ok {
		copy(b, s.buf)
	}
	return ok, err
}

// Have returns the number of distinct shards stored for block, 0 for a block out of range.
func (s *Store) Have(block int) int {
	if block < 0 || block >= len(s.have) {
		return 0
	}
	return int(s.have[block])
}

// Close closes the file. It does not remove it.
func (s *Store) Close() error { return s.f.Close() }
