package kv

import (
	"fmt"
	"path/filepath"
	"testing"

	"github.com/elhadjisall/kvdb/btree"
)

func openTemp(t *testing.T) *KV {
	t.Helper()
	db, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func TestSetGetPersist(t *testing.T) {
	path := filepath.Join(t.TempDir(), "p.db")
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2000; i++ {
		if err := db.Set([]byte(fmt.Sprintf("k%05d", i)), []byte(fmt.Sprintf("v%d", i))); err != nil {
			t.Fatal(err)
		}
	}
	db.Close()

	db, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for i := 0; i < 2000; i++ {
		v, ok := db.Get([]byte(fmt.Sprintf("k%05d", i)))
		if !ok || string(v) != fmt.Sprintf("v%d", i) {
			t.Fatalf("lost key %d after reopen", i)
		}
	}
	if _, ok := db.Get([]byte("missing")); ok {
		t.Fatal("found missing key")
	}
}

func TestDeleteAndReuse(t *testing.T) {
	db := openTemp(t)
	for i := 0; i < 3000; i++ {
		db.Set([]byte(fmt.Sprintf("k%05d", i)), make([]byte, 500))
	}
	before := db.Stats().Pages
	for i := 0; i < 3000; i++ {
		ok, err := db.Del([]byte(fmt.Sprintf("k%05d", i)))
		if err != nil || !ok {
			t.Fatalf("delete %d: ok=%v err=%v", i, ok, err)
		}
	}
	// re-insert: pages must come from the free list, not the end of file
	for i := 0; i < 3000; i++ {
		db.Set([]byte(fmt.Sprintf("k%05d", i)), make([]byte, 500))
	}
	after := db.Stats()
	if after.Pages > before+before/4 {
		t.Fatalf("free pages were not reused: before=%d after=%d", before, after.Pages)
	}
}

func TestTransactionAbort(t *testing.T) {
	db := openTemp(t)
	db.Set([]byte("a"), []byte("1"))

	tx := db.Begin()
	tx.Set([]byte("a"), []byte("2"))
	tx.Set([]byte("b"), []byte("3"))
	if v, _ := tx.Get([]byte("a")); string(v) != "2" {
		t.Fatal("tx should see its own write")
	}
	tx.Abort()

	if v, _ := db.Get([]byte("a")); string(v) != "1" {
		t.Fatal("abort did not discard the change")
	}
	if _, ok := db.Get([]byte("b")); ok {
		t.Fatal("abort leaked a key")
	}
}

func TestSnapshotIsolation(t *testing.T) {
	db := openTemp(t)
	for i := 0; i < 100; i++ {
		db.Set([]byte(fmt.Sprintf("k%03d", i)), []byte("old"))
	}
	r := db.BeginRead()
	defer r.Close()

	// writer churns the tree while the reader holds a snapshot
	for round := 0; round < 5; round++ {
		tx := db.Begin()
		for i := 0; i < 100; i++ {
			tx.Set([]byte(fmt.Sprintf("k%03d", i)), []byte(fmt.Sprintf("new%d", round)))
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 100; i++ {
		if v, _ := r.Get([]byte(fmt.Sprintf("k%03d", i))); string(v) != "old" {
			t.Fatalf("snapshot changed under the reader: %s", v)
		}
	}
	if v, _ := db.Get([]byte("k000")); string(v) != "new4" {
		t.Fatalf("latest read got %s", v)
	}
}

func TestRangeScan(t *testing.T) {
	db := openTemp(t)
	tx := db.Begin()
	for i := 0; i < 500; i++ {
		tx.Set([]byte(fmt.Sprintf("user:%04d", i)), []byte("x"))
	}
	tx.Set([]byte("zzz"), []byte("end"))
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	r := db.BeginRead()
	defer r.Close()
	it := r.Seek([]byte("user:0100"), btree.CmpGE)
	n := 0
	for it.Valid() {
		k, _ := it.Deref()
		if string(k) >= "user:0200" {
			break
		}
		n++
		it.Next()
	}
	if n != 100 {
		t.Fatalf("range scan returned %d keys", n)
	}
}

func TestPageMap(t *testing.T) {
	db := openTemp(t)
	pm := db.PageMap()
	if pm.Used < 2 || len(pm.Pages) != int(pm.Used) || pm.Pages[0].Kind != "master" {
		t.Fatalf("fresh map: %+v", pm)
	}
	free := 0
	for _, p := range pm.Pages {
		if p.Kind == "freelist" {
			free++
		}
	}
	if free < 1 || pm.FreePages != free {
		t.Fatalf("freelist pages: counted %d field %d", free, pm.FreePages)
	}

	for i := 0; i < 200; i++ {
		if err := db.Set([]byte(fmt.Sprintf("k%04d", i)), []byte("v")); err != nil {
			t.Fatal(err)
		}
	}
	pm = db.PageMap()
	if pm.LeafKeys != 200 || pm.Leaves < 1 || pm.Height < 1 {
		t.Fatalf("after insert: height=%d leaves=%d keys=%d", pm.Height, pm.Leaves, pm.LeafKeys)
	}
	leaves := 0
	for _, p := range pm.Pages {
		if p.Kind == "leaf" {
			leaves++
			if p.Keys < 1 || p.Used <= 0 {
				t.Fatalf("leaf page %+v", p)
			}
		}
	}
	if leaves != pm.Leaves {
		t.Fatalf("leaf count %d vs %d", leaves, pm.Leaves)
	}
}
