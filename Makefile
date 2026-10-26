BIN := bin/caldo

.PHONY: build run test lint demo

build:
	go build -o $(BIN) ./cmd/caldo

run: build
	./$(BIN)

test:
	go test ./...

lint:
	golangci-lint run ./...

# Try the UI against a throwaway database with sample data.
demo: build
	rm -f /tmp/caldo-demo.db*
	./$(BIN) --db /tmp/caldo-demo.db demo
	./$(BIN) --db /tmp/caldo-demo.db
