package kv

import "encoding/binary"

// The free list is a FIFO queue of unused page numbers stored in a linked
// list of pages. Items are consumed from the head and appended to the tail.
// Positions are addressed by a monotonically increasing sequence number so
// the master page only needs (page, seq) pairs for the head and the tail.
//
// Node layout:
//
//	| next | item[0] | item[1] | ...
//	|  8B  |   16B   |   16B   |
//
// Each item is the freed page number followed by the version at which it
// was freed, so that pages are only reused once no reader can observe them.

const (
	flHeader = 8
	flItem   = 16
	flCap    = (pageSize - flHeader) / flItem
)

type flNode []byte

func (n flNode) next() uint64       { return binary.LittleEndian.Uint64(n[0:8]) }
func (n flNode) setNext(ptr uint64) { binary.LittleEndian.PutUint64(n[0:8], ptr) }

func (n flNode) item(i uint64) (ptr, ver uint64) {
	off := flHeader + flItem*i
	return binary.LittleEndian.Uint64(n[off:]), binary.LittleEndian.Uint64(n[off+8:])
}

func (n flNode) setItem(i uint64, ptr, ver uint64) {
	off := flHeader + flItem*i
	binary.LittleEndian.PutUint64(n[off:], ptr)
	binary.LittleEndian.PutUint64(n[off+8:], ver)
}

func seq2idx(seq uint64) uint64 { return seq % flCap }

// freeList is the in-memory handle for the queue within one transaction.
type freeList struct {
	get func(uint64) []byte // read a page
	set func(uint64) []byte // read a page for in-place update
	new func([]byte) uint64 // append a page

	headPage, headSeq uint64
	tailPage, tailSeq uint64

	maxSeq uint64 // items at or after this were freed in this tx and are off limits
	curVer uint64 // version being produced by this tx (stamped on freed pages)
	minVer uint64 // oldest version a live reader still needs
}

// pop removes the head item. It returns the page number (0 if nothing is
// available) and, if the head node itself was drained, that node's page so
// the caller can recycle it.
func (fl *freeList) pop() (ptr, drained uint64) {
	if fl.headSeq >= fl.maxSeq {
		return 0, 0
	}
	node := flNode(fl.get(fl.headPage))
	ptr, ver := node.item(seq2idx(fl.headSeq))
	if ver > fl.minVer {
		return 0, 0 // a reader may still need it
	}
	fl.headSeq++
	if seq2idx(fl.headSeq) == 0 {
		drained, fl.headPage = fl.headPage, node.next()
		if fl.headPage == 0 {
			panic("kv: free list head ran past the tail")
		}
	}
	return ptr, drained
}

// popHead returns a reusable page or 0 if none is available.
func (fl *freeList) popHead() uint64 {
	ptr, drained := fl.pop()
	if drained != 0 {
		// the drained head node becomes a free page itself
		fl.pushTail(drained)
	}
	return ptr
}

// pushTail records a page as free.
func (fl *freeList) pushTail(ptr uint64) {
	node := flNode(fl.set(fl.tailPage))
	node.setItem(seq2idx(fl.tailSeq), ptr, fl.curVer)
	fl.tailSeq++
	if seq2idx(fl.tailSeq) == 0 {
		// the tail node is full: link a new one, reusing a free page if
		// possible (but never one freed in this transaction). pop is used
		// directly so that nothing is appended while the tail is unlinked.
		next, drained := fl.pop()
		if next == 0 {
			next = fl.new(make([]byte, pageSize))
		}
		node.setNext(next)
		fl.tailPage = next
		fresh := flNode(fl.set(next))
		fresh.setNext(0) // the new tail starts clean
		if drained != 0 {
			fresh.setItem(0, drained, fl.curVer)
			fl.tailSeq++
		}
	}
}
