package db

import (
	"fmt"
	"path/filepath"
	"testing"

	"github.com/elhadjisall/kvdb/btree"
)

func openTemp(t *testing.T) *DB {
	t.Helper()
	db, err := Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func usersTable() *TableDef {
	return &TableDef{
		Name:    "users",
		Types:   []uint32{TypeInt64, TypeBytes, TypeBytes, TypeInt64},
		Cols:    []string{"id", "name", "email", "age"},
		PKeys:   1,
		Indexes: [][]string{{"email"}, {"age", "name"}},
	}
}

func seed(t *testing.T, db *DB, n int) {
	t.Helper()
	tx := db.Begin()
	if err := tx.CreateTable(usersTable()); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < n; i++ {
		rec := (&Record{}).AddInt64("id", int64(i)).
			AddStr("name", []byte(fmt.Sprintf("user%03d", i))).
			AddStr("email", []byte(fmt.Sprintf("u%03d@example.com", i))).
			AddInt64("age", int64(20+i%30))
		if ok, err := tx.Insert("users", *rec); err != nil || !ok {
			t.Fatalf("insert %d: %v", i, err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

func TestCRUD(t *testing.T) {
	db := openTemp(t)
	seed(t, db, 100)

	tx := db.BeginRead()
	rec := (&Record{}).AddInt64("id", 42)
	ok, err := tx.Get("users", rec)
	if err != nil || !ok {
		t.Fatalf("get: %v", err)
	}
	if rec.Get("name").String() != "user042" || rec.Get("age").I64 != 32 {
		t.Fatalf("unexpected row %v", rec)
	}
	tx.Rollback()

	w := db.Begin()
	upd := (&Record{}).AddInt64("id", 42).AddStr("name", []byte("renamed")).
		AddStr("email", []byte("new@example.com")).AddInt64("age", 99)
	if ok, err := w.Update("users", *upd); err != nil || !ok {
		t.Fatalf("update: %v", err)
	}
	if ok, _ := w.Insert("users", *upd); ok {
		t.Fatal("insert should fail on duplicate key")
	}
	if ok, err := w.Delete("users", *(&Record{}).AddInt64("id", 7)); err != nil || !ok {
		t.Fatalf("delete: %v", err)
	}
	if err := w.Commit(); err != nil {
		t.Fatal(err)
	}

	r := db.BeginRead()
	defer r.Rollback()
	if ok, _ := r.Get("users", (&Record{}).AddInt64("id", 7)); ok {
		t.Fatal("row 7 should be gone")
	}
	// the old index entry must be gone and the new one present
	sc := Scanner{Cmp1: btree.CmpGE, Cmp2: btree.CmpLE,
		Key1: *(&Record{}).AddStr("email", []byte("new@example.com")),
		Key2: *(&Record{}).AddStr("email", []byte("new@example.com"))}
	if err := r.Scan("users", &sc); err != nil {
		t.Fatal(err)
	}
	if !sc.Valid() {
		t.Fatal("index lookup failed")
	}
	var got Record
	if err := sc.Deref(&got); err != nil {
		t.Fatal(err)
	}
	if got.Get("id").I64 != 42 {
		t.Fatalf("index pointed at %v", got)
	}
	sc = Scanner{Cmp1: btree.CmpGE, Cmp2: btree.CmpLE,
		Key1: *(&Record{}).AddStr("email", []byte("u042@example.com")),
		Key2: *(&Record{}).AddStr("email", []byte("u042@example.com"))}
	r.Scan("users", &sc)
	if sc.Valid() {
		t.Fatal("stale index entry")
	}
}

func TestRangeScan(t *testing.T) {
	db := openTemp(t)
	seed(t, db, 100)
	r := db.BeginRead()
	defer r.Rollback()

	// primary key range 10 <= id < 20
	sc := Scanner{Cmp1: btree.CmpGE, Cmp2: btree.CmpLT,
		Key1: *(&Record{}).AddInt64("id", 10), Key2: *(&Record{}).AddInt64("id", 20)}
	if err := r.Scan("users", &sc); err != nil {
		t.Fatal(err)
	}
	n := 0
	for ; sc.Valid(); sc.Next() {
		var rec Record
		if err := sc.Deref(&rec); err != nil {
			t.Fatal(err)
		}
		if rec.Get("id").I64 != int64(10+n) {
			t.Fatalf("out of order: %v", rec.Get("id"))
		}
		n++
	}
	if n != 10 {
		t.Fatalf("got %d rows", n)
	}

	// reverse scan over the whole table
	sc = Scanner{Cmp1: btree.CmpLE, Cmp2: btree.CmpGE, Key1: Record{}, Key2: Record{}}
	if err := r.Scan("users", &sc); err != nil {
		t.Fatal(err)
	}
	n = 0
	for ; sc.Valid(); sc.Next() {
		var rec Record
		sc.Deref(&rec)
		if rec.Get("id").I64 != int64(99-n) {
			t.Fatalf("reverse order broken at %d: %v", n, rec.Get("id"))
		}
		n++
	}
	if n != 100 {
		t.Fatalf("reverse scan got %d rows", n)
	}

	// composite index prefix: everyone aged 25
	sc = Scanner{Cmp1: btree.CmpGE, Cmp2: btree.CmpLE,
		Key1: *(&Record{}).AddInt64("age", 25), Key2: *(&Record{}).AddInt64("age", 25)}
	if err := r.Scan("users", &sc); err != nil {
		t.Fatal(err)
	}
	n = 0
	for ; sc.Valid(); sc.Next() {
		var rec Record
		sc.Deref(&rec)
		if rec.Get("age").I64 != 25 {
			t.Fatalf("wrong age %v", rec.Get("age"))
		}
		n++
	}
	if n != 4 {
		t.Fatalf("age index returned %d rows", n)
	}
}

func TestCatalog(t *testing.T) {
	db := openTemp(t)
	seed(t, db, 5)
	tx := db.Begin()
	if err := tx.CreateTable(usersTable()); err == nil {
		t.Fatal("duplicate table should fail")
	}
	if err := tx.CreateTable(&TableDef{Name: "logs", Types: []uint32{TypeInt64, TypeBytes},
		Cols: []string{"ts", "msg"}, PKeys: 1}); err != nil {
		t.Fatal(err)
	}
	defs, _ := tx.Tables()
	if len(defs) != 2 || defs[0].Name != "logs" || defs[1].Name != "users" {
		t.Fatalf("tables: %v", defs)
	}
	if defs[0].Prefix <= defs[1].IndexPrefixes[1] {
		t.Fatal("prefix collision")
	}
	if err := tx.DropTable("users"); err != nil {
		t.Fatal(err)
	}
	tx.Commit()

	r := db.BeginRead()
	defer r.Rollback()
	if _, err := r.Table("users"); err == nil {
		t.Fatal("dropped table still exists")
	}
	stats := db.Stats()
	if stats.Version == 0 {
		t.Fatal("version not advanced")
	}
}

func TestNegativeIntsOrder(t *testing.T) {
	db := openTemp(t)
	tx := db.Begin()
	tx.CreateTable(&TableDef{Name: "t", Types: []uint32{TypeInt64, TypeBytes}, Cols: []string{"k", "v"}, PKeys: 1})
	for _, k := range []int64{-5, 3, -100, 0, 42} {
		tx.Insert("t", *(&Record{}).AddInt64("k", k).AddStr("v", nil))
	}
	tx.Commit()
	r := db.BeginRead()
	defer r.Rollback()
	sc := Scanner{Cmp1: btree.CmpGE, Cmp2: btree.CmpLE}
	r.Scan("t", &sc)
	want := []int64{-100, -5, 0, 3, 42}
	for i := 0; sc.Valid(); sc.Next() {
		var rec Record
		sc.Deref(&rec)
		if rec.Get("k").I64 != want[i] {
			t.Fatalf("order: got %d want %d", rec.Get("k").I64, want[i])
		}
		i++
	}
}
