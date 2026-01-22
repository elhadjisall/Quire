package db

import (
	"encoding/binary"
	"errors"
	"fmt"

	"github.com/elhadjisall/kvdb/btree"
)

// Scanner describes a range query over the primary key or an index.
//
// Key1 and Key2 are partial records containing a prefix of the key columns.
// Cmp1 is the condition on the starting bound (CmpGE/CmpGT for forward scans,
// CmpLE/CmpLT for reverse) and Cmp2 the condition on the end bound.
type Scanner struct {
	Cmp1 int
	Cmp2 int
	Key1 Record
	Key2 Record

	// internal
	def   *TableDef
	index int // -1 for the primary key
	iter  *btree.Iter
	end   []byte
	r     reader
}

// scanInit validates the scanner and positions the cursor.
func scanInit(r reader, def *TableDef, sc *Scanner) error {
	if !(sc.Cmp1 > 0 && sc.Cmp2 < 0) && !(sc.Cmp1 < 0 && sc.Cmp2 > 0) {
		return errors.New("db: bad scan range")
	}
	// pick a key: the primary key or an index whose leading columns cover
	// both bounds (each bound may be a different length prefix)
	covers := func(key []string) bool {
		return isPrefix(key, sc.Key1.Cols) && isPrefix(key, sc.Key2.Cols)
	}
	sc.index = -1
	if !covers(def.Cols[:def.PKeys]) {
		found := false
		for i, idx := range def.Indexes {
			if covers(idx) {
				sc.index, found = i, true
				break
			}
		}
		if !found {
			return fmt.Errorf("db: no index covers columns %v", sc.Key1.Cols)
		}
	}
	prefix := def.Prefix
	if sc.index >= 0 {
		prefix = def.IndexPrefixes[sc.index]
	}
	// a partial key with an empty column list scans the whole table
	start := encodeKeyPartial(prefix, sc.Key1.Vals, sc.Cmp1)
	sc.end = encodeKeyPartial(prefix, sc.Key2.Vals, sc.Cmp2)
	sc.def, sc.r = def, r
	sc.iter = r.Seek(start, sc.Cmp1)
	return nil
}

// encodeKeyPartial encodes a prefix of a key. For open-ended comparisons
// (GT / LE) the encoded prefix is extended with 0xff so that all keys that
// share the prefix are excluded / included.
func encodeKeyPartial(prefix uint32, vals []Value, cmp int) []byte {
	out := encodeKey(prefix, vals)
	if cmp == btree.CmpGT || cmp == btree.CmpLE {
		out = append(out, 0xff)
	}
	return out
}

func isPrefix(long, short []string) bool {
	if len(short) > len(long) {
		return false
	}
	for i := range short {
		if long[i] != short[i] {
			return false
		}
	}
	return true
}

// Valid reports whether the cursor is inside the range.
func (sc *Scanner) Valid() bool {
	if sc.iter == nil || !sc.iter.Valid() {
		return false
	}
	key, _ := sc.iter.Deref()
	// stay within the table/index prefix
	if binary.BigEndian.Uint32(key) != binary.BigEndian.Uint32(sc.end) {
		return false
	}
	return cmpOK(key, sc.Cmp2, sc.end)
}

func cmpOK(key []byte, cmp int, ref []byte) bool {
	r := btree.Compare(key, ref)
	switch cmp {
	case btree.CmpGE:
		return r >= 0
	case btree.CmpGT:
		return r > 0
	case btree.CmpLT:
		return r < 0
	case btree.CmpLE:
		return r <= 0
	}
	return false
}

// Next advances in the scan direction.
func (sc *Scanner) Next() {
	if sc.Cmp1 > 0 {
		sc.iter.Next()
	} else {
		sc.iter.Prev()
	}
}

// Deref decodes the current row (following the index if necessary).
func (sc *Scanner) Deref(rec *Record) error {
	key, val := sc.iter.Deref()
	def := sc.def
	if sc.index < 0 {
		pk, _, err := decodeValues(key[4:], def.Types[:def.PKeys])
		if err != nil {
			return err
		}
		rest, err := decodeRow(val, def.Types[def.PKeys:])
		if err != nil {
			return err
		}
		rec.Cols = append([]string(nil), def.Cols...)
		rec.Vals = append(pk, rest...)
		return nil
	}
	// index entry: indexed columns followed by the primary key
	idxCols := def.Indexes[sc.index]
	types := make([]uint32, 0, len(idxCols)+def.PKeys)
	for _, c := range idxCols {
		types = append(types, def.Types[def.colIndex(c)])
	}
	types = append(types, def.Types[:def.PKeys]...)
	vals, _, err := decodeValues(key[4:], types)
	if err != nil {
		return err
	}
	rec.Cols = append([]string(nil), def.Cols[:def.PKeys]...)
	rec.Vals = vals[len(idxCols):]
	found, err := dbGet(sc.r, def, rec)
	if err != nil {
		return err
	}
	if !found {
		return errors.New("db: dangling index entry")
	}
	return nil
}
