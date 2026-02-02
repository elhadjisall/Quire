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
