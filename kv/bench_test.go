package kv

import (
	"fmt"
	"path/filepath"
	"testing"
)

func BenchmarkSet(b *testing.B) {
	db, err := Open(filepath.Join(b.TempDir(), "bench.db"))
	if err != nil {
		b.Fatal(err)
	}
	defer db.Close()
	val := make([]byte, 100)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		db.Set([]byte(fmt.Sprintf("key%010d", i)), val)
	}
}

func BenchmarkBatchSet(b *testing.B) {
	db, err := Open(filepath.Join(b.TempDir(), "bench.db"))
	if err != nil {
		b.Fatal(err)
	}
	defer db.Close()
	val := make([]byte, 100)
	b.ResetTimer()
	tx := db.Begin()
	for i := 0; i < b.N; i++ {
		tx.Set([]byte(fmt.Sprintf("key%010d", i)), val)
		if i%1000 == 999 {
			if err := tx.Commit(); err != nil {
				b.Fatal(err)
			}
			tx = db.Begin()
		}
	}
	tx.Commit()
}

func BenchmarkGet(b *testing.B) {
	db, err := Open(filepath.Join(b.TempDir(), "bench.db"))
	if err != nil {
		b.Fatal(err)
	}
	defer db.Close()
	const n = 100000
	tx := db.Begin()
	for i := 0; i < n; i++ {
		tx.Set([]byte(fmt.Sprintf("key%010d", i)), []byte("value"))
	}
	tx.Commit()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		db.Get([]byte(fmt.Sprintf("key%010d", i%n)))
	}
}
