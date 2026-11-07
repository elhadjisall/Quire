package db

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/elhadjisall/kvdb/btree"
	"github.com/elhadjisall/kvdb/kv"
)

// IndexInfo describes the primary key or one secondary index.
type IndexInfo struct {
	Table   string   `json:"table"`
	Columns []string `json:"columns"`
	Kind    string   `json:"kind"`
	Prefix  uint32   `json:"prefix"`
}

func (db *DB) putTable(def *TableDef) {
	db.mu.Lock()
	db.tables[def.Name] = def
	db.mu.Unlock()
}

func (db *DB) clearCache() {
	db.mu.Lock()
	db.tables = map[string]*TableDef{}
	db.mu.Unlock()
}

func cloneDef(def *TableDef) *TableDef {
	cp := *def
	cp.Cols = append([]string(nil), def.Cols...)
	cp.Types = append([]uint32(nil), def.Types...)
	cp.Indexes = make([][]string, len(def.Indexes))
	for i, idx := range def.Indexes {
		cp.Indexes[i] = append([]string(nil), idx...)
	}
	cp.IndexPrefixes = append([]uint32(nil), def.IndexPrefixes...)
	return &cp
}

func cloneVals(vals []Value) []Value {
	out := make([]Value, len(vals))
	for i, v := range vals {
		out[i] = v
		if v.Type == TypeBytes {
			out[i].Str = append([]byte(nil), v.Str...)
		}
	}
	return out
}

func sameCols(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// allocPrefix reserves n consecutive key prefixes and returns the first.
func (db *DB) allocPrefix(tx *kv.Tx, n uint32) (uint32, error) {
	if n == 0 {
		return 0, errors.New("db: allocate zero prefixes")
	}
	prefix := uint32(tablePrefixMin)
	meta := (&Record{}).AddStr("key", []byte("next_prefix"))
	ok, err := dbGet(tx, tdefMeta, meta)
	if err != nil {
		return 0, err
	}
	if ok {
		raw := meta.Get("val").Str
		if len(raw) < 4 {
			return 0, errors.New("db: bad prefix counter")
		}
		prefix = binary.LittleEndian.Uint32(raw)
	} else {
		meta.AddStr("val", nil)
	}
	next := make([]byte, 4)
	binary.LittleEndian.PutUint32(next, prefix+n)
	meta.Get("val").Str = next
	if _, err := dbUpdate(tx, tdefMeta, *meta, ModeUpsert); err != nil {
		return 0, err
	}
	return prefix, nil
}

func saveTableDef(tx *kv.Tx, def *TableDef) error {
	encoded, err := json.Marshal(def)
	if err != nil {
		return err
	}
	row := (&Record{}).AddStr("name", []byte(def.Name)).AddStr("def", encoded)
	_, err = dbUpdate(tx, tdefTable, *row, ModeUpsert)
	return err
}

func deletePrefix(tx *kv.Tx, prefix uint32) {
	start := make([]byte, 4)
	binary.BigEndian.PutUint32(start, prefix)
	var keys [][]byte
	for it := tx.Seek(start, btree.CmpGE); it.Valid(); it.Next() {
		k, _ := it.Deref()
		if len(k) < 4 || binary.BigEndian.Uint32(k) != prefix {
			break
		}
		keys = append(keys, append([]byte(nil), k...))
	}
	for _, k := range keys {
		tx.Del(k)
	}
}

func indexOne(tx *kv.Tx, def *TableDef, i int, vals []Value, del bool) {
	key := indexKey(def, i, vals)
	if del {
		tx.Del(key)
	} else {
		tx.Set(key, nil)
	}
}

func collectRows(tx *kv.Tx, def *TableDef) ([][]Value, error) {
	start := make([]byte, 4)
	binary.BigEndian.PutUint32(start, def.Prefix)
	var rows [][]Value
	for it := tx.Seek(start, btree.CmpGE); it.Valid(); it.Next() {
		k, v := it.Deref()
		if len(k) < 4 || binary.BigEndian.Uint32(k) != def.Prefix {
			break
		}
		pk, _, err := decodeValues(k[4:], def.Types[:def.PKeys])
		if err != nil {
			return nil, err
		}
		rest, err := decodeRow(v, def.Types[def.PKeys:])
		if err != nil {
			return nil, err
		}
		rows = append(rows, cloneVals(append(pk, rest...)))
	}
	return rows, nil
}

func (db *DB) createIndex(tx *kv.Tx, cur *TableDef, cols []string) error {
	if len(cols) == 0 {
		return errors.New("db: empty index")
	}
	seen := map[string]bool{}
	for _, c := range cols {
		if cur.colIndex(c) < 0 {
			return fmt.Errorf("db: unknown column %q", c)
		}
		if seen[c] {
			return fmt.Errorf("db: duplicate index column %q", c)
		}
		seen[c] = true
	}
	for _, idx := range cur.Indexes {
		if sameCols(idx, cols) {
			return fmt.Errorf("db: index (%s) already exists", strings.Join(cols, ", "))
		}
	}
	def := cloneDef(cur)
	prefix, err := db.allocPrefix(tx, 1)
	if err != nil {
		return err
	}
	def.Indexes = append(def.Indexes, append([]string(nil), cols...))
	def.IndexPrefixes = append(def.IndexPrefixes, prefix)
	rows, err := collectRows(tx, def)
	if err != nil {
		return err
	}
	idx := len(def.Indexes) - 1
	for _, vals := range rows {
		indexOne(tx, def, idx, vals, false)
	}
	if err := saveTableDef(tx, def); err != nil {
		return err
	}
	db.putTable(def)
	return nil
}

func (db *DB) dropIndex(tx *kv.Tx, cur *TableDef, cols []string) error {
	found := -1
	for i, idx := range cur.Indexes {
		if sameCols(idx, cols) {
			found = i
			break
		}
	}
	if found < 0 {
		return fmt.Errorf("db: index (%s) does not exist", strings.Join(cols, ", "))
	}
	def := cloneDef(cur)
	deletePrefix(tx, def.IndexPrefixes[found])
	def.Indexes = append(def.Indexes[:found], def.Indexes[found+1:]...)
	def.IndexPrefixes = append(def.IndexPrefixes[:found], def.IndexPrefixes[found+1:]...)
	if err := saveTableDef(tx, def); err != nil {
		return err
	}
	db.putTable(def)
	return nil
}

func listIndexes(defs []*TableDef) []IndexInfo {
	var out []IndexInfo
	for _, d := range defs {
		out = append(out, IndexInfo{
			Table:   d.Name,
			Kind:    "PRIMARY",
			Columns: append([]string(nil), d.Cols[:d.PKeys]...),
			Prefix:  d.Prefix,
		})
		for i, cols := range d.Indexes {
			prefix := uint32(0)
			if i < len(d.IndexPrefixes) {
				prefix = d.IndexPrefixes[i]
			}
			out = append(out, IndexInfo{
				Table:   d.Name,
				Kind:    "INDEX",
				Columns: append([]string(nil), cols...),
				Prefix:  prefix,
			})
		}
	}
	return out
}

func indexEntries(r reader, def *TableDef, cols []string, limit int) ([]string, [][]Value, error) {
	if sameCols(cols, def.Cols[:def.PKeys]) {
		return scanPrimaryKeys(r, def, limit)
	}
	found := -1
	for i, idx := range def.Indexes {
		if sameCols(idx, cols) {
			found = i
			break
		}
	}
	if found < 0 {
		return nil, nil, fmt.Errorf("db: index (%s) does not exist", strings.Join(cols, ", "))
	}
	return scanSecondary(r, def, found, limit)
}

func scanPrimaryKeys(r reader, def *TableDef, limit int) ([]string, [][]Value, error) {
	names := append([]string(nil), def.Cols[:def.PKeys]...)
	start := make([]byte, 4)
	binary.BigEndian.PutUint32(start, def.Prefix)
	var rows [][]Value
	for it := r.Seek(start, btree.CmpGE); it.Valid(); it.Next() {
		k, _ := it.Deref()
		if len(k) < 4 || binary.BigEndian.Uint32(k) != def.Prefix {
			break
		}
		pk, _, err := decodeValues(k[4:], def.Types[:def.PKeys])
		if err != nil {
			return nil, nil, err
		}
		rows = append(rows, cloneVals(pk))
		if limit > 0 && len(rows) >= limit {
			break
		}
	}
	return names, rows, nil
}

func scanSecondary(r reader, def *TableDef, idx, limit int) ([]string, [][]Value, error) {
	indexCols := def.Indexes[idx]
	types := make([]uint32, 0, len(indexCols)+def.PKeys)
	for _, c := range indexCols {
		types = append(types, def.Types[def.colIndex(c)])
	}
	for i := 0; i < def.PKeys; i++ {
		types = append(types, def.Types[i])
	}
	inIdx := map[string]bool{}
	names := append([]string(nil), indexCols...)
	for _, c := range indexCols {
		inIdx[c] = true
	}
	for i := 0; i < def.PKeys; i++ {
		if !inIdx[def.Cols[i]] {
			names = append(names, def.Cols[i])
		}
	}

	start := make([]byte, 4)
	binary.BigEndian.PutUint32(start, def.IndexPrefixes[idx])
	var rows [][]Value
	for it := r.Seek(start, btree.CmpGE); it.Valid(); it.Next() {
		k, _ := it.Deref()
		if len(k) < 4 || binary.BigEndian.Uint32(k) != def.IndexPrefixes[idx] {
			break
		}
		vals, _, err := decodeValues(k[4:], types)
		if err != nil {
			return nil, nil, err
		}
		show := cloneVals(vals[:len(indexCols)])
		pk := vals[len(indexCols):]
		for i := 0; i < def.PKeys; i++ {
			if !inIdx[def.Cols[i]] {
				copied := pk[i]
				if copied.Type == TypeBytes {
					copied.Str = append([]byte(nil), copied.Str...)
				}
				show = append(show, copied)
			}
		}
		rows = append(rows, show)
		if limit > 0 && len(rows) >= limit {
			break
		}
	}
	return names, rows, nil
}
