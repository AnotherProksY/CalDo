BIN := bin/caldo
# Optional built-in Google OAuth client, e.g. GOOGLE_CLIENT_ID=... GOOGLE_CLIENT_SECRET=... make build
LDFLAGS := -X main.version=$(shell git describe --tags --always --dirty 2>/dev/null || echo dev) \
	-X caldo/internal/auth.googleClientID=$(GOOGLE_CLIENT_ID) \
	-X caldo/internal/auth.googleClientSecret=$(GOOGLE_CLIENT_SECRET)

.PHONY: build run test lint demo logo

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

# Re-render the PNG logos from assets/logo.svg (needs rsvg-convert).
logo:
	rsvg-convert -w 256 -h 256 assets/logo.svg -o assets/logo-256.png
	rsvg-convert -w 128 -h 128 assets/logo.svg -o assets/logo-128.png
