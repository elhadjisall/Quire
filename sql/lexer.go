// Package sql implements a small SQL dialect on top of the db package.
//
// Supported statements:
//
//	CREATE TABLE t (a INT64, b STRING, PRIMARY KEY (a), INDEX (b));
//	CREATE INDEX [name] ON t (b);  DROP INDEX ON t (b);
//	DROP TABLE t;
//	INSERT INTO t (a, b) VALUES (1, 'x'), (2, 'y');
//	UPSERT INTO t (a, b) VALUES (1, 'z');
//	SELECT COUNT(*), MIN(a) FROM t WHERE b LIKE 'a%' ORDER BY a LIMIT 5 OFFSET 1;
//	EXPLAIN SELECT a FROM t WHERE a = 1;
//	UPDATE t SET b = 'w' WHERE a = 1;
//	DELETE FROM t WHERE a = 1;
//	BEGIN; COMMIT; ROLLBACK;
//	SHOW TABLES; SHOW INDEX FROM t; DESCRIBE t;
package sql

import (
	"fmt"
	"strings"
	"unicode"
)

type tokenKind int

const (
	tokEOF tokenKind = iota
	tokIdent
	tokNumber
	tokString
	tokSymbol
)

type token struct {
	kind tokenKind
	text string
	pos  int
}

// lex splits the input into tokens. Keywords are returned as identifiers
// and matched case-insensitively by the parser.
func lex(input string) ([]token, error) {
	var toks []token
	i := 0
	for i < len(input) {
		ch := input[i]
		switch {
		case ch == ' ' || ch == '\t' || ch == '\n' || ch == '\r':
			i++
		case ch == '-' && i+1 < len(input) && input[i+1] == '-':
			// comment to end of line
			for i < len(input) && input[i] != '\n' {
				i++
			}
		case unicode.IsLetter(rune(ch)) || ch == '_' || ch == '@':
			start := i
			for i < len(input) && (unicode.IsLetter(rune(input[i])) || unicode.IsDigit(rune(input[i])) || input[i] == '_' || input[i] == '@') {
				i++
			}
			toks = append(toks, token{tokIdent, input[start:i], start})
		case unicode.IsDigit(rune(ch)) || (ch == '-' && i+1 < len(input) && unicode.IsDigit(rune(input[i+1]))):
			start := i
			i++
			for i < len(input) && unicode.IsDigit(rune(input[i])) {
				i++
			}
			toks = append(toks, token{tokNumber, input[start:i], start})
		case ch == '\'' || ch == '"':
			quote := ch
			start := i
			i++
			var sb strings.Builder
			closed := false
			for i < len(input) {
				if input[i] == quote {
					if i+1 < len(input) && input[i+1] == quote { // escaped quote
						sb.WriteByte(quote)
						i += 2
						continue
					}
					closed = true
					i++
					break
				}
				sb.WriteByte(input[i])
				i++
			}
			if !closed {
				return nil, fmt.Errorf("unterminated string at position %d", start)
			}
			kind := tokString
			if quote == '"' { // double quotes delimit identifiers
				kind = tokIdent
			}
			toks = append(toks, token{kind, sb.String(), start})
		default:
			// multi-character operators first
			for _, op := range []string{">=", "<=", "!=", "<>"} {
				if strings.HasPrefix(input[i:], op) {
					toks = append(toks, token{tokSymbol, op, i})
					i += 2
					goto next
				}
			}
			if strings.ContainsRune("(),;=<>*", rune(ch)) {
				toks = append(toks, token{tokSymbol, string(ch), i})
				i++
			} else {
				return nil, fmt.Errorf("unexpected character %q at position %d", ch, i)
			}
		}
	next:
	}
	toks = append(toks, token{tokEOF, "", len(input)})
	return toks, nil
}
