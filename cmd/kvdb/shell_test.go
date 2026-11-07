package main

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"

	"github.com/elhadjisall/kvdb/db"
	"github.com/elhadjisall/kvdb/sql"
)

func TestShellTransaction(t *testing.T) {
	path := filepath.Join(t.TempDir(), "shell.db")
	database, err := db.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if _, err := sql.Run(database, `CREATE TABLE t (id INT64, v STRING, PRIMARY KEY (id));`); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	in := strings.NewReader("BEGIN;\nINSERT INTO t VALUES (1, 'a');\nROLLBACK;\nSELECT * FROM t;\n.quit\n")
	shell(database, in, &out)
	text := out.String()
	if !strings.Contains(text, "transaction rolled back") || !strings.Contains(text, "0 row(s)") {
		t.Fatalf("rollback transcript:\n%s", text)
	}
	if !strings.Contains(text, "kvdb*>") {
		t.Fatalf("open transaction should change the prompt:\n%s", text)
	}

	out.Reset()
	in = strings.NewReader("BEGIN;\nINSERT INTO t VALUES (2, 'b');\nCOMMIT;\n.help\n.quit\n")
	shell(database, in, &out)
	if !strings.Contains(out.String(), "transaction committed") || !strings.Contains(out.String(), ".schema") {
		t.Fatalf("commit transcript:\n%s", out.String())
	}
	res, err := sql.Run(database, `SELECT v FROM t WHERE id = 2`)
	if err != nil || len(res) != 1 || len(res[0].Rows) != 1 || res[0].Rows[0][0] != "b" {
		t.Fatalf("committed row: %+v %v", res, err)
	}
	res, err = sql.Run(database, `SELECT * FROM t WHERE id = 1`)
	if err != nil || len(res[0].Rows) != 0 {
		t.Fatalf("rolled back row came back: %+v", res)
	}
}
