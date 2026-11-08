# kvdb

A small relational database written from scratch in Go, following the
structure of [Build Your Own Database From Scratch](https://build-your-own.org/database/).
No dependencies outside the standard library.

```
┌────────────────────────────────────────────────────────┐
│  SQL  (lexer → parser → planner → executor)            │
├────────────────────────────────────────────────────────┤
│  db   (tables, typed rows, order-preserving keys,      │
│        secondary indexes, range scans, catalog)        │
├────────────────────────────────────────────────────────┤
│  kv   (single-file store, copy-on-write pages,         │
│        free list, atomic commits, snapshot readers)    │
├────────────────────────────────────────────────────────┤
│  btree (immutable B+tree nodes on 4 KB pages)          │
└────────────────────────────────────────────────────────┘
```

## Features

- **Copy-on-write B+tree** – nodes are never updated in place; inserts and
  deletes build new nodes along the root path and split/merge as needed.
- **Crash safe** – an update appends pages, `fsync`s, then atomically
  rewrites the master page. A crash at any point leaves the previous
  version intact.
- **Free list** – freed pages are recycled through an on-disk FIFO queue
  with version stamps so pages still visible to a snapshot reader are
  never reused.
- **Transactions** – one writer at a time, any number of concurrent
  snapshot readers; writes are atomic and isolated.
- **Tables and indexes** – typed columns (`INT64`, `STRING`),
  multi-column primary keys, secondary indexes maintained automatically.
- **SQL** – `CREATE TABLE`, `CREATE INDEX`, `DROP INDEX`, `INSERT`, `UPSERT`,
  `SELECT` with `WHERE`, `LIKE`, `ORDER BY`, `LIMIT`/`OFFSET`, aggregates
  (`COUNT`, `SUM`, `MIN`, `MAX`), `UPDATE`, `DELETE`, `EXPLAIN`,
  `DROP TABLE`, `SHOW TABLES`, `SHOW INDEX`, `DESCRIBE`, and
  `BEGIN`/`COMMIT`/`ROLLBACK`. The planner picks a primary key lookup,
  index scan or range scan from the `WHERE` clause, turns a `LIKE 'prefix%'`
  into index bounds, and sorts in memory when `ORDER BY` is not the scan order.
- **Web console** – a dependency-free browser UI with a query editor,
  schema browser, result table, index browser and a page map.

## Getting started

```sh
go build ./cmd/kvdb

# interactive shell
./kvdb shell data/app.db

# one-shot script
./kvdb exec data/app.db "CREATE TABLE t (id INT64, v STRING, PRIMARY KEY (id)); INSERT INTO t VALUES (1, 'hi'); SELECT * FROM t;"

# web console on http://localhost:7070
./kvdb serve data/app.db
```

`make build` writes `bin/kvdb`. `make serve` builds and starts the console
on http://localhost:7070 against `data/app.db`. In the shell, `BEGIN` holds
the transaction open (the prompt changes to `kvdb*>`) until `COMMIT` or `ROLLBACK`.

## SQL dialect

```sql
CREATE TABLE users (
  id INT64, name STRING, email STRING, age INT64,
  PRIMARY KEY (id), INDEX (email), INDEX (age, name)
);
INSERT INTO users VALUES (1, 'ada', 'ada@lovelace.io', 36);
UPSERT INTO users (id, name, email, age) VALUES (1, 'ada', 'ada@x.io', 37);
SELECT name, age FROM users WHERE age BETWEEN 30 AND 60 ORDER BY age DESC LIMIT 10;
SELECT * FROM users WHERE email = 'ada@x.io';          -- index scan on (email)
UPDATE users SET age = 38 WHERE id = 1;
DELETE FROM users WHERE age > 70;
CREATE INDEX ON users (name);
DROP INDEX ON users (name);
SELECT COUNT(*), MIN(age), MAX(age), SUM(age) FROM users WHERE name LIKE 'a%';
SELECT name FROM users ORDER BY name LIMIT 10 OFFSET 5;
EXPLAIN SELECT * FROM users WHERE email = 'ada@x.io';
SHOW TABLES; SHOW INDEX FROM users; DESCRIBE users; DROP TABLE users;
BEGIN; INSERT INTO users VALUES (9, 'edsger', 'edsger@utexas.edu', 70); COMMIT;
```

Every call to `kvdb exec`, every shell input and every console run executes
inside a single transaction: if any statement fails, nothing is committed.

## Layout

| path          | what                                                        |
|---------------|-------------------------------------------------------------|
| `btree/`      | node format, insert/split, delete/merge, iterator           |
| `kv/`         | file format, master page, free list, transactions           |
| `db/`         | key encoding, tables, indexes, scanner, catalog             |
| `sql/`        | lexer, parser, planner and executor                         |
| `server/`     | HTTP API and the embedded web console (`server/web`)        |
| `cmd/kvdb/`   | CLI: shell, exec, serve                                     |

## Testing

```sh
go test ./...
go test ./kv -bench . -benchmem
```

## File format

Page 0 is the master page:

```
| sig (16B) | root | used | version | fl.head_page | fl.head_seq | fl.tail_page | fl.tail_seq |
```

Every other page is either a B+tree node or a free list node. Tree nodes
look like

```
| type | nkeys | pointers (8B each) | offsets (2B each) | klen vlen key val ... |
```

Keys are prefixed with a 4-byte table or index id and encoded so that
byte-wise comparison matches the typed ordering (`INT64` has its sign bit
flipped and is stored big-endian; strings are null-terminated with `\x00`
and `\x01` escaped).
