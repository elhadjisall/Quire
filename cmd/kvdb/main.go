// Command kvdb is the command line entry point: an interactive shell, a one
// shot executor and the HTTP server that hosts the web console.
package main

import (
	"bufio"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/elhadjisall/kvdb/db"
	"github.com/elhadjisall/kvdb/server"
	"github.com/elhadjisall/kvdb/sql"
)

const usage = `kvdb - a tiny relational database built on a copy-on-write B+tree

usage:
  kvdb shell <file.db>              interactive SQL shell
  kvdb exec  <file.db> "<sql>"      run a script and print the results
  kvdb serve <file.db> [-addr :7070] start the HTTP API and web console
`

func main() {
	if len(os.Args) < 3 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	cmd, path := os.Args[1], os.Args[2]
	database, err := db.Open(path)
	if err != nil {
		fail(err)
	}
	defer database.Close()

	switch cmd {
	case "shell":
		shell(database, os.Stdin, os.Stdout)
	case "exec":
		if len(os.Args) < 4 {
			fail(fmt.Errorf("missing script"))
		}
		results, err := sql.Run(database, strings.Join(os.Args[3:], " "))
		printResults(os.Stdout, results)
		if err != nil {
			fail(err)
		}
	case "serve":
		fs := flag.NewFlagSet("serve", flag.ExitOnError)
		addr := fs.String("addr", ":7070", "listen address")
		fs.Parse(os.Args[3:])
		fmt.Printf("kvdb console on http://localhost%s  (database: %s)\n", *addr, path)
		if err := server.ListenAndServe(*addr, database); err != nil {
			fail(err)
		}
	default:
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
}

func shell(database *db.DB, in io.Reader, w io.Writer) {
	fmt.Fprintln(w, "kvdb shell - statements end with ';', .help for commands")
	sess := sql.NewSession(database)
	defer sess.Close()
	sc := bufio.NewScanner(in)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	var buf strings.Builder
	for {
		prompt := "kvdb> "
		if sess.InTx() {
			prompt = "kvdb*> "
		}
		if buf.Len() > 0 {
			prompt = "   -> "
		}
		fmt.Fprint(w, prompt)
		if !sc.Scan() {
			fmt.Fprintln(w)
			return
		}
		line := strings.TrimSpace(sc.Text())
		switch line {
		case ".quit", ".exit", "\\q":
			return
		case ".help":
			fmt.Fprint(w, ".quit    exit\n.tables  list tables\n.schema  describe every table\n.stats   storage statistics\n.help    this message\n")
			continue
		case ".tables":
			line = "SHOW TABLES;"
		case ".schema":
			writeSchema(sess, w)
			continue
		case ".stats":
			if sess.InTx() {
				fmt.Fprintln(w, "commit or rollback the open transaction first")
				continue
			}
			st := database.Stats()
			fmt.Fprintf(w, "pages=%d free=%d version=%d size=%d bytes\n", st.Pages, st.FreePages, st.Version, st.SizeBytes)
			continue
		}
		buf.WriteString(line)
		buf.WriteByte('\n')
		if !strings.HasSuffix(line, ";") {
			continue
		}
		results, err := sess.Exec(buf.String())
		buf.Reset()
		printResults(w, results)
		if err != nil {
			fmt.Fprintln(w, "error:", err)
		}
	}
}

func writeSchema(sess *sql.Session, w io.Writer) {
	res, err := sess.Exec("SHOW TABLES;")
	printResults(w, res)
	if err != nil {
		fmt.Fprintln(w, "error:", err)
		return
	}
	if len(res) == 0 {
		return
	}
	for _, row := range res[0].Rows {
		dres, derr := sess.Exec("DESCRIBE " + fmt.Sprint(row[0]) + ";")
		printResults(w, dres)
		if derr != nil {
			fmt.Fprintln(w, "error:", derr)
		}
	}
}

func printResults(w io.Writer, results []*sql.Result) {
	for _, r := range results {
		if r.Columns == nil {
			fmt.Fprintf(w, "%s (%.2f ms)\n", r.Message, r.Millis)
			continue
		}
		widths := make([]int, len(r.Columns))
		for i, c := range r.Columns {
			widths[i] = len(c)
		}
		cells := make([][]string, len(r.Rows))
		for ri, row := range r.Rows {
			cells[ri] = make([]string, len(row))
			for i, v := range row {
				cells[ri][i] = cellText(v)
				if len(cells[ri][i]) > widths[i] {
					widths[i] = len(cells[ri][i])
				}
			}
		}
		sep := "+"
		for _, w := range widths {
			sep += strings.Repeat("-", w+2) + "+"
		}
		fmt.Fprintln(w, sep)
		fmt.Fprint(w, "|")
		for i, c := range r.Columns {
			fmt.Fprintf(w, " %-*s |", widths[i], c)
		}
		fmt.Fprintln(w)
		fmt.Fprintln(w, sep)
		for _, row := range cells {
			fmt.Fprint(w, "|")
			for i, v := range row {
				fmt.Fprintf(w, " %-*s |", widths[i], v)
			}
			fmt.Fprintln(w)
		}
		fmt.Fprintln(w, sep)
		plan := ""
		if r.Plan != "" {
			plan = " via " + r.Plan
		}
		fmt.Fprintf(w, "%d row(s)%s (%.2f ms)\n", len(r.Rows), plan, r.Millis)
	}
}

func cellText(v any) string {
	if v == nil {
		return "NULL"
	}
	return fmt.Sprint(v)
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "error:", err)
	os.Exit(1)
}
