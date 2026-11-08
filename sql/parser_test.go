package sql

import "testing"

func TestParseDialect(t *testing.T) {
	stmts, err := Parse(`
		CREATE INDEX idx ON users (email, name);
		DROP INDEX ON users (email);
		SHOW INDEX FROM users;
		SHOW INDEXES;
		EXPLAIN SELECT a FROM t WHERE a > 1 ORDER BY a DESC LIMIT 2 OFFSET 3;
		SELECT COUNT(*), MIN(a), MAX(b), SUM(a) FROM t WHERE name LIKE 'a%';
		BEGIN TRANSACTION;
		COMMIT;
		ROLLBACK;
	`)
	if err != nil {
		t.Fatal(err)
	}
	if len(stmts) != 9 {
		t.Fatalf("got %d statements", len(stmts))
	}
	if stmts[0].Kind != StmtCreateIndex || stmts[0].Table != "users" || len(stmts[0].Cols) != 2 || stmts[0].Cols[1] != "name" {
		t.Fatalf("create index: %+v", stmts[0])
	}
	if stmts[1].Kind != StmtDropIndex || len(stmts[1].Cols) != 1 || stmts[1].Cols[0] != "email" {
		t.Fatalf("drop index: %+v", stmts[1])
	}
	if stmts[2].Kind != StmtShowIndex || stmts[2].Table != "users" {
		t.Fatalf("show index: %+v", stmts[2])
	}
	if stmts[3].Kind != StmtShowIndex || stmts[3].Table != "" {
		t.Fatalf("show indexes: %+v", stmts[3])
	}
	ex := stmts[4]
	if !ex.Explain || ex.Kind != StmtSelect || !ex.Desc || ex.OrderBy != "a" || ex.Limit != 2 || ex.Offset != 3 {
		t.Fatalf("explain: %+v", ex)
	}
	if len(ex.Where) != 1 || ex.Where[0].Op != ">" || ex.Where[0].Val.I64 != 1 {
		t.Fatalf("explain where: %+v", ex.Where)
	}
	sel := stmts[5]
	if len(sel.Aggs) != 4 || sel.Aggs[0].Func != "COUNT" || sel.Aggs[0].Col != "*" || sel.Aggs[3].Func != "SUM" {
		t.Fatalf("aggs: %+v", sel.Aggs)
	}
	if len(sel.Where) != 1 || sel.Where[0].Op != "LIKE" || sel.Where[0].Val.String() != "a%" {
		t.Fatalf("like: %+v", sel.Where)
	}
	if stmts[6].Kind != StmtBegin || stmts[7].Kind != StmtCommit || stmts[8].Kind != StmtRollback {
		t.Fatalf("txn: %d %d %d", stmts[6].Kind, stmts[7].Kind, stmts[8].Kind)
	}

	bad := []string{
		`EXPLAIN CREATE TABLE t (id INT64, PRIMARY KEY (id))`,
		`SELECT SUM(*) FROM t`,
		`SELECT * FROM t LIMIT -1`,
		`SELECT name, COUNT(*) FROM t`,
	}
	for _, q := range bad {
		if _, err := Parse(q); err == nil {
			t.Fatalf("expected parse error for %q", q)
		}
	}
}
