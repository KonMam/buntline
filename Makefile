# tether build targets. `make build` produces the release binary with the
# frontend embedded; plain `go build ./...` works without any frontend
# artifacts (web/dev.go serves from disk instead).

.PHONY: all build web test lint fmt check dev integration clean

all: build

web:
	cd web && npm run build

build: web
	go build -tags embedded -o bin/tether ./cmd/tether

test:
	go test -race ./...
	cd web && npm run test

lint:
	golangci-lint run
	cd web && npm run check && npm run format:check

fmt:
	gofmt -w .
	cd web && npm run format

# Everything CI runs, locally.
check: fmt lint test build

# Dev loop: Go API on :7433, Vite (with proxy + HMR) on :5173.
dev:
	@echo "terminal 1: go run ./cmd/tether -no-open"
	@echo "terminal 2: cd web && npm run dev"

# Live-backend tests: tool pickup against real endpoints. Each test skips
# when its backend is unreachable, so run what you have.
integration:
	TETHER_IT=1 go test ./internal/integration/ -v -count=1 -timeout 10m

clean:
	rm -rf bin web/dist

# Personal targets (untracked), e.g. promoting builds to a local install.
-include Makefile.local
