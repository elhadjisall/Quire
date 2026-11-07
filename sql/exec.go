package sql

import (
	"fmt"
	"sort"
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
		if st.Explain {
			continue
		}
		switch st.Kind {
		case StmtSelect, StmtShowTables, StmtDescribe, StmtShowIndex:
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
	if st.Explain {
		return explain(tx, st)
	}
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
	case StmtShowIndex:
		return showIndex(tx, st.Table)
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

func explain(tx *db.Tx, st *Stmt) (*Result, error) {
	def, err := tx.Table(st.Table)
	if err != nil {
		return nil, err
	}
	p, err := buildPlan(def, st.Where, st.Desc)
	if err != nil {
		return nil, err
	}
	desc := p.desc
	if len(st.Aggs) > 0 {
		desc = "aggregate via " + desc
	}
	return &Result{
		Statement: "EXPLAIN",
		Columns:   []string{"plan"},
		Rows:      [][]any{{desc}},
		Affected:  1,
		Plan:      desc,
	}, nil
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

func showIndex(tx *db.Tx, table string) (*Result, error) {
	info, err := tx.Indexes(table)
	if err != nil {
		return nil, err
	}
	res := &Result{Statement: "SHOW INDEX", Columns: []string{"table", "type", "columns"}, Rows: [][]any{}}
	for _, ix := range info {
		res.Rows = append(res.Rows, []any{ix.Table, ix.Kind, strings.Join(ix.Columns, ", ")})
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
	sc       db.Scanner
	filters  []Cond
	desc     string
	orderCol string // column a forward scan is ordered by
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
	conds = rewriteLike(conds)
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
	if col := scanOrderCol(def, key, len(eq)); col != "" {
		p.orderCol = col
	}

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

// scanOrderCol is the first column that still varies along a forward scan
// after `eq` leading equalities on key.
func scanOrderCol(def *db.TableDef, key []string, eq int) string {
	seen := map[string]bool{}
	order := append([]string(nil), key...)
	for _, c := range key {
		seen[c] = true
	}
	for _, c := range def.Cols[:def.PKeys] {
		if !seen[c] {
			order = append(order, c)
			seen[c] = true
		}
	}
	if eq < 0 || eq >= len(order) {
		return ""
	}
	return order[eq]
}

func rewriteLike(conds []Cond) []Cond {
	out := make([]Cond, 0, len(conds))
	for _, c := range conds {
		if c.Op != "LIKE" {
			out = append(out, c)
			continue
		}
		prefix, rest, exact := splitLike(string(c.Val.Str))
		switch {
		case exact:
			out = append(out, Cond{Col: c.Col, Op: "=", Val: c.Val})
		case prefix != "" && rest == "%":
			out = append(out, Cond{Col: c.Col, Op: ">=", Val: db.Bytes([]byte(prefix))})
			if up, ok := prefixUpper(prefix); ok {
				out = append(out, Cond{Col: c.Col, Op: "<", Val: db.Bytes([]byte(up))})
			}
		default:
			if prefix != "" {
				out = append(out, Cond{Col: c.Col, Op: ">=", Val: db.Bytes([]byte(prefix))})
				if up, ok := prefixUpper(prefix); ok {
					out = append(out, Cond{Col: c.Col, Op: "<", Val: db.Bytes([]byte(up))})
				}
			}
			out = append(out, c)
		}
	}
	return out
}

func splitLike(pat string) (prefix, rest string, exact bool) {
	for i := 0; i < len(pat); i++ {
		if pat[i] == '%' || pat[i] == '_' {
			return pat[:i], pat[i:], false
		}
	}
	return pat, "", true
}

func prefixUpper(prefix string) (string, bool) {
	b := []byte(prefix)
	for i := len(b) - 1; i >= 0; i-- {
		if b[i] < 0xff {
			b[i]++
			return string(b[:i+1]), true
		}
	}
	return "", false
}

// likeMatch implements % (any sequence) and _ (one byte).
func likeMatch(s, pat string) bool {
	n, m := len(s), len(pat)
	prev := make([]bool, n+1)
	prev[0] = true
	for j := 1; j <= m; j++ {
		cur := make([]bool, n+1)
		pc := pat[j-1]
		if pc == '%' {
			cur[0] = prev[0]
			for i := 1; i <= n; i++ {
				cur[i] = cur[i-1] || prev[i]
			}
		} else {
			for i := 1; i <= n; i++ {
				if (pc == '_' || pc == s[i-1]) && prev[i-1] {
					cur[i] = true
				}
			}
		}
		prev = cur
	}
	return prev[n]
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
		case "LIKE":
			ok = likeMatch(string(v.Str), string(f.Val.Str))
		}
		if !ok {
			return false
		}
	}
	return true
}

// scanRows runs the plan and calls fn for each matching row.
func scanRows(tx *db.Tx, table string, conds []Cond, desc bool, offset, limit int, fn func(*db.Record) error) (string, error) {
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
	skipped, n := 0, 0
	for ; p.sc.Valid(); p.sc.Next() {
		rec := &db.Record{}
		if err := p.sc.Deref(rec); err != nil {
			return "", err
		}
		if !matches(rec, p.filters) {
			continue
		}
		if skipped < offset {
			skipped++
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
	if len(st.Aggs) > 0 {
		return selectAgg(tx, st)
	}
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
	if st.OrderBy != "" && indexOf(def.Cols, st.OrderBy) < 0 {
		return nil, fmt.Errorf("unknown column %q", st.OrderBy)
	}
	probe, err := buildPlan(def, st.Where, false)
	if err != nil {
		return nil, err
	}
	needSort := st.OrderBy != "" && probe.orderCol != st.OrderBy
	desc, offset, limit := st.Desc, st.Offset, st.Limit
	if needSort {
		desc, offset, limit = false, 0, 0
	}
	var recs []*db.Record
	plan, err := scanRows(tx, st.Table, st.Where, desc, offset, limit, func(rec *db.Record) error {
		recs = append(recs, rec)
		return nil
	})
	if err != nil {
		return nil, err
	}
	if needSort {
		sort.SliceStable(recs, func(i, j int) bool {
			c := recs[i].Get(st.OrderBy).Compare(*recs[j].Get(st.OrderBy))
			if st.Desc {
				return c > 0
			}
			return c < 0
		})
		if st.Offset >= len(recs) {
			recs = nil
		} else {
			recs = recs[st.Offset:]
		}
		if st.Limit > 0 && len(recs) > st.Limit {
			recs = recs[:st.Limit]
		}
		plan += " + sort"
	}
	res := &Result{Statement: "SELECT", Columns: cols, Rows: [][]any{}, Plan: plan}
	for _, rec := range recs {
		row := make([]any, len(cols))
		for i, c := range cols {
			row[i] = rec.Get(c).Any()
		}
		res.Rows = append(res.Rows, row)
	}
	res.Affected = len(res.Rows)
	return res, nil
}

func selectAgg(tx *db.Tx, st *Stmt) (*Result, error) {
	if st.OrderBy != "" {
		return nil, fmt.Errorf("ORDER BY cannot be used with aggregates")
	}
	def, err := tx.Table(st.Table)
	if err != nil {
		return nil, err
	}
	for _, a := range st.Aggs {
		if a.Col == "*" {
			continue
		}
		i := indexOf(def.Cols, a.Col)
		if i < 0 {
			return nil, fmt.Errorf("unknown column %q", a.Col)
		}
		if a.Func == "SUM" && def.Types[i] != db.TypeInt64 {
			return nil, fmt.Errorf("SUM(%s) needs INT64", a.Col)
		}
	}
	var count int64
	sums := make([]int64, len(st.Aggs))
	mins := make([]db.Value, len(st.Aggs))
	maxs := make([]db.Value, len(st.Aggs))
	seen := make([]bool, len(st.Aggs))
	plan, err := scanRows(tx, st.Table, st.Where, false, 0, 0, func(rec *db.Record) error {
		count++
		for i, a := range st.Aggs {
			if a.Col == "*" {
				continue
			}
			v := rec.Get(a.Col)
			if v == nil {
				return fmt.Errorf("missing column %q", a.Col)
			}
			switch a.Func {
			case "SUM":
				sums[i] += v.I64
			case "MIN":
				if !seen[i] || v.Compare(mins[i]) < 0 {
					mins[i] = copyValue(*v)
					seen[i] = true
				}
			case "MAX":
				if !seen[i] || v.Compare(maxs[i]) > 0 {
					maxs[i] = copyValue(*v)
					seen[i] = true
				}
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	row := make([]any, len(st.Aggs))
	for i, a := range st.Aggs {
		switch a.Func {
		case "COUNT":
			row[i] = count
		case "SUM":
			row[i] = sums[i]
		case "MIN":
			if seen[i] {
				row[i] = mins[i].Any()
			}
		case "MAX":
			if seen[i] {
				row[i] = maxs[i].Any()
			}
		}
	}
	rows := [][]any{row}
	if st.Offset >= len(rows) {
		rows = [][]any{}
	} else if st.Offset > 0 {
		rows = rows[st.Offset:]
	}
	if st.Limit > 0 && len(rows) > st.Limit {
		rows = rows[:st.Limit]
	}
	return &Result{
		Statement: "SELECT",
		Columns:   st.Cols,
		Rows:      rows,
		Affected:  len(rows),
		Plan:      "aggregate via " + plan,
	}, nil
}

func copyValue(v db.Value) db.Value {
	if v.Type == db.TypeBytes {
		v.Str = append([]byte(nil), v.Str...)
	}
	return v
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
	plan, err := scanRows(tx, st.Table, st.Where, false, 0, 0, func(rec *db.Record) error {
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
	plan, err := scanRows(tx, st.Table, st.Where, false, 0, 0, func(rec *db.Record) error {
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
