package sql

import (
	"path/filepath"
	"testing"

	"github.com/elhadjisall/kvdb/db"
)

func openTemp(t *testing.T) *db.DB {
	t.Helper()
	d, err := db.Open(filepath.Join(t.TempDir(), "q.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	return d
}

func mustRun(t *testing.T, d *db.DB, q string) []*Result {
	t.Helper()
	res, err := Run(d, q)
	if err != nil {
		t.Fatalf("%s\n-> %v", q, err)
	}
	return res
}

func TestEndToEnd(t *testing.T) {
	d := openTemp(t)
	mustRun(t, d, `
		CREATE TABLE users (
			id INT64, name STRING, email STRING, age INT64,
			PRIMARY KEY (id), INDEX (email), INDEX (age, name)
		);
		INSERT INTO users VALUES
			(1, 'ada', 'ada@x.io', 36),
			(2, 'linus', 'linus@x.io', 54),
			(3, 'grace', 'grace@x.io', 36),
			(4, 'ken', 'ken@x.io', 80);
	`)

	res := mustRun(t, d, `SELECT name FROM users WHERE id = 2`)
	if len(res[0].Rows) != 1 || res[0].Rows[0][0] != "linus" || res[0].Plan != "primary key lookup" {
		t.Fatalf("pk lookup: %+v", res[0])
	}

	res = mustRun(t, d, `SELECT id FROM users WHERE email = 'grace@x.io'`)
	if len(res[0].Rows) != 1 || res[0].Rows[0][0] != int64(3) || res[0].Plan != "index scan on (email)" {
		t.Fatalf("index lookup: %+v", res[0])
	}

	res = mustRun(t, d, `SELECT name FROM users WHERE age = 36 ORDER BY name`)
	if len(res[0].Rows) != 2 || res[0].Rows[0][0] != "ada" || res[0].Rows[1][0] != "grace" {
		t.Fatalf("composite index: %+v", res[0])
	}

	res = mustRun(t, d, `SELECT id FROM users WHERE id > 1 AND id <= 3 ORDER BY id DESC`)
	if len(res[0].Rows) != 2 || res[0].Rows[0][0] != int64(3) || res[0].Rows[1][0] != int64(2) {
		t.Fatalf("range desc: %+v", res[0])
	}

	res = mustRun(t, d, `SELECT name FROM users WHERE age > 40 LIMIT 1`)
	if len(res[0].Rows) != 1 || res[0].Rows[0][0] != "linus" {
		t.Fatalf("range on index + limit: %+v", res[0])
	}

	res = mustRun(t, d, `SELECT name FROM users WHERE name != 'ada' AND age BETWEEN 30 AND 60`)
	if len(res[0].Rows) != 2 || res[0].Plan != "index scan on (age, name) + 1 filter(s)" {
		t.Fatalf("filters: %+v", res[0])
	}

	res = mustRun(t, d, `UPDATE users SET age = 37, email = 'ada@y.io' WHERE id = 1`)
	if res[0].Affected != 1 {
		t.Fatalf("update: %+v", res[0])
	}
	res = mustRun(t, d, `SELECT id FROM users WHERE email = 'ada@y.io'`)
	if len(res[0].Rows) != 1 {
		t.Fatalf("index not maintained after update")
	}

	res = mustRun(t, d, `DELETE FROM users WHERE age >= 50`)
	if res[0].Affected != 2 {
		t.Fatalf("delete: %+v", res[0])
	}
	res = mustRun(t, d, `SELECT * FROM users`)
	if len(res[0].Rows) != 2 || len(res[0].Columns) != 4 {
		t.Fatalf("select *: %+v", res[0])
	}

	res = mustRun(t, d, `SHOW TABLES; DESCRIBE users`)
	if len(res) != 2 || res[0].Rows[0][0] != "users" || len(res[1].Rows) != 4 {
		t.Fatalf("catalog: %+v %+v", res[0], res[1])
	}

	if _, err := Run(d, `INSERT INTO users VALUES (1, 'dup', 'd@x.io', 1)`); err == nil {
		t.Fatal("duplicate insert should fail")
	}
	if _, err := Run(d, `SELECT nope FROM users`); err == nil {
		t.Fatal("unknown column should fail")
	}
	if _, err := Run(d, `SELECT * FROM users WHERE age = 'x'`); err == nil {
		t.Fatal("type mismatch should fail")
	}
}

func TestTransactionAtomicity(t *testing.T) {
	d := openTemp(t)
	mustRun(t, d, `CREATE TABLE t (k INT64, v STRING, PRIMARY KEY (k))`)
	// second statement fails: nothing from the script may persist
	_, err := Run(d, `INSERT INTO t VALUES (1, 'a'); INSERT INTO t VALUES (1, 'b')`)
	if err == nil {
		t.Fatal("expected failure")
	}
	res := mustRun(t, d, `SELECT * FROM t`)
	if len(res[0].Rows) != 0 {
		t.Fatalf("partial script was committed: %+v", res[0])
	}
}

func TestCreateIndex(t *testing.T) {
	d := openTemp(t)
	mustRun(t, d, `
		CREATE TABLE t (id INT64, name STRING, PRIMARY KEY (id));
		INSERT INTO t VALUES (1, 'b'), (2, 'a'), (3, 'c');
		CREATE INDEX ON t (name);
	`)
	res := mustRun(t, d, `SELECT id FROM t WHERE name = 'a'`)
	if len(res[0].Rows) != 1 || res[0].Rows[0][0] != int64(2) || res[0].Plan != "index scan on (name)" {
		t.Fatalf("created index: %+v", res[0])
	}
	res = mustRun(t, d, `DROP INDEX ON t (name); SELECT id FROM t WHERE name = 'a'`)
	if res[1].Plan != "full table scan + 1 filter(s)" || len(res[1].Rows) != 1 || res[1].Rows[0][0] != int64(2) {
		t.Fatalf("after drop: %+v", res[1])
	}
	mustRun(t, d, `CREATE INDEX t_name ON t (name)`)
	if _, err := Run(d, `CREATE INDEX ON t (name)`); err == nil {
		t.Fatal("duplicate index should fail")
	}
	res = mustRun(t, d, `SELECT id FROM t WHERE name = 'c'`)
	if res[0].Plan != "index scan on (name)" || res[0].Rows[0][0] != int64(3) {
		t.Fatalf("named index: %+v", res[0])
	}
}

func TestAggregates(t *testing.T) {
	d := openTemp(t)
	mustRun(t, d, `
		CREATE TABLE t (id INT64, name STRING, age INT64, PRIMARY KEY (id));
		INSERT INTO t VALUES (1, 'ada', 36), (2, 'linus', 54), (3, 'grace', 36);
	`)
	res := mustRun(t, d, `SELECT COUNT(*), MIN(age), MAX(age), SUM(age) FROM t`)
	row := res[0].Rows[0]
	if row[0] != int64(3) || row[1] != int64(36) || row[2] != int64(54) || row[3] != int64(126) {
		t.Fatalf("agg: %#v", row)
	}
	if len(res[0].Plan) < 14 || res[0].Plan[:14] != "aggregate via " {
		t.Fatalf("plan %q", res[0].Plan)
	}
	res = mustRun(t, d, `SELECT COUNT(name), MIN(name), MAX(name) FROM t WHERE age = 36`)
	if res[0].Rows[0][0] != int64(2) || res[0].Rows[0][1] != "ada" || res[0].Rows[0][2] != "grace" {
		t.Fatalf("filtered: %#v", res[0].Rows[0])
	}
	res = mustRun(t, d, `SELECT COUNT(*), SUM(age), MIN(age) FROM t WHERE id < 0`)
	if res[0].Rows[0][0] != int64(0) || res[0].Rows[0][1] != int64(0) || res[0].Rows[0][2] != nil {
		t.Fatalf("empty: %#v", res[0].Rows[0])
	}
	if _, err := Run(d, `SELECT name, COUNT(*) FROM t`); err == nil {
		t.Fatal("mixed aggregate should fail")
	}
	if _, err := Run(d, `SELECT SUM(name) FROM t`); err == nil {
		t.Fatal("sum of string should fail")
	}
	res = mustRun(t, d, `EXPLAIN SELECT COUNT(*) FROM t WHERE id = 1`)
	if res[0].Rows[0][0] != "aggregate via primary key lookup" {
		t.Fatalf("explain agg: %+v", res[0])
	}
}

func TestExplain(t *testing.T) {
	d := openTemp(t)
	mustRun(t, d, `CREATE TABLE t (id INT64, name STRING, PRIMARY KEY (id)); INSERT INTO t VALUES (1, 'a');`)
	res := mustRun(t, d, `EXPLAIN SELECT * FROM t WHERE id = 1`)
	if len(res[0].Rows) != 1 || res[0].Rows[0][0] != "primary key lookup" {
		t.Fatalf("explain select: %+v", res[0])
	}
	res = mustRun(t, d, `EXPLAIN DELETE FROM t WHERE id = 1; SELECT * FROM t`)
	if res[0].Rows[0][0] != "primary key lookup" || len(res[1].Rows) != 1 {
		t.Fatalf("explain delete mutated the table: %+v %+v", res[0], res[1])
	}
	if _, err := Parse(`EXPLAIN CREATE TABLE u (id INT64, PRIMARY KEY (id))`); err == nil {
		t.Fatal("explain create should fail")
	}
}

func TestShowIndex(t *testing.T) {
	d := openTemp(t)
	mustRun(t, d, `CREATE TABLE t (id INT64, name STRING, PRIMARY KEY (id), INDEX (name))`)
	res := mustRun(t, d, `SHOW INDEX FROM t`)
	if len(res[0].Rows) != 2 || res[0].Rows[0][1] != "PRIMARY" || res[0].Rows[1][1] != "INDEX" || res[0].Rows[1][2] != "name" {
		t.Fatalf("show index: %+v", res[0])
	}
	res = mustRun(t, d, `SHOW INDEXES`)
	if len(res[0].Rows) != 2 {
		t.Fatalf("show indexes: %+v", res[0])
	}
}

func TestParserErrors(t *testing.T) {
	bad := []string{
		"SELEC * FROM t",
		"CREATE TABLE t (a INT64)",
		"INSERT INTO t VALUES (1,",
		"SELECT * FROM t WHERE a ~ 1",
		"DELETE t",
		"SELECT * FROM t WHERE a = 'unterminated",
	}
	for _, q := range bad {
		if _, err := Parse(q); err == nil {
			t.Fatalf("expected parse error for %q", q)
		}
	}
	stmts, err := Parse(`-- a comment
		select a,b from "t" where a >= -5 and b <> 'it''s'; show tables;`)
	if err != nil || len(stmts) != 2 {
		t.Fatalf("parse: %v %d", err, len(stmts))
	}
	if stmts[0].Where[1].Val.String() != "it's" || stmts[0].Where[0].Val.I64 != -5 {
		t.Fatalf("literals: %+v", stmts[0].Where)
	}
}
