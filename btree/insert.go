package btree

// BTree is a copy-on-write B+tree. It does not manage pages itself; instead
// the caller supplies three callbacks so the same tree code can run purely
// in memory (tests) or on top of a disk-backed page store.
type BTree struct {
	// Root is the page number of the root node, 0 for an empty tree.
	Root uint64
	// Get dereferences a page number.
	Get func(uint64) []byte
	// New allocates a page for a node and returns its number.
	New func([]byte) uint64
	// Del frees a page.
	Del func(uint64)
}

// nodeSplit2 splits an oversized node into two so that the left half fits in
// a page. The split point is found by walking backwards from the end.
func nodeSplit2(left, right, old BNode) {
	assert(old.nkeys() >= 2, "cannot split a node with < 2 keys")

	// initial guess: split in the middle
	nleft := old.nkeys() / 2

	// the left half must fit
	leftBytes := func() uint16 {
		return Header + 8*nleft + 2*nleft + old.getOffset(nleft)
	}
	for leftBytes() > PageSize {
		nleft--
	}
	assert(nleft >= 1, "left split too small")

	// the right half must fit too
	rightBytes := func() uint16 {
		return old.nbytes() - leftBytes() + Header
	}
	for rightBytes() > PageSize {
		nleft++
	}
	assert(nleft < old.nkeys(), "right split too small")
	nright := old.nkeys() - nleft

	left.setHeader(old.btype(), nleft)
	right.setHeader(old.btype(), nright)
	nodeAppendRange(left, old, 0, 0, nleft)
	nodeAppendRange(right, old, 0, nleft, nright)
	// the left half may still be too big if the first key is huge, in which
	// case the caller splits again.
	assert(right.nbytes() <= PageSize, "right half too large")
}

// nodeSplit3 splits a node into at most three nodes. The input node may be
// up to twice the page size (an insert can add at most one max-size pair).
func nodeSplit3(old BNode) (uint16, [3]BNode) {
	if old.nbytes() <= PageSize {
		old = old[:PageSize]
		return 1, [3]BNode{old}
	}
	left := BNode(make([]byte, 2*PageSize)) // may be split again
	right := BNode(make([]byte, PageSize))
	nodeSplit2(left, right, old)
	if left.nbytes() <= PageSize {
		left = left[:PageSize]
		return 2, [3]BNode{left, right}
	}
	leftleft := BNode(make([]byte, PageSize))
	middle := BNode(make([]byte, PageSize))
	nodeSplit2(leftleft, middle, left)
	assert(leftleft.nbytes() <= PageSize, "triple split failed")
	return 3, [3]BNode{leftleft, middle, right}
}

// nodeReplaceKidN replaces the child at idx with one or more new children,
// keyed by their first key.
func nodeReplaceKidN(tree *BTree, dst, src BNode, idx uint16, kids ...BNode) {
	inc := uint16(len(kids))
	dst.setHeader(BNodeInternal, src.nkeys()+inc-1)
	nodeAppendRange(dst, src, 0, 0, idx)
	for i, node := range kids {
		nodeAppendKV(dst, idx+uint16(i), tree.New(node), node.getKey(0), nil)
	}
	nodeAppendRange(dst, src, idx+inc, idx+1, src.nkeys()-(idx+1))
}

// treeInsert inserts a key into a (sub)tree rooted at node. The result may
// be oversized and must be split by the caller.
func treeInsert(tree *BTree, node BNode, key, val []byte) BNode {
	// the result can be bigger than one page
	dst := BNode(make([]byte, 2*PageSize))

	idx := nodeLookupLE(node, key)
	switch node.btype() {
	case BNodeLeaf:
		if compare(key, node.getKey(idx)) == 0 {
			leafUpdate(dst, node, idx, key, val)
		} else {
			leafInsert(dst, node, idx+1, key, val)
		}
	case BNodeInternal:
		nodeInsert(tree, dst, node, idx, key, val)
	default:
		panic("btree: bad node type")
	}
	return dst
}

// nodeInsert recurses into child idx then patches the parent.
func nodeInsert(tree *BTree, dst, node BNode, idx uint16, key, val []byte) {
	kptr := node.getPtr(idx)
	knode := treeInsert(tree, tree.Get(kptr), key, val)
	nsplit, split := nodeSplit3(knode)
	tree.Del(kptr)
	nodeReplaceKidN(tree, dst, node, idx, split[:nsplit]...)
}

// Insert adds or replaces a key.
func (tree *BTree) Insert(key, val []byte) {
	assert(len(key) != 0, "empty key")
	assert(len(key) <= MaxKeySize, "key too large")
	assert(len(val) <= MaxValSize, "value too large")

	if tree.Root == 0 {
		// create the first node with a sentinel empty key so that
		// nodeLookupLE always finds a position
		root := BNode(make([]byte, PageSize))
		root.setHeader(BNodeLeaf, 2)
		nodeAppendKV(root, 0, 0, nil, nil)
		nodeAppendKV(root, 1, 0, key, val)
		tree.Root = tree.New(root)
		return
	}

	node := treeInsert(tree, tree.Get(tree.Root), key, val)
	nsplit, split := nodeSplit3(node)
	tree.Del(tree.Root)
	if nsplit > 1 {
		// the root was split: grow the tree by one level
		root := BNode(make([]byte, PageSize))
		root.setHeader(BNodeInternal, nsplit)
		for i, kid := range split[:nsplit] {
			nodeAppendKV(root, uint16(i), tree.New(kid), kid.getKey(0), nil)
		}
		tree.Root = tree.New(root)
	} else {
		tree.Root = tree.New(split[0])
	}
}

// Lookup finds the value for a key.
func (tree *BTree) Lookup(key []byte) ([]byte, bool) {
	if tree.Root == 0 {
		return nil, false
	}
	node := BNode(tree.Get(tree.Root))
	for {
		idx := nodeLookupLE(node, key)
		switch node.btype() {
		case BNodeLeaf:
			if compare(node.getKey(idx), key) == 0 {
				return node.getVal(idx), true
			}
			return nil, false
		case BNodeInternal:
			node = tree.Get(node.getPtr(idx))
		default:
			panic("btree: bad node type")
		}
	}
}
