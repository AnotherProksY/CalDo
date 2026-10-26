BIN := bin/caldo
# Optional built-in Google OAuth client, e.g. GOOGLE_CLIENT_ID=... GOOGLE_CLIENT_SECRET=... make build
LDFLAGS := -X main.version=$(shell git describe --tags --always --dirty 2>/dev/null || echo dev) \
	-X caldo/internal/auth.googleClientID=$(GOOGLE_CLIENT_ID) \
	-X caldo/internal/auth.googleClientSecret=$(GOOGLE_CLIENT_SECRET)

.PHONY: build run test lint demo

build:
	go build -ldflags "$(LDFLAGS)" -o $(BIN) ./cmd/caldo

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
