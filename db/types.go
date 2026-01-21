// Package db layers tables, typed rows and secondary indexes on top of the
// key-value store.
//
// Every row is stored under a key built from a 4-byte table prefix followed
// by the order-preserving encoding of its primary key columns. Secondary
// indexes use their own prefix and append the primary key so that entries
// are unique and point back at the row.
package db

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"strconv"
)

// Column types.
const (
	TypeError = 0
	TypeBytes = 1
	TypeInt64 = 2
)

// Value is a typed cell.
type Value struct {
	Type uint32
	I64  int64
	Str  []byte
}

// Int64 constructs an integer value.
func Int64(v int64) Value { return Value{Type: TypeInt64, I64: v} }

// Bytes constructs a string/bytes value.
func Bytes(v []byte) Value { return Value{Type: TypeBytes, Str: v} }

// String renders a value for display.
func (v Value) String() string {
	switch v.Type {
	case TypeInt64:
		return strconv.FormatInt(v.I64, 10)
	case TypeBytes:
		return string(v.Str)
	}
	return "<error>"
}

// Any converts a value for JSON output.
func (v Value) Any() any {
	if v.Type == TypeInt64 {
		return v.I64
	}
	return string(v.Str)
}

// Compare orders two values of the same type.
func (v Value) Compare(o Value) int {
	if v.Type == TypeInt64 {
		switch {
		case v.I64 < o.I64:
			return -1
		case v.I64 > o.I64:
			return 1
		}
		return 0
	}
	return bytes.Compare(v.Str, o.Str)
}

// TypeName returns the SQL name of a column type.
func TypeName(t uint32) string {
	switch t {
	case TypeBytes:
		return "STRING"
	case TypeInt64:
		return "INT64"
	}
	return "?"
}

// Record is a row or a partial row (for lookups).
type Record struct {
	Cols []string
	Vals []Value
}

// AddStr appends a string column.
func (r *Record) AddStr(col string, val []byte) *Record {
	r.Cols = append(r.Cols, col)
	r.Vals = append(r.Vals, Bytes(val))
	return r
}

// AddInt64 appends an integer column.
func (r *Record) AddInt64(col string, val int64) *Record {
	r.Cols = append(r.Cols, col)
	r.Vals = append(r.Vals, Int64(val))
	return r
}

// Get returns the value of a column, if present.
func (r *Record) Get(col string) *Value {
	for i, c := range r.Cols {
		if c == col {
			return &r.Vals[i]
		}
	}
	return nil
}

// TableDef describes a table.
type TableDef struct {
	Name    string     `json:"name"`
	Types   []uint32   `json:"types"`
	Cols    []string   `json:"cols"`
	PKeys   int        `json:"pkeys"`   // the first PKeys columns form the primary key
	Indexes [][]string `json:"indexes"` // secondary indexes
	// allocated by the database
	Prefix        uint32   `json:"prefix"`
	IndexPrefixes []uint32 `json:"index_prefixes"`
}

func (def *TableDef) colIndex(col string) int {
	for i, c := range def.Cols {
		if c == col {
			return i
		}
	}
	return -1
}

func (def *TableDef) validate() error {
	if def.Name == "" || def.Name[0] == '@' {
		return errors.New("db: invalid table name")
	}
	if len(def.Cols) == 0 || len(def.Cols) != len(def.Types) {
		return errors.New("db: columns and types do not match")
	}
	if def.PKeys < 1 || def.PKeys > len(def.Cols) {
		return errors.New("db: bad primary key")
	}
	seen := map[string]bool{}
	for i, c := range def.Cols {
		if seen[c] {
			return fmt.Errorf("db: duplicate column %q", c)
		}
		seen[c] = true
		if def.Types[i] != TypeBytes && def.Types[i] != TypeInt64 {
			return fmt.Errorf("db: bad type for column %q", c)
		}
	}
	for _, idx := range def.Indexes {
		if len(idx) == 0 {
			return errors.New("db: empty index")
		}
		for _, c := range idx {
			if !seen[c] {
				return fmt.Errorf("db: index on unknown column %q", c)
			}
		}
	}
	return nil
}

// --- order-preserving key encoding ---

// encodeKey builds prefix + encoded values.
func encodeKey(prefix uint32, vals []Value) []byte {
	out := make([]byte, 4, 64)
	binary.BigEndian.PutUint32(out, prefix)
	return encodeValues(out, vals)
}

func encodeValues(out []byte, vals []Value) []byte {
	for _, v := range vals {
		switch v.Type {
		case TypeInt64:
			var buf [8]byte
			// flip the sign bit so that negatives sort before positives
			binary.BigEndian.PutUint64(buf[:], uint64(v.I64)+(1<<63))
			out = append(out, buf[:]...)
		case TypeBytes:
			out = append(out, escapeString(v.Str)...)
			out = append(out, 0) // terminator
		default:
			panic("db: encoding value of unknown type")
		}
	}
	return out
}

// escapeString replaces 0x00 and 0x01 so that 0x00 can terminate strings
// while preserving ordering: "\x00" -> "\x01\x01", "\x01" -> "\x01\x02".
func escapeString(in []byte) []byte {
	zeros := bytes.Count(in, []byte{0})
	ones := bytes.Count(in, []byte{1})
	if zeros+ones == 0 {
		return in
	}
	out := make([]byte, 0, len(in)+zeros+ones)
	for _, ch := range in {
		if ch <= 1 {
			out = append(out, 0x01, ch+1)
		} else {
			out = append(out, ch)
		}
	}
	return out
}

func unescapeString(in []byte) []byte {
	if bytes.IndexByte(in, 1) < 0 {
		return in
	}
	out := make([]byte, 0, len(in))
	for i := 0; i < len(in); i++ {
		if in[i] == 1 {
			i++
			out = append(out, in[i]-1)
		} else {
			out = append(out, in[i])
		}
	}
	return out
}

// decodeValues parses values of the given types from a key (after prefix).
func decodeValues(in []byte, types []uint32) ([]Value, []byte, error) {
	vals := make([]Value, len(types))
	for i, t := range types {
		switch t {
		case TypeInt64:
			if len(in) < 8 {
				return nil, nil, errors.New("db: truncated int64 key")
			}
			vals[i] = Int64(int64(binary.BigEndian.Uint64(in) - (1 << 63)))
			in = in[8:]
		case TypeBytes:
			end := bytes.IndexByte(in, 0)
			if end < 0 {
				return nil, nil, errors.New("db: unterminated string key")
			}
			vals[i] = Bytes(unescapeString(in[:end]))
			in = in[end+1:]
		}
	}
	return vals, in, nil
}

// --- row value encoding (non-key columns) ---

func encodeRow(vals []Value) []byte {
	var out []byte
	for _, v := range vals {
		switch v.Type {
		case TypeInt64:
			var buf [8]byte
			binary.LittleEndian.PutUint64(buf[:], uint64(v.I64))
			out = append(out, buf[:]...)
		case TypeBytes:
			var buf [4]byte
			binary.LittleEndian.PutUint32(buf[:], uint32(len(v.Str)))
			out = append(out, buf[:]...)
			out = append(out, v.Str...)
		}
	}
	return out
}

func decodeRow(in []byte, types []uint32) ([]Value, error) {
	vals := make([]Value, len(types))
	for i, t := range types {
		switch t {
		case TypeInt64:
			if len(in) < 8 {
				return nil, errors.New("db: truncated row")
			}
			vals[i] = Int64(int64(binary.LittleEndian.Uint64(in)))
			in = in[8:]
		case TypeBytes:
			if len(in) < 4 {
				return nil, errors.New("db: truncated row")
			}
			n := binary.LittleEndian.Uint32(in)
			in = in[4:]
			if uint32(len(in)) < n {
				return nil, errors.New("db: truncated row")
			}
			vals[i] = Bytes(append([]byte(nil), in[:n]...))
			in = in[n:]
		}
	}
	return vals, nil
}
