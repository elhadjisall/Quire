.PHONY: build test bench serve clean

build:
	go build -o bin/kvdb ./cmd/kvdb

test:
	go test ./...

bench:
	go test ./kv -bench . -benchmem

serve: build
	./bin/kvdb serve data/app.db

clean:
	rm -rf bin data
