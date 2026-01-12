package btree

// nodeMerge concatenates two sibling nodes into one.
func nodeMerge(dst, left, right BNode) {
	dst.setHeader(left.btype(), left.nkeys()+right.nkeys())
	nodeAppendRange(dst, left, 0, 0, left.nkeys())
	nodeAppendRange(dst, right, left.nkeys(), 0, right.nkeys())
	assert(dst.nbytes() <= PageSize, "merged node too large")
}

// nodeReplace2Kid replaces two adjacent children with a single merged one.
func nodeReplace2Kid(dst, src BNode, idx uint16, ptr uint64, key []byte) {
	dst.setHeader(BNodeInternal, src.nkeys()-1)
	nodeAppendRange(dst, src, 0, 0, idx)
	nodeAppendKV(dst, idx, ptr, key, nil)
	nodeAppendRange(dst, src, idx+1, idx+2, src.nkeys()-(idx+2))
}

// shouldMerge decides whether the updated child at idx should be merged with
// a sibling. Returns -1 to merge with the left sibling, +1 for the right, 0
// for no merge.
func shouldMerge(tree *BTree, node BNode, idx uint16, updated BNode) (int, BNode) {
	if updated.nbytes() > PageSize/4 {
		return 0, nil
	}
	if idx > 0 {
		sibling := BNode(tree.Get(node.getPtr(idx - 1)))
		merged := sibling.nbytes() + updated.nbytes() - Header
		if merged <= PageSize {
			return -1, sibling
		}
	}
	if idx+1 < node.nkeys() {
		sibling := BNode(tree.Get(node.getPtr(idx + 1)))
		merged := sibling.nbytes() + updated.nbytes() - Header
		if merged <= PageSize {
			return +1, sibling
		}
	}
	return 0, nil
}

// treeDelete removes a key from the subtree. It returns nil if the key was
// not found, otherwise the (possibly undersized) replacement node.
func treeDelete(tree *BTree, node BNode, key []byte) BNode {
	idx := nodeLookupLE(node, key)
	switch node.btype() {
	case BNodeLeaf:
		if compare(key, node.getKey(idx)) != 0 {
			return nil
		}
		dst := BNode(make([]byte, PageSize))
		leafDelete(dst, node, idx)
		return dst
	case BNodeInternal:
		return nodeDelete(tree, node, idx, key)
	default:
		panic("btree: bad node type")
	}
}

// nodeDelete recurses into the child and rebalances by merging if needed.
func nodeDelete(tree *BTree, node BNode, idx uint16, key []byte) BNode {
	kptr := node.getPtr(idx)
	updated := treeDelete(tree, tree.Get(kptr), key)
	if len(updated) == 0 {
		return nil // not found
	}
	tree.Del(kptr)

	dst := BNode(make([]byte, PageSize))
	dir, sibling := shouldMerge(tree, node, idx, updated)
	switch {
	case dir < 0: // merge with the left sibling
		merged := BNode(make([]byte, PageSize))
		nodeMerge(merged, sibling, updated)
		tree.Del(node.getPtr(idx - 1))
		nodeReplace2Kid(dst, node, idx-1, tree.New(merged), merged.getKey(0))
	case dir > 0: // merge with the right sibling
		merged := BNode(make([]byte, PageSize))
		nodeMerge(merged, updated, sibling)
		tree.Del(node.getPtr(idx + 1))
		nodeReplace2Kid(dst, node, idx, tree.New(merged), merged.getKey(0))
	case dir == 0 && updated.nkeys() == 0:
		// the child became empty and has no siblings to merge with:
		// this only happens when the parent has a single child
		assert(node.nkeys() == 1 && idx == 0, "empty child in populated parent")
		dst.setHeader(BNodeInternal, 0)
	case dir == 0 && updated.nkeys() > 0:
		nodeReplaceKidN(tree, dst, node, idx, updated)
	}
	return dst
}

// Delete removes a key and reports whether it existed.
func (tree *BTree) Delete(key []byte) bool {
	assert(len(key) != 0, "empty key")
	assert(len(key) <= MaxKeySize, "key too large")
	if tree.Root == 0 {
		return false
	}
	updated := treeDelete(tree, tree.Get(tree.Root), key)
	if len(updated) == 0 {
		return false
	}
	tree.Del(tree.Root)
	if updated.btype() == BNodeInternal && updated.nkeys() == 1 {
		// shrink the tree by one level
		tree.Root = updated.getPtr(0)
	} else {
		tree.Root = tree.New(updated)
	}
	return true
}
