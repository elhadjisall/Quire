package btree

// TreeStats is a structural summary of a tree. Keys excludes the sentinel
// empty key stored at the start of the leftmost leaf.
type TreeStats struct {
	Height   int
	Internal int
	Leaves   int
	Keys     int
}

// Stats walks the tree from the root.
func (tree *BTree) Stats() TreeStats {
	var s TreeStats
	if tree == nil || tree.Root == 0 || tree.Get == nil {
		return s
	}
	var walk func(uint64, int)
	walk = func(ptr uint64, depth int) {
		page := tree.Get(ptr)
		kind, nkeys, _, ok := Describe(page)
		if !ok {
			return
		}
		if depth > s.Height {
			s.Height = depth
		}
		node := BNode(page)
		switch kind {
		case "leaf":
			s.Leaves++
			for i := uint16(0); i < uint16(nkeys); i++ {
				if len(node.getKey(i)) > 0 {
					s.Keys++
				}
			}
		case "internal":
			s.Internal++
			for i := uint16(0); i < node.nkeys(); i++ {
				walk(node.getPtr(i), depth+1)
			}
		}
	}
	walk(tree.Root, 1)
	return s
}

// Walk visits every node once, root first.
func (tree *BTree) Walk(fn func(ptr uint64, kind string, nkeys, used int)) {
	if tree == nil || tree.Root == 0 || tree.Get == nil || fn == nil {
		return
	}
	var walk func(uint64)
	walk = func(ptr uint64) {
		page := tree.Get(ptr)
		kind, nkeys, used, ok := Describe(page)
		if !ok {
			return
		}
		fn(ptr, kind, nkeys, used)
		if kind != "internal" {
			return
		}
		node := BNode(page)
		for i := uint16(0); i < node.nkeys(); i++ {
			walk(node.getPtr(i))
		}
	}
	walk(tree.Root)
}

// Describe reports the kind and occupancy of a B+tree page.
// ok is false when the page is not a node.
func Describe(page []byte) (kind string, nkeys int, used int, ok bool) {
	if len(page) < Header+10 {
		return "", 0, 0, false
	}
	n := BNode(page)
	switch n.btype() {
	case BNodeLeaf:
		kind = "leaf"
	case BNodeInternal:
		kind = "internal"
	default:
		return "", 0, 0, false
	}
	nk := n.nkeys()
	if nk == 0 {
		return "", 0, 0, false
	}
	// pointer table and offset table must sit inside the page
	table := int(Header) + int(nk)*8 + int(nk)*2
	if table > len(page) {
		return "", 0, 0, false
	}
	prev := uint16(0)
	for i := uint16(1); i <= nk; i++ {
		off := n.getOffset(i)
		if off < prev {
			return "", 0, 0, false
		}
		prev = off
	}
	nb := int(n.nbytes())
	if nb < table || nb > len(page) {
		return "", 0, 0, false
	}
	return kind, int(nk), nb, true
}
