package btree

import (
	"fmt"
	"math/rand"
	"sort"
	"testing"
	"unsafe"
)

// memTree is an in-memory page store used to exercise the tree logic.
type memTree struct {
	tree  BTree
	ref   map[string]string
	pages map[uint64]BNode
}

func newMemTree() *memTree {
	m := &memTree{ref: map[string]string{}, pages: map[uint64]BNode{}}
	m.tree = BTree{
		Get: func(ptr uint64) []byte {
			node, ok := m.pages[ptr]
			if !ok {
				panic("bad pointer")
			}
			return node
		},
		New: func(node []byte) uint64 {
			if BNode(node).nbytes() > PageSize {
				panic("node too large")
			}
			ptr := uint64(uintptr(unsafe.Pointer(&node[0])))
			if _, dup := m.pages[ptr]; dup {
				panic("duplicate page")
			}
			m.pages[ptr] = node
			return ptr
		},
		Del: func(ptr uint64) {
			if _, ok := m.pages[ptr]; !ok {
				panic("deleting unknown page")
			}
			delete(m.pages, ptr)
		},
	}
	return m
}

func (m *memTree) add(k, v string) {
	m.tree.Insert([]byte(k), []byte(v))
	m.ref[k] = v
}

func (m *memTree) del(k string) bool {
	delete(m.ref, k)
	return m.tree.Delete([]byte(k))
}

// dump walks the tree and returns all pairs in order.
func (m *memTree) dump() ([]string, []string) {
	var keys, vals []string
	var walk func(BNode)
	walk = func(node BNode) {
		n := node.nkeys()
		for i := uint16(0); i < n; i++ {
			if node.btype() == BNodeLeaf {
				keys = append(keys, string(node.getKey(i)))
				vals = append(vals, string(node.getVal(i)))
			} else {
				walk(m.tree.Get(node.getPtr(i)))
			}
		}
	}
	walk(m.tree.Get(m.tree.Root))
	if keys[0] != "" {
		panic("missing sentinel")
	}
	return keys[1:], vals[1:]
}

func (m *memTree) verify(t *testing.T) {
	t.Helper()
	keys, vals := m.dump()
	if len(keys) != len(m.ref) {
		t.Fatalf("size mismatch: tree=%d ref=%d", len(keys), len(m.ref))
	}
	refKeys := make([]string, 0, len(m.ref))
	for k := range m.ref {
		refKeys = append(refKeys, k)
	}
	sort.Strings(refKeys)
	for i, k := range refKeys {
		if keys[i] != k || vals[i] != m.ref[k] {
			t.Fatalf("mismatch at %d: got (%q,%q) want (%q,%q)", i, keys[i], vals[i], k, m.ref[k])
		}
	}
	// every node must respect the size limit and be reachable
	var check func(BNode)
	check = func(node BNode) {
		if node.nbytes() > PageSize {
			t.Fatalf("oversized node")
		}
		if node.btype() == BNodeInternal {
			for i := uint16(0); i < node.nkeys(); i++ {
				kid := BNode(m.tree.Get(node.getPtr(i)))
				if Compare(kid.getKey(0), node.getKey(i)) != 0 {
					t.Fatalf("child key does not match parent")
				}
				check(kid)
			}
		}
	}
	check(m.tree.Get(m.tree.Root))
}

func TestBasic(t *testing.T) {
	m := newMemTree()
	m.add("k", "v")
	m.verify(t)
	for i := 0; i < 250000; i++ {
		key := fmt.Sprintf("key%d", rand.Intn(1000))
		val := fmt.Sprintf("vvv%d", rand.Intn(1000))
		m.add(key, val)
		if i%20000 == 0 {
			m.verify(t)
		}
	}
	m.verify(t)
	// delete everything
	for i := 0; i < 2000; i++ {
		m.del(fmt.Sprintf("key%d", i))
	}
	m.del("k")
	if len(m.pages) != 1 {
		t.Fatalf("leaked pages: %d", len(m.pages))
	}
}

func TestLargeValues(t *testing.T) {
	m := newMemTree()
	for i := 0; i < 200; i++ {
		key := fmt.Sprintf("k%03d", i)
		m.add(key, string(make([]byte, MaxValSize-rand.Intn(200))))
		m.verify(t)
	}
	for i := 0; i < 200; i += 2 {
		if !m.del(fmt.Sprintf("k%03d", i)) {
			t.Fatalf("delete failed")
		}
		m.verify(t)
	}
}

func TestGet(t *testing.T) {
	m := newMemTree()
	for i := 0; i < 5000; i++ {
		m.add(fmt.Sprintf("%06d", i), fmt.Sprintf("v%d", i))
	}
	for i := 0; i < 5000; i++ {
		v, ok := m.tree.Lookup([]byte(fmt.Sprintf("%06d", i)))
		if !ok || string(v) != fmt.Sprintf("v%d", i) {
			t.Fatalf("get %d failed", i)
		}
	}
	if _, ok := m.tree.Lookup([]byte("nope")); ok {
		t.Fatalf("found missing key")
	}
}
