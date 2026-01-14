package btree

// Comparison modes for Seek.
const (
	CmpGE = +3 // >=
	CmpGT = +2 // >
	CmpLT = -2 // <
	CmpLE = -3 // <=
)

// Iter is a cursor positioned on a key-value pair. It records the path from
// the root so that it can move to adjacent pairs.
type Iter struct {
	tree *BTree
	path []BNode  // nodes from root to leaf
	pos  []uint16 // index into each node
}

// Valid reports whether the iterator points at a real pair.
func (it *Iter) Valid() bool {
	if len(it.path) == 0 {
		return false
	}
	leaf := it.path[len(it.path)-1]
	idx := it.pos[len(it.pos)-1]
	// the empty key is the sentinel at the very beginning of the tree
	return idx < leaf.nkeys() && len(leaf.getKey(idx)) != 0
}

// Deref returns the current key and value.
func (it *Iter) Deref() ([]byte, []byte) {
	leaf := it.path[len(it.path)-1]
	idx := it.pos[len(it.pos)-1]
	return leaf.getKey(idx), leaf.getVal(idx)
}

// Next moves to the following pair. It is safe to call past the end.
func (it *Iter) Next() { iterNext(it, len(it.path)-1) }

// Prev moves to the preceding pair.
func (it *Iter) Prev() { iterPrev(it, len(it.path)-1) }

// parentExhausted reports whether the level above ran off either end.
func (it *Iter) parentExhausted(level int) bool {
	return it.pos[level-1] >= it.path[level-1].nkeys()
}

func iterPrev(it *Iter, level int) {
	if it.pos[level] > 0 {
		it.pos[level]--
	} else if level > 0 {
		iterPrev(it, level-1) // move to the previous sibling subtree
		if it.parentExhausted(level) {
			it.pos[level] = it.path[level].nkeys() // invalid
			return
		}
		parent := it.path[level-1]
		it.path[level] = it.tree.Get(parent.getPtr(it.pos[level-1]))
		it.pos[level] = it.path[level].nkeys() - 1
	} else {
		it.pos[level]-- // underflow: now invalid
	}
}

func iterNext(it *Iter, level int) {
	if it.pos[level]+1 < it.path[level].nkeys() {
		it.pos[level]++
	} else if level > 0 {
		iterNext(it, level-1) // move to the next sibling subtree
		if it.parentExhausted(level) {
			it.pos[level] = it.path[level].nkeys() // invalid
			return
		}
		parent := it.path[level-1]
		it.path[level] = it.tree.Get(parent.getPtr(it.pos[level-1]))
		it.pos[level] = 0
	} else {
		it.pos[level]++ // past the end: now invalid
	}
}

// seekLE positions the iterator at the last key <= key.
func (tree *BTree) seekLE(key []byte) *Iter {
	it := &Iter{tree: tree}
	for ptr := tree.Root; ptr != 0; {
		node := BNode(tree.Get(ptr))
		idx := nodeLookupLE(node, key)
		it.path = append(it.path, node)
		it.pos = append(it.pos, idx)
		if node.btype() == BNodeInternal {
			ptr = node.getPtr(idx)
		} else {
			ptr = 0
		}
	}
	return it
}

// Seek positions the iterator relative to key using one of the Cmp modes.
func (tree *BTree) Seek(key []byte, cmp int) *Iter {
	it := tree.seekLE(key)
	if tree.Root == 0 {
		return it
	}
	if cmp != CmpLE && it.Valid() {
		cur, _ := it.Deref()
		if !cmpOK(cur, cmp, key) {
			// off by one: move in the requested direction
			if cmp > 0 {
				it.Next()
			} else {
				it.Prev()
			}
		}
	} else if cmp != CmpLE && !it.Valid() {
		// positioned at the sentinel: everything is greater
		if cmp > 0 {
			it.Next()
		}
	}
	return it
}

// SeekFirst positions the iterator on the smallest key.
func (tree *BTree) SeekFirst() *Iter {
	it := tree.seekLE(nil)
	if tree.Root != 0 {
		it.Next() // skip the sentinel
	}
	return it
}

// cmpOK reports whether cur satisfies the relation with ref.
func cmpOK(cur []byte, cmp int, ref []byte) bool {
	r := compare(cur, ref)
	switch cmp {
	case CmpGE:
		return r >= 0
	case CmpGT:
		return r > 0
	case CmpLT:
		return r < 0
	case CmpLE:
		return r <= 0
	}
	panic("btree: bad cmp")
}

// Compare exposes the byte-wise ordering used by the tree.
func Compare(a, b []byte) int { return compare(a, b) }
