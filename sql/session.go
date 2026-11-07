package sql

import (
	"fmt"
	"time"

	"github.com/elhadjisall/kvdb/db"
)

// Session runs statements and can hold a transaction open across calls.
// With no open transaction and no BEGIN/COMMIT/ROLLBACK, a script still
// runs atomically through Run.
type Session struct {
	db *db.DB
	tx *db.Tx
}

// NewSession binds a session to a database.
func NewSession(database *db.DB) *Session { return &Session{db: database} }

// InTx reports whether a transaction is open.
func (s *Session) InTx() bool { return s.tx != nil }

// Close rolls back a transaction that was left open.
func (s *Session) Close() {
	if s.tx != nil {
		s.tx.Rollback()
		s.tx = nil
	}
}

// Exec runs a script. BEGIN leaves the transaction open until COMMIT,
// ROLLBACK, a failing statement, or Close.
func (s *Session) Exec(script string) ([]*Result, error) {
	stmts, err := Parse(script)
	if err != nil {
		return nil, err
	}
	if s.tx == nil && !hasControl(stmts) {
		return Run(s.db, script)
	}
	var out []*Result
	for _, st := range stmts {
		started := time.Now()
		res, err := s.execOne(st)
		if res != nil {
			res.Millis = float64(time.Since(started).Microseconds()) / 1000
			out = append(out, res)
		}
		if err != nil {
			return out, err
		}
	}
	if out == nil {
		out = []*Result{}
	}
	return out, nil
}

func hasControl(stmts []*Stmt) bool {
	for _, st := range stmts {
		switch st.Kind {
		case StmtBegin, StmtCommit, StmtRollback:
			return true
		}
	}
	return false
}

func (s *Session) execOne(st *Stmt) (*Result, error) {
	switch st.Kind {
	case StmtBegin:
		if s.tx != nil {
			return nil, fmt.Errorf("already inside a transaction")
		}
		s.tx = s.db.Begin()
		return &Result{Statement: "BEGIN", Message: "transaction started"}, nil
	case StmtCommit:
		if s.tx == nil {
			return nil, fmt.Errorf("no transaction in progress")
		}
		err := s.tx.Commit()
		s.tx = nil
		if err != nil {
			return nil, err
		}
		return &Result{Statement: "COMMIT", Message: "transaction committed"}, nil
	case StmtRollback:
		if s.tx == nil {
			return nil, fmt.Errorf("no transaction in progress")
		}
		s.tx.Rollback()
		s.tx = nil
		return &Result{Statement: "ROLLBACK", Message: "transaction rolled back"}, nil
	}

	auto := s.tx == nil
	var tx *db.Tx
	if auto {
		if isRead(st) {
			tx = s.db.BeginRead()
		} else {
			tx = s.db.Begin()
		}
	} else {
		tx = s.tx
	}
	res, err := Exec(tx, st)
	if err != nil {
		if auto {
			tx.Rollback()
		} else {
			s.tx.Rollback()
			s.tx = nil
		}
		return res, err
	}
	if auto {
		if err := tx.Commit(); err != nil {
			return res, err
		}
	}
	return res, nil
}

func isRead(st *Stmt) bool {
	if st.Explain {
		return true
	}
	switch st.Kind {
	case StmtSelect, StmtShowTables, StmtDescribe, StmtShowIndex:
		return true
	}
	return false
}
