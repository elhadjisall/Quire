# kvdb

A small database engine written from scratch in Go, following
[Build Your Own Database From Scratch](https://build-your-own.org/database/).

Work in progress. Planned layers:

1. copy-on-write B+tree on fixed size pages
2. single-file key-value store with atomic commits and a free list
3. tables, typed rows and secondary indexes
4. a small SQL dialect and a web console
