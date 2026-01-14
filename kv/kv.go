// Package kv implements a durable key-value store backed by a single file.
//
// Pages are never modified in place (apart from the free list, which is
// designed to tolerate it). An update appends new pages, fsyncs, then
// atomically swaps the root by rewriting the master page. A crash at any
// point leaves the previous version intact.
package kv

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/elhadjisall/kvdb/btree"
)

const (
	sig       = "KVDB-ELHADJI-001" // 16 bytes
	pageSize  = btree.PageSize
	cacheSize = 4096 // pages kept in the read cache
)

// Master page layout:
//
//	| sig | root | used | version | fl.head_page | fl.head_seq | fl.tail_page | fl.tail_seq |
//	| 16B |  8B  |  8B  |   8B    |      8B      |     8B      |      8B      |     8B      |
type master struct {
	root     uint64
	used     uint64 // number of pages in use (file size / page size)
	version  uint64 // incremented on every commit
	headPage uint64
	headSeq  uint64
	tailPage uint64
	tailSeq  uint64
}

func (m *master) encode() []byte {
	data := make([]byte, pageSize)
	copy(data, sig)
	fields := []uint64{m.root, m.used, m.version, m.headPage, m.headSeq, m.tailPage, m.tailSeq}
	for i, f := range fields {
		binary.LittleEndian.PutUint64(data[16+8*i:], f)
	}
	return data
}

func (m *master) decode(data []byte) error {
	if !bytes.Equal(data[:16], []byte(sig)) {
		return errors.New("kv: bad signature")
	}
	fields := []*uint64{&m.root, &m.used, &m.version, &m.headPage, &m.headSeq, &m.tailPage, &m.tailSeq}
	for i, f := range fields {
		*f = binary.LittleEndian.Uint64(data[16+8*i:])
	}
	return nil
}

// KV is a single-file key-value store.
type KV struct {
	Path string

	fp   *os.File
	mu   sync.Mutex // serialises writers
	meta master     // last committed master page

	// readers currently holding a snapshot, keyed by version
	readers map[uint64]int
	rmu     sync.Mutex

	cache map[uint64][]byte
	cmu   sync.Mutex
}

// Open creates or opens a database file.
func Open(path string) (*KV, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	fp, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o644)
	if err != nil {
		return nil, err
	}
	kv := &KV{Path: path, fp: fp, readers: map[uint64]int{}, cache: map[uint64][]byte{}}
	if err := kv.loadMaster(); err != nil {
		fp.Close()
		return nil, err
	}
	return kv, nil
}

// Close releases the file.
func (kv *KV) Close() error { return kv.fp.Close() }

// Stats describes the on-disk state.
type Stats struct {
	Pages     uint64 `json:"pages"`
	FreePages uint64 `json:"free_pages"`
	Version   uint64 `json:"version"`
	SizeBytes uint64 `json:"size_bytes"`
}

// Stats returns storage statistics.
func (kv *KV) Stats() Stats {
	kv.mu.Lock()
	defer kv.mu.Unlock()
	return Stats{
		Pages:     kv.meta.used,
		FreePages: kv.meta.tailSeq - kv.meta.headSeq,
		Version:   kv.meta.version,
		SizeBytes: kv.meta.used * pageSize,
	}
}

func (kv *KV) loadMaster() error {
	info, err := kv.fp.Stat()
	if err != nil {
		return err
	}
	if info.Size() == 0 {
		// brand new file: page 0 is the master, page 1 is the first
		// (empty) free list node
		kv.meta = master{used: 2, headPage: 1, tailPage: 1}
		if _, err := kv.fp.WriteAt(make([]byte, pageSize), pageSize); err != nil {
			return err
		}
		return kv.writeMaster()
	}
	if info.Size()%pageSize != 0 || info.Size() < 2*pageSize {
		return errors.New("kv: file size is not a multiple of the page size")
	}
	data := make([]byte, pageSize)
	if _, err := kv.fp.ReadAt(data, 0); err != nil {
		return err
	}
	if err := kv.meta.decode(data); err != nil {
		return err
	}
	if kv.meta.used > uint64(info.Size()/pageSize) || kv.meta.root >= kv.meta.used {
		return errors.New("kv: corrupted master page")
	}
	return nil
}

func (kv *KV) writeMaster() error {
	if _, err := kv.fp.WriteAt(kv.meta.encode(), 0); err != nil {
		return fmt.Errorf("kv: write master: %w", err)
	}
	if err := kv.fp.Sync(); err != nil {
		return fmt.Errorf("kv: fsync master: %w", err)
	}
	return nil
}

// readPage fetches a committed page from the file (or the cache).
func (kv *KV) readPage(ptr uint64) []byte {
	kv.cmu.Lock()
	if page, ok := kv.cache[ptr]; ok {
		kv.cmu.Unlock()
		return page
	}
	kv.cmu.Unlock()
	page := make([]byte, pageSize)
	if _, err := kv.fp.ReadAt(page, int64(ptr*pageSize)); err != nil {
		panic(fmt.Sprintf("kv: read page %d: %v", ptr, err))
	}
	kv.cmu.Lock()
	if len(kv.cache) >= cacheSize {
		kv.cache = map[uint64][]byte{}
	}
	kv.cache[ptr] = page
	kv.cmu.Unlock()
	return page
}

func (kv *KV) invalidate(ptrs map[uint64][]byte) {
	kv.cmu.Lock()
	for ptr := range ptrs {
		delete(kv.cache, ptr)
	}
	kv.cmu.Unlock()
}

// writePages persists pending pages and grows the file if necessary.
func (kv *KV) writePages(updates map[uint64][]byte, used uint64) error {
	info, err := kv.fp.Stat()
	if err != nil {
		return err
	}
	if need := int64(used * pageSize); info.Size() < need {
		// grow in larger steps to avoid constant metadata updates
		grow := int64(pageSize * 64)
		if err := kv.fp.Truncate((need/grow + 1) * grow); err != nil {
			return fmt.Errorf("kv: extend file: %w", err)
		}
	}
	for ptr, data := range updates {
		if _, err := kv.fp.WriteAt(data, int64(ptr*pageSize)); err != nil {
			return fmt.Errorf("kv: write page %d: %w", ptr, err)
		}
	}
	return kv.fp.Sync()
}

// Get reads a single key.
func (kv *KV) Get(key []byte) ([]byte, bool) {
	r := kv.BeginRead()
	defer r.Close()
	return r.Get(key)
}

// Set writes a single key in its own transaction.
func (kv *KV) Set(key, val []byte) error {
	tx := kv.Begin()
	tx.Set(key, val)
	return tx.Commit()
}

// Del deletes a single key in its own transaction.
func (kv *KV) Del(key []byte) (bool, error) {
	tx := kv.Begin()
	ok := tx.Del(key)
	return ok, tx.Commit()
}

// minReaderVersion returns the oldest version still in use by a reader.
func (kv *KV) minReaderVersion() uint64 {
	kv.rmu.Lock()
	defer kv.rmu.Unlock()
	min := kv.meta.version
	for v := range kv.readers {
		if v < min {
			min = v
		}
	}
	return min
}
