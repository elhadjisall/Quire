package sql

import "testing"

func TestSession(t *testing.T) {
	d := openTemp(t)
	mustRun(t, d, `CREATE TABLE t (id INT64, v STRING, PRIMARY KEY (id))`)
	s := NewSession(d)
	defer s.Close()

	if _, err := s.Exec(`BEGIN`); err != nil || !s.InTx() {
		t.Fatalf("begin: %v open=%v", err, s.InTx())
	}
	if _, err := s.Exec(`INSERT INTO t VALUES (1, 'a')`); err != nil {
		t.Fatal(err)
	}
	res, err := s.Exec(`SELECT v FROM t WHERE id = 1`)
	if err != nil || len(res[0].Rows) != 1 || res[0].Rows[0][0] != "a" {
		t.Fatalf("own write: %+v %v", res, err)
	}
	if _, err := s.Exec(`ROLLBACK`); err != nil || s.InTx() {
		t.Fatalf("rollback: %v", err)
	}
	res, err = s.Exec(`SELECT * FROM t`)
	if err != nil || len(res[0].Rows) != 0 {
		t.Fatalf("rolled back row still visible: %+v", res[0])
	}

	if _, err := s.Exec(`BEGIN; INSERT INTO t VALUES (1, 'a'); COMMIT;`); err != nil || s.InTx() {
		t.Fatal(err)
	}
	res = mustRun(t, d, `SELECT v FROM t WHERE id = 1`)
	if res[0].Rows[0][0] != "a" {
		t.Fatalf("committed: %+v", res[0])
	}

	if _, err := s.Exec(`BEGIN`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Exec(`INSERT INTO t VALUES (1, 'dup')`); err == nil || s.InTx() {
		t.Fatal("duplicate insert should abort the transaction")
	}
	res = mustRun(t, d, `SELECT * FROM t`)
	if len(res[0].Rows) != 1 {
		t.Fatalf("aborted tx changed the table: %+v", res[0])
	}

	// a script without BEGIN stays atomic
	if _, err := s.Exec(`INSERT INTO t VALUES (2, 'b'); INSERT INTO t VALUES (2, 'c')`); err == nil {
		t.Fatal("expected duplicate failure")
	}
	res = mustRun(t, d, `SELECT * FROM t WHERE id = 2`)
	if len(res[0].Rows) != 0 {
		t.Fatalf("partial autocommit: %+v", res[0])
	}

	s2 := NewSession(d)
	if _, err := s2.Exec(`BEGIN TRANSACTION; INSERT INTO t VALUES (3, 'c');`); err != nil {
		t.Fatal(err)
	}
	s2.Close()
	res = mustRun(t, d, `SELECT * FROM t WHERE id = 3`)
	if len(res[0].Rows) != 0 {
		t.Fatal("close should roll back")
	}
	if _, err := s.Exec(`COMMIT`); err == nil {
		t.Fatal("commit with no transaction should fail")
	}
}
