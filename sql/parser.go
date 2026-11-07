package sql

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/elhadjisall/kvdb/db"
)

// Statement kinds.
const (
	StmtCreate = iota
	StmtDrop
	StmtInsert
	StmtUpsert
	StmtSelect
	StmtUpdate
	StmtDelete
	StmtShowTables
	StmtDescribe
	StmtCreateIndex
	StmtDropIndex
	StmtShowIndex
)

// Agg is one aggregate function in a select list.
type Agg struct {
	Func string // COUNT, SUM, MIN, MAX
	Col  string // "*" for COUNT(*)
}

// Cond is a single `col op value` predicate; conditions are AND-ed.
type Cond struct {
	Col string
	Op  string // = != < <= > >=
	Val db.Value
}

// Assign is `col = value` in UPDATE.
type Assign struct {
	Col string
	Val db.Value
}

// Stmt is a parsed statement.
type Stmt struct {
	Kind    int
	Table   string
	Def     *db.TableDef // CREATE
	Cols    []string     // INSERT columns / SELECT columns (nil = *)
	Aggs    []Agg        // set when the select list is aggregate-only
	Rows    [][]db.Value // INSERT values
	Where   []Cond
	Set     []Assign
	Limit   int // 0 = unlimited
	Offset  int
	OrderBy string
	Desc    bool
	Explain bool
}

type parser struct {
	toks []token
	pos  int
}

func (p *parser) peek() token { return p.toks[p.pos] }

func (p *parser) next() token {
	t := p.toks[p.pos]
	if t.kind != tokEOF {
		p.pos++
	}
	return t
}

// keyword consumes an identifier matching kw (case-insensitive).
func (p *parser) keyword(kw string) bool {
	t := p.peek()
	if t.kind == tokIdent && strings.EqualFold(t.text, kw) {
		p.pos++
		return true
	}
	return false
}

func (p *parser) expectKeyword(kw string) error {
	if !p.keyword(kw) {
		return p.errorf("expected %s", kw)
	}
	return nil
}

func (p *parser) symbol(s string) bool {
	t := p.peek()
	if t.kind == tokSymbol && t.text == s {
		p.pos++
		return true
	}
	return false
}

func (p *parser) expectSymbol(s string) error {
	if !p.symbol(s) {
		return p.errorf("expected %q", s)
	}
	return nil
}

func (p *parser) ident() (string, error) {
	t := p.peek()
	if t.kind != tokIdent {
		return "", p.errorf("expected identifier")
	}
	p.pos++
	return t.text, nil
}

func (p *parser) errorf(format string, args ...any) error {
	t := p.peek()
	what := t.text
	if t.kind == tokEOF {
		what = "end of input"
	}
	return fmt.Errorf("syntax error near %q: %s", what, fmt.Sprintf(format, args...))
}

// value parses a literal.
func (p *parser) value() (db.Value, error) {
	t := p.next()
	switch t.kind {
	case tokNumber:
		n, err := strconv.ParseInt(t.text, 10, 64)
		if err != nil {
			return db.Value{}, fmt.Errorf("bad number %q", t.text)
		}
		return db.Int64(n), nil
	case tokString:
		return db.Bytes([]byte(t.text)), nil
	}
	p.pos--
	return db.Value{}, p.errorf("expected a value")
}

// Parse parses one or more ';' separated statements.
func Parse(input string) ([]*Stmt, error) {
	toks, err := lex(input)
	if err != nil {
		return nil, err
	}
	p := &parser{toks: toks}
	var stmts []*Stmt
	for p.peek().kind != tokEOF {
		if p.symbol(";") {
			continue
		}
		st, err := p.statement()
		if err != nil {
			return nil, err
		}
		stmts = append(stmts, st)
		if !p.symbol(";") && p.peek().kind != tokEOF {
			return nil, p.errorf("expected ';'")
		}
	}
	return stmts, nil
}

func (p *parser) statement() (*Stmt, error) {
	switch {
	case p.keyword("EXPLAIN"):
		return p.explain()
	case p.keyword("CREATE"):
		return p.create()
	case p.keyword("DROP"):
		if p.keyword("INDEX") {
			return p.dropIndex()
		}
		if err := p.expectKeyword("TABLE"); err != nil {
			return nil, err
		}
		name, err := p.ident()
		return &Stmt{Kind: StmtDrop, Table: name}, err
	case p.keyword("INSERT"):
		return p.insert(StmtInsert)
	case p.keyword("UPSERT"):
		return p.insert(StmtUpsert)
	case p.keyword("SELECT"):
		return p.selectStmt()
	case p.keyword("UPDATE"):
		return p.update()
	case p.keyword("DELETE"):
		return p.deleteStmt()
	case p.keyword("SHOW"):
		if p.keyword("TABLES") {
			return &Stmt{Kind: StmtShowTables}, nil
		}
		if p.keyword("INDEX") || p.keyword("INDEXES") {
			if p.keyword("FROM") || p.keyword("ON") {
				name, err := p.ident()
				return &Stmt{Kind: StmtShowIndex, Table: name}, err
			}
			return &Stmt{Kind: StmtShowIndex}, nil
		}
		return nil, p.errorf("expected TABLES or INDEX")
	case p.keyword("DESCRIBE") || p.keyword("DESC"):
		name, err := p.ident()
		return &Stmt{Kind: StmtDescribe, Table: name}, err
	}
	return nil, p.errorf("unknown statement")
}

func (p *parser) explain() (*Stmt, error) {
	st, err := p.statement()
	if err != nil {
		return nil, err
	}
	switch st.Kind {
	case StmtSelect, StmtUpdate, StmtDelete:
	default:
		return nil, fmt.Errorf("EXPLAIN does not support this statement")
	}
	st.Explain = true
	return st, nil
}

func (p *parser) create() (*Stmt, error) {
	if p.keyword("INDEX") {
		return p.createIndex()
	}
	if err := p.expectKeyword("TABLE"); err != nil {
		return nil, err
	}
	name, err := p.ident()
	if err != nil {
		return nil, err
	}
	if err := p.expectSymbol("("); err != nil {
		return nil, err
	}
	def := &db.TableDef{Name: name}
	var pkey []string
	for {
		if p.keyword("PRIMARY") {
			if err := p.expectKeyword("KEY"); err != nil {
				return nil, err
			}
			pkey, err = p.columnList()
			if err != nil {
				return nil, err
			}
		} else if p.keyword("INDEX") {
			cols, err := p.columnList()
			if err != nil {
				return nil, err
			}
			def.Indexes = append(def.Indexes, cols)
		} else {
			col, err := p.ident()
			if err != nil {
				return nil, err
			}
			typ, err := p.ident()
			if err != nil {
				return nil, err
			}
			var t uint32
			switch strings.ToUpper(typ) {
			case "INT64", "INT", "INTEGER", "BIGINT":
				t = db.TypeInt64
			case "STRING", "BYTES", "TEXT", "VARCHAR":
				t = db.TypeBytes
			default:
				return nil, fmt.Errorf("unknown type %q", typ)
			}
			def.Cols = append(def.Cols, col)
			def.Types = append(def.Types, t)
		}
		if p.symbol(")") {
			break
		}
		if err := p.expectSymbol(","); err != nil {
			return nil, err
		}
	}
	if len(pkey) == 0 {
		return nil, fmt.Errorf("table %q needs a PRIMARY KEY", name)
	}
	// reorder columns so the primary key comes first
	ordered := &db.TableDef{Name: name, Indexes: def.Indexes, PKeys: len(pkey)}
	used := map[string]bool{}
	for _, c := range pkey {
		i := indexOf(def.Cols, c)
		if i < 0 {
			return nil, fmt.Errorf("primary key column %q is not defined", c)
		}
		ordered.Cols = append(ordered.Cols, c)
		ordered.Types = append(ordered.Types, def.Types[i])
		used[c] = true
	}
	for i, c := range def.Cols {
		if !used[c] {
			ordered.Cols = append(ordered.Cols, c)
			ordered.Types = append(ordered.Types, def.Types[i])
		}
	}
	return &Stmt{Kind: StmtCreate, Table: name, Def: ordered}, nil
}

// createIndex parses `CREATE INDEX [name] ON table (cols)`.
// The optional name is accepted and ignored; indexes are identified by columns.
func (p *parser) createIndex() (*Stmt, error) {
	if !p.keyword("ON") {
		if _, err := p.ident(); err != nil {
			return nil, err
		}
		if err := p.expectKeyword("ON"); err != nil {
			return nil, err
		}
	}
	return p.indexStmt(StmtCreateIndex)
}

func (p *parser) dropIndex() (*Stmt, error) {
	if !p.keyword("ON") {
		if _, err := p.ident(); err != nil {
			return nil, err
		}
		if err := p.expectKeyword("ON"); err != nil {
			return nil, err
		}
	}
	return p.indexStmt(StmtDropIndex)
}

func (p *parser) indexStmt(kind int) (*Stmt, error) {
	name, err := p.ident()
	if err != nil {
		return nil, err
	}
	cols, err := p.columnList()
	if err != nil {
		return nil, err
	}
	return &Stmt{Kind: kind, Table: name, Cols: cols}, nil
}

func (p *parser) columnList() ([]string, error) {
	if err := p.expectSymbol("("); err != nil {
		return nil, err
	}
	var cols []string
	for {
		c, err := p.ident()
		if err != nil {
			return nil, err
		}
		cols = append(cols, c)
		if p.symbol(")") {
			return cols, nil
		}
		if err := p.expectSymbol(","); err != nil {
			return nil, err
		}
	}
}

func (p *parser) insert(kind int) (*Stmt, error) {
	if err := p.expectKeyword("INTO"); err != nil {
		return nil, err
	}
	name, err := p.ident()
	if err != nil {
		return nil, err
	}
	st := &Stmt{Kind: kind, Table: name}
	if p.peek().kind == tokSymbol && p.peek().text == "(" {
		st.Cols, err = p.columnList()
		if err != nil {
			return nil, err
		}
	}
	if err := p.expectKeyword("VALUES"); err != nil {
		return nil, err
	}
	for {
		if err := p.expectSymbol("("); err != nil {
			return nil, err
		}
		var row []db.Value
		for {
			v, err := p.value()
			if err != nil {
				return nil, err
			}
			row = append(row, v)
			if p.symbol(")") {
				break
			}
			if err := p.expectSymbol(","); err != nil {
				return nil, err
			}
		}
		st.Rows = append(st.Rows, row)
		if !p.symbol(",") {
			break
		}
	}
	return st, nil
}

func (p *parser) selectStmt() (*Stmt, error) {
	st := &Stmt{Kind: StmtSelect}
	if !p.symbol("*") {
		for {
			if err := p.selectItem(st); err != nil {
				return nil, err
			}
			if !p.symbol(",") {
				break
			}
		}
	}
	if err := p.expectKeyword("FROM"); err != nil {
		return nil, err
	}
	name, err := p.ident()
	if err != nil {
		return nil, err
	}
	st.Table = name
	if st.Where, err = p.where(); err != nil {
		return nil, err
	}
	if p.keyword("ORDER") {
		if err := p.expectKeyword("BY"); err != nil {
			return nil, err
		}
		col, err := p.ident()
		if err != nil {
			return nil, err
		}
		st.OrderBy = col
		if p.keyword("DESC") {
			st.Desc = true
		} else {
			p.keyword("ASC")
		}
	}
	if p.keyword("LIMIT") {
		n, err := p.nonneg("LIMIT")
		if err != nil {
			return nil, err
		}
		st.Limit = n
	}
	if p.keyword("OFFSET") {
		n, err := p.nonneg("OFFSET")
		if err != nil {
			return nil, err
		}
		st.Offset = n
	}
	return st, nil
}

func (p *parser) nonneg(what string) (int, error) {
	t := p.next()
	n, err := strconv.Atoi(t.text)
	if t.kind != tokNumber || err != nil || n < 0 {
		return 0, fmt.Errorf("bad %s", what)
	}
	return n, nil
}

func (p *parser) selectItem(st *Stmt) error {
	t := p.peek()
	if t.kind == tokIdent {
		fn := strings.ToUpper(t.text)
		if (fn == "COUNT" || fn == "SUM" || fn == "MIN" || fn == "MAX") &&
			p.pos+1 < len(p.toks) && p.toks[p.pos+1].kind == tokSymbol && p.toks[p.pos+1].text == "(" {
			if len(st.Aggs) == 0 && len(st.Cols) > 0 {
				return fmt.Errorf("aggregates cannot mix with columns")
			}
			p.next()
			if err := p.expectSymbol("("); err != nil {
				return err
			}
			col := "*"
			if fn == "COUNT" && p.symbol("*") {
				// COUNT(*)
			} else {
				if p.peek().kind == tokSymbol && p.peek().text == "*" {
					return fmt.Errorf("%s(*) is not valid", fn)
				}
				var err error
				col, err = p.ident()
				if err != nil {
					return err
				}
			}
			if err := p.expectSymbol(")"); err != nil {
				return err
			}
			st.Aggs = append(st.Aggs, Agg{Func: fn, Col: col})
			st.Cols = append(st.Cols, fn+"("+col+")")
			return nil
		}
	}
	if len(st.Aggs) > 0 {
		return fmt.Errorf("aggregates cannot mix with columns")
	}
	c, err := p.ident()
	if err != nil {
		return err
	}
	st.Cols = append(st.Cols, c)
	return nil
}

func (p *parser) where() ([]Cond, error) {
	if !p.keyword("WHERE") {
		return nil, nil
	}
	var conds []Cond
	for {
		col, err := p.ident()
		if err != nil {
			return nil, err
		}
		if p.keyword("BETWEEN") {
			lo, err := p.value()
			if err != nil {
				return nil, err
			}
			if err := p.expectKeyword("AND"); err != nil {
				return nil, err
			}
			hi, err := p.value()
			if err != nil {
				return nil, err
			}
			conds = append(conds, Cond{col, ">=", lo}, Cond{col, "<=", hi})
		} else {
			t := p.next()
			if t.kind != tokSymbol || !strings.Contains("= != <> < <= > >=", t.text) || t.text == "(" {
				p.pos--
				return nil, p.errorf("expected comparison operator")
			}
			op := t.text
			if op == "<>" {
				op = "!="
			}
			v, err := p.value()
			if err != nil {
				return nil, err
			}
			conds = append(conds, Cond{col, op, v})
		}
		if !p.keyword("AND") {
			return conds, nil
		}
	}
}

func (p *parser) update() (*Stmt, error) {
	name, err := p.ident()
	if err != nil {
		return nil, err
	}
	if err := p.expectKeyword("SET"); err != nil {
		return nil, err
	}
	st := &Stmt{Kind: StmtUpdate, Table: name}
	for {
		col, err := p.ident()
		if err != nil {
			return nil, err
		}
		if err := p.expectSymbol("="); err != nil {
			return nil, err
		}
		v, err := p.value()
		if err != nil {
			return nil, err
		}
		st.Set = append(st.Set, Assign{col, v})
		if !p.symbol(",") {
			break
		}
	}
	st.Where, err = p.where()
	return st, err
}

func (p *parser) deleteStmt() (*Stmt, error) {
	if err := p.expectKeyword("FROM"); err != nil {
		return nil, err
	}
	name, err := p.ident()
	if err != nil {
		return nil, err
	}
	st := &Stmt{Kind: StmtDelete, Table: name}
	st.Where, err = p.where()
	return st, err
}

func indexOf(list []string, s string) int {
	for i, v := range list {
		if v == s {
			return i
		}
	}
	return -1
}
