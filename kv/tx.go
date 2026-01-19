package kv

import (
	"errors"

	"github.com/elhadjisall/kvdb/btree"
)

// ReadTx is a consistent snapshot of the database. Many readers may run
// concurrently with each other and with a single writer.
type ReadTx struct {
	kv      *KV
	tree    btree.BTree
	version uint64
	closed  bool
}

// BeginRead takes a snapshot.
func (kv *KV) BeginRead() *ReadTx {
	kv.mu.Lock()
	meta := kv.meta
	kv.mu.Unlock()

	kv.rmu.Lock()
	kv.readers[meta.version]++
	kv.rmu.Unlock()

	r := &ReadTx{kv: kv, version: meta.version}
	r.tree = btree.BTree{Root: meta.root, Get: kv.readPage}
	return r
}

// Close releases the snapshot so its pages may eventually be reused.
func (r *ReadTx) Close() {
	if r.closed {
		return
	}
	r.closed = true
	r.kv.rmu.Lock()
	r.kv.readers[r.version]--
	if r.kv.readers[r.version] == 0 {
		delete(r.kv.readers, r.version)
	}
	r.kv.rmu.Unlock()
}

// Get looks up a key in the snapshot.
func (r *ReadTx) Get(key []byte) ([]byte, bool) { return r.tree.Lookup(key) }

// Seek returns a cursor positioned relative to key.
func (r *ReadTx) Seek(key []byte, cmp int) *btree.Iter { return r.tree.Seek(key, cmp) }

// Tx is a read-write transaction. Only one may be open at a time; Begin
// blocks until the previous writer finishes.
type Tx struct {
	kv      *KV
	tree    btree.BTree
	free    freeList
	meta    master
	updates map[uint64][]byte // pending page writes (page number -> data)
	nappend uint64            // pages appended beyond meta.used
	done    bool
}

// ErrTxDone is returned when a finished transaction is reused.
var ErrTxDone = errors.New("kv: transaction already finished")

// Begin starts a write transaction.
func (kv *KV) Begin() *Tx {
	kv.mu.Lock() // released by Commit or Abort
	tx := &Tx{kv: kv, meta: kv.meta, updates: map[uint64][]byte{}}

	tx.free = freeList{
		get:      tx.pageRead,
		set:      tx.pageWrite,
		new:      tx.pageAppend,
		headPage: tx.meta.headPage, headSeq: tx.meta.headSeq,
		tailPage: tx.meta.tailPage, tailSeq: tx.meta.tailSeq,
		maxSeq: tx.meta.tailSeq,
		curVer: tx.meta.version + 1,
		minVer: kv.minReaderVersion(),
	}
	tx.tree = btree.BTree{
		Root: tx.meta.root,
		Get:  tx.pageRead,
		New:  tx.pageAlloc,
		Del:  tx.pageFree,
	}
	return tx
}

// pageRead resolves pending pages first, then committed ones.
func (tx *Tx) pageRead(ptr uint64) []byte {
	if page, ok := tx.updates[ptr]; ok {
		return page
	}
	return tx.kv.readPage(ptr)
}

// pageWrite returns a buffer for in-place modification (free list only).
func (tx *Tx) pageWrite(ptr uint64) []byte {
	if page, ok := tx.updates[ptr]; ok {
		return page
	}
	page := make([]byte, pageSize)
	copy(page, tx.kv.readPage(ptr))
	tx.updates[ptr] = page
	return page
}

// pageAppend allocates a brand new page at the end of the file.
func (tx *Tx) pageAppend(node []byte) uint64 {
	ptr := tx.meta.used + tx.nappend
	tx.nappend++
	tx.updates[ptr] = node
	return ptr
}

// pageAlloc reuses a free page when possible, otherwise appends.
func (tx *Tx) pageAlloc(node []byte) uint64 {
	if ptr := tx.free.popHead(); ptr != 0 {
		tx.updates[ptr] = node
		return ptr
	}
	return tx.pageAppend(node)
}

// pageFree hands a page back to the free list.
func (tx *Tx) pageFree(ptr uint64) {
	tx.free.pushTail(ptr)
}

// Get reads a key, observing this transaction's own writes.
func (tx *Tx) Get(key []byte) ([]byte, bool) { return tx.tree.Lookup(key) }

// Set inserts or updates a key.
func (tx *Tx) Set(key, val []byte) { tx.tree.Insert(key, val) }

// Del removes a key.
func (tx *Tx) Del(key []byte) bool { return tx.tree.Delete(key) }

// Seek returns a cursor over the transaction's view.
func (tx *Tx) Seek(key []byte, cmp int) *btree.Iter { return tx.tree.Seek(key, cmp) }

// Abort discards all pending changes.
func (tx *Tx) Abort() {
	if tx.done {
		return
	}
	tx.done = true
	tx.kv.mu.Unlock()
}

// Commit makes the changes durable.
func (tx *Tx) Commit() error {
	if tx.done {
		return ErrTxDone
	}
	tx.done = true
	defer tx.kv.mu.Unlock()

	if len(tx.updates) == 0 {
		return nil // read-only
	}
	used := tx.meta.used + tx.nappend
	if err := tx.kv.writePages(tx.updates, used); err != nil {
		return err
	}
	// everything is on disk: flip the master page
	tx.kv.meta = master{
		root:     tx.tree.Root,
		used:     used,
		version:  tx.meta.version + 1,
		headPage: tx.free.headPage, headSeq: tx.free.headSeq,
		tailPage: tx.free.tailPage, tailSeq: tx.free.tailSeq,
	}
	if err := tx.kv.writeMaster(); err != nil {
		tx.kv.meta = tx.meta // roll back the in-memory state
		return err
	}
	tx.kv.invalidate(tx.updates)
	return nil
}
