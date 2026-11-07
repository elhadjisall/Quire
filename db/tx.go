package db

import (
	"fmt"

	"github.com/elhadjisall/kvdb/kv"
)

// Tx is a database transaction. Read-only transactions take a snapshot and
// never block; read-write transactions are serialised.
type Tx struct {
	db *DB
	rw *kv.Tx
	ro *kv.ReadTx
}

// Begin starts a read-write transaction.
func (db *DB) Begin() *Tx { return &Tx{db: db, rw: db.kv.Begin()} }

// BeginRead starts a read-only snapshot transaction.
func (db *DB) BeginRead() *Tx { return &Tx{db: db, ro: db.kv.BeginRead()} }

// Commit persists the changes (no-op for read-only transactions).
func (tx *Tx) Commit() error {
	if tx.rw != nil {
		err := tx.rw.Commit()
		tx.db.clearCache()
		return err
	}
	tx.ro.Close()
	return nil
}

// Rollback discards the changes.
func (tx *Tx) Rollback() {
	if tx.rw != nil {
		tx.rw.Abort()
		tx.db.clearCache()
		return
	}
	tx.ro.Close()
}

func (tx *Tx) reader() reader {
	if tx.rw != nil {
		return tx.rw
	}
	return tx.ro
}

func (tx *Tx) writer() (*kv.Tx, error) {
	if tx.rw == nil {
		return nil, fmt.Errorf("db: read-only transaction")
	}
	return tx.rw, nil
}

// Table returns a table definition.
func (tx *Tx) Table(name string) (*TableDef, error) {
	def := tx.db.getTableDef(tx.reader(), name)
	if def == nil {
		return nil, fmt.Errorf("db: table %q does not exist", name)
	}
	return def, nil
}

// Tables lists all user tables.
func (tx *Tx) Tables() ([]*TableDef, error) { return tx.db.listTables(tx.reader()) }

// CreateTable creates a table.
func (tx *Tx) CreateTable(def *TableDef) error {
	w, err := tx.writer()
	if err != nil {
		return err
	}
	return tx.db.createTable(w, def)
}

// DropTable deletes a table and all its data.
func (tx *Tx) DropTable(name string) error {
	w, err := tx.writer()
	if err != nil {
		return err
	}
	def, err := tx.Table(name)
	if err != nil {
		return err
	}
	return tx.db.dropTable(w, def)
}

// Get fetches a row by primary key into rec.
func (tx *Tx) Get(table string, rec *Record) (bool, error) {
	def, err := tx.Table(table)
	if err != nil {
		return false, err
	}
	return dbGet(tx.reader(), def, rec)
}

// Insert adds a new row; it fails silently (false) if the key exists.
func (tx *Tx) Insert(table string, rec Record) (bool, error) {
	return tx.update(table, rec, ModeInsertOnly)
}

// Update replaces an existing row.
func (tx *Tx) Update(table string, rec Record) (bool, error) {
	return tx.update(table, rec, ModeUpdateOnly)
}

// Upsert inserts or replaces a row.
func (tx *Tx) Upsert(table string, rec Record) (bool, error) {
	return tx.update(table, rec, ModeUpsert)
}

func (tx *Tx) update(table string, rec Record, mode int) (bool, error) {
	w, err := tx.writer()
	if err != nil {
		return false, err
	}
	def, err := tx.Table(table)
	if err != nil {
		return false, err
	}
	return dbUpdate(w, def, rec, mode)
}

// Delete removes a row by primary key.
func (tx *Tx) Delete(table string, rec Record) (bool, error) {
	w, err := tx.writer()
	if err != nil {
		return false, err
	}
	def, err := tx.Table(table)
	if err != nil {
		return false, err
	}
	return dbDelete(w, def, rec)
}

// Scan starts a range scan.
func (tx *Tx) Scan(table string, sc *Scanner) error {
	def, err := tx.Table(table)
	if err != nil {
		return err
	}
	return scanInit(tx.reader(), def, sc)
}

// CreateIndex builds a secondary index over the rows already in the table.
func (tx *Tx) CreateIndex(table string, cols []string) error {
	w, err := tx.writer()
	if err != nil {
		return err
	}
	def, err := tx.Table(table)
	if err != nil {
		return err
	}
	return tx.db.createIndex(w, def, cols)
}

// DropIndex removes a secondary index and its entries.
func (tx *Tx) DropIndex(table string, cols []string) error {
	w, err := tx.writer()
	if err != nil {
		return err
	}
	def, err := tx.Table(table)
	if err != nil {
		return err
	}
	return tx.db.dropIndex(w, def, cols)
}

// Indexes lists the primary key and every secondary index.
// An empty table name lists every user table.
func (tx *Tx) Indexes(table string) ([]IndexInfo, error) {
	if table != "" {
		def, err := tx.Table(table)
		if err != nil {
			return nil, err
		}
		return listIndexes([]*TableDef{def}), nil
	}
	defs, err := tx.Tables()
	if err != nil {
		return nil, err
	}
	return listIndexes(defs), nil
}

// IndexEntries reads the ordered keys of a primary key or secondary index.
// limit <= 0 means no limit.
func (tx *Tx) IndexEntries(table string, cols []string, limit int) ([]string, [][]Value, error) {
	def, err := tx.Table(table)
	if err != nil {
		return nil, nil, err
	}
	return indexEntries(tx.reader(), def, cols, limit)
}
