package rs

import (
	"errors"
	"os"

	"github.com/platten/qrvideotransfer/internal/fec"
)

// slotTrailer is the bytes stored after each shard.
const slotTrailer = 8

// Store is a disk-backed fec.ShardStore.
type Store struct {
	f *os.File
}

// StoreSize returns the size of the store file for plan p.
func StoreSize(p fec.Plan) (int64, error) { return 0, errors.New("not implemented") }

// Create creates the store file.
func Create(path string, p fec.Plan) (*Store, error) { return nil, errors.New("not implemented") }

// Open opens an existing store file.
func Open(path string, p fec.Plan) (*Store, error) { return nil, errors.New("not implemented") }

// Put stores a shard.
func (s *Store) Put(block, shard int, b []byte) error { return errors.New("not implemented") }

// Get reads a shard.
func (s *Store) Get(block, shard int, b []byte) (bool, error) {
	return false, errors.New("not implemented")
}

// Have returns the shards stored for block.
func (s *Store) Have(block int) int { return 0 }

// Close closes the store.
func (s *Store) Close() error { return errors.New("not implemented") }

func (s *Store) offset(block, shard int) int64 { return 0 }

func slotBytes(block, shard int, b []byte) []byte { return nil }
