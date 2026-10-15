BIN := bin/sherlook

.PHONY: build test lint clean

build:
	go build -o $(BIN) .

test:
	go test ./...

lint:
	@test -z "$$(gofmt -l .)" || (echo "gofmt needed:"; gofmt -l .; exit 1)
	go vet ./...

clean:
	rm -rf bin
