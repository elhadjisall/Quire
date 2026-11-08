package server

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/elhadjisall/kvdb/db"
	"github.com/elhadjisall/kvdb/sql"
)

func TestHTTP(t *testing.T) {
	database, err := db.Open(filepath.Join(t.TempDir(), "api.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	if _, err := sql.Run(database, `
		CREATE TABLE users (
			id INT64, name STRING, email STRING,
			PRIMARY KEY (id), INDEX (email)
		);
		INSERT INTO users VALUES (1, 'ada', 'ada@x.io'), (2, 'grace', 'grace@navy.mil');
	`); err != nil {
		t.Fatal(err)
	}
	h := New(database)

	body := do(t, h, http.MethodPost, "/api/query", `{"sql":"SELECT COUNT(*), MIN(name) FROM users"}`)
	var q struct {
		Results []struct {
			Columns []string `json:"columns"`
			Rows    [][]any  `json:"rows"`
			Plan    string   `json:"plan"`
		} `json:"results"`
		Error string `json:"error"`
	}
	if err := json.Unmarshal([]byte(body), &q); err != nil {
		t.Fatal(err)
	}
	if q.Error != "" || len(q.Results) != 1 || len(q.Results[0].Rows) != 1 {
		t.Fatalf("query: %s", body)
	}
	if q.Results[0].Rows[0][0] != float64(2) || q.Results[0].Rows[0][1] != "ada" {
		t.Fatalf("agg row: %#v", q.Results[0].Rows[0])
	}

	// a script that fails rolls back, and BEGIN/COMMIT in one request commits
	body = do(t, h, http.MethodPost, "/api/query", `{"sql":"BEGIN; INSERT INTO users VALUES (1, 'dup', 'x'); COMMIT;"}`)
	if !strings.Contains(body, "duplicate primary key") {
		t.Fatalf("expected duplicate error: %s", body)
	}
	body = do(t, h, http.MethodPost, "/api/query", `{"sql":"SELECT COUNT(*) FROM users"}`)
	if !strings.Contains(body, "2") {
		t.Fatalf("count changed: %s", body)
	}

	tables := do(t, h, http.MethodGet, "/api/tables", "")
	if !strings.Contains(tables, "users") || !strings.Contains(tables, "email") {
		t.Fatalf("tables: %s", tables)
	}
	indexes := do(t, h, http.MethodGet, "/api/indexes", "")
	if !strings.Contains(indexes, "PRIMARY") || !strings.Contains(indexes, "email") {
		t.Fatalf("indexes: %s", indexes)
	}
	entries := do(t, h, http.MethodGet, "/api/indexes?table=users&cols=email&limit=10", "")
	if !strings.Contains(entries, "ada@x.io") || !strings.Contains(entries, "grace@navy.mil") {
		t.Fatalf("entries: %s", entries)
	}
	pages := do(t, h, http.MethodGet, "/api/pages", "")
	if !strings.Contains(pages, "master") || !strings.Contains(pages, "leaf") {
		t.Fatalf("pages: %s", pages)
	}
	stats := do(t, h, http.MethodGet, "/api/stats", "")
	if !strings.Contains(stats, "pages") {
		t.Fatalf("stats: %s", stats)
	}
	html := do(t, h, http.MethodGet, "/", "")
	for _, needle := range []string{"kvdb", "Indexes", "Pages", "SQL"} {
		if !strings.Contains(html, needle) {
			t.Fatalf("console missing %q", needle)
		}
	}

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/query", nil)
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusMethodNotAllowed {
		t.Fatalf("query method: %d", rr.Code)
	}
	rr = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/api/indexes", nil)
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusMethodNotAllowed {
		t.Fatalf("indexes method: %d", rr.Code)
	}
}

func do(t *testing.T, h http.Handler, method, path, payload string) string {
	t.Helper()
	var rdr io.Reader
	if payload != "" {
		rdr = strings.NewReader(payload)
	}
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(method, path, rdr)
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("%s %s -> %d %s", method, path, rr.Code, rr.Body.String())
	}
	return rr.Body.String()
}
