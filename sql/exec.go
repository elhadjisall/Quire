package sql

import (
	"fmt"
	"strings"
	"time"

	"github.com/elhadjisall/kvdb/btree"
	"github.com/elhadjisall/kvdb/db"
)

// Result is the outcome of one statement.
type Result struct {
	Statement string   `json:"statement"`
	Columns   []string `json:"columns,omitempty"`
	Rows      [][]any  `json:"rows,omitempty"`
	Affected  int      `json:"affected"`
	Message   string   `json:"message,omitempty"`
	Plan      string   `json:"plan,omitempty"`
	Millis    float64  `json:"millis"`
}

// Run parses and executes a script inside a single transaction. A read-only
// transaction is used when no statement writes.
func Run(database *db.DB, script string) ([]*Result, error) {
	stmts, err := Parse(script)
	if err != nil {
		return nil, err
	}
	writes := false
	for _, st := range stmts {
		switch st.Kind {
		case StmtSelect, StmtShowTables, StmtDescribe:
		default:
			writes = true
		}
	}
	var tx *db.Tx
	if writes {
		tx = database.Begin()
	} else {
		tx = database.BeginRead()
	}
	var results []*Result
	for _, st := range stmts {
		started := time.Now()
		res, err := Exec(tx, st)
		if err != nil {
			tx.Rollback()
			return results, err
		}
		res.Millis = float64(time.Since(started).Microseconds()) / 1000
		results = append(results, res)
	}
	return results, tx.Commit()
}

// Exec runs a single statement inside tx.
func Exec(tx *db.Tx, st *Stmt) (*Result, error) {
	switch st.Kind {
	case StmtCreate:
		if err := tx.CreateTable(st.Def); err != nil {
			return nil, err
		}
		return &Result{Statement: "CREATE TABLE", Message: fmt.Sprintf("table %s created", st.Table)}, nil
	case StmtDrop:
		if err := tx.DropTable(st.Table); err != nil {
			return nil, err
		}
		return &Result{Statement: "DROP TABLE", Message: fmt.Sprintf("table %s dropped", st.Table)}, nil
	case StmtShowTables:
		return showTables(tx)
	case StmtDescribe:
		return describe(tx, st.Table)
	case StmtCreateIndex:
		if err := tx.CreateIndex(st.Table, st.Cols); err != nil {
			return nil, err
		}
		return &Result{Statement: "CREATE INDEX", Message: fmt.Sprintf("index on %s (%s) created", st.Table, strings.Join(st.Cols, ", "))}, nil
	case StmtDropIndex:
		if err := tx.DropIndex(st.Table, st.Cols); err != nil {
			return nil, err
		}
		return &Result{Statement: "DROP INDEX", Message: fmt.Sprintf("index on %s (%s) dropped", st.Table, strings.Join(st.Cols, ", "))}, nil
	case StmtInsert, StmtUpsert:
		return insert(tx, st)
	case StmtSelect:
		return selectRows(tx, st)
	case StmtUpdate:
		return update(tx, st)
	case StmtDelete:
		return deleteRows(tx, st)
	}
	return nil, fmt.Errorf("unsupported statement")
}

func showTables(tx *db.Tx) (*Result, error) {
	defs, err := tx.Tables()
	if err != nil {
		return nil, err
	}
	res := &Result{Statement: "SHOW TABLES", Columns: []string{"table", "columns", "primary_key", "indexes"}}
	for _, d := range defs {
		var idx []string
		for _, i := range d.Indexes {
			idx = append(idx, "("+strings.Join(i, ", ")+")")
		}
		res.Rows = append(res.Rows, []any{d.Name, int64(len(d.Cols)),
			strings.Join(d.Cols[:d.PKeys], ", "), strings.Join(idx, " ")})
	}
	res.Affected = len(res.Rows)
	return res, nil
}

func describe(tx *db.Tx, table string) (*Result, error) {
	def, err := tx.Table(table)
	if err != nil {
		return nil, err
	}
	res := &Result{Statement: "DESCRIBE", Columns: []string{"column", "type", "key"}}
	for i, c := range def.Cols {
		key := ""
		if i < def.PKeys {
			key = "PRIMARY"
		}
		for _, idx := range def.Indexes {
			if idx[0] == c {
				key = strings.TrimSpace(key + " INDEX")
			}
		}
		res.Rows = append(res.Rows, []any{c, db.TypeName(def.Types[i]), key})
	}
	res.Affected = len(res.Rows)
	return res, nil
}

func insert(tx *db.Tx, st *Stmt) (*Result, error) {
	def, err := tx.Table(st.Table)
	if err != nil {
		return nil, err
	}
	cols := st.Cols
	if cols == nil {
		cols = def.Cols
	}
	n := 0
	for _, row := range st.Rows {
		if len(row) != len(cols) {
			return nil, fmt.Errorf("expected %d values, got %d", len(cols), len(row))
		}
		rec := db.Record{Cols: cols, Vals: row}
		var ok bool
		if st.Kind == StmtUpsert {
			ok, err = tx.Upsert(st.Table, rec)
		} else {
			ok, err = tx.Insert(st.Table, rec)
			if err == nil && !ok {
				return nil, fmt.Errorf("duplicate primary key in %s", st.Table)
			}
		}
		if err != nil {
			return nil, err
		}
		if ok {
			n++
		}
	}
	name := "INSERT"
	if st.Kind == StmtUpsert {
		name = "UPSERT"
	}
	return &Result{Statement: name, Affected: n, Message: fmt.Sprintf("%d row(s) written", n)}, nil
}

// plan turns WHERE conditions into a scanner plus leftover filters.
type plan struct {
	sc      db.Scanner
	filters []Cond
	desc    string
}

func buildPlan(def *db.TableDef, conds []Cond, desc bool) (*plan, error) {
	for _, c := range conds {
		i := indexOf(def.Cols, c.Col)
		if i < 0 {
			return nil, fmt.Errorf("unknown column %q", c.Col)
		}
		if def.Types[i] != c.Val.Type {
			return nil, fmt.Errorf("column %q is %s", c.Col, db.TypeName(def.Types[i]))
		}
	}
	// candidate keys: primary key first, then indexes
	keys := [][]string{def.Cols[:def.PKeys]}
	keys = append(keys, def.Indexes...)
	bestScore, bestKey := 0, 0
	for ki, key := range keys {
		score := 0
		for _, col := range key {
			if hasEq(conds, col) {
				score += 2
				continue
			}
			if hasRange(conds, col) {
				score++
			}
			break
		}
		if score > bestScore {
			bestScore, bestKey = score, ki
		}
	}
	key := keys[bestKey]
	used := map[int]bool{}
	var eq []db.Value
	var eqCols []string
	var lo, hi *Cond
	for _, col := range key {
		if i := findCond(conds, col, "="); i >= 0 && lo == nil && hi == nil {
			used[i] = true
			eq = append(eq, conds[i].Val)
			eqCols = append(eqCols, col)
			continue
		}
		for i, c := range conds {
			if c.Col != col || used[i] {
				continue
			}
			switch c.Op {
			case ">", ">=":
				if lo == nil {
					cc := c
					lo, used[i] = &cc, true
				}
			case "<", "<=":
				if hi == nil {
					cc := c
					hi, used[i] = &cc, true
				}
			}
		}
		break
	}
	p := &plan{}
	for i, c := range conds {
		if !used[i] {
			p.filters = append(p.filters, c)
		}
	}
	k1 := db.Record{Cols: eqCols, Vals: eq}
	k2 := db.Record{Cols: eqCols, Vals: eq}
	cmp1, cmp2 := btree.CmpGE, btree.CmpLE
	if lo != nil {
		k1 = db.Record{Cols: append(append([]string{}, eqCols...), lo.Col), Vals: append(append([]db.Value{}, eq...), lo.Val)}
		if lo.Op == ">" {
			cmp1 = btree.CmpGT
		}
	}
	if hi != nil {
		k2 = db.Record{Cols: append(append([]string{}, eqCols...), hi.Col), Vals: append(append([]db.Value{}, eq...), hi.Val)}
		if hi.Op == "<" {
			cmp2 = btree.CmpLT
		}
	}
	if desc {
		// walk backwards: start from the upper bound
		k1, k2 = k2, k1
		cmp1, cmp2 = cmp2, cmp1
	}
	p.sc = db.Scanner{Cmp1: cmp1, Cmp2: cmp2, Key1: k1, Key2: k2}

	switch {
	case bestScore == 0:
		p.desc = "full table scan"
	case bestKey == 0 && len(eq) == def.PKeys:
		p.desc = "primary key lookup"
	case bestKey == 0:
		p.desc = "range scan on primary key"
	default:
		p.desc = "index scan on (" + strings.Join(key, ", ") + ")"
	}
	if len(p.filters) > 0 {
		p.desc += fmt.Sprintf(" + %d filter(s)", len(p.filters))
	}
	return p, nil
}

func hasEq(conds []Cond, col string) bool { return findCond(conds, col, "=") >= 0 }

func hasRange(conds []Cond, col string) bool {
	for _, c := range conds {
		if c.Col == col && (c.Op == "<" || c.Op == "<=" || c.Op == ">" || c.Op == ">=") {
			return true
		}
	}
	return false
}

func findCond(conds []Cond, col, op string) int {
	for i, c := range conds {
		if c.Col == col && c.Op == op {
			return i
		}
	}
	return -1
}

func matches(rec *db.Record, filters []Cond) bool {
	for _, f := range filters {
		v := rec.Get(f.Col)
		if v == nil {
			return false
		}
		c := v.Compare(f.Val)
		ok := false
		switch f.Op {
		case "=":
			ok = c == 0
		case "!=":
			ok = c != 0
		case "<":
			ok = c < 0
		case "<=":
			ok = c <= 0
		case ">":
			ok = c > 0
		case ">=":
			ok = c >= 0
		}
		if !ok {
			return false
		}
	}
	return true
}

// scanRows runs the plan and calls fn for each matching row.
func scanRows(tx *db.Tx, table string, conds []Cond, desc bool, limit int, fn func(*db.Record) error) (string, error) {
	def, err := tx.Table(table)
	if err != nil {
		return "", err
	}
	p, err := buildPlan(def, conds, desc)
	if err != nil {
		return "", err
	}
	if err := tx.Scan(table, &p.sc); err != nil {
		return "", err
	}
	n := 0
	for ; p.sc.Valid(); p.sc.Next() {
		rec := &db.Record{}
		if err := p.sc.Deref(rec); err != nil {
			return "", err
		}
		if !matches(rec, p.filters) {
			continue
		}
		if err := fn(rec); err != nil {
			return "", err
		}
		n++
		if limit > 0 && n >= limit {
			break
		}
	}
	return p.desc, nil
}

func selectRows(tx *db.Tx, st *Stmt) (*Result, error) {
	def, err := tx.Table(st.Table)
	if err != nil {
		return nil, err
	}
	cols := st.Cols
	if cols == nil {
		cols = def.Cols
	}
	for _, c := range cols {
		if indexOf(def.Cols, c) < 0 {
			return nil, fmt.Errorf("unknown column %q", c)
		}
	}
	res := &Result{Statement: "SELECT", Columns: cols, Rows: [][]any{}}
	plan, err := scanRows(tx, st.Table, st.Where, st.Desc, st.Limit, func(rec *db.Record) error {
		row := make([]any, len(cols))
		for i, c := range cols {
			row[i] = rec.Get(c).Any()
		}
		res.Rows = append(res.Rows, row)
		return nil
	})
	if err != nil {
		return nil, err
	}
	res.Plan = plan
	res.Affected = len(res.Rows)
	return res, nil
}

func update(tx *db.Tx, st *Stmt) (*Result, error) {
	def, err := tx.Table(st.Table)
	if err != nil {
		return nil, err
	}
	for _, a := range st.Set {
		i := indexOf(def.Cols, a.Col)
		if i < 0 {
			return nil, fmt.Errorf("unknown column %q", a.Col)
		}
		if i < def.PKeys {
			return nil, fmt.Errorf("cannot update primary key column %q", a.Col)
		}
		if def.Types[i] != a.Val.Type {
			return nil, fmt.Errorf("column %q is %s", a.Col, db.TypeName(def.Types[i]))
		}
	}
	// collect first: the tree must not change under the scanner
	var rows []*db.Record
	plan, err := scanRows(tx, st.Table, st.Where, false, 0, func(rec *db.Record) error {
		rows = append(rows, rec)
		return nil
	})
	if err != nil {
		return nil, err
	}
	for _, rec := range rows {
		for _, a := range st.Set {
			*rec.Get(a.Col) = a.Val
		}
		if _, err := tx.Update(st.Table, *rec); err != nil {
			return nil, err
		}
	}
	return &Result{Statement: "UPDATE", Affected: len(rows), Plan: plan,
		Message: fmt.Sprintf("%d row(s) updated", len(rows))}, nil
}

func deleteRows(tx *db.Tx, st *Stmt) (*Result, error) {
	var rows []*db.Record
	plan, err := scanRows(tx, st.Table, st.Where, false, 0, func(rec *db.Record) error {
		rows = append(rows, rec)
		return nil
	})
	if err != nil {
		return nil, err
	}
	for _, rec := range rows {
		if _, err := tx.Delete(st.Table, *rec); err != nil {
			return nil, err
		}
	}
	return &Result{Statement: "DELETE", Affected: len(rows), Plan: plan,
		Message: fmt.Sprintf("%d row(s) deleted", len(rows))}, nil
}
