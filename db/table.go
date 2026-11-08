package db

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"sync"

	"github.com/elhadjisall/kvdb/btree"
	"github.com/elhadjisall/kvdb/kv"
)

// Internal tables. @meta holds the next free prefix, @table holds table
// definitions keyed by name.
var tdefMeta = &TableDef{
	Name: "@meta", Prefix: 1,
	Types: []uint32{TypeBytes, TypeBytes}, Cols: []string{"key", "val"}, PKeys: 1,
}

var tdefTable = &TableDef{
	Name: "@table", Prefix: 2,
	Types: []uint32{TypeBytes, TypeBytes}, Cols: []string{"name", "def"}, PKeys: 1,
}

const tablePrefixMin = 100

// ErrNotFound is returned when a row or table does not exist.
var ErrNotFound = errors.New("db: not found")

// DB is a relational layer over a KV file.
type DB struct {
	Path string
	kv   *kv.KV

	mu     sync.RWMutex
	tables map[string]*TableDef // definition cache
}

// Open opens or creates a database.
func Open(path string) (*DB, error) {
	store, err := kv.Open(path)
	if err != nil {
		return nil, err
	}
	return &DB{Path: path, kv: store, tables: map[string]*TableDef{}}, nil
}

// Close flushes and closes the file.
func (db *DB) Close() error { return db.kv.Close() }

// Stats exposes storage statistics.
func (db *DB) Stats() kv.Stats { return db.kv.Stats() }

// PageMap classifies the pages of the last committed version.
func (db *DB) PageMap() kv.PageMap { return db.kv.PageMap() }

// reader abstracts over read-only and read-write transactions.
type reader interface {
	Get(key []byte) ([]byte, bool)
	Seek(key []byte, cmp int) *btree.Iter
}

// getTableDef loads a table definition through the cache.
func (db *DB) getTableDef(r reader, name string) *TableDef {
	switch name {
	case "@meta":
		return tdefMeta
	case "@table":
		return tdefTable
	}
	db.mu.RLock()
	def, ok := db.tables[name]
	db.mu.RUnlock()
	if ok {
		return def
	}
	rec := (&Record{}).AddStr("name", []byte(name))
	found, err := dbGet(r, tdefTable, rec)
	if err != nil || !found {
		return nil
	}
	def = &TableDef{}
	if err := json.Unmarshal(rec.Get("def").Str, def); err != nil {
		return nil
	}
	db.mu.Lock()
	db.tables[name] = def
	db.mu.Unlock()
	return def
}

// checkRecord validates a record against a table and reorders its values to
// match the column order. n is the number of leading columns required.
func checkRecord(def *TableDef, rec Record, n int) ([]Value, error) {
	vals := make([]Value, len(def.Cols))
	for i, col := range rec.Cols {
		idx := def.colIndex(col)
		if idx < 0 {
			return nil, fmt.Errorf("db: unknown column %q", col)
		}
		if rec.Vals[i].Type != def.Types[idx] {
			return nil, fmt.Errorf("db: column %q expects %s", col, TypeName(def.Types[idx]))
		}
		vals[idx] = rec.Vals[i]
	}
	for i := 0; i < n; i++ {
		if vals[i].Type == TypeError {
			return nil, fmt.Errorf("db: missing column %q", def.Cols[i])
		}
	}
	return vals, nil
}

// dbGet looks up a row by primary key and fills in the remaining columns.
func dbGet(r reader, def *TableDef, rec *Record) (bool, error) {
	vals, err := checkRecord(def, *rec, def.PKeys)
	if err != nil {
		return false, err
	}
	key := encodeKey(def.Prefix, vals[:def.PKeys])
	val, ok := r.Get(key)
	if !ok {
		return false, nil
	}
	rest, err := decodeRow(val, def.Types[def.PKeys:])
	if err != nil {
		return false, err
	}
	rec.Cols = append([]string(nil), def.Cols...)
	rec.Vals = append(vals[:def.PKeys:def.PKeys], rest...)
	return true, nil
}

// Update modes.
const (
	ModeUpsert     = 0 // insert or replace
	ModeUpdateOnly = 1 // update an existing row
	ModeInsertOnly = 2 // add a new row
)

// dbUpdate writes a row and maintains its indexes. It reports whether the
// row was written.
func dbUpdate(tx *kv.Tx, def *TableDef, rec Record, mode int) (bool, error) {
	vals, err := checkRecord(def, rec, len(def.Cols))
	if err != nil {
		return false, err
	}
	key := encodeKey(def.Prefix, vals[:def.PKeys])
	old, exists := tx.Get(key)
	switch {
	case mode == ModeUpdateOnly && !exists:
		return false, nil
	case mode == ModeInsertOnly && exists:
		return false, nil
	}
	if exists {
		oldVals, err := decodeRow(old, def.Types[def.PKeys:])
		if err != nil {
			return false, err
		}
		full := append(append([]Value(nil), vals[:def.PKeys]...), oldVals...)
		indexOp(tx, def, full, true)
	}
	tx.Set(key, encodeRow(vals[def.PKeys:]))
	indexOp(tx, def, vals, false)
	return true, nil
}

// dbDelete removes a row by primary key.
func dbDelete(tx *kv.Tx, def *TableDef, rec Record) (bool, error) {
	vals, err := checkRecord(def, rec, def.PKeys)
	if err != nil {
		return false, err
	}
	key := encodeKey(def.Prefix, vals[:def.PKeys])
	old, ok := tx.Get(key)
	if !ok {
		return false, nil
	}
	oldVals, err := decodeRow(old, def.Types[def.PKeys:])
	if err != nil {
		return false, err
	}
	full := append(append([]Value(nil), vals[:def.PKeys]...), oldVals...)
	indexOp(tx, def, full, true)
	return tx.Del(key), nil
}

// indexKey builds the key for one secondary index entry.
func indexKey(def *TableDef, i int, vals []Value) []byte {
	cols := make([]Value, 0, len(def.Indexes[i])+def.PKeys)
	for _, c := range def.Indexes[i] {
		cols = append(cols, vals[def.colIndex(c)])
	}
	cols = append(cols, vals[:def.PKeys]...)
	return encodeKey(def.IndexPrefixes[i], cols)
}

// indexOp adds or removes all index entries for a full row.
func indexOp(tx *kv.Tx, def *TableDef, vals []Value, del bool) {
	for i := range def.Indexes {
		key := indexKey(def, i, vals)
		if del {
			tx.Del(key)
		} else {
			tx.Set(key, nil)
		}
	}
}

// createTable allocates prefixes and stores the definition.
func (db *DB) createTable(tx *kv.Tx, def *TableDef) error {
	if err := def.validate(); err != nil {
		return err
	}
	exists := (&Record{}).AddStr("name", []byte(def.Name))
	if ok, _ := dbGet(tx, tdefTable, exists); ok {
		return fmt.Errorf("db: table %q already exists", def.Name)
	}
	base, err := db.allocPrefix(tx, 1+uint32(len(def.Indexes)))
	if err != nil {
		return err
	}
	def.Prefix = base
	def.IndexPrefixes = make([]uint32, len(def.Indexes))
	for i := range def.Indexes {
		def.IndexPrefixes[i] = base + 1 + uint32(i)
	}
	encoded, _ := json.Marshal(def)
	row := (&Record{}).AddStr("name", []byte(def.Name)).AddStr("def", encoded)
	_, err = dbUpdate(tx, tdefTable, *row, ModeInsertOnly)
	return err
}

// dropTable removes all rows, index entries and the definition.
func (db *DB) dropTable(tx *kv.Tx, def *TableDef) error {
	prefixes := append([]uint32{def.Prefix}, def.IndexPrefixes...)
	for _, p := range prefixes {
		deletePrefix(tx, p)
	}
	rec := (&Record{}).AddStr("name", []byte(def.Name))
	if _, err := dbDelete(tx, tdefTable, *rec); err != nil {
		return err
	}
	db.mu.Lock()
	delete(db.tables, def.Name)
	db.mu.Unlock()
	return nil
}

// listTables returns all user table definitions.
func (db *DB) listTables(r reader) ([]*TableDef, error) {
	var defs []*TableDef
	start := make([]byte, 4)
	binary.BigEndian.PutUint32(start, tdefTable.Prefix)
	for it := r.Seek(start, btree.CmpGE); it.Valid(); it.Next() {
		k, v := it.Deref()
		if binary.BigEndian.Uint32(k) != tdefTable.Prefix {
			break
		}
		vals, err := decodeRow(v, tdefTable.Types[1:])
		if err != nil {
			return nil, err
		}
		def := &TableDef{}
		if err := json.Unmarshal(vals[0].Str, def); err != nil {
			return nil, err
		}
		defs = append(defs, def)
	}
	return defs, nil
}
