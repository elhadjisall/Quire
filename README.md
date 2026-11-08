# Quire

Quire is a page-oriented SQL database in Go. It stores `INT64` and `STRING` rows, a primary key, and secondary indexes in one file of 4096-byte copy-on-write pages, and it accepts `CREATE TABLE`, `DROP TABLE`, `CREATE INDEX`, `DROP INDEX`, `INSERT`, `UPSERT`, `SELECT` (`WHERE`, `LIKE`, `ORDER BY`, `LIMIT`/`OFFSET`, `COUNT`, `SUM`, `MIN`, `MAX`), `UPDATE`, `DELETE`, `EXPLAIN`, `SHOW TABLES`, `SHOW INDEX`, `DESCRIBE`, and `BEGIN` / `COMMIT` / `ROLLBACK`.

## Run

```sh
export PATH="/home/esall/github/_tools/go/bin:$PATH"
make test
make serve
```

`make serve` builds `bin/kvdb` and serves `data/app.db`.

Shell, in another terminal after the binary exists:

```sh
./bin/kvdb shell data/app.db
```

Statements end with `;`. `BEGIN` keeps the transaction open (`kvdb*>`) until `COMMIT` or `ROLLBACK`. One shell entry, one `kvdb exec` script, and one console run each commit as a single transaction: a failed statement rolls the batch back.

```sh
./bin/kvdb exec data/app.db "CREATE TABLE t (id INT64, v STRING, PRIMARY KEY (id)); INSERT INTO t VALUES (1, 'hi'); SELECT * FROM t;"
```

## Layout

- `btree/` — B+tree nodes on 4 KB pages: lookup, insert with splits, delete with merges, ordered scan.
- `kv/` — pager. Page 0 is the master page (signature `KVDB-ELHADJI-001`, root, used, version, freelist head and tail). Other pages are tree nodes or freelist nodes. A commit appends pages, fsyncs, then rewrites page 0.
- `db/` — tables, order-preserving keys (4-byte prefix, sign-flipped big-endian `INT64`, escaped strings), secondary indexes, range scan, catalog.
- `sql/` — lexer, parser, planner, executor. `WHERE` picks a primary-key lookup, an index scan, or a table scan. `LIKE 'prefix%'` becomes index bounds.
- `server/` — HTTP (`/api/query`, `/api/tables`, `/api/indexes`, `/api/pages`, `/api/stats`) and the console in `server/web`.
- `cmd/kvdb/` — `shell`, `exec`, `serve`.

## Limits

- No `JOIN`, subquery, `UNION`, `GROUP BY`, `DISTINCT`, `HAVING`, views, or `ALTER TABLE`.
- `WHERE` is `AND` of comparisons, `BETWEEN`, and `LIKE`. There is no `OR`.
- `ORDER BY` is one column. If that is not the scan order, the executor sorts in memory. Aggregates cannot mix with plain columns or with `ORDER BY`.
- `LIKE 'prefix%'` can bound an index. Any other pattern is applied as a filter.
- Column types are `INT64` and `STRING` only. Literals are integers and strings. There is no `NULL`.
- One writer. Readers see the last committed snapshot. No savepoints. No client protocol besides the HTTP API above.
