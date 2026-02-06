// Package server exposes the database over HTTP and hosts the web console.
package server

import (
	"embed"
	"encoding/json"
	"io"
	"io/fs"
	"log"
	"net/http"
	"time"

	"github.com/elhadjisall/kvdb/db"
	"github.com/elhadjisall/kvdb/sql"
)

//go:embed web
var webFS embed.FS

// Server wires the HTTP handlers to a database.
type Server struct {
	db      *db.DB
	mux     *http.ServeMux
	started time.Time
}

// New builds the handler.
func New(database *db.DB) *Server {
	s := &Server{db: database, mux: http.NewServeMux(), started: time.Now()}
	static, _ := fs.Sub(webFS, "web")
	s.mux.Handle("/", http.FileServer(http.FS(static)))
	s.mux.HandleFunc("/api/query", s.handleQuery)
	s.mux.HandleFunc("/api/tables", s.handleTables)
	s.mux.HandleFunc("/api/stats", s.handleStats)
	return s
}

// ServeHTTP implements http.Handler.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	s.mux.ServeHTTP(w, r)
}

// ListenAndServe starts the server.
func ListenAndServe(addr string, database *db.DB) error {
	srv := &http.Server{
		Addr:              addr,
		Handler:           New(database),
		ReadHeaderTimeout: 5 * time.Second,
	}
	return srv.ListenAndServe()
}

type queryRequest struct {
	SQL string `json:"sql"`
}

type queryResponse struct {
	Results []*sql.Result `json:"results"`
	Error   string        `json:"error,omitempty"`
}

func (s *Server) handleQuery(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	var req queryRequest
	if err := json.Unmarshal(body, &req); err != nil {
		http.Error(w, "bad json", http.StatusBadRequest)
		return
	}
	results, err := sql.Run(s.db, req.SQL)
	resp := queryResponse{Results: results}
	if results == nil {
		resp.Results = []*sql.Result{}
	}
	if err != nil {
		resp.Error = err.Error()
	}
	writeJSON(w, resp)
}

type tableInfo struct {
	Name    string     `json:"name"`
	Columns []string   `json:"columns"`
	Types   []string   `json:"types"`
	PKeys   int        `json:"pkeys"`
	Indexes [][]string `json:"indexes"`
}

func (s *Server) handleTables(w http.ResponseWriter, r *http.Request) {
	tx := s.db.BeginRead()
	defer tx.Rollback()
	defs, err := tx.Tables()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	out := []tableInfo{}
	for _, d := range defs {
		types := make([]string, len(d.Types))
		for i, t := range d.Types {
			types[i] = db.TypeName(t)
		}
		idx := d.Indexes
		if idx == nil {
			idx = [][]string{}
		}
		out = append(out, tableInfo{d.Name, d.Cols, types, d.PKeys, idx})
	}
	writeJSON(w, out)
}

func (s *Server) handleStats(w http.ResponseWriter, r *http.Request) {
	st := s.db.Stats()
	writeJSON(w, map[string]any{
		"path":           s.db.Path,
		"pages":          st.Pages,
		"free_pages":     st.FreePages,
		"version":        st.Version,
		"size_bytes":     st.SizeBytes,
		"uptime_seconds": int(time.Since(s.started).Seconds()),
	})
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(v); err != nil {
		log.Println("write:", err)
	}
}
