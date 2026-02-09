// Command kvdb is the command line entry point: an interactive shell, a one
// shot executor and the HTTP server that hosts the web console.
package main

import (
	"bufio"
	"flag"
	"fmt"
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
		shell(database)
	case "exec":
		if len(os.Args) < 4 {
			fail(fmt.Errorf("missing script"))
		}
		results, err := sql.Run(database, strings.Join(os.Args[3:], " "))
		printResults(results)
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

func shell(database *db.DB) {
	fmt.Println("kvdb shell - statements end with ';', type .quit to exit")
	in := bufio.NewScanner(os.Stdin)
	in.Buffer(make([]byte, 1<<20), 1<<20)
	var buf strings.Builder
	prompt := "kvdb> "
	for {
		fmt.Print(prompt)
		if !in.Scan() {
			fmt.Println()
			return
		}
		line := strings.TrimSpace(in.Text())
		switch line {
		case ".quit", ".exit", "\\q":
			return
		case ".tables":
			line = "SHOW TABLES;"
		case ".stats":
			st := database.Stats()
			fmt.Printf("pages=%d free=%d version=%d size=%d bytes\n", st.Pages, st.FreePages, st.Version, st.SizeBytes)
			continue
		}
		buf.WriteString(line)
		buf.WriteByte('\n')
		if !strings.HasSuffix(line, ";") {
			prompt = "   -> "
			continue
		}
		prompt = "kvdb> "
		results, err := sql.Run(database, buf.String())
		buf.Reset()
		printResults(results)
		if err != nil {
			fmt.Println("error:", err)
		}
	}
}

func printResults(results []*sql.Result) {
	for _, r := range results {
		if r.Columns == nil {
			fmt.Printf("%s (%.2f ms)\n", r.Message, r.Millis)
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
				cells[ri][i] = fmt.Sprint(v)
				if len(cells[ri][i]) > widths[i] {
					widths[i] = len(cells[ri][i])
				}
			}
		}
		sep := "+"
		for _, w := range widths {
			sep += strings.Repeat("-", w+2) + "+"
		}
		fmt.Println(sep)
		fmt.Print("|")
		for i, c := range r.Columns {
			fmt.Printf(" %-*s |", widths[i], c)
		}
		fmt.Println()
		fmt.Println(sep)
		for _, row := range cells {
			fmt.Print("|")
			for i, v := range row {
				fmt.Printf(" %-*s |", widths[i], v)
			}
			fmt.Println()
		}
		fmt.Println(sep)
		plan := ""
		if r.Plan != "" {
			plan = " via " + r.Plan
		}
		fmt.Printf("%d row(s)%s (%.2f ms)\n", len(r.Rows), plan, r.Millis)
	}
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "error:", err)
	os.Exit(1)
}
