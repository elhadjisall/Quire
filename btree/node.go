// Package btree implements a copy-on-write B+tree over fixed size pages.
//
// Nodes are plain byte slices so they can be written to disk without any
// serialisation step. The on-page layout is:
//
//	| type | nkeys |  pointers  |   offsets  | key-values | unused |
//	|  2B  |   2B  | nkeys * 8B | nkeys * 2B |     ...    |        |
//
// and every key-value pair is encoded as:
//
//	| klen | vlen | key | val |
//	|  2B  |  2B  | ... | ... |
package btree

import (
	"encoding/binary"
	"fmt"
)

const (
	// BNodeInternal nodes only hold keys and child pointers.
	BNodeInternal = 1
	// BNodeLeaf nodes hold keys and values.
	BNodeLeaf = 2

	// Header is the size of the fixed node header in bytes.
	Header = 4

	// PageSize is the size of a single node/page on disk.
	PageSize = 4096
	// MaxKeySize bounds the key length so that a node can always hold at
	// least one key-value pair.
	MaxKeySize = 1000
	// MaxValSize bounds the value length.
	MaxValSize = 3000
)

func init() {
	// a node must be able to hold a single max-size pair
	max := Header + 8 + 2 + 4 + MaxKeySize + MaxValSize
	if max > PageSize {
		panic(fmt.Sprintf("node layout too large: %d > %d", max, PageSize))
	}
}

// BNode is a single page of the tree.
type BNode []byte

func (n BNode) btype() uint16 { return binary.LittleEndian.Uint16(n[0:2]) }
func (n BNode) nkeys() uint16 { return binary.LittleEndian.Uint16(n[2:4]) }

func (n BNode) setHeader(btype, nkeys uint16) {
	binary.LittleEndian.PutUint16(n[0:2], btype)
	binary.LittleEndian.PutUint16(n[2:4], nkeys)
}

// child pointers

func (n BNode) getPtr(idx uint16) uint64 {
	assert(idx < n.nkeys(), "ptr index out of range")
	pos := Header + 8*idx
	return binary.LittleEndian.Uint64(n[pos:])
}

func (n BNode) setPtr(idx uint16, val uint64) {
	assert(idx < n.nkeys(), "ptr index out of range")
	pos := Header + 8*idx
	binary.LittleEndian.PutUint64(n[pos:], val)
}

// offsets of each key-value pair relative to the first pair. The offset of
// the first pair is implicitly 0 so we store the offset of the *end* of each
// pair instead, which gives us the total size for free.

func offsetPos(n BNode, idx uint16) uint16 {
	assert(1 <= idx && idx <= n.nkeys(), "offset index out of range")
	return Header + 8*n.nkeys() + 2*(idx-1)
}

func (n BNode) getOffset(idx uint16) uint16 {
	if idx == 0 {
		return 0
	}
	return binary.LittleEndian.Uint16(n[offsetPos(n, idx):])
}

func (n BNode) setOffset(idx uint16, offset uint16) {
	binary.LittleEndian.PutUint16(n[offsetPos(n, idx):], offset)
}

// key-values

func (n BNode) kvPos(idx uint16) uint16 {
	assert(idx <= n.nkeys(), "kv index out of range")
	return Header + 8*n.nkeys() + 2*n.nkeys() + n.getOffset(idx)
}

func (n BNode) getKey(idx uint16) []byte {
	assert(idx < n.nkeys(), "key index out of range")
	pos := n.kvPos(idx)
	klen := binary.LittleEndian.Uint16(n[pos:])
	return n[pos+4:][:klen]
}

func (n BNode) getVal(idx uint16) []byte {
	assert(idx < n.nkeys(), "val index out of range")
	pos := n.kvPos(idx)
	klen := binary.LittleEndian.Uint16(n[pos+0:])
	vlen := binary.LittleEndian.Uint16(n[pos+2:])
	return n[pos+4+klen:][:vlen]
}

// nbytes returns the number of bytes actually used by the node.
func (n BNode) nbytes() uint16 { return n.kvPos(n.nkeys()) }

// nodeLookupLE returns the index of the last key that is <= key. The first
// key of a node is always a lower bound (copied from the parent) so the
// result is always a valid position.
func nodeLookupLE(n BNode, key []byte) uint16 {
	nkeys := n.nkeys()
	found := uint16(0)
	// binary search over keys 1..nkeys-1 (key 0 is the lower bound)
	lo, hi := uint16(1), nkeys
	for lo < hi {
		mid := (lo + hi) / 2
		if compare(n.getKey(mid), key) <= 0 {
			found = mid
			lo = mid + 1
		} else {
			hi = mid
		}
	}
	return found
}

// nodeAppendKV writes a single key-value pair at position idx.
func nodeAppendKV(n BNode, idx uint16, ptr uint64, key, val []byte) {
	n.setPtr(idx, ptr)
	pos := n.kvPos(idx)
	binary.LittleEndian.PutUint16(n[pos+0:], uint16(len(key)))
	binary.LittleEndian.PutUint16(n[pos+2:], uint16(len(val)))
	copy(n[pos+4:], key)
	copy(n[pos+4+uint16(len(key)):], val)
	n.setOffset(idx+1, n.getOffset(idx)+4+uint16(len(key)+len(val)))
}

// nodeAppendRange copies n pairs from old[srcOld:] into new[dstNew:].
func nodeAppendRange(dst, src BNode, dstNew, srcOld, n uint16) {
	for i := uint16(0); i < n; i++ {
		nodeAppendKV(dst, dstNew+i, src.getPtr(srcOld+i), src.getKey(srcOld+i), src.getVal(srcOld+i))
	}
}

// leafInsert copies old into new while adding a key at idx.
func leafInsert(dst, src BNode, idx uint16, key, val []byte) {
	dst.setHeader(BNodeLeaf, src.nkeys()+1)
	nodeAppendRange(dst, src, 0, 0, idx)
	nodeAppendKV(dst, idx, 0, key, val)
	nodeAppendRange(dst, src, idx+1, idx, src.nkeys()-idx)
}

// leafUpdate copies old into new while replacing the value at idx.
func leafUpdate(dst, src BNode, idx uint16, key, val []byte) {
	dst.setHeader(BNodeLeaf, src.nkeys())
	nodeAppendRange(dst, src, 0, 0, idx)
	nodeAppendKV(dst, idx, 0, key, val)
	nodeAppendRange(dst, src, idx+1, idx+1, src.nkeys()-(idx+1))
}

// leafDelete copies old into new while removing the key at idx.
func leafDelete(dst, src BNode, idx uint16) {
	dst.setHeader(BNodeLeaf, src.nkeys()-1)
	nodeAppendRange(dst, src, 0, 0, idx)
	nodeAppendRange(dst, src, idx, idx+1, src.nkeys()-(idx+1))
}

func compare(a, b []byte) int {
	for i := 0; i < len(a) && i < len(b); i++ {
		if a[i] != b[i] {
			if a[i] < b[i] {
				return -1
			}
			return 1
		}
	}
	switch {
	case len(a) < len(b):
		return -1
	case len(a) > len(b):
		return 1
	}
	return 0
}

func assert(cond bool, msg string) {
	if !cond {
		panic("btree: " + msg)
	}
}
